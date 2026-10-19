//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/internal/coordination"
	"lumni/internal/core"
	"lumni/internal/resource"
	"lumni/internal/store"
	"lumni/pkg/protocol"
)

type fixture struct {
	pool                *pgxpool.Pool
	tenant, actor, work string
	kernel              *coordination.Kernel
}

func setup(t testing.TB, mode protocol.EnforcementMode) fixture {
	t.Helper()
	url := os.Getenv("LUMNI_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LUMNI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := store.Migrate(ctx, url, "up"); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	slug := "test-" + uuid.NewString()
	var tenant string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants(name,slug,enforcement_mode)VALUES('test',$1,$2)RETURNING id::text`, slug, mode).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	actor := "actor_" + uuid.NewString()
	work := "work_" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO actors(tenant_id,id,type)VALUES($1,$2,'AGENT')`, tenant, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO work(tenant_id,id,actor_id,goal,status)VALUES($1,$2,$3,'integration','RUNNING')`, tenant, work, actor); err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, tenant: tenant, actor: actor, work: work, kernel: coordination.New(pool, resource.NewResolver(pool), 30*time.Second)}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenant); err != nil {
			t.Errorf("delete integration tenant: %v", err)
		}
		pool.Close()
	})
	return f
}
func input(f fixture, operation, external, observed string) protocol.ActionCheckInput {
	var version *string
	if observed != "" {
		version = &observed
	}
	return protocol.ActionCheckInput{WorkID: f.work, ActorID: f.actor, Operation: operation, Arguments: json.RawMessage(`{"value":1}`), Resources: []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{Type: "subscription", ExternalID: external}, Mode: protocol.AccessWrite, ObservedVersion: version, VersionSource: "etag"}}, Risk: protocol.RiskHigh}
}

func multiInput(f fixture, operation string, externalIDs ...string) protocol.ActionCheckInput {
	resources := make([]protocol.ResourceIntent, 0, len(externalIDs))
	for _, externalID := range externalIDs {
		resources = append(resources, protocol.ResourceIntent{
			ResourceRef: protocol.ResourceRef{Type: "subscription", ExternalID: externalID},
			Mode:        protocol.AccessWrite,
		})
	}
	return protocol.ActionCheckInput{
		WorkID: f.work, ActorID: f.actor, Operation: operation,
		Arguments: json.RawMessage(`{"value":1}`), Resources: resources, Risk: protocol.RiskHigh,
	}
}

func TestShadowAndEnforcementLifecycle(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	ctx := context.Background()
	resourceID := resource.CanonicalID(protocol.ResourceRef{Type: "subscription", ExternalID: "sub-1"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence)VALUES($1,$2,'subscription','sub-1','default','19','etag',1)`, f.tenant, resourceID); err != nil {
		t.Fatal(err)
	}
	shadow, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "sub-1", "18"))
	if err != nil {
		t.Fatal(err)
	}
	if shadow.Decision != protocol.DecisionWouldReplan || shadow.Enforced {
		t.Fatalf("unexpected shadow decision %+v", shadow)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tenants SET enforcement_mode='ENFORCE' WHERE id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	stale, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "sub-1", "18"))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Decision != protocol.DecisionReplan {
		t.Fatalf("expected REPLAN, got %+v", stale)
	}
	allowed, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "sub-1", "19"))
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Decision != protocol.DecisionAllow || allowed.LeaseToken == nil || len(allowed.Leases) != 1 {
		t.Fatalf("unexpected allow %+v", allowed)
	}
	if err := f.kernel.Start(ctx, f.tenant, allowed.ActionID, *allowed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	conflict, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.cancel", "sub-1", "19"))
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Decision != protocol.DecisionWait {
		t.Fatalf("expected WAIT, got %+v", conflict)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, conflict.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED"}); !errors.Is(err, coordination.ErrInvalidActionTransition) {
		t.Fatalf("WAIT action accepted a result: %v", err)
	}
	resultID := "external-1"
	result := protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: allowed.LeaseToken, ExternalResultID: &resultID, ResourceVersions: map[string]string{resourceID: "20"}}
	if err := f.kernel.RecordResult(ctx, f.tenant, allowed.ActionID, result); err != nil {
		t.Fatal(err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, allowed.ActionID, result); err != nil {
		t.Fatalf("identical result retry failed: %v", err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, allowed.ActionID, protocol.ActionResultInput{Status: "FAILED", LeaseToken: allowed.LeaseToken}); !errors.Is(err, coordination.ErrResultPayloadMismatch) {
		t.Fatalf("mismatched result retry error=%v", err)
	}
	duplicate, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.upgrade", "sub-1", "20"))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Decision != protocol.DecisionDeduplicate || duplicate.ExistingActionID == nil {
		t.Fatalf("expected DEDUPLICATE, got %+v", duplicate)
	}
}

