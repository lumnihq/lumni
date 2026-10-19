//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestConsoleRateLimitCountsRecentlyAdmittedOlderActions(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	rule := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Review", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	d := c.check(t, "delayed-approval")
	if d.ApprovalID == nil {
		t.Fatal("missing approval")
	}
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE actions SET requested_at=now()-interval '2 hours' WHERE tenant_id=$1 AND id=$2`, c.f.tenant, d.ActionID); err != nil {
		t.Fatal(err)
	}
	c.call(t, "POST", "/v1/approvals/"+*d.ApprovalID+"/approve", map[string]any{}, 200)
	c.call(t, "PATCH", "/v1/config/policy-rules/"+rule["id"].(string), map[string]any{"enabled": false}, 200)
	c.call(t, "PUT", "/v1/config/limits/rate", map[string]any{"per_minute": 1}, 200)
	recheck := c.call(t, "POST", "/v1/actions/"+d.ActionID+"/recheck", map[string]any{"id": "admit-delayed"}, 200)
	if recheck["decision"] != "ALLOW" {
		t.Fatalf("delayed action was not admitted: %v", recheck)
	}
	// A completed action still consumes its admission slot for the minute.
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE actions SET status='SUCCEEDED' WHERE tenant_id=$1 AND id=$2`, c.f.tenant, d.ActionID); err != nil {
		t.Fatal(err)
	}
	blocked := c.check(t, "after-delayed-approval")
	if blocked.Decision != protocol.DecisionWait || blocked.ReasonCode != protocol.ReasonRateLimited || blocked.RetryAfterMS == nil || *blocked.RetryAfterMS < 50000 {
		t.Fatalf("recent admission escaped rate limit: %+v", blocked)
	}
}

