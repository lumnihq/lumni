//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"
	adapter "lumni/adapters/restate"
	"lumni/pkg/protocol"
)

func TestRestateRuntimeGuardedInvocation(t *testing.T) {
	ingress, admin := os.Getenv("LUMNI_TEST_RESTATE_URL"), os.Getenv("LUMNI_TEST_RESTATE_ADMIN_URL")
	if ingress == "" || admin == "" {
		t.Skip("local Restate URLs are not configured")
	}
	s := scenarioServer(t)
	var calls atomic.Int32
	h := &adapter.Handler{Client: s.client, Tools: map[string]func(context.Context, json.RawMessage) (json.RawMessage, error){"fixture.echo": func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return input, nil
	}}}
	service := sdk.Reflect(h)
	handler, err := server.NewRestate().Bidirectional(false).Bind(service).Handler()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewUnstartedServer(handler)
	host.Listener.Close()
	host.Listener = listener
	host.Start()
	defer host.Close()
	address, _ := url.Parse(host.URL)
	if remoteHost := os.Getenv("LUMNI_TEST_RESTATE_HANDLER_HOST"); remoteHost != "" {
		address.Host = net.JoinHostPort(remoteHost, address.Port())
	}
	client := &http.Client{Timeout: 30 * time.Second}
	register, _ := json.Marshal(map[string]any{"uri": address.String(), "use_http_11": true, "force": true})
	response, err := client.Post(admin+"/deployments", "application/json", bytes.NewReader(register))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode >= 300 {
		t.Fatalf("register: %d %s", response.StatusCode, body)
	}
	input := input(s.fixture, "fixture.echo", "echo", "")
	input.ActionID = "restate-action"
	input.Arguments = json.RawMessage(`{"value":"retained"}`)
	input.Resources[0].Mode = protocol.AccessRead
	value, _ := json.Marshal(input)
	identity := uuid.NewString()
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", ingress+"/Handler/Execute", bytes.NewReader(value))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("idempotency-key", identity)
		response, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !bytes.Contains(body, []byte("retained")) {
			t.Fatalf("invoke: %d %s", response.StatusCode, body)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("external calls=%d", calls.Load())
	}
	var mode string
	if err = s.pool.QueryRow(t.Context(), `SELECT execution_mode FROM work WHERE tenant_id=$1 AND id=$2`, s.tenant, s.work).Scan(&mode); err != nil || mode != "EXTERNAL" {
		t.Fatalf("external runtime acquired native state: %s %v", mode, err)
	}
}
