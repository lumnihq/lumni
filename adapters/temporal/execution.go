package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
)

// Register Activities with the Temporal worker. All network and tool calls
// execute in Activities; Temporal owns their history, timers and signals.
type Activities struct {
	Client *lumni.Client
	Tools  map[string]func(context.Context, json.RawMessage) (json.RawMessage, error)
}
type AdmissionInput struct {
	Action    protocol.ActionCheckInput
	RecheckID string
}
type ExecutionInput struct {
	Action   protocol.ActionCheckInput
	Decision protocol.Decision
}

func (a *Activities) Admit(ctx context.Context, in AdmissionInput) (protocol.Decision, error) {
	return a.Client.PrepareExternal(ctx, in.Action, in.RecheckID)
}
func (a *Activities) Execute(ctx context.Context, in ExecutionInput) (lumni.ExternalResult, error) {
	tool, ok := a.Tools[in.Action.Operation]
	if !ok {
		return lumni.ExternalResult{}, temporalsdk.NewNonRetryableApplicationError("tool handler is not registered", "UNREGISTERED_TOOL", nil)
	}
	return a.Client.ExecuteExternal(ctx, in.Action, in.Decision, func(ctx context.Context) (json.RawMessage, error) { return tool(ctx, in.Action.Arguments) })
}
func (a *Activities) Outcome(ctx context.Context, in protocol.ActionCheckInput) (lumni.ExternalResult, error) {
	return a.Client.ExternalOutcome(ctx, in.ActionID, in.Verification != nil)
}

// Execute performs an admitted tool operation and waits for configured evidence.
// Its stable key becomes part of the action identity across workflow replay.
// Signal "lumni:<action id>" can wake approval/outcome polling immediately.
func Execute(ctx workflow.Context, key string, in protocol.ActionCheckInput) (json.RawMessage, error) {
	if in.ActionID == "" {
		in.ActionID = workflow.GetInfo(ctx).WorkflowExecution.ID + ":" + key
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporalsdk.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2}})
	var activities *Activities
	for attempt := 0; ; attempt++ {
		var d protocol.Decision
		if err := workflow.ExecuteActivity(ctx, activities.Admit, AdmissionInput{Action: in, RecheckID: fmt.Sprintf("temporal:%s:%d", key, attempt)}).Get(ctx, &d); err != nil {
			return nil, err
		}
		if d.Enforced && (d.Decision == protocol.DecisionReject || d.Decision == protocol.DecisionReplan) {
			return nil, temporalsdk.NewNonRetryableApplicationError(string(d.ReasonCode), "LUMNI_REJECTED", nil)
		}
		if d.Enforced && (d.Decision == protocol.DecisionWait || d.Decision == protocol.DecisionRequireApproval) {
			if err := awaitWake(ctx, in.ActionID); err != nil {
				return nil, err
			}
			continue
		}
		var result lumni.ExternalResult
		// ExecuteExternal uses a server-persisted one-shot call marker. Retrying the
		// Activity after a lost worker can only recover or await the original effect.
		err := workflow.ExecuteActivity(ctx, activities.Execute, ExecutionInput{Action: in, Decision: d}).Get(ctx, &result)
		if err != nil {
			var failure *temporalsdk.ApplicationError
			if errors.As(err, &failure) && failure.NonRetryable() {
				return nil, err
			}
			if temporalsdk.IsCanceledError(err) {
				return nil, err
			}
			result.Status = "PENDING"
		}
		for result.Status == "PENDING" {
			if err := awaitWake(ctx, in.ActionID); err != nil {
				return nil, err
			}
			if err := workflow.ExecuteActivity(ctx, activities.Outcome, in).Get(ctx, &result); err != nil {
				return nil, err
			}
		}
		if result.Status != "COMPLETED" {
			return nil, temporalsdk.NewNonRetryableApplicationError(result.Status, "OUTCOME_FAILED", nil)
		}
		return result.Output, nil
	}
}
func awaitWake(ctx workflow.Context, id string) error {
	child, cancel := workflow.WithCancel(ctx)
	defer cancel()
	selector := workflow.NewSelector(ctx)
	selector.AddFuture(workflow.NewTimer(child, 2*time.Second), func(workflow.Future) {})
	selector.AddReceive(workflow.GetSignalChannel(ctx, "lumni:"+id), func(c workflow.ReceiveChannel, _ bool) { var signal any; c.Receive(ctx, &signal) })
	selector.Select(ctx)
	return ctx.Err()
}
