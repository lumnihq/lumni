package coordination

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestReplaySnapshotUsesRegisteredKernel(t *testing.T) {
	raw, _ := json.Marshal(DecisionSnapshot{TenantMode: protocol.ModeShadow, CandidateDecision: protocol.DecisionReplan, ReasonCode: protocol.ReasonStaleObservation, EvaluatedAt: time.Now()})
	decision, reason, err := ReplaySnapshot(KernelVersion, raw)
	if err != nil || decision != protocol.DecisionWouldReplan || reason != protocol.ReasonStaleObservation {
		t.Fatalf("replay=%s/%s err=%v", decision, reason, err)
	}
	if _, _, err := ReplaySnapshot("coord-v0", raw); !errors.Is(err, ErrUnsupportedKernelVersion) {
		t.Fatalf("unknown version error=%v", err)
	}
}
