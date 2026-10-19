//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"lumni/internal/api"
	"lumni/internal/auth"
	"lumni/internal/core"
	"lumni/internal/durable"
	"lumni/internal/gateway"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type scenarioAPI struct {
	fixture
	client   *lumni.Client
	url, key string
	runtime  *durable.Service
}

func scenarioServer(t *testing.T) scenarioAPI {
	f := setup(t, protocol.ModeEnforce)
	authn := auth.New(f.pool)
	key, err := authn.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "scenario", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	server := api.New(f.pool, authn, core.New(f.pool), f.kernel, slog.New(slog.NewTextHandler(io.Discard, nil)), 2<<20, "", "scenario-secret")
	host := httptest.NewServer(server.Handler())
	t.Cleanup(host.Close)
	cipher, _ := secrets.New("scenario-secret")
	return scenarioAPI{f, lumni.New(host.URL, key.Secret), host.URL, key.Secret, durable.New(f.pool, f.kernel, cipher)}
}
func (s scenarioAPI) request(t *testing.T, method, path string, input any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(input)
	req, _ := http.NewRequestWithContext(t.Context(), method, s.url+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, body)
	}
	return body
}
func (s scenarioAPI) check(t *testing.T, in protocol.ActionCheckInput, want protocol.DecisionType) protocol.Decision {
	t.Helper()
	d, e := s.client.CheckAction(t.Context(), in)
	if e != nil || d.Decision != want {
		t.Fatalf("admission want %s: %+v %v", want, d, e)
	}
	return d
}
func (s scenarioAPI) invoke(t *testing.T, d protocol.Decision, url, status string) {
	t.Helper()
	if e := s.client.StartAction(t.Context(), d.ActionID, *d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	res, e := http.Post(url, "application/json", bytes.NewBufferString(`{}`))
	var out []byte
	if e == nil {
		out, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	if e != nil && status == "SUCCEEDED" {
		t.Fatal(e)
	}
	if len(out) == 0 {
		out = []byte(`null`)
	}
	if e = s.client.ReportResult(t.Context(), d.ActionID, protocol.ActionResultInput{LeaseToken: d.LeaseToken, Status: status, Output: out}); e != nil {
		t.Fatal(e)
	}
}
func TestSixDocumentedScenariosThroughAPIs(t *testing.T) {
	for _, name := range []string{"duplicate refund", "cancel upgrade conflict", "stale subscription update", "timeout after external write", "partial Stripe CRM workflow", "human agent collision"} {
		t.Run(name, func(t *testing.T) {
			s := scenarioServer(t)
			var calls atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					calls.Add(1)
					if name == "timeout after external write" {
						connection, _, _ := w.(http.Hijacker).Hijack()
						connection.Close()
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"re_fixture","status":"succeeded","amount":100,"currency":"usd"}`)
			}))
			defer external.Close()
			in := input(s.fixture, "payment.refund", "scenario", "1")
			in.ActionID = "first"
			in.Arguments = json.RawMessage(`{"charge":"ch_1","amount":100,"currency":"usd"}`)
			verifier := protocol.Verifier{ID: "readback", Name: "Authoritative fixture", Version: "1", Kind: "HTTP", Config: json.RawMessage(`{"url":"` + external.URL + `"}`), Enabled: true}
			s.request(t, "POST", "/v1/verifiers", verifier)
			in.Verification = &protocol.OutcomeSpec{VerifierID: verifier.ID, Expected: json.RawMessage(`{"status":"succeeded","amount":100,"currency":"usd"}`)}
			switch name {
			case "duplicate refund", "timeout after external write":
				d := s.check(t, in, protocol.DecisionAllow)
				status := "SUCCEEDED"
				if name == "timeout after external write" {
					status = "UNKNOWN"
				}
				s.invoke(t, d, external.URL, status)
				before := s.request(t, "GET", "/v1/outcomes/out_first", nil)
				if bytes.Contains(before, []byte(`"status":"VERIFIED"`)) {
					t.Fatal("transport success produced verified outcome")
				}
				for tries := 0; tries < 100; tries++ {
					if e := s.runtime.VerifyOnce(t.Context()); e != nil {
						t.Fatal(e)
					}
					var status string
					if e := s.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND id='out_first'`, s.tenant).Scan(&status); e != nil {
						t.Fatal(e)
					}
					if status == "VERIFIED" {
						break
					}
				}
				in.ActionID = "second"
				s.check(t, in, protocol.DecisionDeduplicate)
				if calls.Load() != 1 {
					t.Fatalf("external calls=%d", calls.Load())
				}
				outcome := s.request(t, "GET", "/v1/outcomes/out_first", nil)
				if !bytes.Contains(outcome, []byte(`"status":"VERIFIED"`)) {
					t.Fatalf("missing verification: %s", outcome)
				}
				var result string
				if e := s.pool.QueryRow(t.Context(), `SELECT status FROM action_results WHERE tenant_id=$1 AND action_id='first'`, s.tenant).Scan(&result); e != nil || result != status {
					t.Fatalf("transport rewritten %s %v", result, e)
				}
			case "cancel upgrade conflict":
				in.Operation = "subscription.upgrade"
				first := s.check(t, in, protocol.DecisionAllow)
				if e := s.client.StartAction(t.Context(), first.ActionID, *first.LeaseToken); e != nil {
					t.Fatal(e)
				}
				in.ActionID = "second"
				in.Operation = "subscription.cancel"
				s.check(t, in, protocol.DecisionWait)
				if calls.Load() != 0 {
					t.Fatal("conflicting call escaped admission")
				}
			case "stale subscription update", "human agent collision":
				id := s.request(t, "POST", "/v1/resources/resolve", map[string]any{"type": "subscription", "external_id": "scenario"})
				_ = id
				s.request(t, "POST", "/v1/resources/default:subscription:scenario/version", protocol.ResourceVersionInput{Version: "2", VersionSource: "etag", Source: "human-system", Sequence: func() *int64 { n := int64(2); return &n }()})
				in.Operation = "subscription.upgrade"
				s.check(t, in, protocol.DecisionReplan)
				if calls.Load() != 0 {
					t.Fatal("stale action executed")
				}
			case "partial Stripe CRM workflow":
				first := s.check(t, in, protocol.DecisionAllow)
				s.invoke(t, first, external.URL, "SUCCEEDED")
				for tries := 0; tries < 100; tries++ {
					if e := s.runtime.VerifyOnce(t.Context()); e != nil {
						t.Fatal(e)
					}
					var status string
					if e := s.pool.QueryRow(t.Context(), `SELECT status FROM outcomes WHERE tenant_id=$1 AND id='out_first'`, s.tenant).Scan(&status); e != nil {
						t.Fatal(e)
					}
					if status == "VERIFIED" {
						break
					}
				}
				in.ActionID = "crm"
				in.Operation = "crm.note"
				in.Resources[0].ExternalID = "crm"
				second := s.check(t, in, protocol.DecisionAllow)
				s.invoke(t, second, external.URL, "FAILED")
				s.request(t, "POST", "/v1/outcomes/out_crm/evidence", protocol.VerificationEvidence{ID: "crm-readback", Status: "MISMATCH", Observed: json.RawMessage(`{"status":"missing"}`)})
				if e := s.runtime.Tick(t.Context()); e != nil {
					t.Fatal(e)
				}
				findings := s.request(t, "GET", "/v1/findings", nil)
				if !bytes.Contains(findings, []byte("PARTIAL_COMMIT")) {
					t.Fatalf("missing partial finding %s", findings)
				}
				if calls.Load() != 2 {
					t.Fatalf("external calls %d", calls.Load())
				}
			}
			// These are the same evidence APIs used by the connected console.
			for _, collection := range []string{"activity", "actions", "outcomes", "findings", "effects", "journal"} {
				var rows struct {
					Items []json.RawMessage `json:"items"`
				}
				if e := json.Unmarshal(s.request(t, "GET", "/v1/"+collection, nil), &rows); e != nil {
					t.Fatal(e)
				}
			}
			graph := s.request(t, "GET", "/v1/graph/snapshot?lens=live&limit=100", nil)
			if !bytes.Contains(graph, []byte("first")) {
				t.Fatalf("action missing from graph %s", graph)
			}
			s.request(t, "GET", "/v1/replay/first", nil)
		})
	}
}

