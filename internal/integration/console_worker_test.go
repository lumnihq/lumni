//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lumni/internal/analytics"
	"lumni/internal/notifications"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

func TestConsoleExportAndHybridReadsCorrectEarlierCells(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	d := c.check(t, "export-correction")
	var mu sync.Mutex
	var bodies []string
	var snapshot string
	failInsert, failRead := true, false
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/read" {
			if failRead {
				http.Error(w, "temporary", 503)
				return
			}
			_, _ = io.WriteString(w, snapshot)
			return
		}
		if strings.HasPrefix(string(body), "INSERT INTO console_metrics") {
			bodies = append(bodies, string(body))
			if failInsert {
				failInsert = false
				http.Error(w, "temporary", 503)
				return
			}
			if snapshot == "" {
				for _, line := range strings.Split(string(body), "\n")[1:] {
					var row map[string]any
					if json.Unmarshal([]byte(line), &row) != nil {
						continue
					}
					if row["total"].(float64) <= 0 {
						continue
					}
					row["Cost"], row["Kept"] = row["cost_micros_usd"], row["kept_micros_usd"]
					b, _ := json.Marshal(row)
					snapshot += string(b) + "\n"
				}
			}
		}
	}))
	defer host.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exporter := analytics.NewExporter(c.f.pool, host.URL, "lumni", "", "", time.Second, logger)
	if _, err := exporter.ExportConsole(t.Context()); err == nil {
		t.Fatal("failed export acknowledged")
	}
	if _, err := exporter.ExportConsole(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	same := len(bodies) == 2 && bodies[0] == bodies[1]
	mu.Unlock()
	if !same {
		t.Fatal("retry changed cell identity or contents")
	}
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE outcomes SET status='VERIFIED' WHERE tenant_id=$1 AND action_id=$2`, c.f.tenant, d.ActionID); err != nil {
		t.Fatal(err)
	}
	store := analytics.NewResilient(analytics.NewClickHouse(host.URL+"/read", "lumni", "", "", time.Second), analytics.NewPostgres(c.f.pool), logger)
	from := d.CreatedAt.Truncate(15 * time.Minute)
	rows, err := store.Console(t.Context(), c.f.tenant, from, from.Add(15*time.Minute))
	if err != nil || len(rows) != 1 || rows[0].Outcome != "VERIFIED" || rows[0].Total != 1 {
		t.Fatalf("stale primary won: %+v %v", rows, err)
	}
	if n, err := exporter.ExportConsole(t.Context()); err != nil || n != 2 {
		t.Fatalf("replacement cells %d: %v", n, err)
	}
	mu.Lock()
	latest := bodies[len(bodies)-1]
	failRead = true
	mu.Unlock()
	zero, verified := false, false
	for _, line := range strings.Split(latest, "\n")[1:] {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		zero = zero || (row["outcome"] == "UNKNOWN" && row["total"] == float64(0))
		verified = verified || (row["outcome"] == "VERIFIED" && row["total"] == float64(1))
	}
	if !zero || !verified {
		t.Fatalf("missing replacement or zero cell: %s", latest)
	}
	rows, err = store.Console(t.Context(), c.f.tenant, from, from.Add(15*time.Minute))
	if err != nil || len(rows) != 1 || rows[0].Outcome != "VERIFIED" {
		t.Fatalf("primary outage fallback: %+v %v", rows, err)
	}
}

type consoleSlackFixture struct {
	calls   int
	message string
	err     error
}

func (s *consoleSlackFixture) SendSlack(_ context.Context, _, _, _, message string) error {
	s.calls++
	s.message = message
	return s.err
}

func TestConsoleWorkerDeliversProofAndRetriesTransportFailure(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	d := c.check(t, "deliver-proof")
	p := c.call(t, "POST", "/v1/proofs", map[string]any{"action_id": d.ActionID}, 201)
	// The transport is a local fixture; no external Slack account is contacted.
	if _, err := c.f.pool.Exec(t.Context(), `INSERT INTO notification_config(tenant_id,channels,routes)VALUES($1,'[{"id":"local-slack","kind":"slack","target":"C123","connection_id":"fixture"}]','[]')`, c.f.tenant); err != nil {
		t.Fatal(err)
	}
	job := c.call(t, "POST", "/v1/notifications/send", map[string]any{"channel_id": "local-slack", "kind": "proof", "ref_id": p["id"]}, 202)
	cipher, err := secrets.New("integration-secret")
	if err != nil {
		t.Fatal(err)
	}
	transport := &consoleSlackFixture{}
	worker := notifications.New(c.f.pool, transport).WithCipher(cipher)
	if err = worker.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || !strings.Contains(transport.message, p["url"].(string)) {
		t.Fatal("encrypted proof delivery failed")
	}
	var status string
	var attempts int
	if err = c.f.pool.QueryRow(t.Context(), `SELECT status,attempts FROM notification_jobs WHERE tenant_id=$1 AND id=$2`, c.f.tenant, job["id"]).Scan(&status, &attempts); err != nil || status != "SENT" || attempts != 1 {
		t.Fatalf("delivered job %s/%d: %v", status, attempts, err)
	}
	transport.err = errors.New("fixture outage")
	job = c.call(t, "POST", "/v1/notifications/send", map[string]any{"channel_id": "local-slack", "kind": "proof", "ref_id": p["id"]}, 202)
	if err = worker.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = c.f.pool.QueryRow(t.Context(), `SELECT status,attempts FROM notification_jobs WHERE tenant_id=$1 AND id=$2`, c.f.tenant, job["id"]).Scan(&status, &attempts); err != nil || status != "PENDING" || attempts != 1 {
		t.Fatalf("retry job %s/%d: %v", status, attempts, err)
	}
	if err = worker.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 2 {
		t.Fatal("retry backoff was ignored")
	}
}
