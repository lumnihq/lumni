//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestConsoleEncodedResourceIDs(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	resource := c.call(t, "POST", "/v1/resources/resolve", map[string]any{"type": "price_list", "namespace": "salesforce", "external_id": "price/100%"}, 201)
	id := resource["id"].(string)
	// JavaScript's encodeURIComponent escapes colons as well as slashes and %.
	encoded := strings.ReplaceAll(url.PathEscape(id), ":", "%3A")
	path := "/v1/resources/" + encoded
	if got := c.call(t, "GET", path, nil, 200); got["id"] != id {
		t.Fatal(got)
	}
	c.call(t, "POST", path+"/version", map[string]any{"version": "v1", "version_source": "salesforce", "summary": "Price before"}, 200)
	c.call(t, "POST", path+"/version", map[string]any{"version": "v2", "version_source": "salesforce", "summary": "Price after"}, 200)
	if got := c.call(t, "GET", path, nil, 200); got["current_version"] != "v2" {
		t.Fatal(got)
	}
	if versions := c.call(t, "GET", path+"/versions", nil, 200)["items"].([]any); len(versions) != 2 {
		t.Fatal(versions)
	}
}

func TestConsoleResourceLimitsExcludeUnscopedRules(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	for id, scope := range map[string]any{
		"global": map[string]any{}, "actor": map[string]any{"actor_id": c.f.actor},
		"operation": map[string]any{"operation": "stripe.refund"},
		"null":      map[string]any{"resource": nil}, "empty": map[string]any{"resource": ""},
		"exact":  map[string]any{"resource": "stripe:charge:123"},
		"prefix": map[string]any{"resource": "stripe:charge:*"},
		"other":  map[string]any{"resource": "stripe:charge:456"},
	} {
		c.call(t, "PUT", "/v1/config/limits/"+id, map[string]any{"scope": scope, "max_in_flight": 2}, 200)
	}
	path := "/v1/config/limits?resource=" + url.QueryEscape("stripe:charge:123") + "&limit=1"
	seen := map[string]bool{}
	for next := path; next != ""; {
		page := c.call(t, "GET", next, nil, 200)
		for _, raw := range page["items"].([]any) {
			id := raw.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate limit: %s", id)
			}
			seen[id] = true
		}
		next = ""
		if cursor, ok := page["next_cursor"].(string); ok {
			next = path + "&cursor=" + url.QueryEscape(cursor)
		}
	}
	if len(seen) != 2 || !seen["exact"] || !seen["prefix"] {
		t.Fatalf("resource filter returned %v", seen)
	}
	if all := c.call(t, "GET", "/v1/config/limits", nil, 200); len(all["items"].([]any)) != 8 {
		t.Fatal(all)
	}
}

