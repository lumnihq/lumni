//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lumnihq/lumni/internal/api"
	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/compiled"
	"github.com/lumnihq/lumni/internal/contracts"
	"github.com/lumnihq/lumni/internal/durable"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func contractRule(kind, operation string) protocol.ContractRule {
	return protocol.ContractRule{ID: "r1", Type: kind, Slots: map[string]any{}, Binding: protocol.ContractBinding{Operation: operation, KeyPointer: "/order_id"}}
}
func createContract(t *testing.T, c consoleClient, rules []protocol.ContractRule, mode string) map[string]any {
	t.Helper()
	v := c.call(t, "POST", "/v1/contracts", protocol.ContractInput{Title: "Contract", Text: "Explicit contract rule", Rules: rules}, 201)
	if mode != "draft" {
		v = c.call(t, "PUT", "/v1/contracts/"+v["id"].(string)+"/mode", map[string]any{"mode": mode, "revision": v["revision"]}, 200)
	}
	return v
}
func checkedContract(t *testing.T, c consoleClient, id string, amount int) protocol.Decision {
	t.Helper()
	in := input(c.f, "stripe.refund", id, "")
	in.ActionID = id
	in.Arguments = json.RawMessage(fmt.Sprintf(`{"order_id":"one-order","amount":%d,"currency":"USD"}`, amount))
	d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestContractsWatchDoesNotRemoveEnforcedLeases(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	contract := createContract(t, c, []protocol.ContractRule{contractRule("once-per-key", "stripe.refund")}, "watch")
	for _, id := range []string{"watch-first", "watch-second"} {
		d := checkedContract(t, c, id, 100)
		if d.Decision != protocol.DecisionAllow || !d.Enforced || len(d.Leases) == 0 {
			t.Fatalf("watch bypassed enforced admission: %+v", d)
		}
	}
	id := contract["id"].(string)
	week := c.call(t, "GET", "/v1/contracts/"+id+"/week", nil, 200)
	if week["seen"] != float64(2) || week["would_stop"] != float64(1) {
		t.Fatalf("stats %v", week)
	}
	c.call(t, "GET", "/v1/contracts/"+id+"/runs?limit=1", nil, 200)
	c.call(t, "GET", "/v1/contracts/"+id+"/hook-status", nil, 200)
	c.call(t, "GET", "/v1/contracts/"+id+"/tests?lang=ts&action=watch-second", nil, 200)
	c.call(t, "PUT", "/v1/contracts/"+id+"/mode", map[string]any{"mode": "protect", "revision": 1}, 409)
	v := c.call(t, "PUT", "/v1/contracts/"+id+"/mode", map[string]any{"mode": "protect", "revision": contract["revision"]}, 200)
	d := checkedContract(t, c, "protect-third", 100)
	if d.Decision != protocol.DecisionReject || d.ReasonCode != "CONTRACT_DUPLICATE" {
		t.Fatalf("duplicate %+v", d)
	}
	paused := c.call(t, "PUT", "/v1/contracts/"+id+"/mode", map[string]any{"mode": "paused", "revision": v["revision"]}, 200)
	if paused["was"] != "protect" {
		t.Fatal("resume mode lost")
	}
	d = checkedContract(t, c, "paused-fourth", 100)
	if d.Decision != protocol.DecisionAllow {
		t.Fatalf("paused checked %+v", d)
	}
	c.call(t, "DELETE", "/v1/contracts/"+id, nil, 204)
	c.call(t, "GET", "/v1/contracts/"+id, nil, 404)
	var historical int
	if e := c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM action_contract_rules WHERE tenant_id=$1 AND contract_id=$2`, c.f.tenant, id).Scan(&historical); e != nil || historical != 3 {
		t.Fatalf("history %d %v", historical, e)
	}
}
func TestContractsConcurrentOnceAndRateReservations(t *testing.T) {
	for _, kind := range []string{"once-per-key", "rate-limit"} {
		t.Run(kind, func(t *testing.T) {
			c := newConsoleClient(t, protocol.ModeEnforce)
			rule := contractRule(kind, "stripe.refund")
			if kind == "rate-limit" {
				rule.Slots = map[string]any{"n": float64(2), "window": "day"}
			}
			createContract(t, c, []protocol.ContractRule{rule}, "protect")
			var allowed atomic.Int32
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					in := input(c.f, "stripe.refund", fmt.Sprintf("parallel-%s-%d", kind, i), "")
					in.ActionID = fmt.Sprintf("act-%s-%d", kind, i)
					in.Arguments = json.RawMessage(`{"order_id":"shared"}`)
					d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
					if e != nil {
						errs <- e
						return
					}
					if d.Decision == protocol.DecisionAllow {
						allowed.Add(1)
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				t.Error(e)
			}
			want := int32(1)
			if kind == "rate-limit" {
				want = 2
			}
			if allowed.Load() != want {
				t.Fatalf("admitted %d want %d", allowed.Load(), want)
			}
		})
	}
}
func TestContractsApprovalIsPinnedAndManualConfigurationSurvives(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	manual := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "manual-other", "operation": "other.operation", "outcome": "REJECT", "reason_code": "POLICY_REJECTED"}, 201)
	r := contractRule("approval-over", "stripe.refund")
	r.Binding.AmountPointer = "/amount"
	r.Binding.CurrencyPointer = "/currency"
	r.Binding.Currency = "USD"
	r.Slots["amount_minor"] = float64(500)
	ctr := createContract(t, c, []protocol.ContractRule{r}, "protect")
	d := checkedContract(t, c, "approval-first", 600)
	if d.ApprovalID == nil {
		t.Fatalf("approval missing %+v", d)
	}
	c.call(t, "POST", "/v1/approvals/"+*d.ApprovalID+"/approve", map[string]any{}, 200)
	d, e := c.f.kernel.Recheck(t.Context(), c.f.tenant, d.ActionID, "approved-recheck")
	if e != nil || d.Decision != protocol.DecisionAllow {
		t.Fatalf("recheck %+v %v", d, e)
	}
	c.call(t, "PATCH", "/v1/contracts/"+ctr["id"].(string), map[string]any{"title": "Renamed", "revision": ctr["revision"]}, 200)
	d = checkedContract(t, c, "approval-new-action", 600)
	if d.Decision != protocol.DecisionRequireApproval {
		t.Fatalf("old approval leaked %+v", d)
	}
	c.call(t, "DELETE", "/v1/contracts/"+ctr["id"].(string), nil, 204)
	var enabled bool
	if e = c.f.pool.QueryRow(t.Context(), `SELECT enabled FROM policy_rules WHERE tenant_id=$1 AND id=$2`, c.f.tenant, manual["id"]).Scan(&enabled); e != nil || !enabled {
		t.Fatalf("manual rule changed %v %v", enabled, e)
	}
}
func TestContractsReadGrantsAmountCapsAndRevocation(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer restricted-test" {
			t.Error("unexpected read transport")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"balance":1000,"editor":"human","edited":%q}`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	}))
	defer server.Close()
	binding := protocol.ToolBinding{ID: "balance", App: "stripe", Operation: "stripe.balance", Method: "GET", URL: server.URL, ReadOnly: true, Enabled: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Secret: "restricted-test", Objects: map[string][]string{"orders": {"balance", "editor", "edited"}}}
	c.call(t, "PUT", "/v1/tool-bindings/balance", binding, 200)
	list := c.call(t, "GET", "/v1/tool-bindings", nil, 200)
	if bytes.Contains(mustJSON(t, list), []byte("restricted-test")) {
		t.Fatal("credential exposed")
	}
	grant := c.call(t, "POST", "/v1/integrations/stripe/grants", map[string]any{"binding_id": "balance", "objects": binding.Objects}, 201)
	r := contractRule("amount-cap", "stripe.refund")
	r.Binding.AmountPointer = "/amount"
	r.Binding.CurrencyPointer = "/currency"
	r.Binding.Currency = "USD"
	r.Binding.ReadBindingID = "balance"
	r.Binding.ReadArguments = map[string]string{"id": "/order_id"}
	r.Binding.ValuePointer = "/balance"
	r.Slots["limit"] = "amount paid"
	ctr := createContract(t, c, []protocol.ContractRule{r}, "protect")
	d := checkedContract(t, c, "amount-first", 700)
	if d.Decision != protocol.DecisionAllow {
		t.Fatalf("first %+v", d)
	}
	d = checkedContract(t, c, "amount-over", 400)
	if d.ReasonCode != "CONTRACT_AMOUNT_CAP" || d.Decision != protocol.DecisionReject {
		t.Fatalf("cap %+v", d)
	}
	c.call(t, "DELETE", "/v1/integrations/stripe/grants/"+grant["id"].(string), nil, 204)
	d = checkedContract(t, c, "amount-revoked", 1)
	if d.Decision != protocol.DecisionWait || d.ReasonCode != "CONTRACT_EVIDENCE_UNAVAILABLE" {
		t.Fatalf("revoked %+v", d)
	}
	ready := c.call(t, "GET", "/v1/contracts/"+ctr["id"].(string)+"/needs", nil, 200)
	if ready["ready"] != false {
		t.Fatalf("revoked readiness %v", ready)
	}
}
func TestContractsComposeVerifiersAndEnforceDeadlines(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	for _, id := range []string{"base", "state1", "state2"} {
		c.call(t, "POST", "/v1/verifiers", protocol.Verifier{ID: id, Name: id, Kind: "EVENT", Version: "1", Config: json.RawMessage(`{}`), Enabled: true}, 201)
	}
	c.call(t, "PUT", "/v1/config/operation-verifiers", map[string]any{"operation": "stripe.refund", "verifier_id": "base", "expected_template": map[string]any{"status": "succeeded"}}, 200)
	for _, id := range []string{"state1", "state2"} {
		r := contractRule("end-state", "stripe.refund")
		r.Binding.VerifierID = id
		r.Binding.Expected = json.RawMessage(`{"status":"inactive"}`)
		r.Binding.WithinSeconds = 1
		createContract(t, c, []protocol.ContractRule{r}, "protect")
	}
	d := checkedContract(t, c, "verified-composite", 100)
	if d.Decision != protocol.DecisionAllow {
		t.Fatal(d)
	}
	var verifier string
	var expected map[string]any
	if e := c.f.pool.QueryRow(t.Context(), `SELECT verifier_id,expected FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&verifier, &expected); e != nil || len(expected) != 3 {
		t.Fatalf("composed assertions %v %v", expected, e)
	}
	var bound string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT verifier_id FROM operation_verifiers WHERE tenant_id=$1 AND operation='stripe.refund'`, c.f.tenant).Scan(&bound); e != nil || bound != "base" {
		t.Fatalf("manual verifier overwritten %s %v", bound, e)
	}
	if e := c.f.kernel.Start(t.Context(), c.f.tenant, d.ActionID, *d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if e := c.f.kernel.RecordResult(t.Context(), c.f.tenant, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{"id":"external"}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE action_results SET recorded_at=now()-interval '10 seconds' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); e != nil {
		t.Fatal(e)
	}
	c.call(t, "POST", "/v1/outcomes/out_"+d.ActionID+"/evidence", protocol.VerificationEvidence{ID: "late", Status: "VERIFIED", Observed: json.RawMessage(`{"status":"inactive"}`)}, 202)
	var status string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&status); e != nil || status != "FAILED" {
		t.Fatalf("deadline %s %v", status, e)
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

type fixedGenerator struct {
	program protocol.CompiledProgram
	calls   atomic.Int32
}

func (g *fixedGenerator) Generate(_ context.Context, in compiled.Synthesis) (protocol.CompiledProgram, error) {
	g.calls.Add(1)
	if len(in.Traces) < 1 {
		return protocol.CompiledProgram{}, fmt.Errorf("empty synthesis")
	}
	return g.program, nil
}
func compilerFixture(t *testing.T) (consoleClient, *compiled.Service, *fixedGenerator) {
	t.Helper()
	c := newConsoleClient(t, protocol.ModeEnforce)
	cipher, e := secrets.New("integration-secret")
	if e != nil {
		t.Fatal(e)
	}
	g := &fixedGenerator{program: protocol.CompiledProgram{Interpreter: compiled.Interpreter, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Nodes: []protocol.ProgramNode{{ID: "answer", Kind: "return", Value: protocol.Expression{Literal: json.RawMessage(`{"answer":"yes"}`)}}}}}
	c.server.Configure(api.RuntimeOptions{CompilationGenerator: g})
	s := compiled.New(c.f.pool, cipher, c.f.kernel, durable.New(c.f.pool, c.f.kernel, cipher))
	s.Generator = g
	c.call(t, "PUT", "/v1/config/compiler", map[string]any{"enabled": true, "cloud_processing_consent": true}, 200)
	return c, s, g
}
func addTrace(t *testing.T, c consoleClient, id string) {
	t.Helper()
	trace := protocol.Trace{ID: id, WorkID: c.f.work, ActorID: c.f.actor, HandlerVersion: "v1", Operation: "decision.refund", Input: json.RawMessage(`{"customer":"example"}`), Output: json.RawMessage(`{"answer":"yes"}`), Steps: []protocol.TraceStep{{ID: "model", Kind: "llm", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"answer":"yes"}`), DurationMS: 50}}, DurationMS: 50, CompletedAt: time.Now().UTC()}
	c.call(t, "POST", "/v1/traces", trace, 201)
}
func discoverCompile(t *testing.T, c consoleClient, s *compiled.Service) map[string]any {
	t.Helper()
	addTrace(t, c, "trace-one")
	addTrace(t, c, "trace-two")
	job := c.call(t, "POST", "/v1/compiled-paths/discover", map[string]any{}, 202)
	if e := s.Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	c.call(t, "GET", "/v1/jobs/"+job["id"].(string), nil, 200)
	suggestions := c.call(t, "GET", "/v1/compiled-paths/suggestions", nil, 200)
	p := suggestions["items"].([]any)[0].(map[string]any)
	c.call(t, "POST", "/v1/compiled-paths/"+p["id"].(string)+"/compile", map[string]any{"revision": p["revision"]}, 202)
	if e := s.Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	return c.call(t, "GET", "/v1/compiled-paths/"+p["id"].(string), nil, 200)
}
func TestCompiledPathLifecycleReplayAndIdempotentExecution(t *testing.T) {
	c, s, g := compilerFixture(t)
	p := discoverCompile(t, c, s)
	id := p["id"].(string)
	versions := p["versions"].([]any)
	first := versions[0].(map[string]any)
	if first["status"] != "candidate" || first["replay_complete"] != true {
		t.Fatalf("candidate %v", p)
	}
	train := first["synthesis_ids"].([]any)
	held := first["replay_ids"].([]any)
	if train[0] == held[0] {
		t.Fatal("held-out data leaked into synthesis")
	}
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 409)
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/shadow", map[string]any{"revision": p["revision"]}, 200)
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 409)
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/runs", map[string]any{"id": "caller", "actor_id": c.f.actor, "version": 1, "served_by": "llm", "duration_ms": 50}, 201)
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 409)
	addTrace(t, c, "trace-shadow")
	if e := s.ShadowOnce(t.Context()); e != nil {
		t.Fatal(e)
	}
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 200)
	input := protocol.CompiledInvocation{InvocationID: "invoke-once", ActorID: c.f.actor, Input: json.RawMessage(`{"customer":"example"}`)}
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/execute", input, 202)
	if e := s.RuntimeOnce(t.Context()); e != nil {
		t.Fatal(e)
	}
	run := c.call(t, "GET", "/v1/compiled-runs/invoke-once?include=output", nil, 200)
	if run["status"] != "COMPLETED" || run["output"].(map[string]any)["answer"] != "yes" {
		t.Fatalf("run %v", run)
	}
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/execute", input, 202)
	input.Input = json.RawMessage(`{"customer":"changed"}`)
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/execute", input, 409)
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/pause", map[string]any{"revision": p["revision"], "why": "test"}, 200)
	input.InvocationID = "paused-invocation"
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/execute", input, 202)
	run = c.call(t, "GET", "/v1/compiled-runs/paused-invocation", nil, 200)
	if run["status"] != "FALLBACK" || run["reason"] != "PAUSED" {
		t.Fatalf("pause %v", run)
	}
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/resume", map[string]any{"revision": p["revision"]}, 200)
	for _, path := range []string{"/v1/compiled-paths", "/v1/compiled-paths/totals", "/v1/compiled-paths/" + id + "/versions/1/replay", "/v1/compiled-paths/" + id + "/fallbacks", "/v1/compiled-paths/" + id + "/race"} {
		c.call(t, "GET", path, nil, 200)
	}
	if g.calls.Load() != 1 {
		t.Fatal("generation repeated")
	}
}
func TestAutomationScopesTenantIsolationAndPreviewNoWrites(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	r := contractRule("once-per-key", "stripe.refund")
	ctr := createContract(t, c, []protocol.ContractRule{r}, "draft")
	a := auth.New(c.f.pool)
	key, e := a.Create(t.Context(), c.f.tenant, protocol.APIKeyCreateInput{Name: "reader", Scopes: []string{"query"}})
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/v1/contracts", bytes.NewReader(mustJSON(t, protocol.ContractInput{Title: "No", Text: "No", Rules: []protocol.ContractRule{r}})))
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	rr := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("scope %d %s", rr.Code, rr.Body.String())
	}
	foreign := newConsoleClient(t, protocol.ModeEnforce)
	req = httptest.NewRequest("GET", "/v1/contracts/"+ctr["id"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+foreign.key)
	rr = httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("tenant %d", rr.Code)
	}
	var before, after int
	if e = c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1`, c.f.tenant).Scan(&before); e != nil {
		t.Fatal(e)
	}
	c.call(t, "POST", "/v1/contracts/preview", map[string]any{"rules": []protocol.ContractRule{r}, "fixture": map[string]any{"arguments": map[string]any{"order_id": "one"}, "prior_count": 1, "evaluated_at": time.Now().UTC()}}, 200)
	if e = c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1`, c.f.tenant).Scan(&after); e != nil || before != after {
		t.Fatal("preview wrote actions")
	}
	c.call(t, "GET", "/v1/contracts?limit=0", nil, 400)
}

func TestCompiledMutationWaitsForProofAndNeverRepeatsLostResponse(t *testing.T) {
	for _, mode := range []string{"ok", "lost", "schema"} {
		lost := mode == "lost"
		t.Run(mode, func(t *testing.T) {
			c, s, g := compilerFixture(t)
			var writes atomic.Int32
			tool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Idempotency-Key") == "" {
					t.Error("mutation boundary missing")
				}
				writes.Add(1)
				if lost {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if mode == "schema" {
					fmt.Fprint(w, `{"unexpected":true}`)
					return
				}
				fmt.Fprint(w, `{"id":"external-1","status":"succeeded"}`)
			}))
			defer tool.Close()
			c.call(t, "POST", "/v1/verifiers", protocol.Verifier{ID: "proof", Name: "proof", Kind: "EVENT", Version: "1", Config: json.RawMessage(`{}`), Enabled: true}, 201)
			binding := protocol.ToolBinding{ID: "write", App: "test", Operation: "test.refund", Method: "POST", URL: tool.URL, Enabled: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object","required":["id","status"]}`), Resources: []protocol.ResourceMapping{{Type: "order", Mode: protocol.AccessWrite, IDPointer: "/order_id"}}, EffectKeyFields: []string{"/order_id"}, VerifierID: "proof", Expected: json.RawMessage(`{"status":"succeeded"}`), IdempotencyHeader: "Idempotency-Key"}
			c.call(t, "PUT", "/v1/tool-bindings/write", binding, 200)
			args := json.RawMessage(`{"order_id":"order-1"}`)
			out := json.RawMessage(`{"id":"external-1","status":"succeeded"}`)
			g.program.Nodes = []protocol.ProgramNode{{ID: "write", Kind: "call", BindingID: "write", BindingVersion: 1, Value: protocol.Expression{Ref: "/input"}, Next: "done"}, {ID: "done", Kind: "return", Value: protocol.Expression{Ref: "/vars/write"}}}
			for i := 0; i < 2; i++ {
				c.call(t, "POST", "/v1/traces", protocol.Trace{ID: fmt.Sprintf("write-trace-%d", i), WorkID: c.f.work, ActorID: c.f.actor, HandlerVersion: "v1", Operation: "test.refund", Input: args, Output: out, Steps: []protocol.TraceStep{{ID: "write", Kind: "call", BindingID: "write", BindingVersion: 1, Input: args, Output: out}}, CompletedAt: time.Now().UTC()}, 201)
			}
			c.call(t, "POST", "/v1/compiled-paths/discover", map[string]any{}, 202)
			if e := s.Once(t.Context()); e != nil {
				t.Fatal(e)
			}
			p := c.call(t, "GET", "/v1/compiled-paths/suggestions", nil, 200)["items"].([]any)[0].(map[string]any)
			id := p["id"].(string)
			c.call(t, "POST", "/v1/compiled-paths/"+id+"/compile", map[string]any{"revision": p["revision"]}, 202)
			if e := s.Once(t.Context()); e != nil {
				t.Fatal(e)
			}
			p = c.call(t, "GET", "/v1/compiled-paths/"+id, nil, 200)
			p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/shadow", map[string]any{"revision": p["revision"]}, 200)
			c.call(t, "POST", "/v1/traces", protocol.Trace{ID: "write-shadow", WorkID: c.f.work, ActorID: c.f.actor, HandlerVersion: "v1", Operation: "test.refund", Input: args, Output: out, Steps: []protocol.TraceStep{{ID: "write", Kind: "call", BindingID: "write", BindingVersion: 1, Input: args, Output: out}}, CompletedAt: time.Now().UTC()}, 201)
			if e := s.ShadowOnce(t.Context()); e != nil {
				t.Fatal(e)
			}
			p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 200)
			if writes.Load() != 0 {
				t.Fatal("replay/shadow invoked a mutation")
			}
			c.call(t, "POST", "/v1/compiled-paths/"+id+"/execute", protocol.CompiledInvocation{InvocationID: "write-invoke", ActorID: c.f.actor, Input: args}, 202)
			if e := s.RuntimeOnce(t.Context()); e != nil {
				t.Fatal(e)
			}
			run := c.call(t, "GET", "/v1/compiled-runs/write-invoke", nil, 200)
			if run["status"] != "WAITING" || run["served_by"] != "code" || writes.Load() != 1 {
				t.Fatalf("unverified run %v writes %d", run, writes.Load())
			}
			if e := s.RuntimeOnce(t.Context()); e != nil {
				t.Fatal(e)
			}
			if writes.Load() != 1 {
				t.Fatal("uncertain call repeated")
			}
			if mode == "schema" {
				path := c.call(t, "GET", "/v1/compiled-paths/"+id, nil, 200)
				if path["status"] != "drifted" {
					t.Fatalf("missing schema drift %v", path)
				}
				if _, e := c.f.pool.Exec(t.Context(), `UPDATE compiled_drift SET open=false WHERE tenant_id=$1 AND path_id=$2`, c.f.tenant, id); e != nil {
					t.Fatal(e)
				}
			}
			var action string
			if e := c.f.pool.QueryRow(t.Context(), `SELECT id FROM actions WHERE tenant_id=$1 AND work_id=$2`, c.f.tenant, run["work_id"]).Scan(&action); e != nil {
				t.Fatal(e)
			}
			c.call(t, "POST", "/v1/outcomes/out_"+action+"/evidence", protocol.VerificationEvidence{ID: "proved", Status: "VERIFIED", Observed: out}, 202)
			if e := s.Runtime.Tick(t.Context()); e != nil {
				t.Fatal(e)
			}
			if e := s.RuntimeOnce(t.Context()); e != nil {
				t.Fatal(e)
			}
			run = c.call(t, "GET", "/v1/compiled-runs/write-invoke?include=output", nil, 200)
			if run["status"] != "COMPLETED" || writes.Load() != 1 {
				t.Fatalf("recovery %v writes %d", run, writes.Load())
			}
		})
	}
}
func TestCompiledRecompileRollbackAndStableLabels(t *testing.T) {
	c, s, g := compilerFixture(t)
	p := discoverCompile(t, c, s)
	id := p["id"].(string)
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/shadow", map[string]any{"revision": p["revision"]}, 200)
	addTrace(t, c, "live-shadow")
	if e := s.ShadowOnce(t.Context()); e != nil {
		t.Fatal(e)
	}
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 200)
	if _, e := c.f.pool.Exec(t.Context(), `INSERT INTO compiled_drift(tenant_id,path_id,id,kind,detail)VALUES($1,$2,'policy','policy','Changed policy')`, c.f.tenant, id); e != nil {
		t.Fatal(e)
	}
	g.program.Nodes[0].Value = protocol.Expression{Literal: json.RawMessage(`{"answer":"changed"}`)}
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/recompile", map[string]any{"revision": p["revision"]}, 202)
	if e := s.Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	p = c.call(t, "GET", "/v1/compiled-paths/"+id, nil, 200)
	addTrace(t, c, "new-shadow")
	if e := s.ShadowOnce(t.Context()); e != nil {
		t.Fatal(e)
	}
	replay := c.call(t, "GET", "/v1/compiled-paths/"+id+"/versions/2/replay", nil, 200)
	c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 409)
	for _, sample := range replay["samples"].([]any) {
		m := sample.(map[string]any)
		if m["result"] == "disagree" {
			c.call(t, "PUT", "/v1/compiled-paths/"+id+"/disagreements/"+m["id"].(string)+"/label", map[string]any{"right": "code"}, 200)
		}
	}
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/promote", map[string]any{"revision": p["revision"]}, 200)
	p = c.call(t, "POST", "/v1/compiled-paths/"+id+"/rollback", map[string]any{"revision": p["revision"]}, 200)
	if p["status"] != "drifted" {
		t.Fatalf("rollback did not reopen drift %v", p)
	}
}
func TestContractsOrderBeforeUsesVerifiedHistoricalAction(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "POST", "/v1/verifiers", protocol.Verifier{ID: "prior", Name: "prior", Kind: "EVENT", Version: "1", Config: json.RawMessage(`{}`), Enabled: true}, 201)
	in := input(c.f, "stripe.refund", "prior-resource", "")
	in.ActionID = "prior-action"
	in.Arguments = json.RawMessage(`{"order_id":"order-verified"}`)
	in.Verification = &protocol.OutcomeSpec{VerifierID: "prior", Expected: json.RawMessage(`{"ok":true}`)}
	d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.f.kernel.Start(t.Context(), c.f.tenant, d.ActionID, *d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if e = c.f.kernel.RecordResult(t.Context(), c.f.tenant, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{"ok":true}`)}); e != nil {
		t.Fatal(e)
	}
	c.call(t, "POST", "/v1/outcomes/out_prior-action/evidence", protocol.VerificationEvidence{ID: "prior-proof", Status: "VERIFIED", Observed: json.RawMessage(`{"ok":true}`)}, 202)
	rule := contractRule("order-before", "zendesk.close_ticket")
	rule.Binding.PredecessorOperation = "stripe.refund"
	createContract(t, c, []protocol.ContractRule{rule}, "protect")
	for i, key := range []string{"order-verified", "order-unverified"} {
		in = input(c.f, "zendesk.close_ticket", fmt.Sprintf("ticket-%d", i), "")
		in.ActionID = fmt.Sprintf("ticket-action-%d", i)
		in.Arguments = mustJSON(t, map[string]string{"order_id": key})
		d, e = c.f.kernel.Check(t.Context(), c.f.tenant, in)
		if e != nil {
			t.Fatal(e)
		}
		want := protocol.DecisionAllow
		if i == 1 {
			want = protocol.DecisionWait
		}
		if d.Decision != want {
			t.Fatalf("predecessor %s %+v", key, d)
		}
	}
}

func TestContractsWatchVerificationCannotChangeCanonicalOutcome(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"state":"active"}`) }))
	defer server.Close()
	c.call(t, "POST", "/v1/verifiers", protocol.Verifier{ID: "watch-reader", Name: "watch-reader", Kind: "HTTP", Version: "1", Config: mustJSON(t, map[string]any{"url": server.URL}), Enabled: true}, 201)
	r := contractRule("end-state", "stripe.refund")
	r.Binding.VerifierID = "watch-reader"
	r.Binding.Expected = json.RawMessage(`{"state":"inactive"}`)
	ctr := createContract(t, c, []protocol.ContractRule{r}, "watch")
	d := checkedContract(t, c, "watch-outcome", 100)
	var verifier *string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT verifier_id FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&verifier); e != nil || verifier != nil {
		t.Fatalf("Watch changed canonical verifier %v %v", verifier, e)
	}
	if e := c.f.kernel.Start(t.Context(), c.f.tenant, d.ActionID, *d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if e := c.f.kernel.RecordResult(t.Context(), c.f.tenant, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{}`)}); e != nil {
		t.Fatal(e)
	}
	cipher, _ := secrets.New("integration-secret")
	watch := contracts.New(c.f.pool, cipher)
	runtime := durable.New(c.f.pool, c.f.kernel, cipher)
	if e := watch.VerifyWatchOnce(t.Context(), runtime.VerifyAssertion); e != nil {
		t.Fatal(e)
	}
	var status, canonical string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT status FROM contract_assertions WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&status); e != nil || status != "MISMATCH" {
		t.Fatalf("Watch observation %s %v", status, e)
	}
	if e := c.f.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&canonical); e != nil || canonical != "UNKNOWN" {
		t.Fatalf("Watch changed canonical outcome %s %v", canonical, e)
	}
	c.call(t, "GET", "/v1/contracts/"+ctr["id"].(string)+"/runs", nil, 200)
	c.call(t, "GET", "/v1/contracts/"+ctr["id"].(string)+"/week", nil, 200)
}
func TestContractsIndependentProtectDeadlines(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	for _, id := range []string{"early", "later"} {
		c.call(t, "POST", "/v1/verifiers", protocol.Verifier{ID: id, Name: id, Kind: "EVENT", Version: "1", Config: json.RawMessage(`{}`), Enabled: true}, 201)
		r := contractRule("end-state", "stripe.refund")
		r.Binding.VerifierID = id
		r.Binding.Expected = json.RawMessage(`{"ok":true}`)
		r.Binding.WithinSeconds = 1
		if id == "later" {
			r.Binding.WithinSeconds = 300
		}
		createContract(t, c, []protocol.ContractRule{r}, "protect")
	}
	d := checkedContract(t, c, "independent-deadline", 100)
	if e := c.f.kernel.Start(t.Context(), c.f.tenant, d.ActionID, *d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if e := c.f.kernel.RecordResult(t.Context(), c.f.tenant, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{}`)}); e != nil {
		t.Fatal(e)
	}
	var first string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT verifier_id FROM contract_verification_deadlines WHERE tenant_id=$1 AND action_id=$2 AND within_seconds=1`, c.f.tenant, d.ActionID).Scan(&first); e != nil {
		t.Fatal(e)
	}
	c.call(t, "POST", "/v1/outcomes/out_"+d.ActionID+"/evidence", protocol.VerificationEvidence{ID: "partial-proof", Status: "PENDING", Observed: mustJSON(t, map[string]any{first: map[string]any{"verifier_id": first, "evidence": map[string]any{"status": "VERIFIED"}}})}, 202)
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE action_results SET recorded_at=now()-interval '10 seconds' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); e != nil {
		t.Fatal(e)
	}
	cipher, _ := secrets.New("integration-secret")
	runtime := durable.New(c.f.pool, c.f.kernel, cipher)
	if e := runtime.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	var status string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&status); e != nil || status != "PENDING" {
		t.Fatalf("later assertion inherited earlier deadline %s %v", status, e)
	}
}
func TestCompiledJobResumesCommittedArtifact(t *testing.T) {
	c, s, g := compilerFixture(t)
	addTrace(t, c, "resume-trace-one")
	addTrace(t, c, "resume-trace-two")
	c.call(t, "POST", "/v1/compiled-paths/discover", map[string]any{}, 202)
	if e := s.Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	p := c.call(t, "GET", "/v1/compiled-paths/suggestions", nil, 200)["items"].([]any)[0].(map[string]any)
	id := p["id"].(string)
	job := c.call(t, "POST", "/v1/compiled-paths/"+id+"/compile", map[string]any{"revision": p["revision"]}, 202)
	if _, e := c.f.pool.Exec(t.Context(), `INSERT INTO compiled_versions(tenant_id,path_id,v,status,program,program_hash,bindings,synthesis_ids,replay_ids,compilation_job)VALUES($1,$2,1,'candidate',$3,'fixture-hash','{}',ARRAY['resume-trace-one'],ARRAY['resume-trace-two'],$4)`, c.f.tenant, id, mustJSON(t, g.program), job["id"]); e != nil {
		t.Fatal(e)
	}
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE automation_jobs SET status='RUNNING',attempts=1,claim_token='dead-worker',claim_until=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, c.f.tenant, job["id"]); e != nil {
		t.Fatal(e)
	}
	if e := s.Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	if g.calls.Load() != 0 {
		t.Fatal("crash recovery regenerated a committed program")
	}
	v := c.call(t, "GET", "/v1/jobs/"+job["id"].(string), nil, 200)
	if v["status"] != "COMPLETED" {
		t.Fatalf("resumed job %v", v)
	}
}
func TestAutomationSessionCSRFAndRetention(t *testing.T) {
	c, s, _ := compilerFixture(t)
	req := httptest.NewRequest("PUT", "/v1/config/compiler", bytes.NewReader([]byte(`{"enabled":false,"cloud_processing_consent":false}`)))
	login := httptest.NewRequest("POST", "/v1/auth/session", nil)
	login.Header.Set("Authorization", "Bearer "+c.key)
	rr := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, login)
	for _, cookie := range rr.Result().Cookies() {
		req.AddCookie(cookie)
	}
	rr = httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("CSRF %d", rr.Code)
	}
	addTrace(t, c, "retention-trace")
	createContract(t, c, []protocol.ContractRule{contractRule("once-per-key", "stripe.refund")}, "watch")
	d := checkedContract(t, c, "retention-action", 100)
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE action_contract_rules SET evaluated_at=now()-interval '2 hours' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE path_traces SET created_at=now()-interval '2 hours' WHERE tenant_id=$1`, c.f.tenant); e != nil {
		t.Fatal(e)
	}
	s.Retention = time.Hour
	if e := s.Maintain(t.Context()); e != nil {
		t.Fatal(e)
	}
	c.call(t, "GET", "/v1/traces/retention-trace/payload", nil, 409)
	var retained bool
	if e := c.f.pool.QueryRow(t.Context(), `SELECT facts_ciphertext IS NOT NULL FROM action_contract_rules WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID).Scan(&retained); e != nil || retained {
		t.Fatalf("contract snapshot outlived retention: retained=%v err=%v", retained, e)
	}
}
