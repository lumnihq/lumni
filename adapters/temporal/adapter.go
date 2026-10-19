package temporal

import (
	"fmt"
	"time"

	"lumni/pkg/protocol"
)

type WaitError struct {
	RetryAfter time.Duration
	Reason     protocol.ReasonCode
}

func (e WaitError) Error() string { return fmt.Sprintf("lumni wait: %s", e.Reason) }

type ReplanError struct{ Reason protocol.ReasonCode }

func (e ReplanError) Error() string { return fmt.Sprintf("lumni replan: %s", e.Reason) }

type ApprovalError struct {
	ApprovalID string
	Reason     protocol.ReasonCode
}

func (e ApprovalError) Error() string {
	return fmt.Sprintf("lumni approval required: %s", e.ApprovalID)
}

// Translate converts a Lumni decision into values suitable for a Temporal workflow
// branch. Workflows should use a durable timer for WaitError and a signal for ApprovalError.
func Translate(d protocol.Decision) error {
	switch d.Decision {
	case protocol.DecisionAllow, protocol.DecisionWarn, protocol.DecisionWouldAllow, protocol.DecisionWouldWait, protocol.DecisionWouldReplan, protocol.DecisionWouldDedup, protocol.DecisionWouldApproval, protocol.DecisionWouldReject:
		return nil
	case protocol.DecisionWait:
		delay := time.Second
		if d.RetryAfterMS != nil {
			delay = time.Duration(*d.RetryAfterMS) * time.Millisecond
		}
		return WaitError{delay, d.ReasonCode}
	case protocol.DecisionReplan:
		return ReplanError{d.ReasonCode}
	case protocol.DecisionRequireApproval:
		id := ""
		if d.ApprovalID != nil {
			id = *d.ApprovalID
		}
		return ApprovalError{id, d.ReasonCode}
	default:
		return fmt.Errorf("lumni rejected action: %s", d.ReasonCode)
	}
}
