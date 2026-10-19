package gateway

import (
	"context"
	"encoding/json"
	"github.com/lumnihq/lumni/pkg/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEPreservesBytesAndIsError(t *testing.T) {
	for _, test := range []struct{ name, payload, want string }{{"success", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`, "SUCCEEDED"}, {"tool_error", `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"failed"}]}}`, "UNKNOWN"}, {"notification_only", `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`, "UNKNOWN"}} {
		t.Run(test.name, func(t *testing.T) {
			f := &coordinatorFixture{decision: protocol.Decision{ActionID: "stream", Decision: protocol.DecisionWouldAllow}}
			p := New(Config{Client: f.client(), ActorID: "actor", WorkID: "work"})
			wire := "event: message\ndata: " + test.payload + "\n\n"
			handler, _ := p.httpHandler("http://upstream.test", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp := response(r, 200, wire)
				resp.Header.Set("Content-Type", "text/event-stream")
				return resp, nil
			}))
			r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund","arguments":{}}}`))
			r.Header.Set("Mcp-Session-Id", "s1")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Body.String() != wire {
				t.Fatal("SSE changed")
			}
			if len(f.results) != 1 || f.results[0] != test.want {
				t.Fatalf("%v", f.results)
			}
		})
	}
}
func TestSessionScopedRequestIDs(t *testing.T) {
	f := &coordinatorFixture{}
	p := New(Config{Client: f.client()})
	p.active["a:1"] = activeCall{ActionID: "a"}
	p.active["b:1"] = activeCall{ActionID: "b"}
	ctx := context.WithValue(context.Background(), sessionKey{}, "a")
	p.recordResponse(ctx, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	if _, ok := p.active["b:1"]; !ok {
		t.Fatal("another session's request was consumed")
	}
	if _, ok := p.active["a:1"]; ok {
		t.Fatal("completed request retained")
	}
}
func TestChatReconstructionAndInterruptedChoice(t *testing.T) {
	events := []json.RawMessage{json.RawMessage(`{"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello ","tool_calls":[{"index":0,"id":"call_1","function":{"name":"refund","arguments":"{\"amount\":"}}]}}]}`), json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"world","tool_calls":[{"index":0,"function":{"arguments":"100}"}}]},"finish_reason":"tool_calls"}]}`)}
	b, e := assembleChat(events)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `hello world`) || !strings.Contains(string(b), `{\"amount\":100}`) {
		t.Fatalf("%s", b)
	}
	if _, e = assembleChat(events[:1]); e == nil {
		t.Fatal("interrupted stream accepted")
	}
}
func TestWireObserverFragmentedFrames(t *testing.T) {
	var got []string
	o := newWireObserver(true, func(b []byte) { got = append(got, string(b)) })
	for _, b := range []string{"data: {\"a\":", "1}\r\n", "\r\ndata: [DONE]\n\n"} {
		o.feed([]byte(b))
	}
	o.finish(true)
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != "[DONE]" {
		t.Fatalf("%v", got)
	}
}
func TestOpenAPIDiscoveryPathAndOpaqueA2A(t *testing.T) {
	routes, e := OpenAPIRoutes([]byte(`{"paths":{"/orders/{id}":{"post":{"operationId":"cancelOrder"},"parameters":[]}}}`))
	if e != nil || len(routes) != 1 {
		t.Fatalf("%v %v", routes, e)
	}
	params, ok := matchPath(routes[0].Path, "/orders/123")
	if !ok || params["id"] != "123" {
		t.Fatal(params)
	}
	wire := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"kind":"text","text":"cancel anything"}]}}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != wire {
			t.Error("opaque task changed")
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"id":"task","contextId":"ctx"}}`)
	}))
	defer up.Close()
	p := New(Config{Mode: protocol.ModeEnforce})
	h, _ := p.A2AHandler(up.URL)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(wire)))
	if w.Code != 200 || w.Header().Get("X-Lumni-Coverage") != "observational-task" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}
