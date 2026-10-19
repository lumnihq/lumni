//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lumnihq/lumni/internal/api"
	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/composio"
	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/internal/durable"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type composioUpstream struct {
	mu                                                                sync.Mutex
	user, account, session, state, updated, writeFailure, readFailure string
	writes, reads                                                     int
}

func (u *composioUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if r.Header.Get("x-api-key") != "cloud-key" {
		w.WriteHeader(401)
		return
	}
	var body map[string]any
	if r.Method == "POST" {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case r.URL.Path == "/connected_accounts/link":
		u.user, _ = body["user_id"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"connected_account_id": u.account, "redirect_url": "https://connect.composio.dev/test", "expires_at": "2026-10-01T12:00:00Z"})
	case r.URL.Path == "/connected_accounts/"+u.account && r.Method == "GET":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.account, "user_id": u.user, "status": "ACTIVE", "toolkit": map[string]string{"slug": "github"}, "auth_config": map[string]string{"id": "ac_own"}, "state": map[string]string{"access_token": "DO_NOT_EXPOSE"}})
	case r.URL.Path == "/connected_accounts/"+u.account && r.Method == "DELETE":
		_, _ = w.Write([]byte(`{"success":true}`))
	case r.URL.Path == "/tool_router/session":
		_, _ = w.Write([]byte(`{"session_id":"` + u.session + `"}`))
	case r.URL.Path == "/tool_router/session/"+u.session+"/execute":
		args, _ := body["arguments"].(map[string]any)
		if args["owner"] != "test" || args["repo"] != "sandbox" || body["account"] != u.account {
			w.WriteHeader(403)
			return
		}
		if body["tool_slug"] == composio.UpdateTool {
			u.writes++
			if u.writeFailure == "429" {
				w.WriteHeader(429)
				return
			}
			if u.writeFailure == "401" {
				w.WriteHeader(401)
				return
			}
			u.state, _ = args["state"].(string)
			u.updated = "2026-10-01T11:00:01Z"
			if u.writeFailure == "lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
		} else if body["tool_slug"] == composio.ReadTool {
			u.reads++
			if u.readFailure != "" {
				status, _ := strconv.Atoi(u.readFailure)
				w.WriteHeader(status)
				return
			}
		} else {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": 101, "number": 1, "state": u.state, "updated_at": u.updated, "head": map[string]string{"sha": "abc"}, "html_url": "https://github.com/test/sandbox/pull/1", "merged": false}})
	default:
		w.WriteHeader(404)
	}
}

type composioFixture struct {
	fixture
	service    *composio.Service
	runtime    *durable.Service
	upstream   *composioUpstream
	client     *composio.Client
	connection string
	pr         composio.PullRequest
}

