package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenRouterDecisionRequestRetriesAndParses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zdr" {
			_, _ = w.Write([]byte(`{"data":[{"model_id":"typesafe/jev-1.13"}]}`))
			return
		}
		if r.URL.Path != "/decisions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("X-OpenRouter-Title"); got != "Lumni" {
			t.Errorf("title=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["model"] != "typesafe/jev-1.13" || body["state"] == nil || body["questions"] == nil {
			t.Errorf("unexpected body: %#v", body)
		}
		for _, forbidden := range []string{"messages", "plugins", "tools", "provider"} {
			if _, present := body[forbidden]; present {
				t.Errorf("request contains forbidden field %q", forbidden)
			}
		}
		if calls.Add(1) < 3 {
			http.Error(w, "retry", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"typesafe/jev-1.13",
			"answers":{"safe":{"noul":0.91}},
			"usage":{"input_tokens":100,"output_tokens":0,"total_tokens":100}
		}`))
	}))
	defer server.Close()

	evaluator := NewOpenRouter(OpenRouterConfig{
		APIKey: "test-key", Model: "typesafe/jev-1.13",
		DecisionsURL: server.URL + "/decisions", ZDRURL: server.URL + "/zdr",
		Timeout: time.Second, MaxRetries: 3,
	})
	eligible, err := evaluator.ZDREligible(context.Background())
	if err != nil || !eligible {
		t.Fatalf("ZDR eligible=%v err=%v", eligible, err)
	}
	response, err := evaluator.Evaluate(context.Background(), Request{
		State:     map[string]any{"operation": "payment.refund"},
		Questions: map[string]Question{"safe": {Type: QuestionNoul, Instructions: "Is this safe?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want 3", calls.Load())
	}
	if response.Model != "typesafe/jev-1.13" || response.Answers["safe"].Noul == nil || *response.Answers["safe"].Noul != 0.91 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.CostMicros != 4 {
		t.Fatalf("cost_micros=%d, want 4", response.CostMicros)
	}
}

func TestOpenRouterDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()
	evaluator := NewOpenRouter(OpenRouterConfig{APIKey: "key", Model: "typesafe/jev-1.13", DecisionsURL: server.URL, Timeout: time.Second, MaxRetries: 3})
	_, err := evaluator.Evaluate(context.Background(), Request{State: "state", Questions: map[string]Question{"q": {Type: QuestionNoul, Instructions: "Question?"}}})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", calls.Load())
	}
}

func TestLatestAliasMatchesPinnedZDRFamily(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"model_id":"typesafe/jev-1.13"}]}`))
	}))
	defer server.Close()
	evaluator := NewOpenRouter(OpenRouterConfig{Model: "~typesafe/jev-latest", ZDRURL: server.URL, Timeout: time.Second})
	eligible, err := evaluator.ZDREligible(context.Background())
	if err != nil || !eligible {
		t.Fatalf("eligible=%v err=%v", eligible, err)
	}
}
