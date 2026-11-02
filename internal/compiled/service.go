// Package compiled implements versioned trace-to-program compilation and execution.
package compiled

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/coordination"
	"github.com/lumnihq/lumni/internal/durable"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type Synthesis struct {
	Traces      []protocol.Trace          `json:"traces"`
	Bindings    []protocol.ToolBinding    `json:"bindings"`
	Regressions []json.RawMessage         `json:"regressions"`
	Previous    *protocol.CompiledProgram `json:"previous,omitempty"`
}
type Generator interface {
	Generate(context.Context, Synthesis) (protocol.CompiledProgram, error)
}
type Service struct {
	Pool      *pgxpool.Pool
	Registry  *automation.Registry
	Cipher    *secrets.Manager
	Runtime   *durable.Service
	Kernel    *coordination.Kernel
	Generator Generator
	Retention time.Duration
}

func New(p *pgxpool.Pool, c *secrets.Manager, k *coordination.Kernel, d *durable.Service) *Service {
	return &Service{Pool: p, Registry: automation.New(p, c), Cipher: c, Runtime: d, Kernel: k, Generator: FromEnvironment(), Retention: 30 * 24 * time.Hour}
}
func shape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := map[string]any{}
		for k, a := range x {
			o[k] = shape(a)
		}
		return o
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{shape(x[0])}
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}
func (s *Service) Trace(ctx context.Context, tenant string, t protocol.Trace) error {
	if t.ID == "" || len(t.ID) > 200 || t.WorkID == "" || t.ActorID == "" || t.Operation == "" || t.HandlerVersion == "" || len(t.Steps) > 256 || t.CompletedAt.IsZero() || t.CompletedAt.After(time.Now().Add(time.Minute)) || t.DurationMS < 0 || t.CostMicrosUSD != nil && *t.CostMicrosUSD < 0 {
		return automation.Invalid("complete bounded trace identity, times and nonnegative measurements required")
	}
	if !json.Valid(t.Input) || !json.Valid(t.Output) || len(automation.JSON(t)) > 1<<20 {
		return automation.Invalid("valid trace JSON of at most 1 MiB required")
	}
	var workActor string
	if e := s.Pool.QueryRow(ctx, `SELECT actor_id FROM work WHERE tenant_id=$1 AND id=$2`, tenant, t.WorkID).Scan(&workActor); e != nil {
		return e
	}
	if workActor != t.ActorID {
		return automation.Invalid("trace actor does not own work")
	}
	signature := []any{t.ActorID, t.HandlerVersion, t.Operation}
	logical := []any{t.ActorID, t.HandlerVersion, t.Operation}
	seen := map[string]bool{}
	for _, step := range t.Steps {
		if step.ID == "" || seen[step.ID] || step.DurationMS < 0 || !contains([]string{"llm", "read", "call"}, step.Kind) || !json.Valid(step.Input) || !json.Valid(step.Output) {
			return automation.Invalid("invalid trace step")
		}
		seen[step.ID] = true
		if step.Kind != "llm" {
			b, e := s.Registry.Get(ctx, tenant, step.BindingID, step.BindingVersion)
			if e != nil {
				return e
			}
			if step.BindingVersion < 1 || b.ReadOnly != (step.Kind == "read") {
				return automation.Invalid("trace tool kind/version mismatch")
			}
			if e = automation.Validate(b.InputSchema, step.Input); e != nil {
				return e
			}
			if e = automation.Validate(b.OutputSchema, step.Output); e != nil {
				return e
			}
			logical = append(logical, []any{b.ID, step.Kind})
			signature = append(signature, []any{b.ID, b.Version, automation.Hash(json.RawMessage(b.InputSchema)), automation.Hash(json.RawMessage(b.OutputSchema))})
		}
		for _, ref := range []struct{ table, id string }{{"actions", step.ActionID}, {"steps", step.StepID}, {"model_calls", step.ModelCallID}} {
			if ref.id != "" {
				var ok bool
				e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+ref.table+` WHERE tenant_id=$1 AND work_id=$2 AND id=$3)`, tenant, t.WorkID, ref.id).Scan(&ok)
				if e != nil {
					return e
				}
				if !ok {
					return automation.Invalid("trace reference must belong to the same tenant and work")
				}
			}
		}
	}
	input, _ := automation.Decode(t.Input)
	output, _ := automation.Decode(t.Output)
	signature = append(signature, shape(input), shape(output))
	sig := automation.Hash(signature)
	plain, e := automation.Canonical(automation.JSON(t))
	if e != nil {
		return automation.Invalid("trace contains unsupported numbers")
	}
	cipher, e := s.Cipher.Encrypt(plain)
	if e != nil {
		return e
	}
	llmCalls := 0
	for _, step := range t.Steps {
		if step.Kind == "llm" {
			llmCalls++
		}
	}
	metadata := map[string]any{"llm_calls": llmCalls, "duration_ms": t.DurationMS, "cost_micros_usd": t.CostMicrosUSD, "steps": len(t.Steps), "logical_signature": automation.Hash(logical)}
	tag, e := s.Pool.Exec(ctx, `INSERT INTO path_traces(tenant_id,id,work_id,actor_id,operation,handler_version,signature,metadata,payload_ciphertext,payload_hash,input_hash,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)ON CONFLICT(tenant_id,id)DO NOTHING`, tenant, t.ID, t.WorkID, t.ActorID, t.Operation, t.HandlerVersion, sig, automation.JSON(metadata), cipher, automation.Hash(t), automation.Hash(json.RawMessage(t.Input)), t.CompletedAt)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		var hash string
		if e = s.Pool.QueryRow(ctx, `SELECT payload_hash FROM path_traces WHERE tenant_id=$1 AND id=$2`, tenant, t.ID).Scan(&hash); e != nil {
			return e
		}
		if hash != automation.Hash(t) {
			return automation.Conflict("trace identity mismatch")
		}
	}
	return nil
}
func (s *Service) ReadTrace(ctx context.Context, tenant, id string) (protocol.Trace, error) {
	var cipher []byte
	var t protocol.Trace
	e := s.Pool.QueryRow(ctx, `SELECT payload_ciphertext FROM path_traces WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&cipher)
	if e != nil {
		return t, e
	}
	if len(cipher) == 0 {
		return t, automation.Conflict("trace payload retention expired")
	}
	b, e := s.Cipher.Decrypt(cipher)
	if e == nil {
		e = json.Unmarshal(b, &t)
	}
	return t, e
}
func (s *Service) Enabled(ctx context.Context, tenant string) bool {
	var enabled bool
	e := s.Pool.QueryRow(ctx, `SELECT settings @> '{"compiler_enabled":true,"compiler_cloud_consent":true}'::jsonb FROM tenants WHERE id=$1`, tenant).Scan(&enabled)
	return e == nil && enabled && s.Generator != nil
}

type rowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) Get(ctx context.Context, tenant, id string) (map[string]any, error) {
	return s.get(ctx, s.Pool, tenant, id)
}
func (s *Service) get(ctx context.Context, db rowReader, tenant, id string) (map[string]any, error) {
	var data []byte
	e := db.QueryRow(ctx, `SELECT to_jsonb(p)-'tenant_id'||jsonb_build_object('versions',COALESCE((SELECT jsonb_agg(to_jsonb(ver)-'tenant_id' ORDER BY ver.v) FROM compiled_versions ver WHERE ver.tenant_id=p.tenant_id AND ver.path_id=p.id),'[]'),'drift',COALESCE((SELECT jsonb_agg(to_jsonb(d)-'tenant_id' ORDER BY d.created_at,d.id) FROM compiled_drift d WHERE d.tenant_id=p.tenant_id AND d.path_id=p.id),'[]'),'compiling',EXISTS(SELECT 1 FROM automation_jobs j WHERE j.tenant_id=p.tenant_id AND j.subject_id=p.id AND j.kind IN('compile','recompile') AND j.status IN('PENDING','RUNNING'))) FROM compiled_paths p WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&data)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, automation.Missing("compiled path not found")
	}
	if e != nil {
		return nil, e
	}
	var out map[string]any
	e = json.Unmarshal(data, &out)
	if e != nil {
		return nil, e
	}
	out["status"], out["can"] = state(out)
	var consent bool
	if e = db.QueryRow(ctx, `SELECT settings @> '{"compiler_enabled":true,"compiler_cloud_consent":true}'::jsonb FROM tenants WHERE id=$1`, tenant).Scan(&consent); e != nil {
		return nil, e
	}
	if !consent || s.Generator == nil {
		verbs := []string{}
		for _, v := range out["can"].([]string) {
			if v != "compile" && v != "recompile" {
				verbs = append(verbs, v)
			}
		}
		out["can"] = verbs
	}
	stats, e := s.stats(ctx, db, tenant, id, time.Now().UTC().Truncate(24*time.Hour), time.Now().UTC())
	if e != nil {
		return nil, e
	}
	out["stats"] = stats
	return out, nil
}
func state(p map[string]any) (string, []string) {
	live, pending, retired := false, "", false
	for _, v := range p["versions"].([]any) {
		m := v.(map[string]any)
		switch m["status"] {
		case "live":
			live = true
		case "candidate", "shadow":
			pending = m["status"].(string)
		case "retired":
			retired = true
		}
	}
	drift := false
	for _, v := range p["drift"].([]any) {
		m := v.(map[string]any)
		if m["open"] == true && m["fixed_by"] == nil {
			drift = true
		}
	}
	status := "watching"
	if pending != "" {
		status = pending
	}
	if live {
		status = "live"
		if drift {
			status = "drifted"
		}
	}
	if p["paused"] == true {
		status = "paused"
	}
	can := []string{}
	if p["compiling"] == true {
		return status, can
	}
	if !live && pending == "" {
		can = append(can, "compile")
	}
	if pending == "candidate" {
		can = append(can, "shadow")
	}
	if live {
		if p["paused"] == true {
			can = append(can, "resume")
		} else {
			can = append(can, "pause")
			if retired {
				can = append(can, "rollback")
			}
		}
	}
	if pending == "shadow" && p["paused"] != true {
		can = append(can, "promote")
	}
	if drift && pending == "" {
		can = append(can, "recompile")
	}
	return status, can
}
func (s *Service) Stats(ctx context.Context, tenant, id string, from, to time.Time) (map[string]any, error) {
	return s.stats(ctx, s.Pool, tenant, id, from, to)
}
func (s *Service) stats(ctx context.Context, db rowReader, tenant, id string, from, to time.Time) (map[string]any, error) {
	var data []byte
	e := db.QueryRow(ctx, StatisticsQuery, tenant, id, from, to).Scan(&data)
	if e != nil {
		return nil, e
	}
	var out map[string]any
	e = json.Unmarshal(data, &out)
	return out, e
}
func (s *Service) Enqueue(ctx context.Context, tenant, by, id, kind string, revision int) (map[string]any, error) {
	if kind != "discover" && !s.Enabled(ctx, tenant) {
		return nil, &automation.Fault{Status: 503, Code: "COMPILER_DISABLED", Message: "configure a generation model and enable tenant compiler consent"}
	}
	job := "job_" + uuid.NewString()
	e := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if kind != "discover" {
			var v int
			if e := tx.QueryRow(ctx, `SELECT revision FROM compiled_paths WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&v); e != nil {
				return e
			}
			if v != revision {
				return automation.Conflict("path revision mismatch")
			}
			p, e := s.get(ctx, tx, tenant, id)
			if e != nil {
				return e
			}
			if !contains(p["can"].([]string), kind) {
				return automation.Conflict("invalid lifecycle transition")
			}
			if _, e = tx.Exec(ctx, `UPDATE compiled_paths SET revision=revision+1,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO automation_jobs(tenant_id,id,kind,subject_id,input,by)VALUES($1,$2,$3,$4,$5,$6)`, tenant, job, kind, id, automation.JSON(map[string]int{"revision": revision + 1}), by)
		if e != nil {
			return e
		}
		return automation.Audit(ctx, tx, tenant, by, "path."+kind+".requested", id, nil, map[string]string{"job_id": job})
	})
	return map[string]any{"id": job, "status": "PENDING", "kind": kind, "subject_id": id}, e
}
func (s *Service) Transition(ctx context.Context, tenant, by, id, verb, why string, revision int) (map[string]any, error) {
	e := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var v int
		if e := tx.QueryRow(ctx, `SELECT revision FROM compiled_paths WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&v); e != nil {
			return e
		}
		if v != revision {
			return automation.Conflict("path revision mismatch")
		}
		p, e := s.get(ctx, tx, tenant, id)
		if e != nil {
			return e
		}
		if !contains(p["can"].([]string), verb) {
			return automation.Conflict("invalid lifecycle transition")
		}
		switch verb {
		case "pause", "resume":
			_, e = tx.Exec(ctx, `UPDATE compiled_paths SET paused=$3,paused_by=$4,pause_reason=$5,paused_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id, verb == "pause", by, why)
		case "shadow":
			_, e = tx.Exec(ctx, `UPDATE compiled_versions SET status='shadow' WHERE tenant_id=$1 AND path_id=$2 AND status='candidate'`, tenant, id)
		case "promote":
			var version int
			var complete bool
			var program []byte
			var bindings map[string]any
			var raw []byte
			if e = tx.QueryRow(ctx, `SELECT v,replay_complete,program,bindings FROM compiled_versions WHERE tenant_id=$1 AND path_id=$2 AND status='shadow' FOR UPDATE`, tenant, id).Scan(&version, &complete, &program, &raw); e != nil {
				return e
			}
			if !complete {
				return automation.Conflict("held-out replay required")
			}
			if e = json.Unmarshal(raw, &bindings); e != nil {
				return e
			}
			if e = s.checkBindings(ctx, tx, tenant, bindings); e != nil {
				return e
			}
			var shadow, unreviewed int
			if e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='shadow' AND result<>'out'),count(*) FILTER(WHERE result='disagree' AND label IS NULL) FROM compiled_replay_cases WHERE tenant_id=$1 AND path_id=$2 AND version=$3`, tenant, id, version).Scan(&shadow, &unreviewed); e != nil {
				return e
			}
			if shadow < 1 || unreviewed > 0 {
				return automation.Conflict("shadow evidence and reviewed disagreements required")
			}
			if _, e = tx.Exec(ctx, `UPDATE compiled_versions SET status='retired' WHERE tenant_id=$1 AND path_id=$2 AND status='live'`, tenant, id); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE compiled_versions SET status='live',promoted_by=$4 WHERE tenant_id=$1 AND path_id=$2 AND v=$3`, tenant, id, version, by); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE compiled_drift SET open=false,fixed_by=$3 WHERE tenant_id=$1 AND path_id=$2 AND open`, tenant, id, version)
		case "rollback":
			var live, prev int
			if e = tx.QueryRow(ctx, `SELECT max(v)FILTER(WHERE status='live'),max(v)FILTER(WHERE status='retired') FROM compiled_versions WHERE tenant_id=$1 AND path_id=$2`, tenant, id).Scan(&live, &prev); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE compiled_versions SET status='rolled-back' WHERE tenant_id=$1 AND path_id=$2 AND v=$3`, tenant, id, live); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE compiled_versions SET status='live',promoted_by=$4 WHERE tenant_id=$1 AND path_id=$2 AND v=$3`, tenant, id, prev, by); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE compiled_drift SET open=true,fixed_by=NULL WHERE tenant_id=$1 AND path_id=$2 AND fixed_by=$3`, tenant, id, live)
		}
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE compiled_paths SET revision=revision+1,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id); e != nil {
			return e
		}
		return automation.Audit(ctx, tx, tenant, by, "path."+verb, id, nil, map[string]any{"revision": revision + 1, "why": why})
	})
	if e != nil {
		return nil, e
	}
	return s.Get(ctx, tenant, id)
}
func (s *Service) CheckBindings(ctx context.Context, tenant string, pins map[string]any) error {
	return s.checkBindings(ctx, s.Pool, tenant, pins)
}
func (s *Service) checkBindings(ctx context.Context, db rowReader, tenant string, pins map[string]any) error {
	for id, hash := range pins {
		var raw []byte
		var enabled, granted bool
		e := db.QueryRow(ctx, `SELECT b.document,b.enabled,EXISTS(SELECT 1 FROM integration_read_grants g WHERE g.tenant_id=b.tenant_id AND g.binding_id=b.id AND g.binding_version=b.version AND g.revoked_at IS NULL AND g.objects @> (b.document->'objects')) FROM tool_bindings b WHERE b.tenant_id=$1 AND b.id=$2`, tenant, id).Scan(&raw, &enabled, &granted)
		if e != nil {
			return e
		}
		var b protocol.ToolBinding
		if e = json.Unmarshal(raw, &b); e != nil {
			return e
		}
		if !enabled || automation.Hash(b) != hash {
			return automation.Conflict("binding or policy fingerprint changed")
		}
		if b.ReadOnly && !granted {
			return automation.Conflict("read capability revoked")
		}
	}
	return nil
}
func (s *Service) bindings(ctx context.Context, tenant string, p protocol.CompiledProgram) (map[string]any, error) {
	pins := map[string]any{}
	for _, n := range p.Nodes {
		if n.Kind != "read" && n.Kind != "call" {
			continue
		}
		b, e := s.Registry.Get(ctx, tenant, n.BindingID, n.BindingVersion)
		if e != nil {
			return nil, e
		}
		if b.ReadOnly != (n.Kind == "read") {
			return nil, automation.Invalid("binding instruction mismatch")
		}
		if b.ReadOnly && !s.Registry.HasGrant(ctx, tenant, b) {
			return nil, automation.Conflict("read capability required")
		}
		pins[b.ID] = automation.Hash(b)
	}
	return pins, nil
}
func ids(traces []protocol.Trace) []string {
	out := []string{}
	for _, t := range traces {
		out = append(out, t.ID)
	}
	return out
}
func sortTraces(t []protocol.Trace) {
	sort.Slice(t, func(i, j int) bool {
		if t[i].CompletedAt.Equal(t[j].CompletedAt) {
			return t[i].ID < t[j].ID
		}
		return t[i].CompletedAt.Before(t[j].CompletedAt)
	})
}
func (s *Service) Label(ctx context.Context, tenant, by, path, id, right string) error {
	if !contains([]string{"llm", "code"}, right) {
		return automation.Invalid("right must be llm or code")
	}
	return automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE compiled_replay_cases SET label=$4,reviewed_by=$5 WHERE tenant_id=$1 AND path_id=$2 AND id=$3 AND result='disagree'`, tenant, path, id, right, by)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return automation.Missing("disagreement not found")
		}
		return automation.Audit(ctx, tx, tenant, by, "path.case.reviewed", id, nil, map[string]string{"right": right})
	})
}
func errorCode(e error) string {
	var f *automation.Fault
	if errors.As(e, &f) {
		return f.Code
	}
	return "AUTOMATION_JOB_FAILED"
}

