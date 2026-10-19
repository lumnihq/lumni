package composio

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDisabledAndInvalidConfiguration(t *testing.T) {
	client, err := NewClient(Options{})
	if err != nil || client != nil {
		t.Fatal("must default off")
	}
	for _, options := range []Options{
		{APIKey: "key"},
		{APIKey: "key", AuthConfigID: "ac", BaseURL: "http://example.com"},
		{APIKey: "key", AuthConfigID: "ac", CallbackURL: "http://example.com"},
		{APIKey: "key", AuthConfigID: "ac", Timeout: 30 * time.Second},
	} {
		if _, err := NewClient(options); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
}

func TestSessionPinsOwnershipAndDisablesOpaqueExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tool_router/session" || r.Header.Get("x-api-key") != "test-key" {
			t.Error("wrong route or credential")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["user_id"] != "tenant-bound" || body["instant"] != false {
			t.Error("wrong identity or instant access")
		}
		accounts := body["connected_accounts"].(map[string]any)["github"].([]any)
		if len(accounts) != 1 || accounts[0] != "ca_owned" {
			t.Error("account not pinned")
		}
		tools := body["tools"].(map[string]any)["github"].(map[string]any)["enable"].([]any)
		if len(tools) != 2 || tools[0] != ReadTool || tools[1] != UpdateTool {
			t.Error("wrong tool allowlist")
		}
		for _, key := range []string{"workbench", "search", "manage_connections"} {
			if body[key].(map[string]any)["enable"] != false {
				t.Errorf("%s not disabled", key)
			}
		}
		if body["execute"].(map[string]any)["enable_multi_execute"] != false {
			t.Error("batch execution enabled")
		}
		_, _ = w.Write([]byte(`{"session_id":"trs_owned"}`))
	}))
	defer server.Close()
	client, _ := NewClient(Options{APIKey: "test-key", AuthConfigID: "ac", BaseURL: server.URL})
	id, err := client.Session(context.Background(), "tenant-bound", "ca_owned")
	if err != nil || id != "trs_owned" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}

func TestMutationMakesOneRequestAndRedactsErrors(t *testing.T) {
	for _, status := range []int{401, 429, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"secret":"provider-access-token"}`))
			}))
			defer server.Close()
			client, _ := NewClient(Options{APIKey: "project-secret", AuthConfigID: "ac", BaseURL: server.URL})
			_, err := client.Execute(context.Background(), "session", "account", UpdateTool, map[string]any{})
			if err == nil || calls != 1 || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			_, err = client.Execute(context.Background(), "session", "account", "COMPOSIO_MULTI_EXECUTE_TOOL", nil)
			if err == nil || calls != 1 {
				t.Fatal("unapproved tool reached upstream")
			}
		})
	}
}

func TestCredentialsNeverFollowRedirects(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer server.Close()
	client, _ := NewClient(Options{APIKey: "key", AuthConfigID: "ac", BaseURL: server.URL})
	_, err := client.Execute(context.Background(), "session", "account", ReadTool, nil)
	if err == nil || leaked {
		t.Fatal("redirect was followed")
	}
}

func TestStatusRejectsForeignAccountAndDoesNotReturnCredentialState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ca","user_id":"foreign","status":"ACTIVE","toolkit":{"slug":"github"},"auth_config":{"id":"ac"},"state":{"access_token":"hidden"}}`))
	}))
	defer server.Close()
	client, _ := NewClient(Options{APIKey: "key", AuthConfigID: "ac", BaseURL: server.URL})
	account, err := client.Status(context.Background(), "ca", "owned")
	data, _ := json.Marshal(account)
	if err == nil || strings.Contains(string(data), "hidden") || strings.Contains(string(data), "access_token") {
		t.Fatal("ownership or redaction failure")
	}
}

func TestWebhookSignatureAndReplayWindow(t *testing.T) {
	now := time.Now()
	body := []byte(`{"id":"msg"}`)
	header := http.Header{"Webhook-Id": []string{"msg"}, "Webhook-Timestamp": []string{strconv.FormatInt(now.Unix(), 10)}}
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte("msg." + header.Get("webhook-timestamp") + "."))
	_, _ = mac.Write(body)
	header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if !VerifyWebhook("secret", header, body, now) {
		t.Fatal("valid signature rejected")
	}
	for _, test := range []struct {
		secret string
		body   []byte
		now    time.Time
	}{
		{"wrong", body, now}, {"secret", []byte(`{"id":"other"}`), now}, {"secret", body, now.Add(301 * time.Second)}, {"secret", body, now.Add(-301 * time.Second)},
	} {
		if VerifyWebhook(test.secret, header, test.body, test.now) {
			t.Fatal("invalid signature or timestamp accepted")
		}
	}
	header.Set("webhook-signature", "v1,not-base64")
	if VerifyWebhook("secret", header, body, now) {
		t.Fatal("invalid base64 accepted")
	}
}
