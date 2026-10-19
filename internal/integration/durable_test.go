//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lumni/internal/durable"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

func native(t *testing.T, f fixture) (*durable.Service, *protocol.RunClaim) {
	t.Helper()
	cipher, err := secrets.New("integration-runtime-key")
	if err != nil {
		t.Fatal(err)
	}
	s := durable.New(f.pool, f.kernel, cipher)
	_, err = s.Submit(context.Background(), f.tenant, protocol.RunInput{Work: protocol.Work{ID: "native", ActorID: f.actor, Goal: "durable test"}, Handler: "test", HandlerVersion: "1", Input: json.RawMessage(`{"secret":"customer-input"}`)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Claim(context.Background(), f.tenant, protocol.ClaimInput{WorkerID: "worker", Handlers: map[string]string{"test": "1"}})
	if err != nil || c == nil {
		t.Fatalf("claim: %+v %v", c, err)
	}
	return s, c
}
func owner(c *protocol.RunClaim) protocol.RuntimeAuth {
	return protocol.RuntimeAuth{Token: c.Token, Fence: c.Fence}
}

func TestDurableCompletedReplayAndSignal(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	s, c := native(t, f)
	ctx := context.Background()
	in := protocol.StepInput{RuntimeAuth: owner(c), Key: "reason", Type: "LLM", Input: json.RawMessage(`{"prompt":"private"}`)}
	st, err := s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartStep(ctx, f.tenant, c.WorkID, in.Key, protocol.StepResult{RuntimeAuth: owner(c), Attempt: st.Attempt}); err != nil {
		t.Fatal(err)
	}
	_, err = s.CompleteStep(ctx, f.tenant, c.WorkID, in.Key, protocol.StepResult{RuntimeAuth: owner(c), Attempt: st.Attempt, Status: "COMPLETED", Output: json.RawMessage(`{"answer":42}`)})
	if err != nil {
		t.Fatal(err)
	}
	st, err = s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil || !st.Replay || string(st.Output) != `{"answer":42}` {
		t.Fatalf("replay: %+v %v", st, err)
	}
	changed := in
	changed.Input = json.RawMessage(`{"prompt":"different"}`)
	if _, err = s.Begin(ctx, f.tenant, c.WorkID, changed); err == nil {
		t.Fatal("changed replay accepted")
	}
	w := protocol.StepInput{RuntimeAuth: owner(c), Key: "approval-signal", Type: "WAIT", Wait: &protocol.WaitInput{Type: "SIGNAL", Signal: "approved"}}
	st, err = s.Begin(ctx, f.tenant, c.WorkID, w)
	if err != nil || st.Status != "WAITING" {
		t.Fatalf("wait %+v %v", st, err)
	}
	if err = s.Heartbeat(ctx, f.tenant, c.WorkID, owner(c)); err == nil {
		t.Fatal("waiting work retained worker claim")
	}
	if err = s.Signal(ctx, f.tenant, c.WorkID, protocol.SignalInput{ID: "signal-1", Name: "approved", Payload: json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c, err = s.Claim(ctx, f.tenant, protocol.ClaimInput{WorkerID: "replacement", Handlers: map[string]string{"test": "1"}})
	if err != nil || c == nil {
		t.Fatalf("resume %v %v", c, err)
	}
	w.RuntimeAuth = owner(c)
	st, err = s.Begin(ctx, f.tenant, c.WorkID, w)
	if err != nil || !st.Replay || string(st.Output) != "true" {
		t.Fatalf("signal replay %+v %v", st, err)
	}
	var plaintext bool
	if err = f.pool.QueryRow(ctx, `SELECT position('customer-input'::bytea in input_ciphertext)>0 FROM work WHERE tenant_id=$1 AND id='native'`, f.tenant).Scan(&plaintext); err != nil || plaintext {
		t.Fatalf("encryption %v %v", plaintext, err)
	}
}

func TestTimeoutAfterWriteVerifiedWithoutRetry(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	s, c := native(t, f)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"refund-1","amount":100,"status":"succeeded"}`))
	}))
	defer server.Close()
	cfg, _ := json.Marshal(map[string]any{"url": server.URL})
	if err := s.PutVerifier(ctx, f.tenant, protocol.Verifier{ID: "refund", Name: "Refund readback", Kind: "HTTP", Version: "1", Enabled: true, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	action := input(f, "payment.refund", "payment-1", "")
	action.Arguments = json.RawMessage(`{"amount":100}`)
	action.Verification = &protocol.OutcomeSpec{VerifierID: "refund", Expected: json.RawMessage(`{"amount":100,"status":"succeeded"}`)}
	in := protocol.StepInput{RuntimeAuth: owner(c), Key: "refund", Type: "TOOL", Input: action.Arguments, Action: &action}
	st, err := s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "READY" {
		t.Fatalf("not ready %+v", st)
	}
	if err = s.StartStep(ctx, f.tenant, c.WorkID, in.Key, protocol.StepResult{RuntimeAuth: owner(c), Attempt: st.Attempt}); err != nil {
		t.Fatal(err)
	}
	st, err = s.CompleteStep(ctx, f.tenant, c.WorkID, in.Key, protocol.StepResult{RuntimeAuth: owner(c), Attempt: st.Attempt, Status: "UNKNOWN"})
	if err != nil || st.Status != "WAITING" {
		t.Fatalf("unknown %+v %v", st, err)
	}
	other := action
	other.ActionID = "other"
	other.WorkID = f.work
	d, err := f.kernel.Check(ctx, f.tenant, other)
	if err != nil || d.ReasonCode != protocol.ReasonActionResultUnknown {
		t.Fatalf("unknown hold %+v %v", d, err)
	}
	// Verification and wakeup are separate durable jobs. Wait for their observable
	// completion rather than assuming a fixed number of worker iterations.
	c = nil
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && c == nil; {
		if err = s.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		c, err = s.Claim(ctx, f.tenant, protocol.ClaimInput{WorkerID: "replacement", Handlers: map[string]string{"test": "1"}})
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err != nil || c == nil {
		var diagnostic []byte
		_ = f.pool.QueryRow(ctx, `SELECT jsonb_build_object('work',(SELECT jsonb_build_object('status',status,'resume_at',resume_at) FROM work WHERE tenant_id=$1 AND id='native'),'outcomes',(SELECT jsonb_agg(jsonb_build_object('id',id,'status',status,'attempts',attempts,'reason',reason)) FROM outcomes WHERE tenant_id=$1),'waits',(SELECT jsonb_agg(jsonb_build_object('type',type,'status',status)) FROM durable_waits WHERE tenant_id=$1))`, f.tenant).Scan(&diagnostic)
		t.Fatalf("resume %+v %v: %s", c, err, diagnostic)
	}
	in.RuntimeAuth = owner(c)
	st, err = s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil || !st.Replay {
		t.Fatalf("recovered %+v %v", st, err)
	}
	var attempts int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM step_attempts WHERE tenant_id=$1 AND step_id=$2`, f.tenant, st.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("external attempt count %d %v", attempts, err)
	}
	var result string
	if err = f.pool.QueryRow(ctx, `SELECT status FROM action_results WHERE tenant_id=$1 AND action_id=$2`, f.tenant, *st.ActionID).Scan(&result); err != nil || result != "UNKNOWN" {
		t.Fatalf("original result altered %s %v", result, err)
	}
}

func TestDurableFencingAndDependencies(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	s, c := native(t, f)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE work SET claim_expires_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, c.WorkID); err != nil {
		t.Fatal(err)
	}
	newClaim, err := s.Claim(ctx, f.tenant, protocol.ClaimInput{WorkerID: "new", Handlers: map[string]string{"test": "1"}})
	if err != nil || newClaim == nil {
		t.Fatalf("reclaim %v %v", newClaim, err)
	}
	if err = s.Heartbeat(ctx, f.tenant, c.WorkID, owner(c)); err == nil {
		t.Fatal("stale worker accepted")
	}
	if newClaim.Fence <= c.Fence {
		t.Fatal("fence not monotonic")
	}
	first := protocol.DependencyInput{ID: "dep1", SourceID: f.work, TargetID: c.WorkID, EntityType: "work", Relationship: "DEPENDS_ON"}
	if err = s.Dependency(ctx, f.tenant, first); err != nil {
		t.Fatal(err)
	}
	first.ID = "dep2"
	first.SourceID, first.TargetID = first.TargetID, first.SourceID
	if err = s.Dependency(ctx, f.tenant, first); err == nil {
		t.Fatal("dependency cycle accepted")
	}
	_ = time.Second
}
