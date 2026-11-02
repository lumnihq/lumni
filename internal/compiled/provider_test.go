package compiled

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestGenerationUsesExplicitModelSchemaAndRedaction(t *testing.T) {
	program := echoProgram()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid provider request")
		}
		if body["model"] != "configured-model" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("generation model/key not explicit")
		}
		if body["provider"].(map[string]any)["zdr"] != true {
			t.Fatal("ZDR routing missing")
		}
		if body["response_format"].(map[string]any)["type"] != "json_schema" {
			t.Fatal("schema output missing")
		}
		text := body["messages"].([]any)[1].(map[string]any)["content"].(string)
		if strings.Contains(text, "never-send-this") {
			t.Fatal("credential sent to provider")
		}
		content, _ := json.Marshal(program)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	g := &OpenRouter{URL: server.URL, APIKey: "fixture-key", Model: "configured-model", Client: server.Client()}
	_, e := g.Generate(context.Background(), Synthesis{Traces: []protocol.Trace{{Input: json.RawMessage(`{"api_key":"never-send-this","ok":true}`)}}})
	if e != nil {
		t.Fatal(e)
	}
}
func TestGenerationRejectsTruncatedAndMalformedPrograms(t *testing.T) {
	for _, finish := range []string{"length", "stop"} {
		t.Run(finish, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"choices":[{"message":{"content":"{\"interpreter\":\"shell\"}"},"finish_reason":%q}]}`, finish)
			}))
			defer server.Close()
			g := &OpenRouter{URL: server.URL, Model: "model", Client: server.Client()}
			if _, e := g.Generate(context.Background(), Synthesis{}); e == nil {
				t.Fatal("untrusted generated artifact accepted")
			}
		})
	}
}
