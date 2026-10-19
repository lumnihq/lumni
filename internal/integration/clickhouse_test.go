//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"lumni/internal/analytics"
)

func TestClickHouseProfileDeduplicationTenantIsolationRollupsAndRetention(t *testing.T) {
	endpoint := os.Getenv("LUMNI_TEST_CLICKHOUSE_URL")
	if endpoint == "" {
		t.Skip("LUMNI_TEST_CLICKHOUSE_URL is not set")
	}
	user := os.Getenv("LUMNI_CLICKHOUSE_USER")
	password := os.Getenv("LUMNI_CLICKHOUSE_PASSWORD")
	tenantA, tenantB := "test-"+uuid.NewString(), "test-"+uuid.NewString()
	eventID := "event-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Hour)

	facts := []map[string]any{
		clickHouseCostFact(tenantA, eventID, 1, 100, now),
		clickHouseCostFact(tenantA, eventID, 2, 200, now), // ack-loss retry replaces the older event
		clickHouseCostFact(tenantB, eventID, 1, 500, now), // same event ID is isolated by tenant
	}
	clickHouseInsertJSON(t, endpoint, user, password, "analytics_facts", facts)

	store := analytics.NewClickHouse(endpoint, "lumni", user, password, 5*time.Second)
	from, to := now.Add(-time.Hour), now.Add(2*time.Hour)
	resultA, err := store.FinOps(t.Context(), tenantA, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if resultA.TotalMicros != 200 {
		t.Fatalf("tenant A total=%d, want deduplicated total 200", resultA.TotalMicros)
	}
	resultB, err := store.FinOps(t.Context(), tenantB, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if resultB.TotalMicros != 500 {
		t.Fatalf("tenant B total=%d, want isolated total 500", resultB.TotalMicros)
	}

	rollups := []map[string]any{
		clickHouseRollup(tenantA, now, 1, 100, now.Add(time.Minute)),
		clickHouseRollup(tenantA, now, 2, 200, now.Add(2*time.Minute)),
	}
	clickHouseInsertJSON(t, endpoint, user, password, "analytics_rollup_hourly", rollups)
	points, freshness, err := store.Trends(t.Context(), tenantA, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if freshness.Source != "clickhouse" || len(points) != 1 || points[0].Actions != 2 || points[0].CostMicrosUSD != 200 {
		t.Fatalf("unexpected deduplicated rollup: freshness=%+v points=%+v", freshness, points)
	}

	ddl := clickHouseSQL(t, endpoint, user, password, "SELECT create_table_query FROM system.tables WHERE database='lumni' AND name='analytics_facts' FORMAT TabSeparatedRaw")
	if !strings.Contains(ddl, "TTL occurred_at + toIntervalDay(180)") && !strings.Contains(ddl, "TTL occurred_at + INTERVAL 180 DAY") {
		t.Fatalf("analytics fact retention TTL missing from ClickHouse DDL: %s", ddl)
	}
}

func clickHouseCostFact(tenantID, eventID string, version, amount int64, occurred time.Time) map[string]any {
	return map[string]any{
		"tenant_id": tenantID, "event_id": eventID, "source_sequence": version,
		"kind": "cost", "entity_type": "action", "entity_id": "action-1", "relationship": "",
		"source_id": "", "target_id": "", "status": "SUCCEEDED", "decision": "ALLOW",
		"reason_code": "NO_CONFLICT", "provider": "test", "model": "test-model", "waste_reason": "",
		"amount_micros_usd": amount, "duration_ms": 1.0,
		"occurred_at": occurred.Format("2006-01-02 15:04:05.000000"), "ingest_version": version,
	}
}

func clickHouseRollup(tenantID string, bucket time.Time, actions, cost int64, version time.Time) map[string]any {
	return map[string]any{
		"tenant_id": tenantID, "bucket": bucket.Format("2006-01-02 15:04:05.000000"),
		"actions": actions, "decisions": actions, "failures": 0,
		"cost_micros_usd": cost, "waste_micros_usd": 0,
		"rollup_version": version.Format("2006-01-02 15:04:05.000000"),
	}
}

func clickHouseInsertJSON(t *testing.T, endpoint, user, password, table string, rows []map[string]any) {
	t.Helper()
	var body bytes.Buffer
	fmt.Fprintf(&body, "INSERT INTO %s FORMAT JSONEachRow\n", table)
	encoder := json.NewEncoder(&body)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	_ = clickHouseRequest(t, endpoint, user, password, body.String())
}

func clickHouseSQL(t *testing.T, endpoint, user, password, statement string) string {
	t.Helper()
	return clickHouseRequest(t, endpoint, user, password, statement)
}

func clickHouseRequest(t *testing.T, endpoint, user, password, statement string) string {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	query.Set("database", "lumni")
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target.String(), strings.NewReader(statement))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("ClickHouse request failed: %s: %s", resp.Status, payload)
	}
	return string(payload)
}
