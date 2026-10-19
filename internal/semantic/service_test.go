package semantic

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestRedactSummary(t *testing.T) {
	input := "contact dev@example.com with Bearer abc.def_123 and token abcdefghijklmnopqrstuvwxyz1234567890"
	got := redactSummary(input, 512)
	for _, secret := range []string{"dev@example.com", "abc.def_123", "abcdefghijklmnopqrstuvwxyz1234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redaction leaked %q in %q", secret, got)
		}
	}
}

func TestProbabilityAboveRisk(t *testing.T) {
	probabilities := map[string]float64{"LOW": 0.02, "MEDIUM": 0.08, "HIGH": 0.70, "CRITICAL": 0.20}
	if got := probabilityAboveRisk(probabilities, "MEDIUM"); math.Abs(got-0.90) > 1e-9 {
		t.Fatalf("probability=%v, want 0.90", got)
	}
	if got := probabilityAboveRisk(probabilities, "CRITICAL"); got != 0 {
		t.Fatalf("probability=%v, want 0", got)
	}
}

func TestValidateResponseRejectsMissingAndUncalibratedAnswers(t *testing.T) {
	request := Request{Questions: map[string]Question{
		"binary": {Type: QuestionNoul},
		"risk":   {Type: QuestionChoice, Criteria: map[string]string{"LOW": "low", "HIGH": "high"}},
	}}
	noul := 0.8
	response := Response{Model: "typesafe/jev-1.13", Answers: map[string]Answer{
		"binary": {Noul: &noul},
		"risk": {Choice: "HIGH", Probabilities: map[string]float64{
			"LOW": 0.2, "HIGH": 0.2,
		}},
	}}
	if err := validateResponse(request, response); err == nil {
		t.Fatal("expected invalid probability sum")
	}
	response.Answers["risk"] = Answer{Choice: "HIGH", Probabilities: map[string]float64{"LOW": 0.2, "HIGH": 0.8}}
	if err := validateResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticRequestHasOnlyDecisionFields(t *testing.T) {
	raw, err := json.Marshal(Request{State: map[string]any{"goal": "refund"}, Questions: map[string]Question{
		"aligned": {Type: QuestionNoul, Instructions: "Does it align?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"messages", "plugins", "tools", "arguments", "output"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("request contains forbidden field %q: %s", forbidden, text)
		}
	}
}

func TestOutboundStateAllowlistRejectsArguments(t *testing.T) {
	state := map[string]any{
		"work": map[string]any{"goal": "refund", "status": "RUNNING", "declared_risk": "MEDIUM"},
		"action": map[string]any{
			"operation": "payment.refund", "status": "PROPOSED", "declared_risk": "MEDIUM",
			"resources": []any{}, "arguments": map[string]any{"card": "secret"},
		},
	}
	if err := validateOutboundState("RISK_REVIEW", state); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateOrdinalScore(t *testing.T) {
	score := 1.4
	confidence := 0.7
	request := Request{Questions: map[string]Question{
		"severity": {Type: QuestionScore, Criteria: []string{"low", "medium", "high"}},
	}}
	response := Response{Model: "typesafe/jev-1.13", Answers: map[string]Answer{
		"severity": {Score: &score, Probabilities: map[string]float64{"0": 0.1, "1": 0.4, "2": 0.5}, Confidence: &confidence},
	}}
	if err := validateResponse(request, response); err != nil {
		t.Fatal(err)
	}
}
