//go:build integration

package integration

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lumni/internal/analytics"
	"lumni/internal/core"
	"lumni/pkg/protocol"
)

func TestAnalyticsExporterRetriesWithStableFactIdentity(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM analytics_outbox`); err != nil {
		t.Fatal(err)
	}
	actionID := "analytics-action"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,status,risk,arguments_hash)
		VALUES($1,$2,$3,$4,'test.analytics','SUCCEEDED','LOW','hash')`, f.tenant, actionID, f.work, f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := core.New(f.pool).RecordCost(t.Context(), f.tenant, protocol.CostInput{ActionID: &actionID, WorkID: &f.work, Provider: "test", CostType: "MODEL", AmountMicrosUSD: 1234}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var inserts []string
	failFirst := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(string(body), "INSERT INTO analytics_facts") {
			mu.Lock()
			inserts = append(inserts, string(body))
			shouldFail := failFirst
			failFirst = false
			mu.Unlock()
			if shouldFail {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exporter := analytics.NewExporter(f.pool, server.URL, "lumni", "", "", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := exporter.Once(t.Context()); err == nil {
		t.Fatal("first export unexpectedly succeeded")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE analytics_outbox SET next_attempt_at=clock_timestamp() WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	count, err := exporter.Once(t.Context())
	if err != nil || count == 0 {
		t.Fatalf("retry count=%d err=%v", count, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inserts) != 2 {
		t.Fatalf("fact insert attempts=%d, want 2", len(inserts))
	}
	firstID := factEventID(inserts[0])
	secondID := factEventID(inserts[1])
	if firstID == "" || firstID != secondID {
		t.Fatalf("retry identity changed: %q != %q", firstID, secondID)
	}
	var pending int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM analytics_outbox WHERE tenant_id=$1 AND exported_at IS NULL`, f.tenant).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending analytics facts=%d", pending)
	}
}

func factEventID(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `"event_id"`) {
			start := strings.Index(line, `"event_id":"`)
			if start < 0 {
				continue
			}
			value := line[start+len(`"event_id":"`):]
			if end := strings.Index(value, `"`); end >= 0 {
				return value[:end]
			}
		}
	}
	return ""
}
