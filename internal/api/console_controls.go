package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"lumni/internal/auth"
)

func (s *Server) consoleRoutes(r chi.Router) {
	r.Get("/analytics/day", s.withScope("query", s.analyticsDay))
	r.Get("/analytics/daily", s.withScope("query", s.analyticsDaily))
	r.Get("/analytics/freshness", s.withScope("query", s.analyticsFreshness))
	r.Get("/analytics/anomalies", s.withScope("query", s.analyticsAnomalies))
	r.Get("/actions/{id}/journey", s.withScope("query", s.actionJourney))
	r.Post("/approvals/decide", s.withScope("admission", s.decideApprovals))
	r.Post("/actors/{id}/pause", s.withScope("admin", s.pauseActor(true)))
	r.Post("/actors/{id}/resume", s.withScope("admin", s.pauseActor(false)))
	r.Post("/config/pause", s.withScope("admin", s.pauseWorkspace))
	r.Get("/config/limits", s.withScope("admin", s.listLimits))
	r.Put("/config/limits/{id}", s.withScope("admin", s.putLimit))
	r.Delete("/config/limits/{id}", s.withScope("admin", s.deleteLimit))
	r.Get("/config/spend-limit", s.withScope("admin", s.getSpendLimit))
	r.Put("/config/spend-limit", s.withScope("admin", s.putSpendLimit))
	r.Delete("/config/spend-limit", s.withScope("admin", s.deleteSpendLimit))
	r.Get("/config/autopilot", s.withScope("admin", s.listAutopilot))
	r.Put("/config/autopilot/{actor_id}", s.withScope("admin", s.putAutopilot))
	r.Delete("/config/autopilot/{actor_id}", s.withScope("admin", s.deleteAutopilot))
	r.Patch("/config/policy-rules/{id}", s.withScope("admin", s.patchPolicyRule))
	r.Delete("/config/policy-rules/{id}", s.withScope("admin", s.deletePolicyRule))
	r.Post("/config/policy-rules/preview", s.withScope("query", s.previewPolicyRule))
	r.Delete("/config/enforcement", s.withScope("admin", s.clearEnforcement))
	r.Get("/audit", s.withScope("query", s.auditLog))
	r.Post("/proofs", s.withScope("query", s.createProof))
	r.Delete("/proofs/{id}", s.withScope("query", s.revokeProof))
	r.Get("/config/notifications", s.withScope("admin", s.getNotifications))
	r.Put("/config/notifications", s.withScope("admin", s.putNotifications))
	r.Post("/notifications/send", s.withScope("admin", s.sendNotification))
	r.Get("/resources/contended", s.withScope("query", s.contendedResources))
	r.Get("/resources/{id}/lease", s.withScope("query", s.resourceLease))
	r.Get("/resources/{id}/versions", s.withScope("query", s.resourceVersions))
	r.Get("/apps/{app}/health", s.withScope("query", s.appHealth))
	r.Get("/billing/usage", s.withScope("query", s.billingUsage))
}

