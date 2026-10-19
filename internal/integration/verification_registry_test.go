//go:build integration

package integration

import (
	"encoding/json"
	"lumni/pkg/protocol"
	"os"
	"testing"
	"time"
)

func TestPostgresAndCompositeVerifierRegistry(t *testing.T) {
	s := scenarioServer(t)
	ctx := t.Context()
	pg := protocol.Verifier{ID: "database-check", Name: "Parameterized authoritative assertion", Kind: "POSTGRES", Version: "1", Enabled: true, Secret: os.Getenv("LUMNI_TEST_DATABASE_URL"), Config: json.RawMessage(`{"query":"SELECT jsonb_build_object('amount', $1::integer)"}`)}
	if err := s.runtime.PutVerifier(ctx, s.tenant, pg); err != nil {
		t.Fatal(err)
	}
	composite := protocol.Verifier{ID: "composite", Name: "Composite outcome", Kind: "COMPOSITE", Version: "1", Enabled: true, Config: json.RawMessage(`{"verifiers":["database-check"]}`)}
	if err := s.runtime.PutVerifier(ctx, s.tenant, composite); err != nil {
		t.Fatal(err)
	}
	changed := pg
	changed.Version = "2"
	changed.Config = json.RawMessage(`{"query":"SELECT '{}'::jsonb"}`)
	if err := s.runtime.PutVerifier(ctx, s.tenant, changed); err == nil {
		t.Fatal("composite child verifier changed identity")
	}
	in := input(s.fixture, "fixture.issue", "assertion", "")
	in.ActionID = "assertion-action"
	in.Verification = &protocol.OutcomeSpec{VerifierID: "composite", Expected: json.RawMessage(`{"database-check":{"params":[125],"amount":125}}`)}
	d := s.check(t, in, protocol.DecisionAllow)
	if err := s.client.StartAction(ctx, d.ActionID, *d.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := s.client.ReportResult(ctx, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{"accepted":true}`)}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, s.tenant, d.ActionID).Scan(&state); err != nil || state != "PENDING" {
		t.Fatalf("transport auto-verified: %s %v", state, err)
	}
	var observed []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && state == "PENDING"; {
		if err := s.runtime.VerifyOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.pool.QueryRow(ctx, `SELECT status,observed FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, s.tenant, d.ActionID).Scan(&state, &observed); err != nil {
			t.Fatal(err)
		}
		if state == "PENDING" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if state != "VERIFIED" {
		t.Fatalf("assertion: %s %s", state, observed)
	}
	var data map[string]struct {
		Version string `json:"verifier_version"`
	}
	if err := json.Unmarshal(observed, &data); err != nil || data["database-check"].Version != "1" {
		t.Fatalf("missing verifier identity: %s %v", observed, err)
	}
}
