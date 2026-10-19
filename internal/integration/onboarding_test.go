//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func onboardingExchange(c consoleClient, code string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": code, "host": "test-host", "project": "agent-project"})
	r := httptest.NewRequest("POST", "/v1/onboarding/exchange", bytes.NewReader(body))
	r.RemoteAddr = "exchange-" + c.f.tenant + ":1234"
	r.Header.Set("X-Forwarded-For", code)
	w := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(w, r)
	return w
}

func TestOnboardingCodeLifecycleAndScope(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	created := c.call(t, "POST", "/v1/onboarding/codes", map[string]any{"scopes": []string{"query"}, "actor_hint": "support-agent", "expires_in_minutes": 5}, 201)
	code := created["code"].(string)
	var hash []byte
	if err := c.f.pool.QueryRow(t.Context(), `SELECT code_hash FROM onboarding_codes WHERE tenant_id=$1`, c.f.tenant).Scan(&hash); err != nil || len(hash) != 32 || !bytes.Equal(hash, auth.Hash(code)) {
		t.Fatalf("code was not stored hashed: %v", err)
	}
	statusPath := "/v1/onboarding/codes/" + url.PathEscape(code) + "/status"
	if state := c.call(t, "GET", statusPath, nil, 200); state["used"] != false || state["host"] != nil || state["project"] != nil || state["key_id"] != nil {
		t.Fatal(state)
	}
	other := newConsoleClient(t, protocol.ModeShadow)
	other.call(t, "GET", statusPath, nil, 404)
	w := onboardingExchange(c, strings.ToLower(code))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	var exchanged map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &exchanged); err != nil {
		t.Fatal(err)
	}
	p, err := auth.New(c.f.pool).Authenticate(t.Context(), exchanged["api_key"].(string))
	if err != nil || p.TenantID != c.f.tenant || len(p.Scopes) != 1 || p.Scopes[0] != "query" {
		t.Fatalf("wrong issued scope: %+v %v", p, err)
	}
	readOnly := c
	readOnly.key = exchanged["api_key"].(string)
	readOnly.call(t, "GET", "/v1/actors", nil, 200)
	readOnly.call(t, "POST", "/v1/actors", map[string]any{"id": "forbidden", "type": "AGENT"}, 403)
	if retry := onboardingExchange(c, code); retry.Code != 404 {
		t.Fatalf("code replay: %d", retry.Code)
	}
	state := c.call(t, "GET", statusPath, nil, 200)
	if state["used"] != true || state["used_at"] == nil || state["actor_id"] != "support-agent" || state["host"] != "test-host" || state["project"] != "agent-project" || state["key_id"] != exchanged["key_id"] {
		t.Fatal(state)
	}
	c.call(t, "POST", "/v1/onboarding/codes", map[string]any{"expires_in_minutes": 16}, 400)
	c.call(t, "POST", "/v1/onboarding/codes", map[string]any{"expires_in_minutes": 0}, 400)
	c.call(t, "POST", "/v1/onboarding/codes", map[string]any{"scopes": []string{"unknown"}}, 400)
}

