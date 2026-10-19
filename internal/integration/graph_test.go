//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lumnihq/lumni/internal/api"
	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestOperationalGraphSessionAndStream(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	ctx := t.Context()
	agentID := "agent-graph"
	if _, err := f.pool.Exec(ctx, `INSERT INTO agents(tenant_id,id,actor_id,name,framework,model)VALUES($1,$2,$3,'Graph Agent','test','gpt-test')`, f.tenant, agentID, f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE work SET agent_id=$3 WHERE tenant_id=$1 AND id=$2`, f.tenant, f.work, agentID); err != nil {
		t.Fatal(err)
	}
	decision, err := f.kernel.Check(ctx, f.tenant, input(f, "subscription.cancel", "graph-subscription", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Evaluations) != 6 {
		t.Fatalf("decision evaluations=%d, want 6", len(decision.Evaluations))
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO costs(tenant_id,action_id,provider,cost_type,amount_micros_usd,waste_reason,observed_at)
		VALUES($1,$2,'test','MODEL',250000,'CONFLICT',clock_timestamp())`, f.tenant, decision.ActionID); err != nil {
		t.Fatal(err)
	}

	authService := auth.New(f.pool)
	key, err := authService.Create(ctx, f.tenant, protocol.APIKeyCreateInput{Name: "console", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := api.New(f.pool, authService, core.New(f.pool), f.kernel, logger, 1<<20, "", "integration-secret")
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/auth/session", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("session status=%d body=%s", resp.StatusCode, payload)
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookie *http.Cookie
	for _, candidate := range resp.Cookies() {
		if candidate.Name == "lumni_session" {
			cookie = candidate
		}
	}
	if cookie == nil || session.CSRFToken == "" {
		t.Fatal("browser session did not return cookie and CSRF token")
	}

	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/graph/snapshot?lens=agents&limit=500", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot protocol.GraphSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(snapshot.Nodes) < 3 || snapshot.Cursor == 0 {
		t.Fatalf("graph status=%d nodes=%d cursor=%d", resp.StatusCode, len(snapshot.Nodes), snapshot.Cursor)
	}
	foundWaste := false
	for _, item := range snapshot.Attention {
		if item.Category == "HIGH_WASTE" && item.EntityID == decision.ActionID && item.Priority == 40 {
			foundWaste = true
		}
	}
	if !foundWaste {
		t.Fatalf("high-waste action missing from attention queue: %+v", snapshot.Attention)
	}

	req, _ = http.NewRequest(http.MethodPut, httpServer.URL+"/v1/config/mode", strings.NewReader(`{"mode":"WARN"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("session mutation without CSRF status=%d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPut, httpServer.URL+"/v1/config/mode", strings.NewReader(`{"mode":"WARN"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session mutation with CSRF status=%d", resp.StatusCode)
	}

	requestContext, requestCancel := contextWithTimeout(t, 3*time.Second)
	defer requestCancel()
	req, _ = http.NewRequestWithContext(requestContext, http.MethodGet, httpServer.URL+"/v1/graph/stream", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "retry:") {
		t.Fatalf("stream first line=%q err=%v", line, err)
	}
}

func contextWithTimeout(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), timeout)
}

func TestCancelOnlyUnstartedReservation(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	decision, err := f.kernel.Check(t.Context(), f.tenant, input(f, "subscription.cancel", "cancel-safe", ""))
	if err != nil || decision.Decision != protocol.DecisionAllow {
		t.Fatalf("allow=%+v err=%v", decision, err)
	}
	if err := f.kernel.CancelReservation(t.Context(), f.tenant, decision.ActionID); err != nil {
		t.Fatal(err)
	}
	var status string
	var leases int
	if err := f.pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM leases WHERE tenant_id=$1 AND action_id=$2) FROM actions WHERE tenant_id=$1 AND id=$2`, f.tenant, decision.ActionID).Scan(&status, &leases); err != nil {
		t.Fatal(err)
	}
	if status != string(protocol.ActionExpired) || leases != 0 {
		t.Fatalf("status=%s leases=%d", status, leases)
	}
}
