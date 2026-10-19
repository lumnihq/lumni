//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"lumni/internal/analytics"
	"lumni/internal/notifications"
	"lumni/pkg/protocol"
)

func TestConsoleAutopilotPreviewAndFreshness(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	rule := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Large refunds", "operation": "stripe.refund", "argument_pointer": "/amount", "numeric_threshold": 500, "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	c.call(t, "PUT", "/v1/config/autopilot/"+c.f.actor, map[string]any{"operation": "stripe.refund", "skip_rules": []any{rule["id"]}, "below": map[string]any{"pointer": "/amount", "value": 2000}, "revert_after": map[string]any{"slips": 1, "per": 1000}}, 200)
	in := input(c.f, "stripe.refund", "autopilot", "")
	in.Arguments = json.RawMessage(`{"amount":600}`)
	d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if e != nil {
		t.Fatal(e)
	}
	if d.Decision != protocol.DecisionAllow {
		t.Fatalf("autopilot %+v", d)
	}
	found := false
	for _, ev := range d.Evaluations {
		if ev.Stage == "policy" && ev.Details["autopilot"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("missing autopilot evaluation")
	}
	preview := c.call(t, "POST", "/v1/config/policy-rules/preview", map[string]any{"rule": map[string]any{"operation": "stripe.refund", "argument_pointer": "/amount", "numeric_threshold": 500, "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, "from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "to": time.Now().Add(time.Second).UTC().Format(time.RFC3339), "tz": "Asia/Kolkata", "examples": 3}, 200)
	if preview["matched"] != float64(1) || preview["would_change"] != float64(1) {
		t.Fatalf("preview %v", preview)
	}
	if _, e = c.f.pool.Exec(t.Context(), `UPDATE outcomes SET status='MISMATCH' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); e != nil {
		t.Fatal(e)
	}
	grants := c.call(t, "GET", "/v1/config/autopilot", nil, 200)
	if grants["items"].([]any)[0].(map[string]any)["active"] != false {
		t.Fatal("autopilot did not revert")
	}
	in = input(c.f, "stripe.refund", "freshness", "v1")
	observed := time.Now().Add(-45 * time.Second)
	in.Resources[0].ObservedAt = &observed
	d, e = c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if e != nil {
		t.Fatal(e)
	}
	found = false
	for _, ev := range d.Evaluations {
		if ev.Stage == "resource_versions" {
			rs := ev.Details["resources"].([]map[string]any)
			if len(rs) != 1 || rs[0]["observed_at"] == nil || *rs[0]["age_ms"].(*int64) < 45000 {
				t.Fatalf("freshness %+v", rs)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no freshness stage")
	}
	c.call(t, "GET", "/v1/analytics/freshness", nil, 200)
}
func TestConsoleRateLimitLeaseAndNotifications(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "PUT", "/v1/config/limits/rate", map[string]any{"scope": map[string]any{"actor_id": c.f.actor}, "per_minute": 1}, 200)
	first := c.check(t, "rate-first")
	d := c.check(t, "rate-next")
	if d.Decision != protocol.DecisionWait || d.ReasonCode != protocol.ReasonRateLimited || d.RetryAfterMS == nil {
		t.Fatalf("rate %+v", d)
	}
	var resource string
	if e := c.f.pool.QueryRow(t.Context(), `SELECT resource_id FROM action_resources WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, first.ActionID).Scan(&resource); e != nil {
		t.Fatal(e)
	}
	lease := c.call(t, "GET", "/v1/resources/"+url.PathEscape(resource)+"/lease", nil, 200)
	if lease["holder"].(map[string]any)["actor_id"] != c.f.actor {
		t.Fatalf("lease %v", lease)
	}
	c.call(t, "GET", "/v1/resources/"+url.PathEscape(resource)+"/versions", nil, 200)
	c.call(t, "PUT", "/v1/config/notifications", map[string]any{"channels": []any{map[string]any{"id": "mail", "kind": "email", "target": "operator@example.com"}}, "routes": []any{map[string]any{"event": "approval.requested", "channels": []string{"mail"}}}}, 200)
	c.call(t, "DELETE", "/v1/config/limits/rate", nil, 204)
	c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "notify", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	d = c.check(t, "notify-action")
	if d.ApprovalID == nil {
		t.Fatal("missing approval")
	}
	var jobs int
	if e := c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_jobs WHERE tenant_id=$1 AND ref_id=$2`, c.f.tenant, *d.ApprovalID).Scan(&jobs); e != nil || jobs != 1 {
		t.Fatalf("route jobs %d %v", jobs, e)
	}
	if e := notifications.New(c.f.pool, nil).Once(t.Context()); e != nil {
		t.Fatal(e)
	}
	c.call(t, "PUT", "/v1/config/notifications", map[string]any{"channels": []any{}, "routes": []any{map[string]any{"event": "digest.weekly", "channels": []string{"missing"}, "schedule": "MON 09:00 Asia/Kolkata"}}}, 400)
}
func TestConsoleServerDetectsBurstsAndLoops(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "burst", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	for i := 0; i < 12; i++ {
		c.check(t, fmt.Sprintf("burst-%d", i))
	}
	if e := analytics.DetectBursts(t.Context(), c.f.pool); e != nil {
		t.Fatal(e)
	}
	v := c.call(t, "GET", "/v1/incidents?kind=BURST", nil, 200)
	if len(v["items"].([]any)) < 1 {
		t.Fatal("no burst detected")
	}
	item := v["items"].([]any)[0].(map[string]any)
	if item["totals"].(map[string]any)["stepins"].(float64) < 10 {
		t.Fatal(item)
	}
	for i := 0; i < 7; i++ {
		in := input(c.f, "stripe.refund", "loop", "")
		in.ActionID = fmt.Sprintf("loop-%d", i)
		if _, e := c.f.kernel.Check(t.Context(), c.f.tenant, in); e != nil {
			t.Fatal(e)
		}
	}
	v = c.call(t, "GET", "/v1/analytics/anomalies?kinds=loop", nil, 200)
	if len(v["items"].([]any)) != 1 {
		t.Fatalf("loop %v", v)
	}
}
