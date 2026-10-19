//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lumnihq/lumni/internal/api"
	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func tenantServer(t *testing.T, keys []string, perHour int) (fixture, *api.Server) {
	t.Helper()
	f := setup(t, protocol.ModeShadow)
	s := api.New(f.pool, auth.New(f.pool), core.New(f.pool), f.kernel, slog.New(slog.NewTextHandler(io.Discard, nil)), 1<<20, "", "integration-secret")
	s.Configure(api.RuntimeOptions{PlatformKeys: keys, TenantsPerHour: perHour})
	return f, s
}

// postTenant calls POST /v1/tenants, and removes any tenant it creates when the test ends.
func postTenant(t *testing.T, f fixture, s *api.Server, key string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/tenants", bytes.NewReader(b))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	out := map[string]any{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if id, ok := out["tenant_id"].(string); ok && id != "" {
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	}
	return rr.Code, out
}

func TestTenantsRouteIsOffWithoutPlatformKey(t *testing.T) {
	f, s := tenantServer(t, nil, 0)
	if code, out := postTenant(t, f, s, "anything", map[string]string{"name": "Acme"}); code != 404 {
		t.Fatalf("got %d, want 404: %v", code, out)
	}
}

func TestTenantsCreatesAnIsolatedWorkspace(t *testing.T) {
	f, s := tenantServer(t, []string{"old-platform-key", "platform-key"}, 0)
	ctx := context.Background()
	for _, key := range []string{"", "wrong", "platform-key-but-longer"} {
		if code, _ := postTenant(t, f, s, key, map[string]string{"name": "Acme"}); code != 401 {
			t.Fatalf("key %q: got %d, want 401", key, code)
		}
	}
	for _, name := range []string{"", "   ", strings.Repeat("x", 81), "bad\nname"} {
		if code, _ := postTenant(t, f, s, "platform-key", map[string]string{"name": name}); code != 400 {
			t.Fatalf("name %q: got %d, want 400", name, code)
		}
	}

	code, out := postTenant(t, f, s, "platform-key", map[string]string{"name": "  Acme Inc.  "})
	if code != 201 {
		t.Fatalf("got %d, want 201: %v", code, out)
	}
	id, _ := out["tenant_id"].(string)
	secret, _ := out["admin_key"].(string)
	slug, _ := out["slug"].(string)
	if id == "" || id == f.tenant || out["name"] != "Acme Inc." || !strings.HasPrefix(slug, "acme-inc-") || secret == "" {
		t.Fatalf("unexpected response: %v", out)
	}

	// The admin key belongs to the new tenant, and only to it.
	p, err := auth.New(f.pool).Authenticate(ctx, secret)
	if err != nil || p.TenantID != id || !slices.Contains(p.Scopes, "admin") {
		t.Fatalf("admin key: %+v, %v", p, err)
	}
	var mode string
	var audits, keys int
	if err := f.pool.QueryRow(ctx, `SELECT enforcement_mode FROM tenants WHERE id=$1`, id).Scan(&mode); err != nil || mode != "SHADOW" {
		t.Fatalf("mode %q, %v", mode, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND type='tenant.created' AND by='platform'`, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit events %d, %v", audits, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE tenant_id=$1 AND secret_hash=$2`, id, auth.Hash(secret)).Scan(&keys); err != nil || keys != 1 {
		t.Fatalf("stored keys %d, %v", keys, err)
	}

	// Either platform key works (rotation); a chosen slug is kept, and taking it again is a conflict.
	mine := "it-" + uuid.NewString()[:8]
	if code, out := postTenant(t, f, s, "old-platform-key", map[string]string{"name": "Other", "slug": mine}); code != 201 || out["slug"] != mine {
		t.Fatalf("chosen slug: %d %v", code, out)
	}
	if code, _ := postTenant(t, f, s, "platform-key", map[string]string{"name": "Third", "slug": mine}); code != 409 {
		t.Fatalf("taken slug: got %d, want 409", code)
	}
	if code, _ := postTenant(t, f, s, "platform-key", map[string]string{"name": "Fourth", "slug": "Not A Slug!"}); code != 400 {
		t.Fatalf("bad slug: got %d, want 400", code)
	}
}

func TestTenantsAreRateLimited(t *testing.T) {
	f, s := tenantServer(t, []string{"platform-key"}, 2)
	for i := 0; i < 2; i++ {
		if code, out := postTenant(t, f, s, "platform-key", map[string]string{"name": "Busy"}); code != 201 {
			t.Fatalf("creation %d: got %d: %v", i+1, code, out)
		}
	}
	if code, _ := postTenant(t, f, s, "platform-key", map[string]string{"name": "Busy"}); code != 429 {
		t.Fatalf("got %d, want 429", code)
	}
}

func TestSignupWorkspacesStartWithLimits(t *testing.T) {
	f, s := tenantServer(t, []string{"platform-key"}, 0)
	s.Configure(api.RuntimeOptions{PlatformKeys: []string{"platform-key"}, TenantChecksPerDay: 2000, TenantChecksPerSecond: 10})
	code, out := postTenant(t, f, s, "platform-key", map[string]string{"name": "Limited"})
	if code != 201 {
		t.Fatalf("got %d: %v", code, out)
	}
	var day, second int
	if err := f.pool.QueryRow(context.Background(), `SELECT (settings->'limits'->>'checks_per_day')::int,(settings->'limits'->>'checks_per_second')::int FROM tenants WHERE id=$1`, out["tenant_id"]).Scan(&day, &second); err != nil || day != 2000 || second != 10 {
		t.Fatalf("limits %d/%d, %v", day, second, err)
	}
}

func TestDailyCheckLimit(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	if _, err := c.f.pool.Exec(context.Background(), `UPDATE tenants SET settings=jsonb_set(settings,'{limits}','{"checks_per_day":2}') WHERE id=$1`, c.f.tenant); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c.call(t, "POST", "/v1/actions/check", checkInput(c.f, fmt.Sprintf("quota-%d", i)), 200)
	}
	out := c.call(t, "POST", "/v1/actions/check", checkInput(c.f, "quota-2"), 429)
	if e, _ := out["error"].(map[string]any); e["code"] != "QUOTA_EXCEEDED" {
		t.Fatalf("got %v", out)
	}
}

