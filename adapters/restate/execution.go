package restate

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/restatedev/sdk-go"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
	"time"
)

// Handler can be registered directly as a Restate service handler. Restate owns
// replay and timers; Lumni owns admission and independent effect evidence.
type Handler struct {
	Client *lumni.Client
	Tools  map[string]func(context.Context, json.RawMessage) (json.RawMessage, error)
}

func (h *Handler) Execute(ctx sdk.Context, in protocol.ActionCheckInput) (json.RawMessage, error) {
	if in.ActionID == "" {
		in.ActionID = "restate_" + sdk.UUID(ctx).String()
	}
	tool, ok := h.Tools[in.Operation]
	if !ok {
		return nil, sdk.TerminalErrorf("unregistered tool: %s", in.Operation)
	}
	for attempt := 0; ; attempt++ {
		d, err := sdk.Run(ctx, func(c sdk.RunContext) (protocol.Decision, error) {
			return h.Client.PrepareExternal(c, in, fmt.Sprintf("restate:%d", attempt))
		}, sdk.WithName("Lumni admission"), sdk.WithMaxRetryAttempts(3))
		if err != nil {
			return nil, err
		}
		if d.Enforced && (d.Decision == protocol.DecisionReject || d.Decision == protocol.DecisionReplan) {
			return nil, sdk.TerminalErrorf("admission: %s", d.ReasonCode)
		}
		if d.Enforced && (d.Decision == protocol.DecisionWait || d.Decision == protocol.DecisionRequireApproval) {
			if err := sdk.Sleep(ctx, 2*time.Second); err != nil {
				return nil, err
			}
			continue
		}
		result, err := sdk.Run(ctx, func(c sdk.RunContext) (lumni.ExternalResult, error) {
			return h.Client.ExecuteExternal(c, in, d, func(callCtx context.Context) (json.RawMessage, error) { return tool(callCtx, in.Arguments) })
		}, sdk.WithName("Lumni guarded tool"), sdk.WithMaxRetryAttempts(3))
		if err != nil {
			result.Status = "PENDING"
		}
		for result.Status == "PENDING" {
			if err := sdk.Sleep(ctx, 2*time.Second); err != nil {
				return nil, err
			}
			result, err = sdk.Run(ctx, func(c sdk.RunContext) (lumni.ExternalResult, error) {
				return h.Client.ExternalOutcome(c, in.ActionID, in.Verification != nil)
			}, sdk.WithName("Lumni verification"), sdk.WithMaxRetryAttempts(3))
			if err != nil {
				return nil, err
			}
		}
		if result.Status != "COMPLETED" {
			return nil, sdk.TerminalErrorf("outcome: %s", result.Status)
		}
		return result.Output, nil
	}
}