func TestConsoleAutopilotRequiresNumericGuard(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	rule := c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Review", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	c.call(t, "PUT", "/v1/config/autopilot/"+c.f.actor, map[string]any{"operation": "stripe.refund", "skip_rules": []any{rule["id"]}, "below": map[string]any{"pointer": "/amount", "value": 100}, "revert_after": map[string]any{"slips": 2, "per": 10}}, 200)
	for i, amount := range []string{`null`, `false`, `"unknown"`, `{}`, `[]`, `100`, `101`} {
		in := input(c.f, "stripe.refund", fmt.Sprintf("guard-%d", i), "")
		in.Arguments = json.RawMessage(`{"amount":` + amount + `}`)
		d, err := c.f.kernel.Check(t.Context(), c.f.tenant, in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Decision != protocol.DecisionRequireApproval {
			t.Errorf("amount %s bypassed approval: %+v", amount, d)
		}
	}
	allowed := input(c.f, "stripe.refund", "numeric-guard", "")
	allowed.Arguments = json.RawMessage(`{"amount":99}`)
	if d, err := c.f.kernel.Check(t.Context(), c.f.tenant, allowed); err != nil || d.Decision != protocol.DecisionAllow {
		t.Fatalf("numeric guard rejected a valid amount: %+v %v", d, err)
	}
}

func TestConsoleNotificationConfigurationIsAudited(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	body := map[string]any{"channels": []any{map[string]any{"id": "mail", "kind": "email", "target": "operator@example.com"}}, "routes": []any{}}
	c.call(t, "PUT", "/v1/config/notifications", body, 200)
	c.call(t, "PUT", "/v1/config/notifications", map[string]any{"channels": []any{}, "routes": []any{}}, 200)
	audit := c.call(t, "GET", "/v1/audit?type=notifications.updated&actor=operator%40example.com", nil, 200)
	items := audit["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("notification changes missing from audit: %v", audit)
	}
	latest := items[0].(map[string]any)
	if len(latest["before"].(map[string]any)["channels"].([]any)) != 1 || len(latest["after"].(map[string]any)["channels"].([]any)) != 0 {
		t.Fatalf("configuration history missing before/after: %v", latest)
	}
}

func TestConsoleCollectionsBeyondTwoHundred(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	const total = 237
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	statements := []string{
		`INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,arguments_hash,requested_at) SELECT $1,'bulk-'||n,$2,$3,'stripe.refund','hash',$4 FROM generate_series(1,237)n`,
		`INSERT INTO approvals(tenant_id,action_id,reason_code,requested_at,expires_at) SELECT $1,'bulk-'||n,'APPROVAL_REQUIRED',$2,$2::timestamptz+interval '1 day' FROM generate_series(1,237)n`,
		`INSERT INTO outcomes(tenant_id,id,action_id,work_id,created_at) SELECT $1,'out-'||n,'bulk-'||n,$2,$3 FROM generate_series(1,237)n`,
		`INSERT INTO incidents(tenant_id,id,kind,title,created_at) SELECT $1,'inc-'||n,'BURST','fixture',$2 FROM generate_series(1,237)n`,
	}
	args := [][]any{{c.f.tenant, c.f.work, c.f.actor, at}, {c.f.tenant, at}, {c.f.tenant, c.f.work, at}, {c.f.tenant, at}}
	for i, statement := range statements {
		if _, err := c.f.pool.Exec(t.Context(), statement, args[i]...); err != nil {
			t.Fatal(err)
		}
	}
	for _, collection := range []string{"actions", "approvals", "outcomes", "incidents"} {
		t.Run(collection, func(t *testing.T) {
			path := "/v1/" + collection + "?from=" + url.QueryEscape(at.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(at.Add(time.Second).Format(time.RFC3339Nano))
			if collection == "approvals" {
				path += "&status=PENDING&include=action"
			}
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 3; page++ {
				next := path
				if cursor != "" {
					next += "&cursor=" + url.QueryEscape(cursor)
				}
				out := c.call(t, "GET", next, nil, 200)
				for _, raw := range out["items"].([]any) {
					item := raw.(map[string]any)
					id := item["id"].(string)
					if seen[id] {
						t.Fatalf("duplicate %s", id)
					}
					seen[id] = true
					if collection == "approvals" && item["action"].(map[string]any)["actor_id"] != c.f.actor {
						t.Fatalf("missing embedded action: %v", item)
					}
				}
				var ok bool
				cursor, ok = out["next_cursor"].(string)
				if !ok {
					break
				}
			}
			if len(seen) != total {
				t.Fatalf("returned %d of %d", len(seen), total)
			}
		})
	}
}

func TestConsoleAnomaliesUseExactWindowsAndPrecedingBaseline(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	// The candidate hour must not contaminate its own preceding baseline.
	from := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	to := from.Add(3 * time.Hour)
	for hour, count := range []int{1, 1, 12} {
		_, err := c.f.pool.Exec(t.Context(), `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,arguments_hash,requested_at) SELECT $1,$5||n,$2,$3,'stripe.refund','hash',$4 FROM generate_series(1,$6::int)n`, c.f.tenant, c.f.work, c.f.actor, from.Add(time.Duration(hour)*time.Hour+time.Minute), fmt.Sprintf("runaway-%d-", hour), count)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := "/v1/analytics/anomalies?kinds=runaway&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	out := c.call(t, "GET", path, nil, 200)
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["factor"] != float64(12) {
		t.Fatalf("historical runaway not detected against prior hours: %v", out)
	}
	// Exclude a busy first cell when from falls after its actual actions.
	start := from.Add(2*time.Hour + 2*time.Minute)
	path = "/v1/analytics/anomalies?kinds=runaway,drift&from=" + url.QueryEscape(start.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	if out = c.call(t, "GET", path, nil, 200); len(out["items"].([]any)) != 0 {
		t.Fatalf("actions outside exact window included: %v", out)
	}
}

func TestConsoleAnomalyPartialCellsAndLoopLookback(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	cell := time.Now().UTC().Add(-time.Hour).Truncate(15 * time.Minute)
	from, to := cell.Add(5*time.Minute), cell.Add(7*time.Minute)
	// Both sets share an aggregate cell, but only one belongs to the window.
	for group, at := range []time.Time{cell.Add(6 * time.Minute), cell.Add(8 * time.Minute)} {
		_, err := c.f.pool.Exec(t.Context(), `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,arguments_hash,requested_at) SELECT $1,$5||n,$2,$3,'drift.write','hash',$4 FROM generate_series(1,20)n`, c.f.tenant, c.f.work, c.f.actor, at, fmt.Sprintf("partial-drift-%d-", group))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.f.pool.Exec(t.Context(), `INSERT INTO decisions(tenant_id,action_id,decision,reason_code,enforced,snapshot,kernel_version,resolver_version,policy_version) SELECT tenant_id,id,'REPLAN','STALE_READ',false,'{}','coord-v1','resolver-v2','policy-v1' FROM actions WHERE tenant_id=$1 AND operation='drift.write'`, c.f.tenant); err != nil {
		t.Fatal(err)
	}
	path := "/v1/analytics/anomalies?kinds=drift&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	out := c.call(t, "GET", path, nil, 200)
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["count"] != float64(20) {
		t.Fatalf("drift lost exact partial-cell bounds: %v", out)
	}
	if _, err := c.f.pool.Exec(t.Context(), `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,arguments_hash,requested_at) SELECT $1,'partial-loop-'||n,$2,$3,'loop.write','identical',$4::timestamptz-make_interval(mins=>n) FROM generate_series(0,4)n`, c.f.tenant, c.f.work, c.f.actor, from); err != nil {
		t.Fatal(err)
	}
	path = "/v1/analytics/anomalies?kinds=loop&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	out = c.call(t, "GET", path, nil, 200)
	items = out["items"].([]any)
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["operation"] == "loop.write" && item["count"] == float64(5) {
			return
		}
	}
	t.Fatalf("loop lost actions immediately before from: %v", out)
}

func TestConsoleReasonExamplesStayWithinTheirApp(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Review", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	latest := map[string]string{}
	from := time.Now().UTC().Truncate(15 * time.Minute).Add(-time.Hour)
	to := from.Add(15 * time.Minute)
	for _, app := range []string{"stripe", "github"} {
		for i := 0; i < 2; i++ {
			in := input(c.f, "stripe.refund", fmt.Sprintf("reason-%s-%d", app, i), "")
			in.ActionID = fmt.Sprintf("reason-%s-%d", app, i)
			in.Metadata = json.RawMessage(`{"app":"` + app + `"}`)
			d, err := c.f.kernel.Check(t.Context(), c.f.tenant, in)
			if err != nil {
				t.Fatal(err)
			}
			latest[app] = d.ActionID
			if _, err := c.f.pool.Exec(t.Context(), `UPDATE actions SET requested_at=$3,metadata=metadata WHERE tenant_id=$1 AND id=$2`, c.f.tenant, d.ActionID, from.Add(time.Duration(i+1)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, bounds := range [][2]time.Time{{from, to}, {from.Add(30 * time.Second), to.Add(-30 * time.Second)}} {
		path := "/v1/analytics/day?group_by=reason&from=" + url.QueryEscape(bounds[0].Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(bounds[1].Format(time.RFC3339Nano))
		out := c.call(t, "GET", path, nil, 200)
		items := out["by_reason"].([]any)
		if len(items) != 2 {
			t.Fatalf("expected one reason group per app: %v", out)
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			app := item["app"].(string)
			if item["sample_action_id"] != latest[app] || item["top_resource"] != "default:subscription:reason-"+app+"-0" {
				t.Fatalf("reason example belongs to another app: %v", item)
			}
		}
	}
}

func TestConsoleReasonRollupsReplaceOutcomeContributions(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	d := c.check(t, "corrected-outcome")
	from := time.Now().UTC().Truncate(15 * time.Minute).Add(-time.Hour)
	to := from.Add(15 * time.Minute)
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE actions SET requested_at=$3,metadata=metadata WHERE tenant_id=$1 AND id=$2`, c.f.tenant, d.ActionID, from.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	path := "/v1/analytics/day?group_by=reason&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	for _, status := range []string{"MISMATCH", "VERIFIED"} {
		if _, err := c.f.pool.Exec(t.Context(), `UPDATE outcomes SET status=$3 WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID, status); err != nil {
			t.Fatal(err)
		}
		out := c.call(t, "GET", path, nil, 200)
		items := out["by_reason"].([]any)
		if status == "MISMATCH" {
			if len(items) != 1 || items[0].(map[string]any)["kind"] != "bad" || items[0].(map[string]any)["top_resource"] != "default:subscription:corrected-outcome" {
				t.Fatalf("bad outcome lost reason resource: %v", out)
			}
		} else if len(items) != 0 {
			t.Fatalf("corrected outcome retained its old contribution: %v", out)
		}
	}
	var total, bad int64
	if err := c.f.pool.QueryRow(t.Context(), `SELECT COALESCE(sum(total),0),COALESCE(sum(total)FILTER(WHERE kind='bad'),0) FROM console_reason_resource_metrics WHERE tenant_id=$1`, c.f.tenant).Scan(&total, &bad); err != nil || total != 1 || bad != 0 {
		t.Fatalf("rollup counted a correction twice: total=%d bad=%d err=%v", total, bad, err)
	}
}
