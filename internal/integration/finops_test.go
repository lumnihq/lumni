//go:build integration

package integration

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"lumni/internal/core"
	"lumni/internal/resource"
	workerpkg "lumni/internal/worker"
	"lumni/pkg/protocol"
)

func TestFinOpsAttributesExactWasteCategories(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := t.Context()
	resourceID := resource.CanonicalID(protocol.ResourceRef{Type: "subscription", ExternalID: "finops"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence)
		VALUES($1,$2,'subscription','finops','default','1','etag',1)`, f.tenant, resourceID); err != nil {
		t.Fatal(err)
	}
	allowed, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "finops", "1"))
	if err != nil || allowed.Decision != protocol.DecisionAllow {
		t.Fatalf("allow: %+v err=%v", allowed, err)
	}
	if err := f.kernel.Start(ctx, f.tenant, allowed.ActionID, *allowed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, allowed.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: allowed.LeaseToken, ResourceVersions: map[string]string{resourceID: "2"}}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "finops", "2"))
	if err != nil || duplicate.Decision != protocol.DecisionDeduplicate {
		t.Fatalf("duplicate: %+v err=%v", duplicate, err)
	}
	stale, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.cancel", "finops", "1"))
	if err != nil || stale.Decision != protocol.DecisionReplan {
		t.Fatalf("stale: %+v err=%v", stale, err)
	}
	holder, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.pause", "finops", "2"))
	if err != nil || holder.Decision != protocol.DecisionAllow {
		t.Fatalf("holder: %+v err=%v", holder, err)
	}
	if err := f.kernel.Start(ctx, f.tenant, holder.ActionID, *holder.LeaseToken); err != nil {
		t.Fatal(err)
	}
	conflict, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.resume", "finops", "2"))
	if err != nil || conflict.Decision != protocol.DecisionWait {
		t.Fatalf("conflict: %+v err=%v", conflict, err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, holder.ActionID, protocol.ActionResultInput{Status: "FAILED", LeaseToken: holder.LeaseToken}); err != nil {
		t.Fatal(err)
	}

	service := core.New(f.pool)
	for _, actionID := range []string{duplicate.ActionID, stale.ActionID, conflict.ActionID} {
		if _, err := service.RecordCost(ctx, f.tenant, protocol.CostInput{ActionID: &actionID, WorkID: &f.work, Provider: "test", CostType: "MODEL", AmountMicrosUSD: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SetWorkStatus(ctx, f.tenant, f.work, protocol.WorkSuperseded); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordCost(ctx, f.tenant, protocol.CostInput{WorkID: &f.work, Provider: "test", CostType: "MODEL", AmountMicrosUSD: 100}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := workerpkg.New(f.pool, f.kernel, time.Second, logger, "integration-secret", "", "")
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var duplicateCost, staleCost, conflictCost, supersededCost int64
	if err := f.pool.QueryRow(ctx, `SELECT
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='DUPLICATE'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='STALE'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='CONFLICT'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='SUPERSEDED'),0)
		FROM costs WHERE tenant_id=$1`, f.tenant).Scan(&duplicateCost, &staleCost, &conflictCost, &supersededCost); err != nil {
		t.Fatal(err)
	}
	if duplicateCost != 100 || staleCost != 100 || conflictCost != 100 || supersededCost != 100 {
		t.Fatalf("waste attribution mismatch: duplicate=%d stale=%d conflict=%d superseded=%d", duplicateCost, staleCost, conflictCost, supersededCost)
	}
}
