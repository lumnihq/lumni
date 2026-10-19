//go:build integration

package integration

import (
	"encoding/json"
	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/pkg/protocol"
	"strings"
	"testing"
)

func TestActionRetryPinsResourcesAndPermissions(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	in := input(f, "fixture.update", "original", "")
	in.ActionID = "stable-action"
	in.Resources[0].Mode = protocol.AccessRead
	if _, err := f.kernel.Check(t.Context(), f.tenant, in); err != nil {
		t.Fatal(err)
	}
	in.Resources[0].Mode = protocol.AccessWrite
	if _, err := f.kernel.Check(t.Context(), f.tenant, in); err == nil || !strings.Contains(err.Error(), "ACTION_IDENTITY_MISMATCH") {
		t.Fatalf("read decision reused for mutation: %v", err)
	}
	key := "stable-key"
	a := input(f, "fixture.update", "first", "")
	a.ActionID = "first"
	a.IdempotencyKey = &key
	if _, err := f.kernel.Check(t.Context(), f.tenant, a); err != nil {
		t.Fatal(err)
	}
	a.ActionID = "second"
	a.Resources[0].ExternalID = "different"
	d, err := f.kernel.Check(t.Context(), f.tenant, a)
	if err != nil || d.Decision != protocol.DecisionReject || d.ReasonCode != protocol.ReasonIdempotencyPayloadMismatch {
		t.Fatalf("idempotency accepted changed resource: %+v %v", d, err)
	}
}

func TestShadowIdempotencyRecordsEveryOccurrence(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	key := "shadow-key"
	in := input(f, "fixture.update", "shared", "")
	in.IdempotencyKey = &key
	for _, id := range []string{"first", "second"} {
		in.ActionID = id
		d, err := f.kernel.Check(t.Context(), f.tenant, in)
		if err != nil || d.ActionID != id {
			t.Fatalf("shadow correlation %+v %v", d, err)
		}
		if err = f.kernel.Start(t.Context(), f.tenant, id, ""); err != nil {
			t.Fatal(err)
		}
		if err = f.kernel.RecordResult(t.Context(), f.tenant, id, protocol.ActionResultInput{Status: "SUCCEEDED", Output: json.RawMessage(`{"ok":true}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var occurrences int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM effect_occurrences WHERE tenant_id=$1`, f.tenant).Scan(&occurrences); err != nil || occurrences != 2 {
		t.Fatalf("occurrences=%d %v", occurrences, err)
	}
	var owner string
	if err := f.pool.QueryRow(t.Context(), `SELECT action_id FROM idempotency_records WHERE tenant_id=$1 AND idempotency_key=$2`, f.tenant, key).Scan(&owner); err != nil || owner != "first" {
		t.Fatalf("owner changed: %s %v", owner, err)
	}
}

func TestObservationAndCostRetryIdentity(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	s := core.New(f.pool)
	in := protocol.ObservationInput{ID: "retry-observation", WorkID: f.work, Resource: protocol.ResourceRef{Type: "order", ExternalID: "retry"}, Version: "1", VersionSource: "etag"}
	first, err := s.Observe(t.Context(), f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Observe(t.Context(), f.tenant, in)
	if err != nil || second != first {
		t.Fatalf("observation retry: %s %v", second, err)
	}
	in.Version = "2"
	if _, err = s.Observe(t.Context(), f.tenant, in); err == nil || !strings.Contains(err.Error(), "OBSERVATION_IDENTITY_MISMATCH") {
		t.Fatalf("changed observation: %v", err)
	}
	cost := protocol.CostInput{ID: "retry-cost", WorkID: &f.work, Provider: "fixture", CostType: "LLM", AmountMicrosUSD: 100}
	first, err = s.RecordCost(t.Context(), f.tenant, cost)
	if err != nil {
		t.Fatal(err)
	}
	second, err = s.RecordCost(t.Context(), f.tenant, cost)
	if err != nil || second != first {
		t.Fatalf("cost retry: %s %v", second, err)
	}
	cost.AmountMicrosUSD = 200
	if _, err = s.RecordCost(t.Context(), f.tenant, cost); err == nil || !strings.Contains(err.Error(), "COST_IDENTITY_MISMATCH") {
		t.Fatalf("changed cost: %v", err)
	}
}
