package temporal

import (
	"errors"
	"lumni/pkg/protocol"
	"testing"
)

func TestTranslateWait(t *testing.T) {
	ms := int64(250)
	err := Translate(protocol.Decision{Decision: protocol.DecisionWait, ReasonCode: protocol.ReasonLeaseBusy, RetryAfterMS: &ms})
	var wait WaitError
	if !errors.As(err, &wait) || wait.RetryAfter.Milliseconds() != 250 {
		t.Fatalf("unexpected %v", err)
	}
}
