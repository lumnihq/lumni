// Package semantic implements optional, asynchronous semantic recommendations.
// Its outputs are evidence for findings only; they never participate in admission.
package semantic

import (
	"context"
)

const ProviderOpenRouter = "openrouter"

type QuestionType string

const (
	QuestionNoul   QuestionType = "noul"
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
)

// Question mirrors the provider-neutral System One decision primitives.
// Criteria is omitted for a plain Noul, a map for Choice, and an ordered slice
// for Score.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

type Usage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	PromptTokens     int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens int64   `json:"completion_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CostUSD          float64 `json:"cost,omitempty"`
}

func (u Usage) EffectiveInputTokens() int64 {
	if u.InputTokens > 0 {
		return u.InputTokens
	}
	return u.PromptTokens
}

type Response struct {
	Model      string            `json:"model"`
	Answers    map[string]Answer `json:"answers"`
	Usage      Usage             `json:"usage"`
	CostMicros int64             `json:"-"`
}

// SemanticEvaluator is deliberately independent of OpenRouter so a direct TypeSafe or
// Cloudflare adapter can implement the same contract later.
type SemanticEvaluator interface {
	Provider() string
	RequestedModel() string
	Evaluate(context.Context, Request) (Response, error)
	PrivacyEligible(context.Context) (bool, error)
}