func setupComposio(t *testing.T, mode protocol.EnforcementMode) composioFixture {
	t.Helper()
	f := setup(t, mode)
	upstream := &composioUpstream{account: "ca_" + uuid.NewString(), session: "trs_" + uuid.NewString(), state: "open", updated: "2026-10-01T11:00:00Z"}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	client, err := composio.NewClient(composio.Options{APIKey: "cloud-key", AuthConfigID: "ac_own", BaseURL: server.URL, WebhookSecret: "webhook-secret"})
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secrets.New("integration-secret")
	runtime := durable.New(f.pool, f.kernel, cipher)
	service := composio.NewService(f.pool, client, f.kernel, runtime, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runtime.SetGitHubConnectorReader(service)
	connected, err := service.Connect(t.Context(), f.tenant, composio.ConnectInput{Repository: "Test/Sandbox", CloudProcessingConsent: true})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := service.Status(t.Context(), f.tenant, connected.Connection.ID)
	if err != nil || connection.Status != "ACTIVE" {
		t.Fatalf("connection=%+v err=%v", connection, err)
	}
	data, err := service.Read(t.Context(), f.tenant, connection.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	var pr composio.PullRequest
	if err = json.Unmarshal(data, &pr); err != nil {
		t.Fatal(err)
	}
	return composioFixture{fixture: f, service: service, runtime: runtime, upstream: upstream, client: client, connection: connection.ID, pr: pr}
}
func (f composioFixture) action(id string) composio.ExecuteInput {
	return composio.ExecuteInput{ActionID: id, ActorID: f.actor, WorkID: f.work, Number: 1, State: "closed", ObservedVersion: f.pr.ObservedVersion}
}
func outcomeStatus(t *testing.T, f composioFixture, action string) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, f.tenant, action).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestComposioBlockedActionDoesNotReachProvider(t *testing.T) {
	f := setupComposio(t, protocol.ModeEnforce)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO policy_rules(tenant_id,name,outcome,reason_code)VALUES($1,'block-composio','REJECT','POLICY_REJECTED')`, f.tenant); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Execute(t.Context(), f.tenant, f.connection, f.action("blocked"))
	if err != nil || out.Status != "BLOCKED" || f.upstream.writes != 0 {
		t.Fatalf("out=%+v writes=%d err=%v", out, f.upstream.writes, err)
	}
}

func TestComposioConcurrentDuplicatesStartOneProviderMutation(t *testing.T) {
	f := setupComposio(t, protocol.ModeEnforce)
	otherActor, otherWork := f.actor+"_other", f.work+"_other"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO actors(tenant_id,id,type)VALUES($1,$2,'AGENT')`, f.tenant, otherActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO work(tenant_id,id,actor_id,goal,status)VALUES($1,$2,$3,'same GitHub effect','RUNNING')`, f.tenant, otherWork, otherActor); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		i := i
		wg.Go(func() {
			in := f.action("concurrent_" + strconv.Itoa(i))
			if i%2 == 1 {
				in.ActorID, in.WorkID = otherActor, otherWork
			}
			_, err := f.service.Execute(t.Context(), f.tenant, f.connection, in)
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.upstream.writes != 1 {
		t.Fatalf("provider writes=%d", f.upstream.writes)
	}
	// Provider success alone must leave an unverified outcome.
	var action string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM actions WHERE tenant_id=$1 AND started_at IS NOT NULL`, f.tenant).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if status := outcomeStatus(t, f, action); status != "PENDING" {
		t.Fatalf("premature outcome=%s", status)
	}
	// One worker iteration claims one eligible outcome; concurrent deduplication
	// receipts may also have eligible outcomes, so drain this bounded fixture.
	for i := 0; i < 17 && outcomeStatus(t, f, action) == "PENDING"; i++ {
		if err := f.runtime.VerifyOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if status := outcomeStatus(t, f, action); status != "VERIFIED" {
		t.Fatalf("outcome=%s", status)
	}
	if _, err := f.service.Execute(t.Context(), f.tenant, f.connection, f.action("another_duplicate")); err != nil {
		t.Fatal(err)
	}
	if f.upstream.writes != 1 {
		t.Fatal("duplicate was sent upstream after verification")
	}
}

func TestComposioStaleVersionAndRepositoryAllowlist(t *testing.T) {
	f := setupComposio(t, protocol.ModeEnforce)
	in := f.action("stale")
	in.ObservedVersion = "an-older-provider-version"
	out, err := f.service.Execute(t.Context(), f.tenant, f.connection, in)
	if err != nil || out.Status != "BLOCKED" || f.upstream.writes != 0 {
		t.Fatalf("stale action=%+v writes=%d err=%v", out, f.upstream.writes, err)
	}
	reads := f.upstream.reads
	if _, err := f.service.ReadPullRequest(t.Context(), f.tenant, f.connection, "other/repository", 1); err == nil || f.upstream.reads != reads {
		t.Fatal("repository outside the allowlist reached the provider")
	}
}

func TestComposioUncertainWritesAndVerifierOutagesPreserveHolds(t *testing.T) {
	for _, failure := range []string{"lost", "401", "429"} {
		t.Run(failure, func(t *testing.T) {
			f := setupComposio(t, protocol.ModeEnforce)
			f.upstream.writeFailure = failure
			f.upstream.readFailure = "429"
			out, err := f.service.Execute(t.Context(), f.tenant, f.connection, f.action("uncertain"))
			if err != nil || out.Status != "PENDING" {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if err = f.runtime.VerifyOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if outcomeStatus(t, f, "uncertain") != "PENDING" {
				t.Fatal("outage resolved an uncertain effect")
			}
			var held bool
			if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM effect_holds WHERE tenant_id=$1 AND action_id='uncertain')`, f.tenant).Scan(&held); err != nil || !held {
				t.Fatalf("held=%v err=%v", held, err)
			}
			if _, err = f.service.Execute(t.Context(), f.tenant, f.connection, f.action("uncertain")); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Execute(t.Context(), f.tenant, f.connection, f.action("uncertain_new_id")); err != nil {
				t.Fatal(err)
			}
			if f.upstream.writes != 1 {
				t.Fatalf("blind retry, writes=%d", f.upstream.writes)
			}
			if failure == "lost" {
				f.upstream.readFailure = ""
				_, _ = f.pool.Exec(t.Context(), `UPDATE outcomes SET next_attempt_at=now() WHERE tenant_id=$1 AND action_id='uncertain'`, f.tenant)
				if err = f.runtime.VerifyOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
				if outcomeStatus(t, f, "uncertain") != "VERIFIED" || f.upstream.writes != 1 {
					t.Fatal("lost response was not recovered by read-back")
				}
			}
		})
	}
}

func TestComposioTenantBoundaryAndDisconnect(t *testing.T) {
	f := setupComposio(t, protocol.ModeEnforce)
	foreign := setup(t, protocol.ModeEnforce)
	key, err := auth.New(f.pool).Create(t.Context(), foreign.tenant, protocol.APIKeyCreateInput{Name: "foreign", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	server := api.New(f.pool, auth.New(f.pool), core.New(f.pool), f.kernel, slog.New(slog.NewTextHandler(io.Discard, nil)), 1<<20, "", "integration-secret")
	server.Configure(api.RuntimeOptions{Composio: f.client})
	body, _ := json.Marshal(f.action("foreign"))
	for _, test := range []struct {
		method, path string
		body         []byte
	}{
		{"GET", "", nil}, {"DELETE", "", nil}, {"GET", "/pull-requests/1", nil}, {"GET", "/metrics", nil}, {"POST", "/execute", body},
	} {
		r := httptest.NewRequest(test.method, "/v1/integrations/composio/connections/"+f.connection+test.path, bytes.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+key.Secret)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("cross tenant %s %s returned %d: %s", test.method, test.path, w.Code, w.Body)
		}
	}
	if f.upstream.writes != 0 {
		t.Fatal("foreign tenant reached provider")
	}
	if err = f.service.Disconnect(t.Context(), f.tenant, f.connection); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Read(t.Context(), f.tenant, f.connection, 1); err == nil {
		t.Fatal("disconnected account still readable")
	}
	if _, err = f.service.Execute(t.Context(), f.tenant, f.connection, f.action("after_disconnect")); err == nil {
		t.Fatal("disconnected account executed")
	}
}

func TestComposioSignedEventsDeduplicateAndRemainObservational(t *testing.T) {
	f := setupComposio(t, protocol.ModeEnforce)
	server := api.New(f.pool, auth.New(f.pool), core.New(f.pool), f.kernel, slog.New(slog.NewTextHandler(io.Discard, nil)), 1<<20, "", "integration-secret")
	server.Configure(api.RuntimeOptions{Composio: f.client})
	event := map[string]any{"id": "msg_1", "type": "composio.trigger.message", "timestamp": time.Now().UTC(), "metadata": map[string]string{"connected_account_id": f.upstream.account, "user_id": f.upstream.user, "trigger_slug": "GITHUB_PULL_REQUEST_EVENT"}, "data": map[string]string{"state": "closed"}}
	body, _ := json.Marshal(event)
	deliver := func(valid bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/webhooks/composio", bytes.NewReader(body))
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		r.Header.Set("webhook-id", "msg_1")
		r.Header.Set("webhook-timestamp", stamp)
		r.Header.Set("x-composio-webhook-version", "V3")
		mac := hmac.New(sha256.New, []byte("webhook-secret"))
		_, _ = mac.Write([]byte("msg_1." + stamp + "."))
		_, _ = mac.Write(body)
		signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "bad"
		}
		r.Header.Set("webhook-signature", "v1,"+signature)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	if w := deliver(false); w.Code != 401 {
		t.Fatalf("invalid signature status=%d", w.Code)
	}
	if w := deliver(true); w.Code != 200 || !strings.Contains(w.Body.String(), `"recorded":true`) {
		t.Fatalf("event=%s", w.Body)
	}
	if w := deliver(true); w.Code != 200 || !strings.Contains(w.Body.String(), `"recorded":false`) {
		t.Fatalf("duplicate=%s", w.Body)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND source='composio'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("events=%d err=%v", count, err)
	}
	data, err := f.service.Read(t.Context(), f.tenant, f.connection, 1)
	if err != nil || !strings.Contains(string(data), `"state":"open"`) {
		t.Fatal("webhook became authoritative state")
	}
}
