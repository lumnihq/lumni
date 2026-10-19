//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"lumni/internal/api"
	"lumni/internal/auth"
	"lumni/internal/core"
	"lumni/pkg/protocol"
)

type consoleClient struct {
	f      fixture
	server *api.Server
	key    string
}

func newConsoleClient(t *testing.T, mode protocol.EnforcementMode) consoleClient {
	t.Helper()
	f := setup(t, mode)
	a := auth.New(f.pool)
	key, e := a.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "operator@example.com", Scopes: []string{"admin"}})
	if e != nil {
		t.Fatal(e)
	}
	s := api.New(f.pool, a, core.New(f.pool), f.kernel, slog.New(slog.NewTextHandler(io.Discard, nil)), 1<<20, "", "integration-secret")
	return consoleClient{f, s, key.Secret}
}
func (c consoleClient) call(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, req)
	if rr.Code != status {
		t.Fatalf("%s %s: got %d expected %d: %s", method, path, rr.Code, status, rr.Body.String())
	}
	out := map[string]any{}
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func (c consoleClient) check(t *testing.T, id string) protocol.Decision {
	t.Helper()
	in := input(c.f, "stripe.refund", id, "")
	in.ActionID = id
	d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestConsoleListsAndSeekPagination(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("paged-%d", i)
		c.check(t, id)
		if _, e := c.f.pool.Exec(t.Context(), `UPDATE actions SET requested_at=$3 WHERE tenant_id=$1 AND id=$2`, c.f.tenant, id, now); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[string]bool{}
	path := "/v1/actions?limit=2&actor_id=" + c.f.actor + "&status=PROPOSED&from=" + url.QueryEscape(now.Add(-time.Minute).Format(time.RFC3339))
	cursor := ""
	for i := 0; i < 10; i++ {
		p := path
		if cursor != "" {
			p += "&cursor=" + url.QueryEscape(cursor)
		}
		v := c.call(t, "GET", p, nil, 200)
		for _, raw := range v["items"].([]any) {
			a := raw.(map[string]any)
			id := a["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate on seek pages: %s", id)
			}
			seen[id] = true
			if a["arguments"] != nil || a["lease_token_hash"] != nil {
				t.Fatal("sensitive action fields exposed")
			}
		}
		next, ok := v["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	if len(seen) != 7 {
		t.Fatalf("paged %d of 7", len(seen))
	}
	c.call(t, "GET", "/v1/outcomes?limit=2&cursor="+url.QueryEscape(cursor), nil, 400)
	for _, kind := range []string{"findings", "incidents", "outcomes", "verifiers", "steps", "journal", "waits", "reconciliation", "discovery", "dependencies", "enforcement", "activity", "effects", "step-attempts", "verification-attempts", "operation-verifiers", "model-calls", "reconciliation-handlers", "semantic-evaluations"} {
		c.call(t, "GET", "/v1/"+kind+"?limit=2", nil, 200)
	}
	c.call(t, "GET", "/v1/actions?from=broken", nil, 400)
	c.call(t, "GET", "/v1/actions?limit=1001", nil, 400)
}
func TestConsolePauseLimitAndBudgetEnforcement(t *testing.T) {
	for _, mode := range []protocol.EnforcementMode{protocol.ModeEnforce, protocol.ModeShadow} {
		t.Run(string(mode), func(t *testing.T) {
			c := newConsoleClient(t, mode)
			v := c.call(t, "POST", "/v1/actors/"+c.f.actor+"/pause", map[string]any{"reason": "containment"}, 200)
			if v["paused_by"] != "operator@example.com" {
				t.Fatalf("operator identity: %v", v)
			}
			d := c.check(t, "paused")
			expected := protocol.DecisionWait
			if mode == protocol.ModeShadow {
				expected = protocol.DecisionWouldWait
			}
			if d.Decision != expected || d.ReasonCode != protocol.ReasonActorPaused {
				t.Fatalf("pause: %+v", d)
			}
			c.call(t, "POST", "/v1/actors/"+c.f.actor+"/pause", map[string]any{}, 409)
			c.call(t, "POST", "/v1/actors/"+c.f.actor+"/resume", map[string]any{}, 200)
			c.call(t, "POST", "/v1/actors/"+c.f.actor+"/resume", map[string]any{}, 409)
			c.call(t, "PUT", "/v1/config/limits/cap", map[string]any{"scope": map[string]any{"operation": "stripe.refund"}, "hold": true, "mode": "ENFORCE"}, 200)
			d = c.check(t, "held")
			if d.Decision != expected || d.ReasonCode != protocol.ReasonConcurrencyLimit {
				t.Fatalf("hold: %+v", d)
			}
			c.call(t, "DELETE", "/v1/config/limits/cap", nil, 204)
			c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 0, "when_reached": "HOLD"}, 400)
		})
	}
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 1000000, "when_reached": "HOLD"}, 200)
	if _, e := c.f.pool.Exec(t.Context(), `INSERT INTO costs(tenant_id,id,work_id,provider,cost_type,amount_micros_usd,observed_at)VALUES($1,$3,$2,'test','MODEL',1000000,now())`, c.f.tenant, c.f.work, uuid.NewString()); e != nil {
		t.Fatal(e)
	}
	d := c.check(t, "budgeted")
	if d.Decision != protocol.DecisionReject || d.ReasonCode != protocol.ReasonBudgetPacing {
		t.Fatalf("budget: %+v", d)
	}
	c.call(t, "DELETE", "/v1/config/spend-limit", nil, 204)
}
func TestConsoleConcurrentAdmissionLimit(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "PUT", "/v1/config/limits/cap", map[string]any{"scope": map[string]any{"operation": "stripe.refund"}, "max_in_flight": 2}, 200)
	var wg sync.WaitGroup
	ds := make(chan protocol.Decision, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			in := input(c.f, "stripe.refund", uuid.NewString(), "")
			d, e := c.f.kernel.Check(t.Context(), c.f.tenant, in)
			ds <- d
			errs <- e
		})
	}
	wg.Wait()
	close(ds)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	allowed := 0
	for d := range ds {
		if d.Decision == protocol.DecisionAllow {
			allowed++
		} else if d.ReasonCode != protocol.ReasonConcurrencyLimit {
			t.Fatalf("unexpected %+v", d)
		}
	}
	if allowed != 2 {
		t.Fatalf("limit admitted %d, wanted 2", allowed)
	}
}
func TestConsoleApprovalJourneyAndRuleLifecycle(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	rule := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Review refunds", "operation": "stripe.refund", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	d := c.check(t, "needs-review")
	if d.ApprovalID == nil {
		t.Fatal("no approval")
	}
	list := c.call(t, "GET", "/v1/approvals?status=PENDING&include=action,rule&action_id="+d.ActionID, nil, 200)
	item := list["items"].([]any)[0].(map[string]any)
	if item["action"].(map[string]any)["actor_id"] != c.f.actor || item["rule"].(map[string]any)["id"] != rule["id"] {
		t.Fatalf("embedded data: %v", item)
	}
	c.call(t, "POST", "/v1/approvals/decide", map[string]any{"ids": []string{*d.ApprovalID, "missing"}, "decision": "approve", "note": "reviewed"}, 200)
	j := c.call(t, "GET", "/v1/actions/"+d.ActionID+"/journey", nil, 200)
	if j["approval"].(map[string]any)["resolved_by"] != "operator@example.com" {
		t.Fatal(j)
	}
	id := rule["id"].(string)
	c.call(t, "PATCH", "/v1/config/policy-rules/"+id, map[string]any{"enabled": false, "version": 1}, 200)
	c.call(t, "PATCH", "/v1/config/policy-rules/"+id, map[string]any{"name": "stale update", "version": 1}, 409)
	c.call(t, "DELETE", "/v1/config/policy-rules/"+id, nil, 204)
	c.call(t, "PATCH", "/v1/config/policy-rules/"+id, map[string]any{"enabled": true}, 404)
	c.call(t, "PUT", "/v1/config/enforcement", map[string]any{"operation": "stripe.refund", "mode": "invalid"}, 400)
	c.call(t, "PUT", "/v1/config/enforcement", map[string]any{"operation": "stripe.refund", "mode": "WARN"}, 200)
	c.call(t, "DELETE", "/v1/config/enforcement", map[string]any{"operation": "stripe.refund"}, 204)
	c.call(t, "DELETE", "/v1/config/enforcement", map[string]any{"operation": "stripe.refund"}, 404)
	audit := c.call(t, "GET", "/v1/audit?type=policy_rule.created,approval.decided&actor=operator%40example.com", nil, 200)
	if len(audit["items"].([]any)) != 2 {
		t.Fatalf("audit %v", audit)
	}
}
func TestConsoleAnalyticsAndPublicProofPrivacy(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	d := c.check(t, "proven")
	if _, e := c.f.pool.Exec(t.Context(), `UPDATE outcomes SET status='VERIFIED' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); e != nil {
		t.Fatal(e)
	}
	v := c.call(t, "GET", "/v1/analytics/day?group_by=kind,hour,actor,app,reason,resource,operation&tz=Asia%2FKolkata", nil, 200)
	if v["total"] != float64(1) || v["outcomes"].(map[string]any)["VERIFIED"] != float64(1) {
		t.Fatalf("aggregates %v", v)
	}
	p := c.call(t, "POST", "/v1/proofs", map[string]any{"action_id": d.ActionID, "show": map[string]any{"what": true, "checks": true, "actor": false}}, 201)
	token := p["token"].(string)
	rr := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/public/proofs/"+token, nil))
	if rr.Code != 200 {
		t.Fatalf("proof %d %s", rr.Code, rr.Body.String())
	}
	for _, secret := range []string{c.f.actor, "arguments", "numeric_threshold", "secret", "amount_micros_usd"} {
		if bytes.Contains(rr.Body.Bytes(), []byte(secret)) {
			t.Fatalf("proof leaked %s: %s", secret, rr.Body.String())
		}
	}
	var stored []byte
	if e := c.f.pool.QueryRow(t.Context(), `SELECT token_hash FROM proofs WHERE tenant_id=$1 AND id=$2`, c.f.tenant, p["id"]).Scan(&stored); e != nil || string(stored) == token {
		t.Fatal("token not hashed")
	}
	c.call(t, "DELETE", "/v1/proofs/"+p["id"].(string), nil, 204)
	rr = httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/public/proofs/"+token, nil))
	if rr.Code != 410 {
		t.Fatal(rr.Code)
	}
	c.call(t, "GET", "/v1/analytics/day?group_by=invalid", nil, 400)
	c.call(t, "GET", "/v1/analytics/day?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z", nil, 400)
	for _, path := range []string{"/v1/analytics/daily?days=7", "/v1/analytics/freshness", "/v1/analytics/anomalies", "/v1/billing/usage", "/v1/apps/stripe/health", "/v1/resources/contended", "/v1/config/autopilot", "/v1/config/notifications"} {
		c.call(t, "GET", path, nil, 200)
	}
}
func TestConsoleGraphPagesCoverWholeWindow(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	for i := 0; i < 8; i++ {
		c.check(t, fmt.Sprintf("graph-%d", i))
	}
	path := "/v1/graph/snapshot?lens=transaction&limit=3"
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for pages < 100 {
		p := path
		if cursor != "" {
			p += "&cursor=" + url.QueryEscape(cursor)
		}
		v := c.call(t, "GET", p, nil, 200)
		for _, raw := range v["nodes"].([]any) {
			n := raw.(map[string]any)
			id := n["id"].(string)
			if seen[id] {
				t.Fatalf("graph duplicate %s", id)
			}
			seen[id] = true
			if n["type"] == "action" && n["metadata"].(map[string]any)["actor_id"] != c.f.actor {
				t.Fatal(n)
			}
		}
		pages++
		next, ok := v["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	actions := 0
	evaluations := 0
	for id := range seen {
		if len(id) > 7 && id[:7] == "action:" {
			actions++
		}
		if len(id) > 11 && id[:11] == "evaluation:" {
			evaluations++
		}
	}
	if actions != 8 || evaluations < 8 || pages < 3 {
		t.Fatalf("graph actions=%d evaluations=%d pages=%d", actions, evaluations, pages)
	}
}