func (s *Server) principalName(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	name := p.KeyID
	_ = s.pool.QueryRow(r.Context(), `SELECT name FROM api_keys WHERE tenant_id=$1 AND id=$2`, p.TenantID, p.KeyID).Scan(&name)
	return name
}
func (s *Server) auditTx(r *http.Request, fn func(pgx.Tx) error) error {
	name := s.principalName(r)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT set_config('lumni.audit_by',$1,true)`, name); err == nil {
		err = fn(tx)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	return err
}
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func (s *Server) jsonRow(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	var b []byte
	err := s.pool.QueryRow(r.Context(), query, args...).Scan(&b)
	s.respond(w, r, 200, json.RawMessage(b), err)
}

func (s *Server) pauseActor(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string     `json:"reason"`
			Until  *time.Time `json:"until"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		if in.Until != nil && !in.Until.After(time.Now()) {
			s.fail(w, r, 400, "INVALID_TIME_RANGE", "until must be in the future", nil)
			return
		}
		id := chi.URLParam(r, "id")
		name := s.principalName(r)
		var out []byte
		conflict := ""
		err := s.auditTx(r, func(tx pgx.Tx) error {
			var current bool
			if e := tx.QueryRow(r.Context(), `SELECT paused AND (until IS NULL OR until>now()) FROM actors WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant(r), id).Scan(&current); e != nil {
				return e
			}
			if current == paused {
				if paused {
					conflict = "ALREADY_PAUSED"
				} else {
					conflict = "NOT_PAUSED"
				}
				return errors.New(conflict)
			}
			return tx.QueryRow(r.Context(), `UPDATE actors SET paused=$3,paused_at=CASE WHEN $3 THEN now() ELSE NULL END,paused_by=CASE WHEN $3 THEN $4 ELSE NULL END,until=CASE WHEN $3 THEN $5::timestamptz ELSE NULL END,pause_reason=$6 WHERE tenant_id=$1 AND id=$2 RETURNING jsonb_build_object('actor_id',id,'paused',paused,'paused_at',paused_at,'paused_by',paused_by,'until',until)`, tenant(r), id, paused, name, in.Until, in.Reason).Scan(&out)
		})
		if conflict != "" {
			s.fail(w, r, 409, conflict, "Actor already has this pause state", nil)
			return
		}
		s.respond(w, r, 200, json.RawMessage(out), err)
	}
}
func (s *Server) pauseWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused *bool `json:"paused"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Paused == nil {
		s.fail(w, r, 400, "INVALID_QUERY", "paused is required", nil)
		return
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		var before bool
		if e := tx.QueryRow(r.Context(), `SELECT paused FROM tenants WHERE id=$1 FOR UPDATE`, tenant(r)).Scan(&before); e != nil {
			return e
		}
		if _, e := tx.Exec(r.Context(), `UPDATE tenants SET paused=$2 WHERE id=$1`, tenant(r), *in.Paused); e != nil {
			return e
		}
		_, e := tx.Exec(r.Context(), `INSERT INTO audit_events(tenant_id,by,type,target,before,after)VALUES($1,current_setting('lumni.audit_by'),'workspace.paused','{}',$2,$3)`, tenant(r), jsonBytes(map[string]any{"paused": before}), jsonBytes(in))
		return e
	})
	s.respond(w, r, 200, in, err)
}

type controlScope struct {
	Operation *string `json:"operation,omitempty"`
	Actor     *string `json:"actor_id,omitempty"`
	Resource  *string `json:"resource,omitempty"`
}
type limitInput struct {
	Scope     controlScope `json:"scope"`
	Max       *int         `json:"max_in_flight"`
	PerMinute *int         `json:"per_minute"`
	Hold      bool         `json:"hold"`
	Mode      string       `json:"mode"`
}

const limitCounts = `to_jsonb(l)||jsonb_build_object('in_flight_now',(SELECT count(*) FROM actions a WHERE a.tenant_id=l.tenant_id AND a.status IN('RESERVED','ACTIVE','UNKNOWN') AND console_scope_matches(l.scope,a.operation,a.actor_id,ARRAY(SELECT resource_id FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id)) AND (a.status<>'RESERVED' OR EXISTS(SELECT 1 FROM leases WHERE tenant_id=a.tenant_id AND action_id=a.id AND expires_at>now()))),'waiting_now',(SELECT count(*) FROM actions a WHERE a.tenant_id=l.tenant_id AND a.status='PROPOSED' AND console_scope_matches(l.scope,a.operation,a.actor_id,ARRAY(SELECT resource_id FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id)) AND (SELECT decision FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1)='WAIT'))`

func (s *Server) listLimits(w http.ResponseWriter, r *http.Request) {
	spec := listSpec{From: "admission_limits l", Tenant: "l.tenant_id", Key: "l.id", ID: "l.id", Time: "l.created_at", JSON: limitCounts, Scope: "l.scope"}
	s.pagedList(w, r, spec, "", false)
}
func (s *Server) putLimit(w http.ResponseWriter, r *http.Request) {
	var in limitInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.Mode == "" {
		in.Mode = "ENFORCE"
	}
	if (in.Max != nil && *in.Max <= 0) || (in.PerMinute != nil && *in.PerMinute <= 0) || (!in.Hold && in.Max == nil && in.PerMinute == nil) || (in.Mode != "ENFORCE" && in.Mode != "SHADOW" && in.Mode != "WARN") {
		s.fail(w, r, 400, "INVALID_LIMIT", "Positive concurrency or rate, or hold, and a valid mode are required", nil)
		return
	}
	if in.Scope.Resource != nil && strings.Contains(strings.TrimSuffix(*in.Scope.Resource, "*"), "*") {
		s.fail(w, r, 400, "INVALID_LIMIT", "Resource wildcard is allowed only as a suffix", nil)
		return
	}
	id := chi.URLParam(r, "id")
	err := s.auditTx(r, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `INSERT INTO admission_limits(tenant_id,id,scope,max_in_flight,per_minute,hold,mode)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT(tenant_id,id)DO UPDATE SET scope=EXCLUDED.scope,max_in_flight=EXCLUDED.max_in_flight,per_minute=EXCLUDED.per_minute,hold=EXCLUDED.hold,mode=EXCLUDED.mode,updated_at=now()`, tenant(r), id, jsonBytes(in.Scope), in.Max, in.PerMinute, in.Hold, in.Mode)
		return e
	})
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	s.jsonRow(w, r, `SELECT `+limitCounts+` FROM admission_limits l WHERE tenant_id=$1 AND id=$2`, tenant(r), id)
}
func (s *Server) deleteLimit(w http.ResponseWriter, r *http.Request) {
	s.deleteControl(w, r, "admission_limits", "id", chi.URLParam(r, "id"))
}
func (s *Server) deleteControl(w http.ResponseWriter, r *http.Request, table, key, id string) {
	err := s.auditTx(r, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `DELETE FROM `+table+` WHERE tenant_id=$1 AND `+key+`=$2`, tenant(r), id)
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

type spendInput struct {
	Daily int64        `json:"daily_micros_usd"`
	When  string       `json:"when_reached"`
	TZ    string       `json:"tz"`
	Scope controlScope `json:"scope"`
}

func (s *Server) putSpendLimit(w http.ResponseWriter, r *http.Request) {
	var in spendInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.Daily <= 0 || (in.When != "SLOW" && in.When != "HOLD") || in.Scope.Operation != nil || in.Scope.Resource != nil {
		s.fail(w, r, 400, "INVALID_LIMIT", "A positive daily budget and SLOW or HOLD are required; scope supports actor_id", nil)
		return
	}
	if in.TZ == "" {
		in.TZ = "UTC"
	}
	if _, err := time.LoadLocation(in.TZ); err != nil || in.TZ == "Local" {
		s.fail(w, r, 400, "INVALID_TIME_ZONE", "tz must be an IANA zone", nil)
		return
	}
	key := ""
	if in.Scope.Actor != nil {
		key = *in.Scope.Actor
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		if key != "" {
			var exists bool
			if e := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM actors WHERE tenant_id=$1 AND id=$2)`, tenant(r), key).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return pgx.ErrNoRows
			}
		}
		_, e := tx.Exec(r.Context(), `INSERT INTO spend_limits(tenant_id,scope_key,daily_micros_usd,when_reached,tz)VALUES($1,$2,$3,$4,$5)ON CONFLICT(tenant_id,scope_key)DO UPDATE SET daily_micros_usd=EXCLUDED.daily_micros_usd,when_reached=EXCLUDED.when_reached,tz=EXCLUDED.tz`, tenant(r), key, in.Daily, in.When, in.TZ)
		return e
	})
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	s.spendResponse(w, r, key)
}
func (s *Server) getSpendLimit(w http.ResponseWriter, r *http.Request) {
	s.spendResponse(w, r, r.URL.Query().Get("actor_id"))
}
func (s *Server) deleteSpendLimit(w http.ResponseWriter, r *http.Request) {
	s.deleteControl(w, r, "spend_limits", "scope_key", r.URL.Query().Get("actor_id"))
}
func (s *Server) spendResponse(w http.ResponseWriter, r *http.Request, key string) {
	var budget, spent int64
	var when, zone string
	var now, start, end time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT l.daily_micros_usd,l.when_reached,l.tz,now(),w.start_at,w.end_at,console_spent(l.tenant_id,l.scope_key,w.start_at,now()) FROM spend_limits l CROSS JOIN LATERAL console_spend_window(l.tz,now()) w WHERE tenant_id=$1 AND scope_key=$2`, tenant(r), key).Scan(&budget, &when, &zone, &now, &start, &end, &spent)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, 200, nil)
		return
	}
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	pace := float64(spent) * end.Sub(start).Seconds() / math.Max(1, now.Sub(start).Seconds())
	var slow *time.Time
	if spent > 0 && pace > float64(budget) {
		v := start.Add(time.Duration(float64(budget) / pace * float64(end.Sub(start))))
		slow = &v
	}
	paceInteger := int64(math.MaxInt64)
	if pace < float64(math.MaxInt64) {
		paceInteger = int64(pace)
	}
	writeJSON(w, 200, map[string]any{"daily_micros_usd": budget, "when_reached": when, "tz": zone, "day_start": start.UTC(), "resets_at": end.UTC(), "scope": map[string]any{"actor_id": key}, "spent_today_micros_usd": spent, "pace_micros_usd": paceInteger, "slow_at": slow})
}

