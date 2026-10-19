package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type proofShow struct {
	What    bool `json:"what"`
	Checks  bool `json:"checks"`
	Stopped bool `json:"stopped_repeat"`
	Actor   bool `json:"actor"`
}

func (s *Server) createProof(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action     string    `json:"action_id"`
		Show       proofShow `json:"show"`
		Hours      int       `json:"expires_in_hours"`
		Recipients []string  `json:"recipients"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Hours == 0 {
		in.Hours = 168
	}
	if in.Action == "" || in.Hours < 1 || in.Hours > 8760 || len(in.Recipients) > 100 {
		s.fail(w, r, 400, "INVALID_QUERY", "Action and expiry between 1 and 8760 hours are required", nil)
		return
	}
	for _, v := range in.Recipients {
		if !validEmail(v) {
			s.fail(w, r, 400, "INVALID_QUERY", "Invalid recipient email", nil)
			return
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	id := "prf_" + uuid.NewString()
	expires := time.Now().UTC().Add(time.Duration(in.Hours) * time.Hour)
	base := strings.TrimRight(os.Getenv("LUMNI_PUBLIC_BASE_URL"), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	url := base + "/p/" + token
	cipher, err := s.secrets.Encrypt([]byte(url))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	sent := []string{}
	err = s.auditTx(r, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM actions WHERE tenant_id=$1 AND id=$2)`, tenant(r), in.Action).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return pgx.ErrNoRows
		}
		if _, e := tx.Exec(r.Context(), `INSERT INTO proofs(tenant_id,id,action_id,token_hash,show,expires_at,url_ciphertext)VALUES($1,$2,$3,$4,$5,$6,$7)`, tenant(r), id, in.Action, hash[:], jsonBytes(in.Show), expires, cipher); e != nil {
			return e
		}
		return s.queueProofEmails(r, tx, id, in.Recipients, &sent)
	})
	s.respond(w, r, 201, map[string]any{"id": id, "url": url, "token": token, "expires_at": expires, "sent_to": sent}, err)
}
func (s *Server) revokeProof(w http.ResponseWriter, r *http.Request) {
	err := s.auditTx(r, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `UPDATE proofs SET revoked_at=COALESCE(revoked_at,now()) WHERE tenant_id=$1 AND id=$2`, tenant(r), chi.URLParam(r, "id"))
		if e == nil && tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return e
	})
	if err != nil {
		s.respond(w, r, 0, nil, err)
	} else {
		w.WriteHeader(204)
	}
}
func (s *Server) proofData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	peer, _ := r.Context().Value(peerKey{}).(string)
	if peer == "" {
		peer = r.RemoteAddr
	}
	ip, _, e := net.SplitHostPort(peer)
	if e != nil {
		ip = peer
	}
	key := sha256.Sum256([]byte(ip))
	var n int
	err := s.pool.QueryRow(r.Context(), `INSERT INTO public_proof_rate(key,bucket,requests)VALUES($1,date_trunc('minute',now()),1)ON CONFLICT(key,bucket)DO UPDATE SET requests=public_proof_rate.requests+1 RETURNING requests`, key[:]).Scan(&n)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return nil, false
	}
	if n > 60 {
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, 429, "RATE_LIMITED", "Too many requests", nil)
		return nil, false
	}
	token := chi.URLParam(r, "token")
	if len(token) < 22 || len(token) > 128 {
		s.fail(w, r, 404, "NOT_FOUND", "Proof was not found", nil)
		return nil, false
	}
	hash := sha256.Sum256([]byte(token))
	var tenantID, actionID, workspace string
	var show []byte
	var expiry time.Time
	var revoked *time.Time
	err = s.pool.QueryRow(r.Context(), `SELECT p.tenant_id::text,p.action_id,p.show,p.expires_at,p.revoked_at,t.name FROM proofs p JOIN tenants t ON t.id=p.tenant_id WHERE p.token_hash=$1`, hash[:]).Scan(&tenantID, &actionID, &show, &expiry, &revoked, &workspace)
	if err == pgx.ErrNoRows || (!expiry.IsZero() && !expiry.After(time.Now())) {
		s.fail(w, r, 404, "NOT_FOUND", "Proof was not found", nil)
		return nil, false
	}
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return nil, false
	}
	if revoked != nil {
		s.fail(w, r, 410, "REVOKED", "Proof was revoked", nil)
		return nil, false
	}
	var flags proofShow
	_ = json.Unmarshal(show, &flags)
	var operation, actor, status string
	var at time.Time
	var receipt *string
	var outcome *string
	err = s.pool.QueryRow(r.Context(), `SELECT a.operation,COALESCE(NULLIF(ac.display_name,''),a.actor_id),a.status,a.requested_at,(SELECT id::text FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1),(SELECT status FROM outcomes WHERE tenant_id=a.tenant_id AND action_id=a.id) FROM actions a JOIN actors ac ON ac.tenant_id=a.tenant_id AND ac.id=a.actor_id WHERE a.tenant_id=$1 AND a.id=$2`, tenantID, actionID).Scan(&operation, &actor, &status, &at, &receipt, &outcome)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return nil, false
	}
	proofStatus := "pending"
	if outcome != nil && *outcome == "VERIFIED" {
		proofStatus = "proven"
	} else if outcome != nil && contains([]string{"FAILED", "MISMATCH", "PARTIAL"}, *outcome) {
		proofStatus = "unproven"
	}
	out := map[string]any{"workspace": workspace, "status": proofStatus, "receipt": receipt}
	// The public projection uses a strict allowlist and no arbitrary metadata,
	// arguments, evaluation details, costs, rule text, or verification evidence.
	if flags.What {
		out["what"] = map[string]any{"summary": strings.ReplaceAll(operation, ".", " "), "at": at, "app": strings.Split(operation, ".")[0]}
	}
	if flags.Checks {
		out["checks"] = []map[string]any{{"t": "App outcome verified", "ok": proofStatus == "proven"}}
	}
	if flags.Actor {
		out["actor"] = actor
	}
	if flags.Stopped {
		var stopped *time.Time
		_ = s.pool.QueryRow(r.Context(), `SELECT min(created_at) FROM decisions WHERE tenant_id=$1 AND snapshot->>'existing_action_id'=$2 AND decision='DEDUPLICATE'`, tenantID, actionID).Scan(&stopped)
		if stopped != nil {
			out["stopped_repeat"] = map[string]any{"at": stopped}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return out, true
}
func (s *Server) publicProof(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.proofData(w, r); ok {
		writeJSON(w, 200, out)
	}
}

var proofPage = template.Must(template.New("proof").Parse(`<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Lumni proof</title><body><main><h1>{{.workspace}}</h1><p>Proof status: {{.status}}</p>{{with .what}}<h2>{{.summary}}</h2><p>{{.at}}</p>{{end}}{{with .actor}}<p>Actor: {{.}}</p>{{end}}{{range .checks}}<p>{{.t}}: {{.ok}}</p>{{end}}<p>Receipt: {{.receipt}}</p></main></body></html>`))

func (s *Server) publicProofPage(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.proofData(w, r); ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		_ = proofPage.Execute(w, out)
	}
}