func TestChecksPerSecondAllowAShortBurst(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	if _, err := c.f.pool.Exec(context.Background(), `UPDATE tenants SET settings=jsonb_set(settings,'{limits}','{"checks_per_second":1}') WHERE id=$1`, c.f.tenant); err != nil {
		t.Fatal(err)
	}
	// A burst of six (six seconds' worth) goes through; the seventh at once doesn't.
	for i := 0; i < 6; i++ {
		c.call(t, "POST", "/v1/actions/check", checkInput(c.f, fmt.Sprintf("burst-%d", i)), 200)
	}
	out := c.call(t, "POST", "/v1/actions/check", checkInput(c.f, "burst-6"), 429)
	if e, _ := out["error"].(map[string]any); e["code"] != "RATE_LIMITED" {
		t.Fatalf("got %v", out)
	}
}

func TestUnlimitedWorkspacesAreNotCounted(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	for i := 0; i < 70; i++ {
		c.call(t, "POST", "/v1/actions/check", checkInput(c.f, fmt.Sprintf("free-%d", i)), 200)
	}
}

// checkInput is a refund check with its own action id, as SDKs send them.
func checkInput(f fixture, id string) protocol.ActionCheckInput {
	in := input(f, "stripe.refund", id, "")
	in.ActionID = id
	return in
}

// platformCall calls a platform route on the console client's server.
func platformCall(t *testing.T, c consoleClient, method, path, key string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rr, req)
	out := map[string]any{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestPlanMakesAWorkspaceWatchOnlyAndUsageCountsChecks(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeEnforce)
	c.server.Configure(api.RuntimeOptions{PlatformKeys: []string{"platform-key"}})
	ctx := context.Background()
	from := time.Now().Add(-time.Minute).UTC()

	if out := c.call(t, "POST", "/v1/actions/check", checkInput(c.f, "plan-before"), 200); out["enforced"] != true {
		t.Fatalf("before a plan, an enforcing workspace enforces: %v", out)
	}
	plan := map[string]any{"plan": "watch", "limits": map[string]int{"checks_per_day": 500, "checks_per_second": 10}, "watch_only": true}
	if code, _ := platformCall(t, c, "PUT", "/v1/tenants/"+c.f.tenant+"/plan", "wrong", plan); code != 401 {
		t.Fatalf("wrong key: got %d", code)
	}
	if code, _ := platformCall(t, c, "PUT", "/v1/tenants/not-a-tenant/plan", "platform-key", plan); code != 404 {
		t.Fatalf("bad id: got %d", code)
	}
	if code, out := platformCall(t, c, "PUT", "/v1/tenants/"+c.f.tenant+"/plan", "platform-key", plan); code != 200 || out["watch_only"] != true {
		t.Fatalf("plan: %d %v", code, out)
	}
	var watch bool
	var perDay int
	if err := c.f.pool.QueryRow(ctx, `SELECT (settings->>'watch_only')::boolean,(settings->'limits'->>'checks_per_day')::int FROM tenants WHERE id=$1`, c.f.tenant).Scan(&watch, &perDay); err != nil || !watch || perDay != 500 {
		t.Fatalf("settings: %v %d %v", watch, perDay, err)
	}
	if out := c.call(t, "POST", "/v1/actions/check", checkInput(c.f, "plan-after"), 200); out["enforced"] != false {
		t.Fatalf("a watch-only workspace doesn't enforce: %v", out)
	}

	to := time.Now().Add(time.Minute).UTC()
	q := "?from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339))
	code, out := platformCall(t, c, "GET", "/v1/tenants/"+c.f.tenant+"/usage"+q, "platform-key", nil)
	if code != 200 || out["checks"] != float64(2) {
		t.Fatalf("usage: %d %v", code, out)
	}
	if code, _ := platformCall(t, c, "GET", "/v1/tenants/"+c.f.tenant+"/usage?from=x&to=y", "platform-key", nil); code != 400 {
		t.Fatalf("bad range: got %d", code)
	}
}