func TestConcurrentWriteAdmission(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := context.Background()
	first := multiInput(f, "subscription.cancel", "sub-race-a", "sub-race-b")
	second := multiInput(f, "subscription.upgrade", "sub-race-b", "sub-race-a")
	start := make(chan struct{})
	results := make(chan protocol.Decision, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, in := range []protocol.ActionCheckInput{first, second} {
		wg.Add(1)
		go func(value protocol.ActionCheckInput) {
			defer wg.Done()
			<-start
			d, err := f.kernel.Check(ctx, f.tenant, value)
			results <- d
			errs <- err
		}(in)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	allow, wait := 0, 0
	for d := range results {
		if d.Decision == protocol.DecisionAllow {
			allow++
		}
		if d.Decision == protocol.DecisionWait {
			wait++
		}
	}
	if allow != 1 || wait != 1 {
		t.Fatalf("allow=%d wait=%d", allow, wait)
	}
}

func TestMultiResourceReservationIsAtomicAndFencingMonotonic(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := context.Background()

	holder, err := f.kernel.Check(ctx, f.tenant, multiInput(f, "subscription.cancel", "atomic-a"))
	if err != nil || holder.Decision != protocol.DecisionAllow {
		t.Fatalf("holder admission: decision=%+v err=%v", holder, err)
	}
	blocked, err := f.kernel.Check(ctx, f.tenant, multiInput(f, "subscription.batch", "atomic-b", "atomic-a"))
	if err != nil || blocked.Decision != protocol.DecisionWait {
		t.Fatalf("multi-resource conflict: decision=%+v err=%v", blocked, err)
	}
	var blockedLeases int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM leases WHERE tenant_id=$1 AND action_id=$2`, f.tenant, blocked.ActionID).Scan(&blockedLeases); err != nil {
		t.Fatal(err)
	}
	if blockedLeases != 0 {
		t.Fatalf("blocked reservation acquired %d partial leases", blockedLeases)
	}
	atomicB := resource.CanonicalID(protocol.ResourceRef{Type: "subscription", ExternalID: "atomic-b"})
	var untouchedFence int64
	if err := f.pool.QueryRow(ctx, `SELECT fencing_token FROM resources WHERE tenant_id=$1 AND id=$2`, f.tenant, atomicB).Scan(&untouchedFence); err != nil {
		t.Fatal(err)
	}
	if untouchedFence != 0 {
		t.Fatalf("blocked reservation advanced an unrelated fence to %d", untouchedFence)
	}

	if err := f.kernel.Start(ctx, f.tenant, holder.ActionID, *holder.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, holder.ActionID, protocol.ActionResultInput{Status: "FAILED", LeaseToken: holder.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	granted, err := f.kernel.Check(ctx, f.tenant, multiInput(f, "subscription.batch", "atomic-b", "atomic-a"))
	if err != nil || granted.Decision != protocol.DecisionAllow || len(granted.Leases) != 2 {
		t.Fatalf("multi-resource admission: decision=%+v err=%v", granted, err)
	}
	fences := make(map[string]int64, len(granted.Leases))
	for _, lease := range granted.Leases {
		fences[lease.ResourceID] = lease.FencingToken
	}
	atomicA := resource.CanonicalID(protocol.ResourceRef{Type: "subscription", ExternalID: "atomic-a"})
	if fences[atomicA] != 2 || fences[atomicB] != 1 {
		t.Fatalf("unexpected fences: %v", fences)
	}
	if err := f.kernel.Start(ctx, f.tenant, granted.ActionID, *granted.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := f.kernel.RecordResult(ctx, f.tenant, granted.ActionID, protocol.ActionResultInput{Status: "FAILED", LeaseToken: granted.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	next, err := f.kernel.Check(ctx, f.tenant, multiInput(f, "subscription.retry", "atomic-a"))
	if err != nil || next.Decision != protocol.DecisionAllow || len(next.Leases) != 1 || next.Leases[0].FencingToken != 3 {
		t.Fatalf("fencing did not increase monotonically: decision=%+v err=%v", next, err)
	}
}

func TestConcurrentIdempotencyOwnershipAndMismatch(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := context.Background()
	key := "idem-" + uuid.NewString()
	in := input(f, "subscription.cancel", "idempotent", "")
	in.IdempotencyKey = &key
	start := make(chan struct{})
	decisions := make(chan protocol.Decision, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			decision, err := f.kernel.Check(ctx, f.tenant, in)
			decisions <- decision
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(decisions)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var actionID string
	for decision := range decisions {
		if actionID == "" {
			actionID = decision.ActionID
		} else if decision.ActionID != actionID {
			t.Fatalf("idempotency key acquired by multiple actions: %s and %s", actionID, decision.ActionID)
		}
	}

	mismatch := in
	mismatch.Arguments = json.RawMessage(`{"value":2}`)
	decision, err := f.kernel.Check(ctx, f.tenant, mismatch)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != protocol.DecisionReject || decision.ReasonCode != protocol.ReasonIdempotencyPayloadMismatch {
		t.Fatalf("expected deterministic idempotency rejection, got %+v", decision)
	}
}

func TestDuplicateEventsAreIdempotent(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	events := core.New(f.pool)
	envelope := protocol.EventEnvelope{SchemaVersion: protocol.SchemaVersion, EventID: "evt-" + uuid.NewString(), Type: "test.event", Source: "integration", OccurredAt: time.Now().UTC()}
	created, err := events.IngestEvent(t.Context(), f.tenant, envelope)
	if err != nil || !created {
		t.Fatalf("first event: created=%v err=%v", created, err)
	}
	created, err = events.IngestEvent(t.Context(), f.tenant, envelope)
	if err != nil || created {
		t.Fatalf("duplicate event: created=%v err=%v", created, err)
	}
}

func TestApprovalDoesNotHoldLease(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO policy_rules(tenant_id,name,operation,resource_type,minimum_risk,outcome,reason_code)VALUES($1,'high-risk','*','*','HIGH','REQUIRE_APPROVAL','APPROVAL_REQUIRED')`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	key := "approval-1"
	in := input(f, "refund.create", "payment-1", "")
	in.IdempotencyKey = &key
	decision, err := f.kernel.Check(ctx, f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != protocol.DecisionRequireApproval || decision.ApprovalID == nil {
		t.Fatalf("unexpected %+v", decision)
	}
	var leases int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM leases WHERE tenant_id=$1 AND action_id=$2`, f.tenant, decision.ActionID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("approval held %d leases", leases)
	}
	if err := f.kernel.ResolveApproval(ctx, f.tenant, *decision.ApprovalID, "test", "APPROVED"); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.kernel.Check(ctx, f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision != protocol.DecisionAllow {
		t.Fatalf("expected approved action to revalidate and allow: %+v", resumed)
	}
}

func TestApprovedActionRevalidatesStaleState(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	ctx := t.Context()
	_, err := f.pool.Exec(ctx, `INSERT INTO policy_rules(tenant_id,name,operation,resource_type,minimum_risk,outcome,reason_code)
		VALUES($1,'approval-stale','refund.create','payment','HIGH','REQUIRE_APPROVAL','APPROVAL_REQUIRED')`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	resourceID := resource.CanonicalID(protocol.ResourceRef{Type: "payment", ExternalID: "approval-payment"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence)
		VALUES($1,$2,'payment','approval-payment','default','1','etag',1)`, f.tenant, resourceID); err != nil {
		t.Fatal(err)
	}
	observed := "1"
	key := "approval-stale"
	in := protocol.ActionCheckInput{WorkID: f.work, ActorID: f.actor, Operation: "refund.create", Arguments: json.RawMessage(`{"amount":25}`), IdempotencyKey: &key, Risk: protocol.RiskHigh,
		Resources: []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{Type: "payment", ExternalID: "approval-payment"}, Mode: protocol.AccessWrite, ObservedVersion: &observed, VersionSource: "etag"}}}
	decision, err := f.kernel.Check(ctx, f.tenant, in)
	if err != nil || decision.Decision != protocol.DecisionRequireApproval || decision.ApprovalID == nil {
		t.Fatalf("approval: %+v err=%v", decision, err)
	}
	if err := f.kernel.ResolveApproval(ctx, f.tenant, *decision.ApprovalID, "test", "APPROVED"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE resources SET current_version='2' WHERE tenant_id=$1 AND id=$2`, f.tenant, resourceID); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.kernel.Check(ctx, f.tenant, in)
	if err != nil || resumed.Decision != protocol.DecisionReplan || resumed.ReasonCode != protocol.ReasonStaleObservation {
		t.Fatalf("approved stale retry: %+v err=%v", resumed, err)
	}
}

func TestLeaseLossDistinguishesUnstartedAndUnknownEffect(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	shortKernel := coordination.New(f.pool, resource.NewResolver(f.pool), 100*time.Millisecond)
	unstarted, err := shortKernel.Check(t.Context(), f.tenant, input(f, "subscription.reserve", "lease-loss", ""))
	if err != nil || unstarted.Decision != protocol.DecisionAllow {
		t.Fatalf("unstarted reservation: %+v err=%v", unstarted, err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := shortKernel.ExpireLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status protocol.ActionStatus
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE tenant_id=$1 AND id=$2`, f.tenant, unstarted.ActionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != protocol.ActionExpired {
		t.Fatalf("unstarted action status=%s", status)
	}

	started, err := shortKernel.Check(t.Context(), f.tenant, input(f, "subscription.mutate", "lease-loss", ""))
	if err != nil || started.Decision != protocol.DecisionAllow {
		t.Fatalf("started reservation: %+v err=%v", started, err)
	}
	if err := shortKernel.Start(t.Context(), f.tenant, started.ActionID, *started.LeaseToken); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := shortKernel.ExpireLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE tenant_id=$1 AND id=$2`, f.tenant, started.ActionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != protocol.ActionUnknown {
		t.Fatalf("started action status=%s", status)
	}
}
