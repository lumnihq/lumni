package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

func (s *Server) durableRoutes(r chi.Router) {
	r.Post("/model-calls", s.withScope("ingest", s.modelCall))
	r.Get("/gateway/config", s.withScope("query", s.gatewayConfig))
	r.Put("/config/operation-verifiers", s.withScope("admin", s.operationVerifier))
	r.Post("/effects/resolve-identity", s.withScope("admin", s.resolveEffectIdentity))
	r.Post("/reconciliation/{id}/review", s.withScope("admission", s.reviewReconciliation))
	r.Get("/model-calls/{id}/payload", s.withScope("payload", s.modelPayload))
	r.Handle("/mcp", s.withScope("query", s.queryMCP().ServeHTTP))
	r.Post("/runtime/work", s.withScope("runtime", s.submitRun))
	r.Post("/runtime/claim", s.withScope("runtime", s.claimRun))
	r.Post("/runtime/work/{id}/heartbeat", s.withScope("runtime", s.heartbeatRun))
	r.Post("/runtime/work/{id}/finish", s.withScope("runtime", s.finishRun))
	r.Post("/runtime/work/{id}/cancel", s.withScope("runtime", s.cancelRun))
	r.Post("/runtime/work/{id}/signals", s.withScope("runtime", s.signalRun))
	r.Post("/runtime/work/{id}/steps/begin", s.withScope("runtime", s.beginStep))
	r.Post("/runtime/work/{id}/steps/{key}/start", s.withScope("runtime", s.startStep))
	r.Post("/runtime/work/{id}/steps/{key}/result", s.withScope("runtime", s.completeStep))
	r.Post("/actions/{id}/recheck", s.withScope("admission", s.recheckAction))
	r.Post("/actions/{id}/start-once", s.withScope("admission", s.startActionOnce))
	r.Get("/actions/{id}/previous-result", s.withScope("payload", s.previousResult))
	r.Post("/dependencies", s.withScope("admission", s.addDependency))
	r.Post("/verifiers", s.withScope("admin", s.putVerifier))
	r.Post("/outcomes/{id}/evidence", s.withScope("verification", s.recordEvidence))
	r.Post("/outcomes/{id}/retry", s.withScope("verification", s.retryVerification))
	r.Put("/config/enforcement", s.withScope("admin", s.putEnforcement))
	r.Post("/discovery/tools", s.withScope("ingest", s.discoverTool))
	r.Post("/discovery/confirm", s.withScope("admin", s.confirmMapping))
	r.Post("/reconciliation/handlers", s.withScope("admin", s.registerReconciliation))
	r.Post("/reconciliation", s.withScope("runtime", s.reconcile))
	for _, kind := range []string{"findings", "incidents", "outcomes", "verifiers", "steps", "journal", "waits", "reconciliation", "discovery", "dependencies", "enforcement", "activity", "effects", "step-attempts", "verification-attempts", "actions", "operation-verifiers", "model-calls", "reconciliation-handlers", "semantic-evaluations"} {
		r.Get("/"+kind, s.withScope("query", s.pagedDurable(kind)))
		r.Get("/"+kind+"/{id}", s.withScope("query", s.pagedDurable(kind)))
	}
}
func (s *Server) startActionOnce(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"lease_token"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	started, err := s.kernel.StartOnce(r.Context(), tenant(r), chi.URLParam(r, "id"), in.Token)
	s.respond(w, r, 200, map[string]bool{"started": started}, err)
}
func (s *Server) confirmMapping(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source          string                  `json:"source"`
		Tool            string                  `json:"tool"`
		Operation       string                  `json:"operation"`
		Resources       []resource.ResourceRule `json:"resources"`
		EffectKeyFields []string                `json:"effect_key_fields"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Source == "" || in.Tool == "" || in.Operation == "" || len(in.Resources) == 0 {
		s.respond(w, r, 0, nil, errors.New("source, tool, operation and resources are required"))
		return
	}
	for _, ref := range in.Resources {
		if ref.Type == "" || !strings.HasPrefix(ref.IDPointer, "/") || (!protocol.IsWrite(ref.Mode) && ref.Mode != protocol.AccessRead && ref.Mode != protocol.AccessObserve) {
			s.respond(w, r, 0, nil, errors.New("resource type, ID pointer and valid access mode are required"))
			return
		}
	}
	for _, field := range in.EffectKeyFields {
		if !strings.HasPrefix(field, "/") {
			s.respond(w, r, 0, nil, errors.New("effect identity fields must be JSON pointers"))
			return
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer tx.Rollback(r.Context())
	resources, _ := json.Marshal(in.Resources)
	fields, _ := json.Marshal(in.EffectKeyFields)
	proposal, _ := json.Marshal(in)
	_, err = tx.Exec(r.Context(), `INSERT INTO resource_rules(tenant_id,source,tool,operation,resources,effect_key_fields)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,source,tool)DO UPDATE SET operation=EXCLUDED.operation,resources=EXCLUDED.resources,effect_key_fields=EXCLUDED.effect_key_fields,version=resource_rules.version+1,updated_at=now()`, tenant(r), in.Source, in.Tool, strings.ToLower(in.Operation), resources, fields)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO discovered_tools(tenant_id,source,name,schema,proposal,confirmed)VALUES($1,$2,$3,'{}',$4,true)ON CONFLICT(tenant_id,source,name)DO UPDATE SET proposal=EXCLUDED.proposal,confirmed=true`, tenant(r), in.Source, in.Tool, proposal)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	s.respond(w, r, 200, map[string]bool{"confirmed": err == nil}, err)
}
func (s *Server) submitRun(w http.ResponseWriter, r *http.Request) {
	var in protocol.RunInput
	if !s.decode(w, r, &in) {
		return
	}
	id, err := s.durable.Submit(r.Context(), tenant(r), in)
	s.respond(w, r, 201, map[string]any{"id": id}, err)
}
func (s *Server) claimRun(w http.ResponseWriter, r *http.Request) {
	var in protocol.ClaimInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.durable.Claim(r.Context(), tenant(r), in)
	s.respond(w, r, 200, map[string]any{"claim": out}, err)
}
func (s *Server) heartbeatRun(w http.ResponseWriter, r *http.Request) {
	var in protocol.RuntimeAuth
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.Heartbeat(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.respond(w, r, 200, map[string]bool{"renewed": err == nil}, err)
}
func (s *Server) beginStep(w http.ResponseWriter, r *http.Request) {
	var in protocol.StepInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.durable.Begin(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.respond(w, r, 200, out, err)
}
func (s *Server) startStep(w http.ResponseWriter, r *http.Request) {
	var in protocol.StepResult
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.StartStep(r.Context(), tenant(r), chi.URLParam(r, "id"), chi.URLParam(r, "key"), in)
	s.respond(w, r, 200, map[string]bool{"started": err == nil}, err)
}
func (s *Server) completeStep(w http.ResponseWriter, r *http.Request) {
	var in protocol.StepResult
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.durable.CompleteStep(r.Context(), tenant(r), chi.URLParam(r, "id"), chi.URLParam(r, "key"), in)
	s.respond(w, r, 200, out, err)
}
func (s *Server) finishRun(w http.ResponseWriter, r *http.Request) {
	var in protocol.StepResult
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.Finish(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.respond(w, r, 200, map[string]bool{"finished": err == nil}, err)
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	err := s.durable.Cancel(r.Context(), tenant(r), chi.URLParam(r, "id"))
	s.respond(w, r, 200, map[string]bool{"cancelled": err == nil}, err)
}
func (s *Server) signalRun(w http.ResponseWriter, r *http.Request) {
	var in protocol.SignalInput
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.Signal(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.respond(w, r, 202, map[string]bool{"recorded": err == nil}, err)
}
func (s *Server) recheckAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.kernel.Recheck(r.Context(), tenant(r), chi.URLParam(r, "id"), in.ID)
	s.respond(w, r, 200, out, err)
}
func (s *Server) previousResult(w http.ResponseWriter, r *http.Request) {
	out, err := s.kernel.PreviousResult(r.Context(), tenant(r), chi.URLParam(r, "id"))
	s.respond(w, r, 200, map[string]any{"output": out}, err)
}
func (s *Server) addDependency(w http.ResponseWriter, r *http.Request) {
	var in protocol.DependencyInput
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.Dependency(r.Context(), tenant(r), in)
	s.respond(w, r, 201, map[string]bool{"created": err == nil}, err)
}
func (s *Server) putVerifier(w http.ResponseWriter, r *http.Request) {
	in := protocol.Verifier{Enabled: true, Version: "1"}
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.PutVerifierAs(r.Context(), tenant(r), in, s.principalName(r))
	in.Secret = ""
	s.respond(w, r, 201, in, err)
}
func (s *Server) recordEvidence(w http.ResponseWriter, r *http.Request) {
	var in protocol.VerificationEvidence
	if !s.decode(w, r, &in) {
		return
	}
	err := s.durable.Evidence(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.respond(w, r, 202, map[string]bool{"recorded": err == nil}, err)
}
func (s *Server) retryVerification(w http.ResponseWriter, r *http.Request) {
	attempt := "va_" + uuid.NewString()
	tag, err := s.pool.Exec(r.Context(), `UPDATE outcomes SET status='PENDING',attempts=0,next_attempt_at=now(),claim_until=NULL,retry_attempt_id=$3,retry_started_at=now(),updated_at=now() WHERE tenant_id=$1 AND id=$2 AND verifier_id IS NOT NULL AND status<>'VERIFIED' AND (claim_until IS NULL OR claim_until<=now())`, tenant(r), chi.URLParam(r, "id"), attempt)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	s.respond(w, r, 202, map[string]any{"queued": err == nil, "attempt_id": attempt}, err)
}
func (s *Server) putEnforcement(w http.ResponseWriter, r *http.Request) {
	var in protocol.EnforcementRule
	if !s.decode(w, r, &in) {
		return
	}
	if in.Mode != "ENFORCE" && in.Mode != "SHADOW" && in.Mode != "WARN" {
		s.fail(w, r, 400, "INVALID_MODE", "Mode must be SHADOW, WARN, or ENFORCE", nil)
		return
	}
	if in.Operation == "" {
		s.respond(w, r, 0, nil, errors.New("operation is required"))
		return
	}
	if in.Rule == "" {
		in.Rule = "*"
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `INSERT INTO enforcement_rules(tenant_id,operation,rule,mode)VALUES($1,$2,$3,$4)ON CONFLICT(tenant_id,operation,rule)DO UPDATE SET mode=EXCLUDED.mode`, tenant(r), in.Operation, in.Rule, in.Mode)
		return e
	})
	s.respond(w, r, 200, map[string]any{"operation": in.Operation, "rule": in.Rule, "mode": in.Mode, "effective_mode": in.Mode}, err)
}

func (s *Server) discoverTool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string          `json:"source"`
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Source == "" || in.Name == "" || !json.Valid(in.Schema) {
		s.respond(w, r, 0, nil, errors.New("source, name and schema are required"))
		return
	}
	proposal := map[string]any{"operation": strings.ReplaceAll(in.Name, "_", "."), "evidence_type": "MCP_SCHEMA", "confidence": 0.5, "requires_confirmation": true}
	b, _ := json.Marshal(proposal)
	_, err := s.pool.Exec(r.Context(), `INSERT INTO discovered_tools(tenant_id,source,name,schema,proposal)VALUES($1,$2,$3,$4,$5)ON CONFLICT(tenant_id,source,name)DO UPDATE SET confirmed=discovered_tools.confirmed AND discovered_tools.schema=EXCLUDED.schema,schema=EXCLUDED.schema,last_seen_at=now()`, tenant(r), in.Source, in.Name, in.Schema, b)
	s.respond(w, r, 201, proposal, err)
}
func (s *Server) registerReconciliation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Name == "" || in.Version == "" {
		s.respond(w, r, 0, nil, errors.New("name and version are required"))
		return
	}
	_, err := s.pool.Exec(r.Context(), `INSERT INTO reconciliation_handlers(tenant_id,name,version)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, tenant(r), in.Name, in.Version)
	s.respond(w, r, 201, in, err)
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID        string          `json:"id"`
		OutcomeID string          `json:"outcome_id"`
		Handler   string          `json:"handler"`
		Version   string          `json:"handler_version"`
		Input     json.RawMessage `json:"input"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.ID == "" || in.OutcomeID == "" || in.Handler == "" || in.Version == "" {
		s.respond(w, r, 0, nil, errors.New("id, outcome_id, handler and handler_version are required"))
		return
	}
	var registered bool
	err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM reconciliation_handlers WHERE tenant_id=$1 AND name=$2 AND version=$3)`, tenant(r), in.Handler, in.Version).Scan(&registered)
	if err == nil && !registered {
		err = errors.New("reconciliation handler is not registered")
	}
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	var parent, actor string
	err = s.pool.QueryRow(r.Context(), `SELECT o.work_id,w.actor_id FROM outcomes o JOIN work w ON w.tenant_id=o.tenant_id AND w.id=o.work_id WHERE o.tenant_id=$1 AND o.id=$2`, tenant(r), in.OutcomeID).Scan(&parent, &actor)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	run := protocol.RunInput{Work: protocol.Work{ID: "reconcile_" + in.ID, ActorID: actor, ParentWorkID: &parent, Goal: "Reconcile " + in.OutcomeID}, Handler: in.Handler, HandlerVersion: in.Version, Input: in.Input}
	id, err := s.durable.Submit(r.Context(), tenant(r), run)
	if err == nil {
		var same bool
		err = s.pool.QueryRow(r.Context(), `INSERT INTO reconciliation_jobs(tenant_id,id,outcome_id,work_id,handler,handler_version)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,id) DO UPDATE SET id=EXCLUDED.id RETURNING outcome_id=$3 AND work_id=$4 AND handler=$5 AND handler_version=$6`, tenant(r), in.ID, in.OutcomeID, id, in.Handler, in.Version).Scan(&same)
		if err == nil && !same {
			err = errors.New("RECONCILIATION_IDENTITY_MISMATCH")
		}
	}
	s.respond(w, r, 201, map[string]string{"work_id": id}, err)
}
