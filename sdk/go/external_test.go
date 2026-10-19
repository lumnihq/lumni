package lumni

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExternalResultRecoveryErrors(t *testing.T) {
	for _, code := range []string{"RESULT_PENDING", "RESULT_UNAVAILABLE", "UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			client := New("http://lumni.test", "test")
			client.MaxRetries = 0
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, status := `{"items":[{"status":"UNKNOWN"}]}`, 200
				if strings.HasSuffix(r.URL.Path, "previous-result") {
					body, status = `{"error":{"code":"`+code+`","message":"fixture"}}`, 422
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}
			result, err := client.ExternalOutcome(context.Background(), "action", false)
			if code == "RESULT_PENDING" {
				if err != nil || result.Status != "PENDING" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != code {
					t.Fatalf("expected %s, got %v", code, err)
				}
			}
		})
	}
}
