//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"lumni/internal/durable"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

func TestConsoleWorkspaceBudgetCannotBeOverriddenByActor(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 1000000, "when_reached": "HOLD"}, 200)
	c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 2000000, "when_reached": "SLOW", "scope": map[string]any{"actor_id": c.f.actor}}, 200)
	_, err := c.f.pool.Exec(t.Context(), `INSERT INTO costs(tenant_id,id,work_id,provider,cost_type,amount_micros_usd,observed_at)VALUES($1,gen_random_uuid(),$2,'fixture','MODEL',1000000,now())`, c.f.tenant, c.f.work)
	if err != nil {
		t.Fatal(err)
	}
	d := c.check(t, "workspace-budget")
	if d.Decision != protocol.DecisionReject || d.ReasonCode != protocol.ReasonBudgetPacing {
		t.Fatalf("workspace budget bypassed: %+v", d)
	}
	c.call(t, "POST", "/v1/config/pause", map[string]any{"paused": true}, 200)
	in := input(c.f, "stripe.refund", "unresolved", "")
	d, err = c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if err != nil || d.Decision != protocol.DecisionWait || d.ReasonCode != protocol.ReasonActorPaused {
		t.Fatalf("workspace pause: %+v %v", d, err)
	}
}

func TestConsoleObservedCostWindowAndOtherResources(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	for i := 0; i < 3; i++ {
		c.check(t, fmt.Sprintf("resource-%d", i))
	}
	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Second)
	_, err := c.f.pool.Exec(t.Context(), `INSERT INTO costs(tenant_id,id,action_id,work_id,provider,cost_type,amount_micros_usd,observed_at)VALUES($1,gen_random_uuid(),'resource-0',$2,'fixture','TOOL',900000,$3::timestamptz-interval '1 second'),($1,gen_random_uuid(),'resource-0',$2,'fixture','TOOL',123456,$4),($1,gen_random_uuid(),NULL,$2,'fixture','MODEL',654321,$4)`, c.f.tenant, c.f.work, from, to.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/analytics/day?group_by=actor,resource&top=1&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	out := c.call(t, "GET", path, nil, 200)
	if out["total"] != float64(3) || out["cost_micros_usd"] != float64(777777) {
		t.Fatalf("cost window: %v", out)
	}
	if out["by_actor"].([]any)[0].(map[string]any)["cost_micros_usd"] != float64(777777) {
		t.Fatal("work costs lost actor attribution")
	}
	resources := out["by_resource"].([]any)
	if len(resources) != 2 || out["resource_count"] != float64(3) {
		t.Fatalf("resource top: %v", out)
	}
	other := resources[1].(map[string]any)
	if other["resource_id"] != "other" || other["touches"] != float64(2) || other["actors"] != float64(1) {
		t.Fatalf("other resources: %v", other)
	}
}

func TestConsoleRecheckPreservesEarlierWatchingDecision(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	c.call(t, "PUT", "/v1/config/limits/watched", map[string]any{"scope": map[string]any{"operation": "stripe.refund"}, "hold": true}, 200)
	d := c.check(t, "watched-recheck")
	if d.Decision != protocol.DecisionWouldWait {
		t.Fatal(d)
	}
	c.call(t, "DELETE", "/v1/config/limits/watched", nil, 204)
	c.call(t, "POST", "/v1/actions/"+d.ActionID+"/recheck", map[string]any{"id": "second-check"}, 200)
	out := c.call(t, "GET", "/v1/analytics/day", nil, 200)
	if out["total"] != float64(1) || out["would"].(map[string]any)["wait"] != float64(1) || out["kinds"].(map[string]any)["wait"] != float64(0) {
		t.Fatalf("recheck analytics: %v", out)
	}
}