func autopilotJSON(alias string) string {
	return "to_jsonb(" + alias + ")||jsonb_build_object('slips',(SELECT count(*) FROM (SELECT f.kind,f.outcome FROM console_action_facts f WHERE f.tenant_id=" + alias + ".tenant_id AND f.actor_id=" + alias + ".actor_id AND f.operation=" + alias + ".operation AND f.at>=" + alias + ".since ORDER BY f.at DESC,f.action_id DESC LIMIT (" + alias + ".revert_after->>'per')::int) f WHERE kind='recheck' OR outcome IN('MISMATCH','PARTIAL','FAILED')))"
}

func (s *Server) listAutopilot(w http.ResponseWriter, r *http.Request) {
	s.pagedList(w, r, listSpec{From: "autopilot_grants t", Tenant: "t.tenant_id", Time: "t.since", Key: "t.actor_id", ID: "t.actor_id", JSON: autopilotJSON("t")}, "", false)
}
func (s *Server) putAutopilot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operation string   `json:"operation"`
		Skip      []string `json:"skip_rules"`
		Below     struct {
			Pointer string  `json:"pointer"`
			Value   float64 `json:"value"`
		} `json:"below"`
		Revert struct {
			Slips int `json:"slips"`
			Per   int `json:"per"`
		} `json:"revert_after"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Operation == "" || len(in.Skip) == 0 || len(in.Skip) > 100 || !strings.HasPrefix(in.Below.Pointer, "/") || in.Below.Value <= 0 || in.Revert.Slips < 1 || in.Revert.Per < in.Revert.Slips || in.Revert.Per > 100000 {
		s.fail(w, r, 400, "INVALID_LIMIT", "Operation, rules, positive threshold, and valid slip window are required", nil)
		return
	}
	id := chi.URLParam(r, "actor_id")
	var out []byte
	err := s.auditTx(r, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(r.Context(), `SELECT count(*) FROM policy_rules WHERE tenant_id=$1 AND id::text=ANY($2) AND enabled AND deleted_at IS NULL AND outcome='REQUIRE_APPROVAL' AND operation IN('*',$3)`, tenant(r), in.Skip, in.Operation).Scan(&n); e != nil {
			return e
		}
		if n != len(in.Skip) {
			return errors.New("INVALID_RULE: skip_rules must refer to enabled approval rules for this operation")
		}
		return tx.QueryRow(r.Context(), `INSERT INTO autopilot_grants(tenant_id,actor_id,operation,skip_rules,below,revert_after)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,actor_id)DO UPDATE SET operation=EXCLUDED.operation,skip_rules=EXCLUDED.skip_rules,below=EXCLUDED.below,revert_after=EXCLUDED.revert_after,active=true,since=CASE WHEN autopilot_grants.active AND autopilot_grants.operation=EXCLUDED.operation AND autopilot_grants.skip_rules=EXCLUDED.skip_rules AND autopilot_grants.below=EXCLUDED.below AND autopilot_grants.revert_after=EXCLUDED.revert_after THEN autopilot_grants.since ELSE now() END,reverted_at=NULL RETURNING `+autopilotJSON("autopilot_grants"), tenant(r), id, in.Operation, in.Skip, jsonBytes(in.Below), jsonBytes(in.Revert)).Scan(&out)
	})
	s.respond(w, r, 200, json.RawMessage(out), err)
}
func (s *Server) deleteAutopilot(w http.ResponseWriter, r *http.Request) {
	s.deleteControl(w, r, "autopilot_grants", "actor_id", chi.URLParam(r, "actor_id"))
}

func (s *Server) patchPolicyRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled   *bool    `json:"enabled"`
		Threshold *float64 `json:"numeric_threshold"`
		Name      *string  `json:"name"`
		Version   *int     `json:"version"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Enabled == nil && in.Threshold == nil && in.Name == nil {
		s.fail(w, r, 400, "INVALID_RULE", "At least one editable field is required", nil)
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		s.fail(w, r, 400, "INVALID_RULE", "Name cannot be empty", nil)
		return
	}
	if m := r.Header.Get("If-Match"); m != "" {
		v, e := strconv.Atoi(strings.Trim(m, "\""))
		if e != nil || (in.Version != nil && *in.Version != v) {
			s.fail(w, r, 400, "INVALID_RULE", "Invalid or conflicting If-Match version", nil)
			return
		}
		in.Version = &v
	}
	var out []byte
	conflict := false
	err := s.auditTx(r, func(tx pgx.Tx) error {
		var version int
		if e := tx.QueryRow(r.Context(), `SELECT version FROM policy_rules WHERE tenant_id=$1 AND id::text=$2 AND deleted_at IS NULL FOR UPDATE`, tenant(r), chi.URLParam(r, "id")).Scan(&version); e != nil {
			return e
		}
		if in.Version != nil && *in.Version != version {
			conflict = true
			return errors.New("VERSION_CONFLICT")
		}
		return tx.QueryRow(r.Context(), `UPDATE policy_rules SET enabled=COALESCE($3,enabled),numeric_threshold=COALESCE($4,numeric_threshold),name=COALESCE($5,name),version=version+1 WHERE tenant_id=$1 AND id::text=$2 RETURNING to_jsonb(policy_rules)`, tenant(r), chi.URLParam(r, "id"), in.Enabled, in.Threshold, in.Name).Scan(&out)
	})
	if conflict {
		s.fail(w, r, 409, "VERSION_CONFLICT", "Rule changed since it was read", nil)
		return
	}
	s.respond(w, r, 200, json.RawMessage(out), err)
}
func (s *Server) deletePolicyRule(w http.ResponseWriter, r *http.Request) {
	err := s.auditTx(r, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `UPDATE policy_rules SET enabled=false,deleted_at=now(),version=version+1 WHERE tenant_id=$1 AND id::text=$2 AND deleted_at IS NULL`, tenant(r), chi.URLParam(r, "id"))
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
func (s *Server) clearEnforcement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operation string `json:"operation"`
		Rule      string `json:"rule"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Operation == "" {
		s.fail(w, r, 400, "INVALID_RULE", "operation is required", nil)
		return
	}
	if in.Rule == "" {
		in.Rule = "*"
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `DELETE FROM enforcement_rules WHERE tenant_id=$1 AND operation=$2 AND rule=$3`, tenant(r), in.Operation, in.Rule)
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
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	spec := listSpec{From: "audit_events t", Tenant: "t.tenant_id", Time: "t.at", Key: "t.id::text", ID: "t.id::text", JSON: "to_jsonb(t)-'tenant_id'", Status: "t.type", Actor: "t.by"}
	q := r.URL.Query()
	if v := q.Get("type"); v != "" {
		q.Set("status", v)
	}
	if v := q.Get("actor"); v != "" {
		q.Set("actor_id", v)
	}
	r.URL.RawQuery = q.Encode()
	s.pagedList(w, r, spec, "", false)
}
