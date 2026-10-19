package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"lumni/internal/notifications"
)

func validEmail(v string) bool { return notifications.ValidEmail(v) }
func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request) {
	var ch, rs []byte
	err := s.pool.QueryRow(r.Context(), `SELECT channels,routes FROM notification_config WHERE tenant_id=$1`, tenant(r)).Scan(&ch, &rs)
	if err == pgx.ErrNoRows {
		writeJSON(w, 200, notifications.Config{Channels: []notifications.Channel{}, Routes: []notifications.Route{}})
		return
	}
	s.respond(w, r, 200, map[string]any{"channels": json.RawMessage(ch), "routes": json.RawMessage(rs)}, err)
}
func (s *Server) putNotifications(w http.ResponseWriter, r *http.Request) {
	var in notifications.Config
	if !s.decode(w, r, &in) {
		return
	}
	if err := notifications.Validate(in); err != nil {
		s.fail(w, r, 400, "INVALID_NOTIFICATIONS", err.Error(), nil)
		return
	}
	for _, ch := range in.Channels {
		if ch.Kind == "slack" {
			var exists bool
			err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM composio_connections WHERE tenant_id=$1 AND id=$2 AND toolkit='slack' AND NOT disabled)`, tenant(r), ch.Connection).Scan(&exists)
			if err != nil {
				s.respond(w, r, 0, nil, err)
				return
			}
			if !exists {
				s.fail(w, r, 400, "INVALID_NOTIFICATIONS", "Slack connection is not owned by this workspace", nil)
				return
			}
		}
	}
	if in.Channels == nil {
		in.Channels = []notifications.Channel{}
	}
	if in.Routes == nil {
		in.Routes = []notifications.Route{}
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `INSERT INTO notification_config(tenant_id,channels,routes)VALUES($1,$2,$3)ON CONFLICT(tenant_id)DO UPDATE SET channels=EXCLUDED.channels,routes=EXCLUDED.routes,updated_at=now()`, tenant(r), jsonBytes(in.Channels), jsonBytes(in.Routes))
		return err
	})
	s.respond(w, r, 200, in, err)
}
func (s *Server) sendNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel string `json:"channel_id"`
		Kind    string `json:"kind"`
		Ref     string `json:"ref_id"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Channel == "" || in.Ref == "" || !contains([]string{"incident", "digest", "proof"}, in.Kind) {
		s.fail(w, r, 400, "INVALID_NOTIFICATION", "channel_id, ref_id, and incident, digest, or proof are required", nil)
		return
	}
	var exists bool
	err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM notification_config c CROSS JOIN LATERAL jsonb_array_elements(c.channels) ch WHERE c.tenant_id=$1 AND ch->>'id'=$2)`, tenant(r), in.Channel).Scan(&exists)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if !exists {
		s.respond(w, r, 0, nil, pgx.ErrNoRows)
		return
	}
	query := map[string]string{"incident": `SELECT EXISTS(SELECT 1 FROM incidents WHERE tenant_id=$1 AND id=$2)`, "proof": `SELECT EXISTS(SELECT 1 FROM proofs WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL AND expires_at>now())`, "digest": `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND $2::text<>'')`}[in.Kind]
	err = s.pool.QueryRow(r.Context(), query, tenant(r), in.Ref).Scan(&exists)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if !exists {
		s.respond(w, r, 0, nil, pgx.ErrNoRows)
		return
	}
	id := "ntf_" + uuid.NewString()
	_, err = s.pool.Exec(r.Context(), `INSERT INTO notification_jobs(tenant_id,id,channel_id,kind,ref_id)VALUES($1,$2,$3,$4,$5)`, tenant(r), id, in.Channel, in.Kind, in.Ref)
	s.respond(w, r, 202, map[string]any{"queued": err == nil, "id": id}, err)
}
func (s *Server) queueProofEmails(r *http.Request, tx pgx.Tx, id string, recipients []string, sent *[]string) error {
	if len(recipients) == 0 {
		return nil
	}
	var channels []byte
	err := tx.QueryRow(r.Context(), `SELECT channels FROM notification_config WHERE tenant_id=$1`, tenant(r)).Scan(&channels)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cs []notifications.Channel
	_ = json.Unmarshal(channels, &cs)
	channel := ""
	for _, c := range cs {
		if c.Kind == "email" {
			channel = c.ID
			break
		}
	}
	if channel == "" {
		return nil
	}
	for _, recipient := range recipients {
		_, err = tx.Exec(r.Context(), `INSERT INTO notification_jobs(tenant_id,id,channel_id,kind,ref_id,payload)VALUES($1,$2,$3,'proof',$4,$5)`, tenant(r), "ntf_"+uuid.NewString(), channel, id, jsonBytes(map[string]string{"recipient": recipient}))
		if err != nil {
			return err
		}
		*sent = append(*sent, recipient)
	}
	return nil
}