func TestRecoveryBeforeCallMarkerAndRetryIdentity(t *testing.T) {
	f := setup(t, protocol.ModeEnforce)
	s, c := native(t, f)
	ctx := context.Background()
	action := input(f, "subscription.cancel", "recover", "")
	in := protocol.StepInput{RuntimeAuth: owner(c), Key: "mutate", Type: "TOOL", Action: &action}
	st, err := s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "READY" {
		t.Fatalf("%+v", st)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE work SET claim_expires_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, c.WorkID); err != nil {
		t.Fatal(err)
	}
	c, err = s.Claim(ctx, f.tenant, protocol.ClaimInput{WorkerID: "replacement", Handlers: map[string]string{"test": "1"}})
	if err != nil || c == nil {
		t.Fatalf("%v %v", c, err)
	}
	in.RuntimeAuth = owner(c)
	recovered, err := s.Begin(ctx, f.tenant, c.WorkID, in)
	if err != nil || recovered.Status != "READY" {
		t.Fatalf("resume %+v %v", recovered, err)
	}
	if *recovered.ActionID == *st.ActionID {
		t.Fatal("unstarted abandoned attempt reused")
	}
	wait := protocol.StepInput{RuntimeAuth: owner(c), Key: "timer", Type: "WAIT", Wait: &protocol.WaitInput{Type: "TIMER", DelayMS: 10}}
	first, err := s.Begin(ctx, f.tenant, c.WorkID, wait)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Begin(ctx, f.tenant, c.WorkID, wait)
	if err != nil || again.ID != first.ID {
		t.Fatalf("lost wait response %+v %v", again, err)
	}
	time.Sleep(15 * time.Millisecond)
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestModelGatewayCompletenessAndRetryIdentity(t *testing.T) {
	s := scenarioServer(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chat","choices":[{"index":0,"message":{"role":"assistant","content":"complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer provider.Close()
	proxy := gateway.New(gateway.Config{Client: s.client, ActorID: s.actor, WorkID: s.work})
	handler, e := proxy.ModelHandler(provider.URL)
	if e != nil {
		t.Fatal(e)
	}
	host := httptest.NewServer(handler)
	defer host.Close()
	call := func(id string, stream bool) []byte {
		body, _ := json.Marshal(map[string]any{"model": "fixture", "stream": stream, "messages": []any{}})
		req, _ := http.NewRequest("POST", host.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("X-Lumni-Call", id)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
		return b
	}
	first := call("complete-model", false)
	again := call("complete-model", false)
	if !bytes.Equal(first, again) || calls.Load() != 1 {
		t.Fatalf("model repeated %d", calls.Load())
	}
	call("interrupted-model", true)
	data := s.request(t, "GET", "/v1/model-calls/interrupted-model", nil)
	if !bytes.Contains(data, []byte(`"status":"INCOMPLETE"`)) {
		t.Fatalf("interruption marked complete %s", data)
	}
	if bytes.Contains(data, []byte("partial")) {
		t.Fatal("plaintext leaked to metadata query")
	}
}
func TestQueryMCPToolsDoNotCreateActions(t *testing.T) {
	s := scenarioServer(t)
	req, _ := http.NewRequest("POST", s.url+"/v1/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	for _, name := range []string{"lumni_recent_findings", "lumni_explain_action", "lumni_explain_incident", "lumni_get_resource_activity", "lumni_get_outcome", "lumni_get_conflicts", "lumni_replay_incident"} {
		if !bytes.Contains(b, []byte(name)) {
			t.Fatalf("missing query tool %s: %d %s", name, res.StatusCode, b)
		}
	}
	var count int
	s.pool.QueryRow(t.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1`, s.tenant).Scan(&count)
	if count != 0 {
		t.Fatal("query interception created actions")
	}
}
