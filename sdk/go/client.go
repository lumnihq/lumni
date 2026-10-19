// Package lumni is the Go client for the Lumni v1 protocol.
package lumni

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lumnihq/lumni/pkg/protocol"
)

type Client struct {
	BaseURL    string
	APIKey     string
	TenantID   string
	HTTPClient *http.Client
	MaxRetries int
}

// APIError preserves the server's stable error code for recovery decisions.
type APIError struct {
	Code    string
	Message string
	Status  int
	Details any
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func New(baseURL, apiKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxRetries: 2}
}

func (c *Client) PutActor(ctx context.Context, in protocol.Actor) (protocol.Actor, error) {
	var out protocol.Actor
	err := c.do(ctx, http.MethodPost, "/v1/actors", in, &out, true)
	return out, err
}
func (c *Client) StartSession(ctx context.Context, in protocol.Session) (protocol.Session, error) {
	var out protocol.Session
	err := c.do(ctx, http.MethodPost, "/v1/sessions", in, &out, true)
	return out, err
}
func (c *Client) StartWork(ctx context.Context, in protocol.Work) (protocol.Work, error) {
	if in.ID == "" {
		in.ID = "work_" + uuid.NewString()
	}
	var out protocol.Work
	err := c.do(ctx, http.MethodPost, "/v1/work", in, &out, true)
	return out, err
}
func (c *Client) CompleteWork(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/work/"+id+"/complete", map[string]any{}, nil, true)
}
func (c *Client) Observe(ctx context.Context, in protocol.ObservationInput) (string, error) {
	if in.ID == "" {
		in.ID = "obs_" + uuid.NewString()
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/observations", in, &out, true)
	return out.ID, err
}
func (c *Client) UpdateResourceVersion(ctx context.Context, id string, in protocol.ResourceVersionInput) error {
	return c.do(ctx, http.MethodPost, "/v1/resources/"+id+"/version", in, nil, true)
}
func (c *Client) CheckAction(ctx context.Context, in protocol.ActionCheckInput) (protocol.Decision, error) {
	if in.ActionID == "" {
		in.ActionID = "act_" + uuid.NewString()
	}
	var out protocol.Decision
	err := c.do(ctx, http.MethodPost, "/v1/actions/check", in, &out, true)
	return out, err
}
func (c *Client) ObserveAction(ctx context.Context, in protocol.ActionCheckInput) (protocol.Decision, error) {
	if in.ActionID == "" {
		in.ActionID = "act_" + uuid.NewString()
	}
	var out protocol.Decision
	err := c.do(ctx, http.MethodPost, "/v1/actions/observe", in, &out, true)
	return out, err
}
func (c *Client) StartAction(ctx context.Context, id, token string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+id+"/start", map[string]any{"lease_token": token}, nil, true)
}
func (c *Client) Heartbeat(ctx context.Context, id, token string) (time.Time, error) {
	var out struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/actions/"+id+"/heartbeat", map[string]any{"lease_token": token}, &out, true)
	return out.ExpiresAt, err
}
func (c *Client) ReportResult(ctx context.Context, id string, in protocol.ActionResultInput) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+id+"/result", in, nil, true)
}
func (c *Client) Approval(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, "/v1/approvals/"+id, nil, &out, true)
	return out, err
}
func (c *Client) IngestEvent(ctx context.Context, in protocol.EventEnvelope) (bool, error) {
	if in.EventID == "" {
		in.EventID = "evt_" + uuid.NewString()
	}
	if in.SchemaVersion == "" {
		in.SchemaVersion = protocol.SchemaVersion
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}
	var out struct {
		Created bool `json:"created"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/events", in, &out, true)
	return out.Created, err
}
func (c *Client) RecordCost(ctx context.Context, in protocol.CostInput) (string, error) {
	if in.ID == "" {
		in.ID = "cost_" + uuid.NewString()
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/costs", in, &out, true)
	return out.ID, err
}

func (c *Client) do(ctx context.Context, method, path string, input, output any, retryable bool) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	attempts := 1
	if retryable {
		attempts += c.MaxRetries
	}
	var last error
	requestID := "req_" + uuid.NewString()
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", requestID)
		if c.TenantID != "" {
			req.Header.Set("X-Lumni-Tenant", c.TenantID)
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			last = err
			if attempt+1 < attempts {
				time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
				continue
			}
			return err
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if output != nil && len(responseBody) > 0 {
				if err := json.Unmarshal(responseBody, output); err != nil {
					return err
				}
			}
			return nil
		}
		var apiErr protocol.ErrorResponse
		if json.Unmarshal(responseBody, &apiErr) == nil && apiErr.Error.Code != "" {
			last = &APIError{Code: apiErr.Error.Code, Message: apiErr.Error.Message, Status: resp.StatusCode, Details: apiErr.Error.Details}
		} else {
			last = fmt.Errorf("lumni returned HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode < 500 || attempt+1 >= attempts {
			return last
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
	return last
}

type contextValue struct{ ActorID, AgentID, SessionID, WorkID string }
type contextKey struct{}

func WithContext(ctx context.Context, actorID, agentID, sessionID, workID string) context.Context {
	return context.WithValue(ctx, contextKey{}, contextValue{actorID, agentID, sessionID, workID})
}
func Context(ctx context.Context) (actorID, agentID, sessionID, workID string) {
	value, _ := ctx.Value(contextKey{}).(contextValue)
	return value.ActorID, value.AgentID, value.SessionID, value.WorkID
}

func RequireExecution(decision protocol.Decision) error {
	switch decision.Decision {
	case protocol.DecisionAllow, protocol.DecisionWarn, protocol.DecisionWouldAllow, protocol.DecisionWouldWait, protocol.DecisionWouldReplan, protocol.DecisionWouldDedup, protocol.DecisionWouldApproval, protocol.DecisionWouldReject:
		return nil
	default:
		return errors.New("lumni decision prevents execution: " + string(decision.Decision) + "/" + string(decision.ReasonCode))
	}
}
