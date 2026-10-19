//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"lumni/internal/api"
	"lumni/internal/auth"
	"lumni/internal/core"
	"lumni/pkg/protocol"
)

func TestHTTPAuthenticationAndActionCheck(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	authService := auth.New(f.pool)
	key, err := authService.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "integration", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := api.New(f.pool, authService, core.New(f.pool), f.kernel, logger, 1<<20, "", "integration-secret")

	unauthorized := httptest.NewRequest(http.MethodGet, "/v1/work/"+f.work, nil)
	unauthorizedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorizedRecorder.Code)
	}
	var apiErr protocol.ErrorResponse
	if err := json.Unmarshal(unauthorizedRecorder.Body.Bytes(), &apiErr); err != nil || apiErr.Error.Code != "UNAUTHENTICATED" {
		t.Fatalf("noncanonical error: %s", unauthorizedRecorder.Body.String())
	}

	payload := input(f, "subscription.cancel", "api-resource", "")
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/v1/actions/check", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var decision protocol.Decision
	if err := json.Unmarshal(recorder.Body.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Decision != protocol.DecisionWouldAllow || decision.Enforced {
		t.Fatalf("unexpected decision %+v", decision)
	}

	replayRequest := httptest.NewRequest(http.MethodGet, "/v1/replay/"+decision.ActionID, nil)
	replayRequest.Header.Set("Authorization", "Bearer "+key.Secret)
	replayRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayRecorder, replayRequest)
	if replayRecorder.Code != http.StatusOK || !bytes.Contains(replayRecorder.Body.Bytes(), []byte(`"deterministic_match":true`)) {
		t.Fatalf("replay status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE decisions SET kernel_version='coord-v0' WHERE tenant_id=$1 AND action_id=$2`, f.tenant, decision.ActionID); err != nil {
		t.Fatal(err)
	}
	replayRequest = httptest.NewRequest(http.MethodGet, "/v1/replay/"+decision.ActionID, nil)
	replayRequest.Header.Set("Authorization", "Bearer "+key.Secret)
	replayRecorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(replayRecorder, replayRequest)
	if replayRecorder.Code != http.StatusUnprocessableEntity || !bytes.Contains(replayRecorder.Body.Bytes(), []byte("UNSUPPORTED_KERNEL_VERSION")) {
		t.Fatalf("unsupported replay status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
}

func TestHTTPKeyCannotReadAnotherTenant(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	authService := auth.New(f.pool)
	var otherTenant string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO tenants(name,slug,enforcement_mode)VALUES('other',$1,'SHADOW')RETURNING id::text`, "other-"+uuid.NewString()).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, otherTenant); err != nil {
			t.Errorf("delete secondary integration tenant: %v", err)
		}
	})
	key, err := authService.Create(t.Context(), otherTenant, protocol.APIKeyCreateInput{Name: "other-query", Scopes: []string{"query"}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := api.New(f.pool, authService, core.New(f.pool), f.kernel, logger, 1<<20, "", "integration-secret")

	request := httptest.NewRequest(http.MethodGet, "/v1/work/"+f.work, nil)
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read returned status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/work/"+f.work, nil)
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	request.Header.Set("X-Lumni-Tenant", f.tenant)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("tenant header mismatch returned status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSemanticTenantOptIn(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	authService := auth.New(f.pool)
	key, err := authService.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "semantic-admin", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := api.New(f.pool, authService, core.New(f.pool), f.kernel, logger, 1<<20, "", "integration-secret")

	set := func(enabled bool) *httptest.ResponseRecorder {
		body := []byte(`{"enabled":false}`)
		if enabled {
			body = []byte(`{"enabled":true}`)
		}
		request := httptest.NewRequest(http.MethodPut, "/v1/config/semantic", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key.Secret)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := set(true); recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"semantic_enabled":true`)) {
		t.Fatalf("enable status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"semantic_enabled":true`)) {
		t.Fatalf("config status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var jobID string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO semantic_jobs(tenant_id,kind,subject_type,subject_id,question_set_version)VALUES($1,'DUPLICATE_WORK','work',$2,'test-v1')RETURNING id::text`, f.tenant, f.work).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if recorder := set(false); recorder.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status, lastError string
	if err := f.pool.QueryRow(t.Context(), `SELECT status,last_error FROM semantic_jobs WHERE id=$1`, jobID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || lastError != "tenant opted out" {
		t.Fatalf("job status=%s last_error=%q", status, lastError)
	}
	if recorder := set(true); recorder.Code != http.StatusOK {
		t.Fatalf("re-enable status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT status,last_error FROM semantic_jobs WHERE id=$1`, jobID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || lastError != "" {
		t.Fatalf("re-enabled job status=%s last_error=%q", status, lastError)
	}
}

func TestHTTPRuleConfigurationAndResolution(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	authService := auth.New(f.pool)
	key, err := authService.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "rules", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := api.New(f.pool, authService, core.New(f.pool), f.kernel, logger, 1<<20, "", "integration-secret")
	rule := []byte(`{"source":"mcp","tool":"cancel_subscription","operation":"subscription.cancel","resources":[{"mode":"WRITE","type":"subscription","id_pointer":"/subscription_id","observed_version_pointer":"/version","version_source":"etag"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/config/resource-rules", bytes.NewReader(rule))
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("resource rule status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	action := []byte(`{"work_id":"` + f.work + `","actor_id":"` + f.actor + `","operation":"cancel_subscription","tool":"cancel_subscription","source":"mcp","arguments":{"subscription_id":"sub-api","version":"1"},"risk":"HIGH"}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/actions/check", bytes.NewReader(action))
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("resolved action status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var decision protocol.Decision
	if err := json.Unmarshal(recorder.Body.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Decision != protocol.DecisionWouldAllow {
		t.Fatalf("resolved decision=%+v", decision)
	}

	policy := []byte(`{"name":"high-refund","operation":"refund.create","resource_type":"payment","minimum_risk":"HIGH","outcome":"REQUIRE_APPROVAL","reason_code":"APPROVAL_REQUIRED"}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/config/policy-rules", bytes.NewReader(policy))
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("policy rule status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var resourceType, outcome, reason string
	if err := f.pool.QueryRow(t.Context(), `SELECT resource_type,outcome,reason_code FROM policy_rules WHERE tenant_id=$1 AND name='high-refund'`, f.tenant).Scan(&resourceType, &outcome, &reason); err != nil {
		t.Fatal(err)
	}
	if resourceType != "payment" || outcome != "REQUIRE_APPROVAL" || reason != "APPROVAL_REQUIRED" {
		t.Fatalf("policy fields=%s/%s/%s", resourceType, outcome, reason)
	}
}