func TestOnboardingExchangeIsAtomicAndRateLimited(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	code := c.call(t, "POST", "/v1/onboarding/codes", map[string]any{}, 201)["code"].(string)
	var before int
	if err := c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM api_keys WHERE tenant_id=$1`, c.f.tenant).Scan(&before); err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { statuses <- onboardingExchange(c, code).Code })
	}
	wg.Wait()
	close(statuses)
	issued := 0
	for status := range statuses {
		if status == 200 {
			issued++
		} else if status != 404 {
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	var after int
	if err := c.f.pool.QueryRow(t.Context(), `SELECT count(*) FROM api_keys WHERE tenant_id=$1`, c.f.tenant).Scan(&after); err != nil || after != before+1 || issued != 1 {
		t.Fatalf("issued=%d keys=%d -> %d error=%v", issued, before, after, err)
	}
	for range 2 {
		if w := onboardingExchange(c, "unknown"); w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if w := onboardingExchange(c, "another-forwarded-peer"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d", w.Code)
	}
}

func TestOnboardingExpiredAndRevokedIssuerCodesAreIndistinguishable(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	code := c.call(t, "POST", "/v1/onboarding/codes", map[string]any{}, 201)["code"].(string)
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE onboarding_codes SET expires_at=clock_timestamp()-interval '1 second' WHERE code_hash=$1`, auth.Hash(code)); err != nil {
		t.Fatal(err)
	}
	expired := onboardingExchange(c, code)
	unknown := onboardingExchange(c, "unknown")
	if expired.Code != 404 || unknown.Code != 404 {
		t.Fatalf("expired=%d unknown=%d", expired.Code, unknown.Code)
	}
	var a, b map[string]any
	_ = json.Unmarshal(expired.Body.Bytes(), &a)
	_ = json.Unmarshal(unknown.Body.Bytes(), &b)
	if a["error"].(map[string]any)["message"] != b["error"].(map[string]any)["message"] {
		t.Fatal("expiry revealed")
	}
	code = c.call(t, "POST", "/v1/onboarding/codes", map[string]any{}, 201)["code"].(string)
	if _, err := c.f.pool.Exec(t.Context(), `UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1`, auth.Hash(c.key)); err != nil {
		t.Fatal(err)
	}
	if w := onboardingExchange(c, code); w.Code != 404 {
		t.Fatalf("revoked issuer still valid: %d", w.Code)
	}
}

func TestActorsSinceAndTypePreservePagination(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range []struct{ ID, Type string }{{"new-agent-1", "AGENT"}, {"new-human", "HUMAN"}, {"new-agent-2", "AGENT"}} {
		c.call(t, "POST", "/v1/actors", map[string]any{"id": item.ID, "type": item.Type}, 201)
	}
	path := "/v1/actors?since=" + url.QueryEscape(since) + "&type=AGENT&limit=1"
	first := c.call(t, "GET", path, nil, 200)
	if len(first["items"].([]any)) != 1 || first["items"].([]any)[0].(map[string]any)["id"] != "new-agent-2" {
		t.Fatal(first)
	}
	cursor := first["next_cursor"].(string)
	second := c.call(t, "GET", path+"&cursor="+url.QueryEscape(cursor), nil, 200)
	if len(second["items"].([]any)) != 1 || second["items"].([]any)[0].(map[string]any)["id"] != "new-agent-1" || second["next_cursor"] != nil {
		t.Fatal(second)
	}
	c.call(t, "GET", "/v1/actors?since=invalid", nil, 400)
	c.call(t, "GET", "/v1/actors?type=UNKNOWN", nil, 400)
	c.call(t, "GET", path+"&from=2020-01-01T00:00:00Z", nil, 400)
}

func TestOnboardingFilteredGraphStream(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	var cursor int64
	if err := c.f.pool.QueryRow(t.Context(), `SELECT COALESCE(max(sequence),0) FROM graph_changes WHERE tenant_id=$1`, c.f.tenant).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		c.call(t, "POST", "/v1/actors", map[string]any{"id": "stream-actor", "type": "AGENT"}, 201)
	}
	d := c.check(t, "stream-action")
	ctx, cancel := context.WithTimeout(t.Context(), 1300*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", fmt.Sprintf("/v1/graph/stream?types=actor.created,action.checked&cursor=%d", cursor), nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+c.key)
	w := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("stream: %d %s", w.Code, w.Body.String())
	}
	seen := map[string]string{}
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if kind, ok := event["event_type"].(string); ok {
			if _, exists := seen[kind]; exists {
				t.Fatalf("duplicate created event: %s", w.Body.String())
			}
			seen[kind] = event["entity_id"].(string)
		}
	}
	if len(seen) != 2 || seen["actor.created"] != "stream-actor" || seen["action.checked"] != d.ActionID {
		t.Fatalf("wrong filtered events: %s", w.Body.String())
	}
	c.call(t, "GET", "/v1/graph/stream?cursor=not-a-sequence", nil, 400)
	c.call(t, "GET", "/v1/graph/stream?types=invalid-type", nil, 400)
}