// StatisticsQuery keeps observed execution separate from estimates against a
// comparable retained input. Missing baseline/pricing data stays null.
const StatisticsQuery = `WITH selected AS(
 SELECT r.*,baseline.metadata baseline,COALESCE((SELECT sum(c.amount_micros_usd) FROM costs c WHERE c.tenant_id=r.tenant_id AND c.work_id=r.work_id),r.cost_micros_usd) actual_cost
 FROM compiled_runs r LEFT JOIN LATERAL(
 SELECT pt.metadata FROM compiled_replay_cases rc JOIN path_traces pt ON pt.tenant_id=rc.tenant_id AND pt.id=rc.trace_id
 WHERE rc.tenant_id=r.tenant_id AND rc.path_id=r.path_id AND rc.version=r.version AND rc.result='agree' AND pt.input_hash=r.request_hash
 ORDER BY pt.completed_at DESC,pt.id DESC LIMIT 1) baseline ON true
 WHERE r.tenant_id=$1 AND ($2='' OR r.path_id=$2) AND r.created_at>=$3 AND r.created_at<$4)
 SELECT jsonb_build_object('runs',count(*),'served',count(*) FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported),
 'llm',count(*) FILTER(WHERE served_by='llm'),'fallback',count(*) FILTER(WHERE served_by='fallback'),'reported_runs',count(*) FILTER(WHERE reported),
 'hit',count(*) FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported)::numeric/NULLIF(count(*)FILTER(WHERE NOT reported),0),
 'duration_ms',avg(duration_ms) FILTER(WHERE status='COMPLETED' AND NOT reported),'cost_micros_usd',sum(actual_cost),
 'ms_before',avg((baseline->>'duration_ms')::numeric)FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported),
 'cost_before_micros_usd',avg((baseline->>'cost_micros_usd')::numeric)FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported),
 'estimated_savings_micros_usd',sum(GREATEST(0,(baseline->>'cost_micros_usd')::bigint-actual_cost)) FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported AND baseline->>'cost_micros_usd' IS NOT NULL AND actual_cost IS NOT NULL),
 'calls_saved',sum((baseline->>'llm_calls')::int)FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported),
 'time_saved_ms',sum(GREATEST(0,(baseline->>'duration_ms')::bigint-duration_ms))FILTER(WHERE served_by='code' AND status='COMPLETED' AND NOT reported AND baseline->>'duration_ms' IS NOT NULL AND duration_ms IS NOT NULL),
 'comparison_is_estimated',true) FROM selected`

// StatisticsProjection substitutes only server-owned SQL expressions, never request text.
func StatisticsProjection(tenant, path, from, to string) string {
	return strings.NewReplacer("$1", tenant, "$2", path, "$3", from, "$4", to).Replace(StatisticsQuery)
}
