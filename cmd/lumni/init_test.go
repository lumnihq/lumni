package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type initTransport func(*http.Request) (*http.Response, error)

func (f initTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInitHelpDoesNotPrintEnvironmentCredentials(t *testing.T) {
	t.Setenv("LUMNI_API_KEY", "lm_private-environment-key")
	t.Setenv("LUMNI_CODE", "LUM-PRIVATE-CODE")
	cmd := initCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "lm_private-environment-key") || strings.Contains(out.String(), "LUM-PRIVATE-CODE") {
		t.Fatal("help exposed environment credentials")
	}
}

func writeInitFixture(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func TestInitDryRunHasNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	writeInitFixture(t, filepath.Join(dir, "package.json"), `{"dependencies":{"openai":"1"}}`, 0600)
	client := &http.Client{Transport: initTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("dry run accessed network")
		return nil, errors.New("unexpected request")
	})}
	var out bytes.Buffer
	if err := runInit(t.Context(), initOptions{Dir: dir, Home: t.TempDir(), URL: "https://example.com", Dry: true, Code: "LUM-PRIVATE"}, &out, client); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", name)
		}
	}
	if !strings.Contains(out.String(), "javascript") || strings.Contains(out.String(), "LUM-PRIVATE") {
		t.Fatal(out.String())
	}
}

func TestInitExchangesCodeWrapsMCPAndRecordsHello(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writeInitFixture(t, filepath.Join(dir, "pyproject.toml"), "dependencies = [\"langgraph\"]\n", 0600)
	writeInitFixture(t, filepath.Join(dir, ".env"), "OTHER_SETTING=preserve\n", 0600)
	original := `{"mcpServers":{"payments":{"command":"python","args":["server.py"],"env":{"UPSTREAM_API_KEY":"preserve-private"}},"remote":{"url":"https://example.com/mcp"}},"other":true}`
	config := filepath.Join(dir, ".mcp.json")
	writeInitFixture(t, config, original, 0600)
	gateway := filepath.Join(dir, "gateway")
	writeInitFixture(t, gateway, "#!/bin/sh\nexit 0\n", 0700)
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/onboarding/exchange" {
			if r.Header.Get("Authorization") != "" {
				t.Error("exchange sent an unrelated key")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["code"] != "LUM-ONCE" || body["project"] != filepath.Base(dir) {
				t.Error("invalid exchange payload")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "lm_issued-private", "api_url": server.URL, "actor_hint": "support-agent"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer lm_issued-private" {
			t.Error("issued key not used")
		}
		switch r.URL.Path {
		case "/v1/actors":
			if r.Method == "POST" {
				var actor map[string]any
				_ = json.NewDecoder(r.Body).Decode(&actor)
				if actor["id"] != "support-agent" || actor["runtime"] != "python" {
					t.Error("actor hint or project detection lost")
				}
			}
			io.WriteString(w, `{"items":[]}`)
		case "/v1/work":
			io.WriteString(w, `{}`)
		case "/v1/actions/check":
			var action map[string]any
			_ = json.NewDecoder(r.Body).Decode(&action)
			if action["operation"] != "lumni.hello" {
				t.Error("not a local hello action")
			}
			io.WriteString(w, `{"action_id":"hello","decision":"ALLOW","lease_token":"lease"}`)
		case "/v1/actions/hello/start", "/v1/actions/hello/result":
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	o := initOptions{Dir: dir, Home: home, URL: server.URL, Code: "LUM-ONCE", Gateway: gateway}
	if err := runInit(t.Context(), o, &out, server.Client()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 7 {
		t.Fatalf("made %d requests, expected seven", calls.Load())
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || !strings.Contains(string(env), "OTHER_SETTING=preserve") || !strings.Contains(string(env), "lm_issued-private") {
		t.Fatalf("env setup: %v", err)
	}
	for _, path := range []string{filepath.Join(dir, ".env"), config, config + ".lumni-backup"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("credentials permissions for %s: %v", path, err)
		}
	}
	backup, _ := os.ReadFile(config + ".lumni-backup")
	if string(backup) != original {
		t.Fatal("backup differs from original")
	}
	configured, _ := os.ReadFile(config)
	var parsed map[string]any
	_ = json.Unmarshal(configured, &parsed)
	payments := parsed["mcpServers"].(map[string]any)["payments"].(map[string]any)
	if payments["command"] != gateway || payments["env"].(map[string]any)["UPSTREAM_API_KEY"] != "preserve-private" {
		t.Fatal("MCP configuration was not preserved")
	}
	if strings.Contains(out.String(), "issued-private") || strings.Contains(out.String(), "preserve-private") {
		t.Fatal("credentials printed")
	}
	o.Code = ""
	o.Key = ""
	o.URL = "http://localhost:8080"
	o.NoHello = true
	if err := runInit(t.Context(), o, &out, server.Client()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(configured, after) {
		t.Fatal("repeat init wrapped a server twice")
	}
}

func TestInitRetainsIssuedCredentialAfterNetworkFailure(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/onboarding/exchange" {
			io.WriteString(w, `{"api_key":"lm_recoverable"}`)
			return
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	var out bytes.Buffer
	err := runInit(t.Context(), initOptions{Dir: dir, Home: t.TempDir(), URL: server.URL, Code: "LUM-ONCE", NoMCP: true}, &out, server.Client())
	if err == nil {
		t.Fatal("network failure was hidden")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(b), "lm_recoverable") || !strings.Contains(string(ignore), ".env") {
		t.Fatal("one-time credential was not recoverable and git-ignored")
	}
}

func TestInitPreservesConflictingConfigurationBeforeExchange(t *testing.T) {
	dir := t.TempDir()
	text := "LUMNI_URL=https://existing.example.com\nLUMNI_API_KEY=lm_existing\n"
	writeInitFixture(t, filepath.Join(dir, ".env"), text, 0600)
	client := &http.Client{Transport: initTransport(func(*http.Request) (*http.Response, error) {
		t.Error("conflicting setup accessed network")
		return nil, errors.New("unexpected request")
	})}
	for _, o := range []initOptions{{Dir: dir, URL: "https://new.example.com", URLExplicit: true, Code: "LUM-ONCE"}, {Dir: dir, URL: "https://existing.example.com", Code: "LUM-ONCE"}, {Dir: dir, URL: "https://existing.example.com", Key: "lm_different"}} {
		if err := runInit(t.Context(), o, io.Discard, client); err == nil {
			t.Error("expected configuration conflict")
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(b) != text {
		t.Fatal("existing configuration changed")
	}
}

func TestInitMCPVscodeAndRemoteTransports(t *testing.T) {
	b, names, err := initMCPPlan([]byte(`{"servers":{"local":{"command":"uvx","args":["tool"]},"remote":{"url":"https://example.com"}}}`), "/bin/gateway", "https://lumni.example", "lm_private", "agent")
	if err != nil || len(names) != 1 || names[0] != "local" {
		t.Fatalf("wrap: %v %v", names, err)
	}
	b2, names, err := initMCPPlan(b, "/bin/gateway", "https://lumni.example", "lm_private", "agent")
	if err != nil || len(names) != 0 || !bytes.Equal(b, b2) {
		t.Fatal("MCP wrapping is not idempotent")
	}
}
