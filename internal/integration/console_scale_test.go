//go:build integration

package integration

import (
	"context"
	"fmt"
	"lumni/pkg/protocol"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

// Opt-in because the fixture occupies hundreds of MB. It seeds authoritative
// action rows plus their analytical projection, avoiding two million HTTP
// admissions during a query-performance test. Ordinary tests cover triggers.
func TestConsoleAnalyticsScale(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv("LUMNI_TEST_SCALE_ACTIONS"))
	if err != nil || n < 1 {
		t.Skip("set LUMNI_TEST_SCALE_ACTIONS=2000000 for the full query fixture")
	}
	c := newConsoleClient(t, protocol.ModeEnforce)
	// These synthetic actions have no children except the facts inserted below.
	// Avoid millions of individual foreign-key checks during fixture cleanup.
	// If the test role cannot suppress triggers, the ordinary tenant cleanup runs.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		tx, err := c.f.pool.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			t.Log("using normal tenant cleanup")
			return
		}
		if _, err = tx.Exec(ctx, `DELETE FROM console_action_facts WHERE tenant_id=$1`, c.f.tenant); err != nil {
			t.Error(err)
			return
		}
		if _, err = tx.Exec(ctx, `DELETE FROM actions WHERE tenant_id=$1`, c.f.tenant); err != nil {
			t.Error(err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			t.Error(err)
		}
	})
	to := time.Now().UTC().Truncate(24 * time.Hour)
	from := to.Add(-24 * time.Hour)
	tx, err := c.f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	// This constructed fixture has known parent identities and projections.
	// Ordinary integration tests cover all constraints and incremental triggers.
	if _, err = tx.Exec(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,status,arguments_hash,requested_at)SELECT $1,'scale_'||lpad(g::text,8,'0'),$2,$3,'stripe.refund','SUCCEEDED','scale',$4::timestamptz+((g-1)::double precision/$5)*interval '24 hours' FROM generate_series(1,$5::int)g`, c.f.tenant, c.f.work, c.f.actor, from, n)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO console_action_facts SELECT tenant_id,id,requested_at,date_bin(interval '15 minutes',requested_at,'2000-01-01'::timestamptz),actor_id,operation,'stripe',CASE WHEN right(id,2)='00' THEN 'stop' WHEN right(id,2)='01' THEN 'wait' WHEN right(id,2)='02' THEN 'recheck' ELSE 'allow' END,CASE WHEN right(id,2)='00' THEN 'EXACT_EFFECT_ALREADY_COMMITTED' WHEN right(id,2)='01' THEN 'LEASE_BUSY' WHEN right(id,2)='02' THEN 'STALE_OBSERVATION' ELSE 'NO_CONFLICT' END,'UNKNOWN','',0,0,ARRAY['scale-resource'],right(id,2) NOT IN('00','01','02') FROM actions WHERE tenant_id=$1`, c.f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO console_metrics(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at)SELECT tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would,count(*),0,0,max(at) FROM console_action_facts WHERE tenant_id=$1 GROUP BY tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would`, c.f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO console_resource_metrics SELECT tenant_id,bucket,'scale-resource',actor_id,kind,sum(total) FROM console_metrics WHERE tenant_id=$1 GROUP BY tenant_id,bucket,actor_id,kind`, c.f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO console_reason_resource_metrics SELECT tenant_id,bucket,actor_id,operation,app,kind,reason_code,'scale-resource',sum(total) FROM console_metrics WHERE tenant_id=$1 GROUP BY tenant_id,bucket,actor_id,operation,app,kind,reason_code`, c.f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.f.pool.Exec(t.Context(), `ANALYZE console_action_facts; ANALYZE console_metrics`); err != nil {
		t.Fatal(err)
	}
	var worst time.Duration
	for _, window := range []struct {
		from, to time.Time
		total    int
	}{
		{from, to, n},
		{from.Add(time.Minute), to.Add(-time.Minute), (n*1439+1439)/1440 - (n+1439)/1440},
	} {
		path := fmt.Sprintf("/v1/analytics/day?group_by=kind,hour,actor,app,reason,resource,operation&from=%s&to=%s", url.QueryEscape(window.from.Format(time.RFC3339)), url.QueryEscape(window.to.Format(time.RFC3339)))
		for i := 0; i < 3; i++ {
			start := time.Now()
			out := c.call(t, "GET", path, nil, 200)
			elapsed := time.Since(start)
			if out["total"] != float64(window.total) {
				t.Fatalf("total=%v want %d", out["total"], window.total)
			}
			if out["by_resource"].([]any)[0].(map[string]any)["touches"] != float64(window.total) {
				t.Fatalf("resource touches: %v", out["by_resource"])
			}
			for _, raw := range out["by_reason"].([]any) {
				if reason := raw.(map[string]any); reason["top_resource"] != "scale-resource" {
					t.Fatalf("reason resource: %v", reason)
				}
			}
			t.Logf("window %s–%s read %d: %s", window.from.Format("15:04"), window.to.Format("15:04"), i+1, elapsed)
			if elapsed > worst {
				worst = elapsed
			}
		}
	}
	t.Logf("%d actions: slowest of 6 aligned/partial day aggregate reads %s", n, worst)
	if worst >= 500*time.Millisecond {
		t.Fatalf("day aggregate exceeded 500 ms: %s", worst)
	}
}
