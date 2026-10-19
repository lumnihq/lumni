package restate

import (
	"fmt"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
)

type Directive struct {
	Kind       string
	Delay      time.Duration
	ApprovalID string
	Reason     protocol.ReasonCode
}

func FromDecision(d protocol.Decision) (Directive, error) {
	switch d.Decision {
	case protocol.DecisionAllow, protocol.DecisionWarn, protocol.DecisionWouldAllow, protocol.DecisionWouldWait, protocol.DecisionWouldReplan, protocol.DecisionWouldDedup, protocol.DecisionWouldApproval, protocol.DecisionWouldReject:
		return Directive{Kind: "EXECUTE", Reason: d.ReasonCode}, nil
	case protocol.DecisionWait:
		delay := time.Second
		if d.RetryAfterMS != nil {
			delay = time.Duration(*d.RetryAfterMS) * time.Millisecond
		}
		return Directive{Kind: "SLEEP", Delay: delay, Reason: d.ReasonCode}, nil
	case protocol.DecisionReplan:
		return Directive{Kind: "REPLAN", Reason: d.ReasonCode}, nil
	case protocol.DecisionRequireApproval:
		id := ""
		if d.ApprovalID != nil {
			id = *d.ApprovalID
		}
		return Directive{Kind: "AWAIT_SIGNAL", ApprovalID: id, Reason: d.ReasonCode}, nil
	default:
		return Directive{}, fmt.Errorf("lumni rejected action: %s", d.ReasonCode)
	}
}
