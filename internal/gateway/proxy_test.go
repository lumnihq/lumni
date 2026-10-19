package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
	lumni "github.com/lumnihq/lumni/sdk/go"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

type coordinatorFixture struct {
	mu       sync.Mutex
	decision protocol.Decision
	starts   int
	results  []string
}

func (f *coordinatorFixture) client() *lumni.Client {
	client := lumni.New("http://coordinator.test", "test")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v1/actors" || r.URL.Path == "/v1/sessions" || r.URL.Path == "/v1/work":
			body, _ := io.ReadAll(r.Body)
			return response(r, http.StatusCreated, string(body)), nil
		case r.URL.Path == "/v1/actions/check":
			data, _ := json.Marshal(f.decision)
			return response(r, http.StatusOK, string(data)), nil
		case r.URL.Path == "/v1/actions/"+f.decision.ActionID+"/start":
			f.mu.Lock()
			f.starts++
			f.mu.Unlock()
			return response(r, http.StatusOK, `{"status":"ACTIVE"}`), nil
		case r.URL.Path == "/v1/actions/"+f.decision.ActionID+"/result":
			var result protocol.ActionResultInput
			_ = json.NewDecoder(r.Body).Decode(&result)
			f.mu.Lock()
			f.results = append(f.results, result.Status)
			f.mu.Unlock()
			return response(r, http.StatusOK, `{"status":"recorded"}`), nil
		default:
			return response(r, http.StatusNotFound, `{}`), nil
		}
	})}
	return client
}

func upstreamTransport(calls *int, body string) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		(*calls)++
		return response(r, http.StatusOK, body), nil
	})
}

func TestHTTPProxyTransparentlyRelaysDiscovery(t *testing.T) {
	proxy := New(Config{Client: lumni.New("http://unused.invalid", "test")})
	upstreamCalls := 0
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"next"}}`
	handler, err := proxy.httpHandler("http://upstream.test", upstreamTransport(&upstreamCalls, body))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
	if upstreamCalls != 1 || recorder.Code != http.StatusOK || recorder.Body.String() != body {
		t.Fatalf("discovery was not transparent: calls=%d status=%d body=%s", upstreamCalls, recorder.Code, recorder.Body.String())
	}
}

func TestStdioDiscoveryAndEnforcementResponse(t *testing.T) {
	discovery := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	proxy := New(Config{Client: lumni.New("http://unused.invalid", "test")})
	var upstream, client bytes.Buffer
	if err := proxy.forwardRequests(context.Background(), bytes.NewReader(discovery), &upstream, &client); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream.Bytes(), discovery) || client.Len() != 0 {
		t.Fatalf("stdio discovery changed: upstream=%q client=%q", upstream.String(), client.String())
	}

	decision := protocol.Decision{SchemaVersion: protocol.SchemaVersion, ActionID: "stdio-wait", ReceiptID: "receipt", Decision: protocol.DecisionWait, ReasonCode: protocol.ReasonWriteWriteConflict, Enforced: true, CreatedAt: time.Now()}
	coordinator := &coordinatorFixture{decision: decision}
	proxy = New(Config{Client: coordinator.client(), ActorID: "actor", WorkID: "work", Risk: protocol.RiskHigh})
	call := []byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"cancel\",\"arguments\":{}}}\n")
	upstream.Reset()
	client.Reset()
	if err := proxy.forwardRequests(context.Background(), bytes.NewReader(call), &upstream, &client); err != nil {
		t.Fatal(err)
	}
	if upstream.Len() != 0 || !strings.Contains(client.String(), `"lumni_code":"WRITE_WRITE_CONFLICT"`) {
		t.Fatalf("stdio WAIT was not structured: upstream=%q client=%q", upstream.String(), client.String())
	}
}

func TestHTTPProxyForwardsShadowAndRecordsResult(t *testing.T) {
	decision := protocol.Decision{SchemaVersion: protocol.SchemaVersion, ActionID: "shadow-action", ReceiptID: "receipt", Decision: protocol.DecisionWouldReplan, ReasonCode: protocol.ReasonStaleObservation, Enforced: false, CreatedAt: time.Now()}
	coordinator := &coordinatorFixture{decision: decision}
	upstreamCalls := 0
	proxy := New(Config{Client: coordinator.client(), ActorID: "actor", SessionID: "session", WorkID: "work", Risk: protocol.RiskHigh})
	handler, _ := proxy.httpHandler("http://upstream.test", upstreamTransport(&upstreamCalls, `{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"refund","arguments":{"id":"p1"}}}`)))
	if upstreamCalls != 1 || recorder.Code != http.StatusOK {
		t.Fatalf("shadow call was blocked: calls=%d status=%d body=%s", upstreamCalls, recorder.Code, recorder.Body.String())
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.results) != 1 || coordinator.results[0] != "SUCCEEDED" || coordinator.starts != 1 {
		t.Fatalf("unexpected lifecycle: starts=%d results=%v", coordinator.starts, coordinator.results)
	}
}

func TestHTTPProxyBlocksWaitWithoutCallingUpstream(t *testing.T) {
	decision := protocol.Decision{SchemaVersion: protocol.SchemaVersion, ActionID: "wait-action", ReceiptID: "receipt", Decision: protocol.DecisionWait, ReasonCode: protocol.ReasonWriteWriteConflict, Enforced: true, CreatedAt: time.Now()}
	coordinator := &coordinatorFixture{decision: decision}
	upstreamCalls := 0
	proxy := New(Config{Client: coordinator.client(), ActorID: "actor", WorkID: "work", Risk: protocol.RiskHigh})
	handler, _ := proxy.httpHandler("http://upstream.test", upstreamTransport(&upstreamCalls, `{}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"cancel","arguments":{}}}`)))
	if upstreamCalls != 0 {
		t.Fatalf("WAIT reached upstream %d times", upstreamCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"lumni_code":"WRITE_WRITE_CONFLICT"`) {
		t.Fatalf("unexpected structured response: %s", recorder.Body.String())
	}
}

func TestCoordinatorOutagePolicy(t *testing.T) {
	client := lumni.New("http://coordinator.test", "test")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("coordinator unavailable")
	})}
	upstreamCalls := 0
	transport := upstreamTransport(&upstreamCalls, `{"jsonrpc":"2.0","id":3,"result":{}}`)
	requestBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{}}}`
	low := New(Config{Client: client, ActorID: "actor", WorkID: "work", Risk: protocol.RiskLow})
	lowHandler, _ := low.httpHandler("http://upstream.test", transport)
	lowRecorder := httptest.NewRecorder()
	lowHandler.ServeHTTP(lowRecorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(requestBody)))
	if upstreamCalls != 1 {
		t.Fatalf("low-risk outage did not fail open")
	}
	high := New(Config{Client: client, ActorID: "actor", WorkID: "work", Risk: protocol.RiskHigh, Mode: protocol.ModeEnforce})
	highHandler, _ := high.httpHandler("http://upstream.test", transport)
	highRecorder := httptest.NewRecorder()
	highHandler.ServeHTTP(highRecorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(requestBody)))
	if upstreamCalls != 1 || !strings.Contains(highRecorder.Body.String(), "COORDINATOR_UNAVAILABLE") {
		t.Fatalf("high-risk outage did not fail closed: calls=%d body=%s", upstreamCalls, highRecorder.Body.String())
	}
}
