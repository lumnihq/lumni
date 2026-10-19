package durable

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStripeLostIDUsesPaginatedAuthoritativeCorrelation(t *testing.T) {
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("missing authentication")
		}
		if r.URL.Query().Get("charge") != "ch_1" {
			t.Error("charge lookup missing")
		}
		if r.URL.Query().Get("starting_after") == "re_other" {
			io.WriteString(w, `{"data":[{"id":"re_target","amount":125,"currency":"usd","status":"succeeded","charge":"ch_1","metadata":{"lumni_effect_id":"effect_1"}}],"has_more":false}`)
		} else {
			io.WriteString(w, `{"data":[{"id":"re_other","metadata":{"lumni_effect_id":"other"}}],"has_more":true}`)
		}
	}))
	defer server.Close()
	s := &Service{client: server.Client()}
	config, _ := json.Marshal(map[string]string{"base_url": server.URL})
	j := verifyJob{Kind: "STRIPE", Secret: []byte("fixture-key"), Config: config, Expected: json.RawMessage(`{"charge":"ch_1","correlation_id":"effect_1","amount":125,"currency":"usd"}`)}
	result, err := s.verify(context.Background(), j, 0)
	if err != nil || result.Status != "VERIFIED" || pages.Load() != 2 {
		t.Fatalf("%+v pages=%d %v", result, pages.Load(), err)
	}
	j.Expected = json.RawMessage(`{"charge":"ch_1","correlation_id":"effect_1","amount":126,"currency":"usd"}`)
	result, err = s.verify(context.Background(), j, 0)
	if err != nil || result.Status != "MISMATCH" {
		t.Fatalf("wrong amount verified %+v %v", result, err)
	}
}
func TestMissingRefundIsPendingAndNotAuthoritativeAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[],"has_more":false}`) }))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"base_url": server.URL})
	s := &Service{client: server.Client()}
	result, err := s.verify(context.Background(), verifyJob{Kind: "STRIPE", Config: config, Expected: json.RawMessage(`{"charge":"ch_1","correlation_id":"pending","amount":100,"currency":"usd"}`)}, 0)
	if err != nil || result.Status != "PENDING" || result.EffectAbsent || result.PriorAttemptTerminated {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestHTTPStatusDoesNotReplaceOutcomeAssertion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"missing"}`) }))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"url": server.URL})
	s := &Service{client: server.Client()}
	result, err := s.verify(context.Background(), verifyJob{Kind: "HTTP", Config: config, Expected: json.RawMessage(`{"status":"succeeded"}`)}, 0)
	if err != nil || result.Status != "MISMATCH" {
		t.Fatalf("HTTP 200 incorrectly verified %+v %v", result, err)
	}
}
func TestGitHubHeadAndMergeReadback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/lumni/test/pulls/7" {
			t.Error(r.URL.Path)
		}
		io.WriteString(w, `{"number":7,"state":"closed","head":{"sha":"expected-head"},"merged":true}`)
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"base_url": server.URL})
	s := &Service{client: server.Client()}
	j := verifyJob{Kind: "GITHUB", Config: config, Expected: json.RawMessage(`{"repository":"lumni/test","number":7,"state":"closed","head":{"sha":"expected-head"},"merged":true}`)}
	result, err := s.verify(context.Background(), j, 0)
	if err != nil || result.Status != "VERIFIED" {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestShopifyCancellationReadback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Shopify-Access-Token") != "fixture" {
			t.Error("unauthenticated GraphQL")
		}
		io.WriteString(w, `{"data":{"order":{"id":"gid://shopify/Order/1","cancelledAt":"2026-09-11T00:00:00Z","refunds":[]}}}`)
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"url": server.URL})
	s := &Service{client: server.Client()}
	j := verifyJob{Kind: "SHOPIFY", Config: config, Secret: []byte("fixture"), Expected: json.RawMessage(`{"order_id":"gid://shopify/Order/1","cancelledAt":"2026-09-11T00:00:00Z"}`)}
	result, err := s.verify(context.Background(), j, 0)
	if err != nil || result.Status != "VERIFIED" {
		t.Fatalf("%+v %v", result, err)
	}
}
