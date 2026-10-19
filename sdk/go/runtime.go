package lumni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func (c *Client) SubmitRun(ctx context.Context, in protocol.RunInput) (string, error) {
	if in.ID == "" {
		in.ID = "work_" + uuid.NewString()
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/runtime/work", in, &out, true)
	return out.ID, err
}
func (c *Client) ClaimRun(ctx context.Context, in protocol.ClaimInput) (*protocol.RunClaim, error) {
	var out struct {
		Claim *protocol.RunClaim `json:"claim"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/runtime/claim", in, &out, false)
	return out.Claim, err
}
func (c *Client) HeartbeatRun(ctx context.Context, id string, in protocol.RuntimeAuth) error {
	return c.do(ctx, http.MethodPost, runPath(id)+"/heartbeat", in, nil, true)
}
func (c *Client) BeginStep(ctx context.Context, id string, in protocol.StepInput) (protocol.Step, error) {
	var out protocol.Step
	err := c.do(ctx, http.MethodPost, runPath(id)+"/steps/begin", in, &out, true)
	return out, err
}
func (c *Client) StartStep(ctx context.Context, id, key string, in protocol.StepResult) error {
	return c.do(ctx, http.MethodPost, stepPath(id, key)+"/start", in, nil, true)
}
func (c *Client) CompleteStep(ctx context.Context, id, key string, in protocol.StepResult) (protocol.Step, error) {
	var out protocol.Step
	err := c.do(ctx, http.MethodPost, stepPath(id, key)+"/result", in, &out, true)
	return out, err
}
func (c *Client) FinishRun(ctx context.Context, id string, in protocol.StepResult) error {
	return c.do(ctx, http.MethodPost, runPath(id)+"/finish", in, nil, true)
}
func (c *Client) SignalRun(ctx context.Context, id string, in protocol.SignalInput) error {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	return c.do(ctx, http.MethodPost, runPath(id)+"/signals", in, nil, true)
}
func (c *Client) CancelRun(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, runPath(id)+"/cancel", map[string]any{}, nil, true)
}
func (c *Client) RecheckAction(ctx context.Context, id, recheckID string) (protocol.Decision, error) {
	var out protocol.Decision
	err := c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/recheck", map[string]string{"id": recheckID}, &out, true)
	return out, err
}
func (c *Client) PreviousResult(ctx context.Context, id string) (json.RawMessage, error) {
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id)+"/previous-result", nil, &out, true)
	return out.Output, err
}
func (c *Client) PutVerifier(ctx context.Context, in protocol.Verifier) error {
	return c.do(ctx, http.MethodPost, "/v1/verifiers", in, nil, true)
}
func (c *Client) RecordEvidence(ctx context.Context, id string, in protocol.VerificationEvidence) error {
	return c.do(ctx, http.MethodPost, "/v1/outcomes/"+url.PathEscape(id)+"/evidence", in, nil, true)
}
func (c *Client) AddDependency(ctx context.Context, in protocol.DependencyInput) error {
	return c.do(ctx, http.MethodPost, "/v1/dependencies", in, nil, true)
}
func (c *Client) Query(ctx context.Context, collection, id, work string) ([]json.RawMessage, error) {
	var out protocol.List[json.RawMessage]
	path := "/v1/" + url.PathEscape(collection)
	if id != "" {
		path += "/" + url.PathEscape(id)
	}
	path += "?work_id=" + url.QueryEscape(work)
	err := c.do(ctx, http.MethodGet, path, nil, &out, true)
	return out.Items, err
}
func runPath(id string) string       { return "/v1/runtime/work/" + url.PathEscape(id) }
func stepPath(id, key string) string { return runPath(id) + "/steps/" + url.PathEscape(key) }

var ErrSuspended = errors.New("work suspended durably")

type StepError struct{ Status, Code string }

func (e *StepError) Error() string { return fmt.Sprintf("step %s: %s", e.Status, e.Code) }

// SafeFailure explicitly states that an external effect did not occur.
// Ordinary tool errors are UNKNOWN and are verified before any retry.
type SafeFailure struct {
	Code      string
	Retryable bool
}

func (e *SafeFailure) Error() string { return e.Code }

type Handler func(*RunContext, json.RawMessage) (json.RawMessage, error)
type registration struct {
	version string
	handler Handler
}
type Worker struct {
	Client   *Client
	ID       string
	handlers map[string]registration
}

func NewWorker(c *Client) *Worker {
	return &Worker{Client: c, ID: "worker_" + uuid.NewString(), handlers: map[string]registration{}}
}
func (w *Worker) Register(name, version string, handler Handler) {
	w.handlers[name] = registration{version, handler}
}
func (w *Worker) Run(ctx context.Context) error {
	for {
		ran, err := w.RunOnce(ctx)
		if err != nil {
			return err
		}
		if !ran {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	versions := map[string]string{}
	for name, r := range w.handlers {
		versions[name] = r.version
	}
	claim, err := w.Client.ClaimRun(ctx, protocol.ClaimInput{WorkerID: w.ID, Handlers: versions})
	if err != nil || claim == nil {
		return false, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc := &RunContext{Context: runCtx, Client: w.Client, Claim: *claim}
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if e := w.Client.HeartbeatRun(runCtx, claim.WorkID, rc.auth()); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	output, runErr := w.handlers[claim.Handler].handler(rc, claim.Input)
	if errors.Is(runErr, ErrSuspended) {
		cancel()
		<-heartbeatDone
		return true, nil
	}
	if runCtx.Err() != nil {
		return true, runCtx.Err()
	}
	status := "COMPLETED"
	code := ""
	if runErr != nil {
		status = "FAILED"
		code = "HANDLER_FAILED"
	}
	err = w.Client.FinishRun(runCtx, claim.WorkID, protocol.StepResult{RuntimeAuth: rc.auth(), Status: status, Output: output, Error: code})
	cancel()
	<-heartbeatDone
	if err != nil {
		return true, err
	}
	return true, runErr
}

type RunContext struct {
	context.Context
	Client *Client
	Claim  protocol.RunClaim
	mu     sync.Mutex
}

func (r *RunContext) auth() protocol.RuntimeAuth {
	return protocol.RuntimeAuth{Token: r.Claim.Token, Fence: r.Claim.Fence}
}
func (r *RunContext) LLM(key string, input json.RawMessage, call func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	return r.execute(key, "LLM", input, nil, call)
}
func (r *RunContext) Step(key string, input json.RawMessage, call func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	return r.execute(key, "CUSTOM", input, nil, call)
}
func (r *RunContext) Tool(key string, action protocol.ActionCheckInput, call func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	return r.execute(key, "TOOL", action.Arguments, &action, call)
}
func (r *RunContext) execute(key, kind string, input json.RawMessage, action *protocol.ActionCheckInput, call func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if !r.mu.TryLock() {
		return nil, errors.New("concurrent steps in one handler are unsupported; use child work")
	}
	defer r.mu.Unlock()
	st, err := r.Client.BeginStep(r, r.Claim.WorkID, protocol.StepInput{RuntimeAuth: r.auth(), Key: key, Type: kind, Input: input, Action: action})
	if err != nil {
		return nil, err
	}
	if st.Status == "COMPLETED" {
		return st.Output, nil
	}
	if st.Status == "WAITING" || st.Status == "UNKNOWN" {
		return nil, ErrSuspended
	}
	if st.Status != "READY" {
		return nil, &StepError{st.Status, st.Error}
	}
	if err = r.Client.StartStep(r, r.Claim.WorkID, key, protocol.StepResult{RuntimeAuth: r.auth(), Attempt: st.Attempt}); err != nil {
		return nil, err
	}
	output, callErr := call(r)
	result := protocol.StepResult{RuntimeAuth: r.auth(), Attempt: st.Attempt, Status: "COMPLETED", Output: output}
	if callErr != nil {
		result.Status = "FAILED"
		result.Error = "STEP_EXECUTION_FAILED"
		result.Retryable = kind == "LLM"
		var safe *SafeFailure
		if errors.As(callErr, &safe) {
			result.EffectAbsent = true
			result.Retryable = safe.Retryable
			result.Error = safe.Code
		} else if kind == "TOOL" {
			result.Status = "UNKNOWN"
		}
	}
	st, err = r.Client.CompleteStep(r, r.Claim.WorkID, key, result)
	if err != nil {
		return nil, err
	}
	if st.Status == "WAITING" || st.Status == "UNKNOWN" {
		return nil, ErrSuspended
	}
	if st.Status != "COMPLETED" {
		return nil, &StepError{st.Status, st.Error}
	}
	return st.Output, nil
}
func (r *RunContext) Wait(key string, condition protocol.WaitInput) (json.RawMessage, error) {
	st, err := r.Client.BeginStep(r, r.Claim.WorkID, protocol.StepInput{RuntimeAuth: r.auth(), Key: key, Type: "WAIT", Wait: &condition})
	if err != nil {
		return nil, err
	}
	if st.Status == "COMPLETED" {
		return st.Output, nil
	}
	if st.Status == "FAILED" {
		return nil, &StepError{st.Status, st.Error}
	}
	return nil, ErrSuspended
}
func (r *RunContext) Approval(key, id string) (json.RawMessage, error) {
	return r.Wait(key, protocol.WaitInput{Type: "APPROVAL", ApprovalID: id})
}
func (r *RunContext) Child(key, handler, version string, input json.RawMessage) (json.RawMessage, error) {
	id := r.Claim.WorkID + ":" + key
	parent := r.Claim.WorkID
	_, err := r.Client.SubmitRun(r, protocol.RunInput{Work: protocol.Work{ID: id, ActorID: r.Claim.ActorID, ParentWorkID: &parent, Goal: key}, Handler: handler, HandlerVersion: version, Input: input})
	if err != nil {
		return nil, err
	}
	return r.Wait(key, protocol.WaitInput{Type: "WORK", WorkID: id})
}
func (r *RunContext) Handoff(key, handler, version string, input json.RawMessage) (json.RawMessage, error) {
	return r.Child(key, handler, version, input)
}

func (c *Client) DiscoverTool(ctx context.Context, source, name string, schema json.RawMessage) error {
	return c.do(ctx, http.MethodPost, "/v1/discovery/tools", map[string]any{"source": source, "name": name, "schema": schema}, nil, true)
}
func (c *Client) RecordModelCall(ctx context.Context, input map[string]any) error {
	return c.do(ctx, http.MethodPost, "/v1/model-calls", input, nil, true)
}

func (c *Client) GatewayConfig(ctx context.Context, out any) error {
	return c.do(ctx, http.MethodGet, "/v1/gateway/config", nil, out, false)
}
func (c *Client) BindOperationVerifier(ctx context.Context, in protocol.OperationVerifier) error {
	return c.do(ctx, http.MethodPut, "/v1/config/operation-verifiers", in, nil, true)
}

func (r *RunContext) Sleep(key string, duration time.Duration) (json.RawMessage, error) {
	return r.Wait(key, protocol.WaitInput{Type: "TIMER", DelayMS: duration.Milliseconds()})
}

type ModelCallAdmission struct {
	Created bool   `json:"created"`
	Status  string `json:"status"`
}

func (c *Client) BeginModelCall(ctx context.Context, input any) (ModelCallAdmission, error) {
	var result ModelCallAdmission
	err := c.do(ctx, http.MethodPost, "/v1/model-calls", input, &result, false)
	return result, err
}
func (c *Client) ModelResult(ctx context.Context, id string) (json.RawMessage, error) {
	var result struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/model-calls/"+url.PathEscape(id)+"/payload", nil, &result, true)
	return result.Output, err
}
func (c *Client) ConfirmMapping(ctx context.Context, in protocol.ToolMapping) error {
	return c.do(ctx, http.MethodPost, "/v1/discovery/confirm", in, nil, true)
}
func (c *Client) SetEnforcement(ctx context.Context, in protocol.EnforcementRule) error {
	return c.do(ctx, http.MethodPut, "/v1/config/enforcement", in, nil, true)
}
func (c *Client) RetryVerification(ctx context.Context, outcome string) error {
	return c.do(ctx, http.MethodPost, "/v1/outcomes/"+url.PathEscape(outcome)+"/retry", map[string]any{}, nil, true)
}
func (c *Client) RegisterReconciliation(ctx context.Context, in protocol.ReconciliationHandler) error {
	return c.do(ctx, http.MethodPost, "/v1/reconciliation/handlers", in, nil, true)
}
func (c *Client) Reconcile(ctx context.Context, in protocol.ReconciliationInput) (string, error) {
	var out struct {
		WorkID string `json:"work_id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/reconciliation", in, &out, true)
	return out.WorkID, err
}
func (c *Client) ResolveEffectIdentity(ctx context.Context, in protocol.EffectIdentityResolution) error {
	return c.do(ctx, http.MethodPost, "/v1/effects/resolve-identity", in, nil, true)
}
