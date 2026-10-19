//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
	"testing"
	"time"
)

func TestGoSDKWorkerResumesThroughRealAPI(t *testing.T) {
	s := scenarioServer(t)
	ctx := t.Context()
	if err := s.client.PutVerifier(ctx, protocol.Verifier{ID: "go-event", Name: "Go fixture event", Kind: "EVENT", Version: "1", Config: json.RawMessage(`{}`), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := s.client.SubmitRun(ctx, protocol.RunInput{Work: protocol.Work{ID: "go-worker", ActorID: s.actor, Goal: "SDK live recovery"}, Handler: "go-fixture", HandlerVersion: "1", Input: json.RawMessage(`{"private":"fixture"}`), ExpectedEffects: map[string]protocol.OutcomeSpec{"issue": {VerifierID: "go-event", Expected: json.RawMessage(`{"record":"saved"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	llmCalls, toolCalls, passes := 0, 0, 0
	handler := func(rc *lumni.RunContext, _ json.RawMessage) (json.RawMessage, error) {
		passes++
		if _, err := rc.LLM("reason", json.RawMessage(`{"prompt":"fixture"}`), func(context.Context) (json.RawMessage, error) {
			llmCalls++
			return json.RawMessage(`{"answer":42}`), nil
		}); err != nil {
			return nil, err
		}
		if _, err := rc.Wait("continue", protocol.WaitInput{Type: "SIGNAL", Signal: "continue"}); err != nil {
			return nil, err
		}
		action := input(s.fixture, "fixture.issue", "go-fixture", "")
		action.Arguments = json.RawMessage(`{"record":"saved"}`)
		return rc.Tool("issue", action, func(context.Context) (json.RawMessage, error) {
			toolCalls++
			return json.RawMessage(`{"record":"saved"}`), nil
		})
	}
	newWorker := func() *lumni.Worker { w := lumni.NewWorker(s.client); w.Register("go-fixture", "1", handler); return w }
	if _, err = newWorker().RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if llmCalls != 1 || toolCalls != 0 {
		t.Fatalf("initial calls: %d %d", llmCalls, toolCalls)
	}
	if err = s.client.SignalRun(ctx, "go-worker", protocol.SignalInput{ID: "continue", Name: "continue", Payload: json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	complete, evidence := false, false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err = s.runtime.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = newWorker().RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if toolCalls == 1 && !evidence {
			var outcome string
			if err = s.pool.QueryRow(ctx, `SELECT id FROM outcomes WHERE tenant_id=$1 AND work_id='go-worker' AND status='PENDING'`, s.tenant).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			if err = s.client.RecordEvidence(ctx, outcome, protocol.VerificationEvidence{ID: "external-callback", Status: "VERIFIED", Observed: json.RawMessage(`{"record":"saved"}`)}); err != nil {
				t.Fatal(err)
			}
			evidence = true
		}
		var state string
		if err = s.pool.QueryRow(ctx, `SELECT status FROM work WHERE tenant_id=$1 AND id='go-worker'`, s.tenant).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETED" {
			complete = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !complete || !evidence || llmCalls != 1 || toolCalls != 1 || passes < 3 {
		t.Fatalf("complete=%v evidence=%v llm=%d tool=%d passes=%d", complete, evidence, llmCalls, toolCalls, passes)
	}
}