func TestConsoleGrantRetryPreservesRecordAndSlipCount(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	rule := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Routine approval", "operation": "stripe.refund", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	body := map[string]any{"operation": "stripe.refund", "skip_rules": []any{rule["id"]}, "below": map[string]any{"pointer": "/value", "value": 100}, "revert_after": map[string]any{"slips": 2, "per": 1000}}
	before := c.call(t, "PUT", "/v1/config/autopilot/"+c.f.actor, body, 200)
	d := c.check(t, "one-slip")
	if d.Decision != protocol.DecisionAllow {
		t.Fatal(d)
	}
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE outcomes SET status='MISMATCH' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); err != nil {
		t.Fatal(err)
	}
	after := c.call(t, "PUT", "/v1/config/autopilot/"+c.f.actor, body, 200)
	if after["slips"] != float64(1) || after["since"] != before["since"] || after["active"] != true {
		t.Fatalf("grant retry reset record: before=%v after=%v", before, after)
	}
}

func TestConsoleRetryReturnsRecordedAttemptAndRenewsWindow(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	cipher, err := secrets.New("integration-secret")
	if err != nil {
		t.Fatal(err)
	}
	runtime := durable.New(c.f.pool, c.f.kernel, cipher)
	v := protocol.Verifier{ID: "retry-verifier", Name: "Database assertion", Kind: "POSTGRES", Version: "1", Enabled: true, Secret: os.Getenv("LUMNI_TEST_DATABASE_URL"), Config: json.RawMessage(`{"query":"SELECT jsonb_build_object('amount', $1::integer)"}`)}
	if err = runtime.PutVerifier(t.Context(), c.f.tenant, v); err != nil {
		t.Fatal(err)
	}
	in := input(c.f, "stripe.refund", "retry-record", "")
	in.Verification = &protocol.OutcomeSpec{VerifierID: v.ID, Expected: json.RawMessage(`{"params":[125],"amount":125}`)}
	d, err := c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.f.kernel.Start(t.Context(), c.f.tenant, d.ActionID, *d.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err = c.f.kernel.RecordResult(t.Context(), c.f.tenant, d.ActionID, protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	var outcome string
	err = c.f.pool.QueryRow(t.Context(), `UPDATE outcomes SET status='UNKNOWN',attempts=12,created_at=now()-interval '2 days' WHERE tenant_id=$1 AND action_id=$2 RETURNING id`, c.f.tenant, d.ActionID).Scan(&outcome)
	if err != nil {
		t.Fatal(err)
	}
	out := c.call(t, "POST", "/v1/outcomes/"+outcome+"/retry", map[string]any{}, 202)
	if err = runtime.VerifyOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var attempt, status string
	err = c.f.pool.QueryRow(t.Context(), `SELECT v.id,o.status FROM verification_attempts v JOIN outcomes o ON o.tenant_id=v.tenant_id AND o.id=v.outcome_id WHERE v.tenant_id=$1 AND v.outcome_id=$2`, c.f.tenant, outcome).Scan(&attempt, &status)
	if err != nil || attempt != out["attempt_id"] || status != "VERIFIED" {
		t.Fatalf("retry correlation: attempt=%s status=%s response=%v err=%v", attempt, status, out, err)
	}
}

func TestConsolePublicProofExpiryAndPeerRateLimit(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	d := c.check(t, "expires")
	p := c.call(t, "POST", "/v1/proofs", map[string]any{"action_id": d.ActionID}, 201)
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE proofs SET expires_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, c.f.tenant, p["id"]); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{p["token"].(string), "unknown-proof-token-that-does-not-exist"} {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/public/proofs/"+token, nil)
		r.RemoteAddr = "192.0.2.34:1234"
		c.server.Handler().ServeHTTP(rr, r)
		if rr.Code != 404 {
			t.Fatalf("token existence status %d", rr.Code)
		}
	}
	key := sha256.Sum256([]byte("192.0.2.35"))
	if _, err := c.f.pool.Exec(t.Context(), `INSERT INTO public_proof_rate(key,bucket,requests)VALUES($1,date_trunc('minute',now()),60)`, key[:]); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/public/proofs/"+p["token"].(string), nil)
	r.RemoteAddr = "192.0.2.35:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.99")
	c.server.Handler().ServeHTTP(rr, r)
	if rr.Code != 429 {
		t.Fatalf("forwarded header bypassed peer limit: %d", rr.Code)
	}
}
