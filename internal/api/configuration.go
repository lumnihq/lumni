package api

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/lumnihq/lumni/pkg/protocol"
	"net/http"
	"strings"
)

func (s *Server) operationVerifier(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operation  string          `json:"operation"`
		VerifierID string          `json:"verifier_id"`
		Expected   json.RawMessage `json:"expected_template"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	var expected map[string]any
	if in.Operation == "" || in.VerifierID == "" || json.Unmarshal(in.Expected, &expected) != nil || len(expected) == 0 {
		s.respond(w, r, 0, nil, errors.New("operation, verifier_id and nonempty expected_template required"))
		return
	}
	_, err := s.pool.Exec(r.Context(), `INSERT INTO operation_verifiers(tenant_id,operation,verifier_id,expected_template)VALUES($1,$2,$3,$4)ON CONFLICT(tenant_id,operation)DO UPDATE SET verifier_id=EXCLUDED.verifier_id,expected_template=EXCLUDED.expected_template,updated_at=now()`, tenant(r), strings.ToLower(in.Operation), in.VerifierID, in.Expected)
	s.respond(w, r, 200, in, err)
}
func (s *Server) resolveEffectIdentity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Legacy   string          `json:"legacy_key"`
		Current  string          `json:"current_key"`
		Evidence json.RawMessage `json:"evidence"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Legacy == "" || in.Current == "" || !json.Valid(in.Evidence) || string(in.Evidence) == "{}" {
		s.respond(w, r, 0, nil, errors.New("legacy_key, current_key and confirming evidence required"))
		return
	}
	tag, err := s.pool.Exec(r.Context(), `INSERT INTO effect_aliases(tenant_id,legacy_key,current_key,evidence)VALUES($1,$2,$3,$4)ON CONFLICT(tenant_id,legacy_key)DO UPDATE SET legacy_key=EXCLUDED.legacy_key WHERE effect_aliases.current_key=EXCLUDED.current_key AND effect_aliases.evidence=EXCLUDED.evidence`, tenant(r), in.Legacy, in.Current, in.Evidence)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("EFFECT_ALIAS_IDENTITY_MISMATCH")
	}
	s.respond(w, r, 200, map[string]bool{"resolved": err == nil}, err)
}
func (s *Server) gatewayConfig(w http.ResponseWriter, r *http.Request) {
	var mode protocol.EnforcementMode
	if err := s.pool.QueryRow(r.Context(), `SELECT enforcement_mode FROM tenants WHERE id=$1`, tenant(r)).Scan(&mode); err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT operation,mode FROM enforcement_rules WHERE tenant_id=$1 ORDER BY (mode='ENFORCE')`, tenant(r))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	modes := map[string]protocol.EnforcementMode{}
	for rows.Next() {
		var op string
		var m protocol.EnforcementMode
		if err = rows.Scan(&op, &m); err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		modes[op] = m
	}
	rows.Close()
	mapped, err := s.pool.Query(r.Context(), `SELECT tool,operation FROM resource_rules WHERE tenant_id=$1 AND enabled`, tenant(r))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer mapped.Close()
	for mapped.Next() {
		var tool, operation string
		if err = mapped.Scan(&tool, &operation); err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		if m, ok := modes[operation]; ok {
			modes[tool] = m
		} else {
			modes[tool] = mode
		}
	}
	s.respond(w, r, 200, map[string]any{"mode": mode, "operations": modes}, mapped.Err())
}
func (s *Server) reviewReconciliation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolution string `json:"resolution"`
		Reason     string `json:"reason"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Resolution != "ACCEPTED" && in.Resolution != "ESCALATED" && in.Resolution != "RESOLVED" {
		s.respond(w, r, 0, nil, errors.New("invalid review resolution"))
		return
	}
	tag, err := s.pool.Exec(r.Context(), `UPDATE incidents SET status=CASE WHEN $3='RESOLVED' THEN 'RESOLVED' ELSE status END,evidence=evidence||jsonb_build_object('operator_review',$3::text,'reason',$4::text),updated_at=now() WHERE tenant_id=$1 AND action_id IN(SELECT o.action_id FROM outcomes o JOIN reconciliation_jobs j ON j.tenant_id=o.tenant_id AND j.outcome_id=o.id WHERE j.tenant_id=$1 AND j.id=$2)`, tenant(r), chi.URLParam(r, "id"), in.Resolution, in.Reason)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("reconciliation incident not found")
	}
	s.respond(w, r, 200, map[string]bool{"reviewed": err == nil}, err)
}
