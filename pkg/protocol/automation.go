package protocol

import (
	"encoding/json"
	"time"
)

// ContractBinding maps a proposed sentence to explicit, executable identities.
// Pointers are RFC 6901 JSON pointers; monetary values are integer minor units.
type ContractBinding struct {
	Operation            string            `json:"operation"`
	ActorID              string            `json:"actor_id,omitempty"`
	KeyPointer           string            `json:"key_pointer,omitempty"`
	AmountPointer        string            `json:"amount_pointer,omitempty"`
	CurrencyPointer      string            `json:"currency_pointer,omitempty"`
	Currency             string            `json:"currency,omitempty"`
	ReadBindingID        string            `json:"read_binding_id,omitempty"`
	ReadArguments        map[string]string `json:"read_arguments,omitempty"`
	ValuePointer         string            `json:"value_pointer,omitempty"`
	LastEditorPointer    string            `json:"last_editor_pointer,omitempty"`
	LastEditedAtPointer  string            `json:"last_edited_at_pointer,omitempty"`
	VerifierID           string            `json:"verifier_id,omitempty"`
	Expected             json.RawMessage   `json:"expected,omitempty"`
	WithinSeconds        int               `json:"within_seconds,omitempty"`
	MaxAgeSeconds        int               `json:"max_age_seconds,omitempty"`
	PredecessorOperation string            `json:"predecessor_operation,omitempty"`
}

type ContractRule struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Slots   map[string]any  `json:"slots"`
	Binding ContractBinding `json:"binding"`
}

type Contract struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Text          string          `json:"text"`
	Rules         []ContractRule  `json:"rules"`
	Revision      int             `json:"revision"`
	Mode          string          `json:"mode"`
	Was           string          `json:"was"`
	EffectiveMode EnforcementMode `json:"effective_mode"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type ContractInput struct {
	Title    string         `json:"title"`
	Text     string         `json:"text"`
	Rules    []ContractRule `json:"rules"`
	Revision int            `json:"revision,omitempty"`
}

type ToolBinding struct {
	ID                string              `json:"id"`
	Version           int                 `json:"version"`
	App               string              `json:"app"`
	Operation         string              `json:"operation"`
	Method            string              `json:"method"`
	URL               string              `json:"url"`
	ReadOnly          bool                `json:"read_only"`
	Enabled           bool                `json:"enabled"`
	InputSchema       json.RawMessage     `json:"input_schema"`
	OutputSchema      json.RawMessage     `json:"output_schema"`
	Resources         []ResourceMapping   `json:"resources,omitempty"`
	EffectKeyFields   []string            `json:"effect_key_fields,omitempty"`
	VerifierID        string              `json:"verifier_id,omitempty"`
	Expected          json.RawMessage     `json:"expected,omitempty"`
	Secret            string              `json:"secret,omitempty"`
	IdempotencyHeader string              `json:"idempotency_header,omitempty"`
	Objects           map[string][]string `json:"objects,omitempty"`
	PolicyFingerprint string              `json:"policy_fingerprint,omitempty"`
}

type TraceStep struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	BindingID      string          `json:"binding_id,omitempty"`
	BindingVersion int             `json:"binding_version,omitempty"`
	ActionID       string          `json:"action_id,omitempty"`
	ModelCallID    string          `json:"model_call_id,omitempty"`
	StepID         string          `json:"step_id,omitempty"`
	Input          json.RawMessage `json:"input"`
	Output         json.RawMessage `json:"output"`
	DurationMS     int64           `json:"duration_ms"`
}

type Trace struct {
	ID             string          `json:"id"`
	WorkID         string          `json:"work_id"`
	ActorID        string          `json:"actor_id"`
	HandlerVersion string          `json:"handler_version"`
	Operation      string          `json:"operation"`
	Input          json.RawMessage `json:"input"`
	Output         json.RawMessage `json:"output"`
	Steps          []TraceStep     `json:"steps"`
	CostMicrosUSD  *int64          `json:"cost_micros_usd,omitempty"`
	DurationMS     int64           `json:"duration_ms"`
	CompletedAt    time.Time       `json:"completed_at"`
}

// Expression is deliberately not source code. Ref addresses /input or /vars.
type Expression struct {
	Literal json.RawMessage       `json:"literal,omitempty"`
	Ref     string                `json:"ref,omitempty"`
	Op      string                `json:"op,omitempty"`
	Args    []Expression          `json:"args,omitempty"`
	Object  map[string]Expression `json:"object,omitempty"`
}

type ProgramNode struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	BindingID      string     `json:"binding_id,omitempty"`
	BindingVersion int        `json:"binding_version,omitempty"`
	Value          Expression `json:"value"`
	Next           string     `json:"next,omitempty"`
	Otherwise      string     `json:"otherwise,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

type CompiledProgram struct {
	Interpreter  string          `json:"interpreter"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Nodes        []ProgramNode   `json:"nodes"`
}

type CompiledInvocation struct {
	InvocationID string          `json:"invocation_id"`
	ActorID      string          `json:"actor_id"`
	ParentWorkID *string         `json:"parent_work_id,omitempty"`
	Input        json.RawMessage `json:"input"`
}
