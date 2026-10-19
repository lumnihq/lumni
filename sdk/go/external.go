package lumni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lumnihq/lumni/pkg/protocol"
	"net/http"
	"net/url"
	"time"
)

type ExternalResult struct {
	Status   string             `json:"status"`
	Output   json.RawMessage    `json:"output,omitempty"`
	Decision *protocol.Decision `json:"decision,omitempty"`
}

func (c *Client) StartActionOnce(ctx context.Context, id, token string) (bool, error) {
	var out struct {
		Started bool `json:"started"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/start-once", map[string]string{"lease_token": token}, &out, false)
	return out.Started, err
}
func (c *Client) PrepareExternal(ctx context.Context, in protocol.ActionCheckInput, recheckID string) (protocol.Decision, error) {
	if in.ActionID == "" {
		return protocol.Decision{}, errors.New("external runtimes must provide a stable action_id")
	}
	if _, err := c.PutActor(ctx, protocol.Actor{ID: in.ActorID, Type: protocol.ActorAgent, Runtime: "external"}); err != nil {
		return protocol.Decision{}, err
	}
	if _, err := c.StartWork(ctx, protocol.Work{ID: in.WorkID, ActorID: in.ActorID, Goal: in.Operation}); err != nil {
		return protocol.Decision{}, err
	}
	d, err := c.CheckAction(ctx, in)
	if err != nil {
		return d, err
	}
	expired := d.ExpiresAt != nil && d.ExpiresAt.Before(time.Now())
	if recheckID != "" && (d.Decision == protocol.DecisionWait || d.Decision == protocol.DecisionRequireApproval || expired) {
		return c.RecheckAction(ctx, d.ActionID, recheckID)
	}
	return d, nil
}
func (c *Client) ExternalOutcome(ctx context.Context, action string, verified bool) (ExternalResult, error) {
	rows, err := c.Query(ctx, "outcomes", "out_"+action, "")
	if err != nil {
		return ExternalResult{}, err
	}
	var o struct {
		Status     string          `json:"status"`
		VerifierID *string         `json:"verifier_id"`
		Observed   json.RawMessage `json:"observed"`
	}
	if len(rows) > 0 {
		if err = json.Unmarshal(rows[0], &o); err != nil {
			return ExternalResult{}, err
		}
	}
	if o.Status == "MISMATCH" || o.Status == "FAILED" || o.Status == "PARTIAL" {
		return ExternalResult{Status: o.Status}, nil
	}
	if (verified || o.VerifierID != nil) && o.Status != "VERIFIED" {
		return ExternalResult{Status: "PENDING"}, nil
	}
	output, err := c.PreviousResult(ctx, action)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "RESULT_PENDING" {
			return ExternalResult{Status: "PENDING"}, nil
		}
		return ExternalResult{}, err
	}
	// An unknown transport result can have an encrypted null payload. Only
	// authoritative evidence may turn that uncertainty into a returned result.
	if o.Status == "VERIFIED" && (string(output) == "null" || len(output) == 0) {
		output = o.Observed
	}
	return ExternalResult{Status: "COMPLETED", Output: output}, nil
}

// ExecuteExternal is safe to put inside an at-least-once framework activity.
// History and suspension belong to that framework; no native Work claim exists.
func (c *Client) ExecuteExternal(ctx context.Context, in protocol.ActionCheckInput, d protocol.Decision, call func(context.Context) (json.RawMessage, error)) (ExternalResult, error) {
	if d.Decision == protocol.DecisionDeduplicate {
		if d.ExistingActionID == nil {
			return ExternalResult{}, errors.New("RESULT_UNAVAILABLE")
		}
		return c.ExternalOutcome(ctx, *d.ExistingActionID, in.Verification != nil)
	}
	if err := RequireExecution(d); err != nil {
		return ExternalResult{Status: "PENDING", Decision: &d}, nil
	}
	token := ""
	if d.LeaseToken != nil {
		token = *d.LeaseToken
	}
	started, err := c.StartActionOnce(ctx, d.ActionID, token)
	if err != nil {
		return ExternalResult{}, err
	}
	if !started {
		return c.ExternalOutcome(ctx, d.ActionID, in.Verification != nil)
	}
	output, callErr := call(ctx)
	status := "SUCCEEDED"
	if callErr != nil {
		status = "UNKNOWN"
		output = nil
	}
	if err = c.ReportResult(ctx, d.ActionID, protocol.ActionResultInput{Status: status, Output: output, LeaseToken: d.LeaseToken}); err != nil {
		return ExternalResult{}, fmt.Errorf("result persistence: %w", err)
	}
	if callErr != nil {
		return ExternalResult{Status: "PENDING"}, nil
	}
	return c.ExternalOutcome(ctx, d.ActionID, in.Verification != nil)
}