func TestConsoleContendedResourcesEmbedOrderedQueues(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	first := c.check(t, "shared-queue")
	var resource string
	if err := c.f.pool.QueryRow(t.Context(), `SELECT resource_id FROM action_resources WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, first.ActionID).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		in := input(c.f, "stripe.refund", "shared-queue", "")
		in.ActionID = fmt.Sprintf("queued-%d", i)
		in.Arguments = json.RawMessage(fmt.Sprintf(`{"different":%d}`, i))
		decision, err := c.f.kernel.Check(t.Context(), c.f.tenant, in)
		if err != nil || decision.Decision != protocol.DecisionWait {
			t.Fatalf("queue admission: %+v %v", decision, err)
		}
	}
	result := c.call(t, "GET", "/v1/resources/contended", nil, 200)
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatal(result)
	}
	item := items[0].(map[string]any)
	if item["resource_id"] != resource || item["holder"].(map[string]any)["action_id"] != first.ActionID {
		t.Fatal(item)
	}
	queue := item["queue"].([]any)
	if len(queue) != 2 {
		t.Fatal(queue)
	}
	for i, raw := range queue {
		entry := raw.(map[string]any)
		if entry["action_id"] != fmt.Sprintf("queued-%d", i) || entry["position"] != float64(i+1) || entry["actor_id"] != c.f.actor {
			t.Fatal(queue)
		}
	}
	other := newConsoleClient(t, protocol.ModeEnforce)
	if got := other.call(t, "GET", "/v1/resources/contended", nil, 200); len(got["items"].([]any)) != 0 {
		t.Fatal(got)
	}
}

func TestConsoleApprovalAuditSnapshotsActionSummary(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.call(t, "POST", "/v1/config/policy-rules", map[string]any{"name": "Review", "outcome": "REQUIRE_APPROVAL", "reason_code": "APPROVAL_REQUIRED"}, 201)
	for i, decision := range []string{"approve", "deny"} {
		in := input(c.f, "stripe.refund", fmt.Sprintf("audit-%d", i), "")
		in.Metadata = json.RawMessage(`{"summary":"Refund the annual subscription","private":"do not copy"}`)
		if i == 1 {
			in.Metadata = json.RawMessage(`{}`)
		}
		d, err := c.f.kernel.Check(t.Context(), c.f.tenant, in)
		if err != nil || d.ApprovalID == nil {
			t.Fatalf("approval: %+v %v", d, err)
		}
		c.call(t, "POST", "/v1/approvals/decide", map[string]any{"ids": []string{*d.ApprovalID}, "decision": decision}, 200)
		if _, err = c.f.pool.Exec(t.Context(), `UPDATE actions SET metadata='{"summary":"changed later"}' WHERE tenant_id=$1 AND id=$2`, c.f.tenant, d.ActionID); err != nil {
			t.Fatal(err)
		}
	}
	entries := c.call(t, "GET", "/v1/audit?type=approval.decided", nil, 200)["items"].([]any)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	for i, raw := range entries {
		target := raw.(map[string]any)["target"].(map[string]any)
		expected := "stripe.refund"
		if i == 1 {
			expected = "Refund the annual subscription"
		}
		if target["summary"] != expected || target["actor_id"] != c.f.actor || target["operation"] != "stripe.refund" || target["private"] != nil {
			t.Fatal(target)
		}
	}
}

func TestConsoleSpendLimitsUseConfiguredDay(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "Etc/GMT+12"} {
		t.Run(zone, func(t *testing.T) {
			c := newConsoleClient(t, protocol.ModeEnforce)
			v := c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "HOLD", "tz": zone, "scope": map[string]any{"actor_id": c.f.actor}}, 200)
			if v["tz"] != zone {
				t.Fatal(v)
			}
			localStart, err := time.Parse(time.RFC3339, v["day_start"].(string))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			utcStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			costTime := localStart.Add(utcStart.Sub(localStart) / 2)
			localIncludes := localStart.Before(utcStart)
			if _, err = c.f.pool.Exec(t.Context(), `INSERT INTO costs(tenant_id,id,work_id,provider,cost_type,amount_micros_usd,observed_at)VALUES($1,$2,$3,'test','MODEL',600,$4)`, c.f.tenant, uuid.NewString(), c.f.work, costTime); err != nil {
				t.Fatal(err)
			}
			for _, currentZone := range []string{zone, "UTC"} {
				v = c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "HOLD", "tz": currentZone, "scope": map[string]any{"actor_id": c.f.actor}}, 200)
				included := localIncludes == (currentZone == zone)
				wantSpend, wantDecision := float64(0), protocol.DecisionAllow
				if included {
					wantSpend, wantDecision = 600, protocol.DecisionReject
				}
				if v["spent_today_micros_usd"] != wantSpend {
					t.Fatalf("day accounting: %v", v)
				}
				if d := c.check(t, "spend-"+currentZone); d.Decision != wantDecision {
					t.Fatalf("%s admission: %+v", currentZone, d)
				}
				v = c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "SLOW", "tz": currentZone, "scope": map[string]any{"actor_id": c.f.actor}}, 200)
				d := c.check(t, "paced-"+currentZone)
				if included {
					reset, err := time.Parse(time.RFC3339, v["resets_at"].(string))
					if err != nil || d.Decision != protocol.DecisionWait || d.RetryAfterMS == nil || *d.RetryAfterMS < time.Until(reset).Milliseconds()-2000 || *d.RetryAfterMS > time.Until(reset).Milliseconds()+2000 {
						t.Fatalf("%s retry must end at local midnight: %+v %v", currentZone, d, err)
					}
				} else if d.Decision != protocol.DecisionAllow {
					t.Fatalf("%s excluded spend affected pacing: %+v", currentZone, d)
				}
			}
			c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "SLOW", "tz": "Invalid/Zone"}, 400)
			c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "SLOW", "tz": "Local"}, 400)
			if got := c.call(t, "PUT", "/v1/config/spend-limit", map[string]any{"daily_micros_usd": 500, "when_reached": "SLOW"}, 200); got["tz"] != "UTC" {
				t.Fatal(got)
			}
		})
	}
}

func TestConsoleSpendDayWindowAcrossDST(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	for _, tc := range []struct {
		at, start, end string
		hours          int
	}{
		{"2026-03-08T16:00:00Z", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", 23},
		{"2026-11-01T16:00:00Z", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", 25},
	} {
		var start, end time.Time
		if err := c.f.pool.QueryRow(t.Context(), `SELECT start_at,end_at FROM console_spend_window('America/New_York',$1)`, tc.at).Scan(&start, &end); err != nil {
			t.Fatal(err)
		}
		if start.UTC().Format(time.RFC3339) != tc.start || end.UTC().Format(time.RFC3339) != tc.end || end.Sub(start) != time.Duration(tc.hours)*time.Hour {
			t.Fatalf("%s: %v to %v", tc.at, start, end)
		}
	}
}
