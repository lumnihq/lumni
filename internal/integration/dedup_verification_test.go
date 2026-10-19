//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
	"testing"
)

func TestNativeDedupStillRequiresConfiguredVerification(t *testing.T) {
	s := scenarioServer(t)
	ctx := t.Context()
	in := input(s.fixture, "fixture.issue", "dedup-verified", "")
	in.ActionID = "original"
	d := s.check(t, in, protocol.DecisionAllow)
	if err := s.client.StartAction(ctx, d.ActionID, *d.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := s.client.ReportResult(ctx, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{"record":"retained"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.client.PutVerifier(ctx, protocol.Verifier{ID: "dedup-check", Name: "Independent dedup verification", Kind: "EVENT", Version: "1", Enabled: true, Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.SubmitRun(ctx, protocol.RunInput{Work: protocol.Work{ID: "dedup-work", ActorID: s.actor, Goal: "Verify a retained effect"}, Handler: "dedup", HandlerVersion: "1", Input: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	in.Verification = &protocol.OutcomeSpec{VerifierID: "dedup-check", Expected: json.RawMessage(`{"record":"retained"}`)}
	calls := 0
	w := lumni.NewWorker(s.client)
	w.Register("dedup", "1", func(rc *lumni.RunContext, _ json.RawMessage) (json.RawMessage, error) {
		return rc.Tool("issue", in, func(context.Context) (json.RawMessage, error) { calls++; return nil, nil })
	})
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var status, outcome string
	if err := s.pool.QueryRow(ctx, `SELECT w.status,o.id FROM work w JOIN outcomes o ON o.tenant_id=w.tenant_id AND o.work_id=w.id WHERE w.tenant_id=$1 AND w.id='dedup-work'`, s.tenant).Scan(&status, &outcome); err != nil || status != "WAITING" || calls != 0 {
		t.Fatalf("dedup skipped verification: %s calls=%d %v", status, calls, err)
	}
	if err := s.client.RecordEvidence(ctx, outcome, protocol.VerificationEvidence{ID: "confirmed", Status: "VERIFIED", Observed: json.RawMessage(`{"record":"retained","authoritative":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT status FROM work WHERE tenant_id=$1 AND id='dedup-work'`, s.tenant).Scan(&status); err != nil || status != "COMPLETED" || calls != 0 {
		t.Fatalf("verified dedup: %s calls=%d %v", status, calls, err)
	}
}
