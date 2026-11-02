package contracts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestAllRulePredicatesAndUnavailableEvidence(t *testing.T) {
	now := time.Now().UTC()
	base := Snapshot{Arguments: map[string]any{"amount": float64(600), "currency": "USD"}, Fact: map[string]any{"balance": float64(1000), "editor": "human", "edited": now.Add(-time.Minute).Format(time.RFC3339Nano)}, FactAt: now, EvaluatedAt: now, PriorCount: 1, ReservedAmount: 500}
	cases := []struct {
		kind     string
		slots    map[string]any
		b        protocol.ContractBinding
		decision protocol.DecisionType
	}{
		{"once-per-key", nil, protocol.ContractBinding{}, protocol.DecisionReject},
		{"approval-over", map[string]any{"amount_minor": float64(500)}, protocol.ContractBinding{AmountPointer: "/amount", CurrencyPointer: "/currency", Currency: "USD"}, protocol.DecisionRequireApproval},
		{"amount-cap", nil, protocol.ContractBinding{AmountPointer: "/amount", ValuePointer: "/balance", ReadBindingID: "read"}, protocol.DecisionReject},
		{"order-before", nil, protocol.ContractBinding{}, protocol.DecisionWait},
		{"rate-limit", map[string]any{"n": float64(1)}, protocol.ContractBinding{}, protocol.DecisionWait},
		{"human-wins", map[string]any{"minutes": float64(10)}, protocol.ContractBinding{ReadBindingID: "read", LastEditorPointer: "/editor", LastEditedAtPointer: "/edited"}, protocol.DecisionReject},
		{"end-state", nil, protocol.ContractBinding{}, protocol.DecisionAllow},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			rule := protocol.ContractRule{Type: c.kind, Slots: c.slots, Binding: c.b}
			d, _ := Evaluate(rule, base)
			if d != c.decision {
				t.Fatalf("got %s want %s", d, c.decision)
			}
			missing := base
			missing.Unavailable = true
			d, r := Evaluate(rule, missing)
			if d != protocol.DecisionWait || r != "CONTRACT_EVIDENCE_UNAVAILABLE" {
				t.Fatalf("missing facts: %s/%s", d, r)
			}
		})
	}
	stale := base
	stale.FactAt = now.Add(-time.Hour)
	_, reason := Evaluate(protocol.ContractRule{Type: "amount-cap", Binding: protocol.ContractBinding{ReadBindingID: "read"}}, stale)
	if reason != "CONTRACT_EVIDENCE_UNAVAILABLE" {
		t.Fatal("stale facts accepted")
	}
	for _, v := range []any{nil, "600", true, float64(1.5)} {
		bad := base
		bad.Arguments = map[string]any{"amount": v}
		_, reason := Evaluate(protocol.ContractRule{Type: "approval-over", Slots: map[string]any{"amount_minor": float64(500)}, Binding: protocol.ContractBinding{AmountPointer: "/amount"}}, bad)
		if reason != "CONTRACT_EVIDENCE_UNAVAILABLE" {
			t.Fatalf("noninteger amount accepted: %v", v)
		}
	}
}
func TestClassifierProposalsAreAdvisory(t *testing.T) {
	for _, text := range []string{"Only one refund per order.", "Ask a person before any refund over $500.", "Never overwrite a record a person edited in the last 10 minutes.", "No more than 3 emails per customer a day."} {
		rs := Classify(text)
		if len(rs) != 1 || len(rs[0].Ranked) != 7 || len(rs[0].Guessed) == 0 {
			t.Fatalf("bad proposal: %+v", rs)
		}
	}
	r := Classify("Ask a person before a refund over $500")[0]
	b, _ := json.Marshal(r.Slots)
	if string(b) == "{}" {
		t.Fatal("slots absent")
	}
	if r.Slots["amount_minor"] != float64(50000) {
		t.Fatalf("minor units: %v", r.Slots)
	}
}

func TestClassifierCueOffsetsUseUTF16(t *testing.T) {
	read := Classify("😀 refund twice")[0]
	for _, cue := range read.Cues {
		if cue.Type == "once-per-key" {
			if cue.Start != 10 || cue.End != 15 {
				t.Fatalf("cue offsets do not match JS string positions: %+v", cue)
			}
			return
		}
	}
	t.Fatal("duplicate cue absent")
}
