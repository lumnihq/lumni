package lumni

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/lumnihq/lumni/pkg/protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestActionRetryKeepsIdentityAndIgnoresAdditiveFields(t *testing.T) {
	fixture, err := os.ReadFile("../../api/fixtures/decision-wait.json")
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	var actionID, requestID string
	client := New("http://lumni.test", "secret")
	client.MaxRetries = 1
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		body, _ := io.ReadAll(request.Body)
		value := string(body)
		start := strings.Index(value, `"action_id":"`) + len(`"action_id":"`)
		end := strings.Index(value[start:], `"`)
		currentActionID := value[start : start+end]
		if attempts == 1 {
			actionID, requestID = currentActionID, request.Header.Get("X-Request-ID")
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"UNAVAILABLE","message":"retry","request_id":"req"}}`)), Header: http.Header{}, Request: request}, nil
		}
		if currentActionID != actionID || request.Header.Get("X-Request-ID") != requestID {
			t.Fatalf("retry identity changed: action=%s request=%s", currentActionID, request.Header.Get("X-Request-ID"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(fixture))), Header: http.Header{}, Request: request}, nil
	})}
	decision, err := client.CheckAction(context.Background(), protocol.ActionCheckInput{WorkID: "work", ActorID: "actor", Operation: "write", Resources: []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{Type: "x", ExternalID: "1"}, Mode: protocol.AccessWrite}}})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || decision.Decision != protocol.DecisionWait || decision.RetryAfterMS == nil || *decision.RetryAfterMS != 1000 {
		t.Fatalf("attempts=%d decision=%+v", attempts, decision)
	}
}
