package protocol

import (
	"encoding/json"
	"time"
)

// OutcomeSpec describes the authoritative assertion required by an action.
type OutcomeSpec struct {
	VerifierID string          `json:"verifier_id"`
	Expected   json.RawMessage `json:"expected"`
}

type RetryPolicy struct {
	MaxAttempts    int      `json:"max_attempts,omitempty"`
	InitialDelayMS int64    `json:"initial_delay_ms,omitempty"`
	MaxDelayMS     int64    `json:"max_delay_ms,omitempty"`
	Multiplier     float64  `json:"multiplier,omitempty"`
	RetryOn        []string `json:"retry_on,omitempty"`
}

type RunInput struct {
	Work
	Handler            string                 `json:"handler"`
	HandlerVersion     string                 `json:"handler_version"`
	Input              json.RawMessage        `json:"input,omitempty"`
	HandleChildFailure bool                   `json:"handle_child_failure,omitempty"`
	ExpectedEffects    map[string]OutcomeSpec `json:"expected_effects,omitempty"`
}

type ClaimInput struct {
	WorkerID string            `json:"worker_id"`
	Handlers map[string]string `json:"handlers"`
}

type RunClaim struct {
	WorkID         string          `json:"work_id"`
	ActorID        string          `json:"actor_id"`
	Handler        string          `json:"handler"`
	HandlerVersion string          `json:"handler_version"`
	Input          json.RawMessage `json:"input"`
	Token          string          `json:"token"`
	Fence          int64           `json:"fence"`
	ExpiresAt      time.Time       `json:"expires_at"`
}

type RuntimeAuth struct {
	Token string `json:"token"`
	Fence int64  `json:"fence"`
}

type StepInput struct {
	RuntimeAuth
	Key         string            `json:"key"`
	Type        string            `json:"type"`
	Name        string            `json:"name,omitempty"`
	Input       json.RawMessage   `json:"input,omitempty"`
	Action      *ActionCheckInput `json:"action,omitempty"`
	RetryPolicy RetryPolicy       `json:"retry_policy,omitempty"`
	Wait        *WaitInput        `json:"wait,omitempty"`
}

type Step struct {
	ID         string          `json:"id"`
	WorkID     string          `json:"work_id"`
	Key        string          `json:"key"`
	Type       string          `json:"type"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Attempt    int             `json:"attempt"`
	InputHash  string          `json:"input_hash"`
	OutputHash string          `json:"output_hash,omitempty"`
	ActionID   *string         `json:"action_id,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Decision   *Decision       `json:"decision,omitempty"`
	Error      string          `json:"error,omitempty"`
	Replay     bool            `json:"replay,omitempty"`
}

type StepResult struct {
	RuntimeAuth
	Attempt          int             `json:"attempt"`
	Status           string          `json:"status"`
	Output           json.RawMessage `json:"output,omitempty"`
	Error            string          `json:"error,omitempty"`
	Retryable        bool            `json:"retryable,omitempty"`
	EffectAbsent     bool            `json:"effect_absent,omitempty"`
	ExternalResultID *string         `json:"external_result_id,omitempty"`
}

type WaitInput struct {
	Type       string     `json:"type"`
	DelayMS    int64      `json:"delay_ms,omitempty"`
	WakeAt     *time.Time `json:"wake_at,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	ResourceID string     `json:"resource_id,omitempty"`
	Version    string     `json:"version,omitempty"`
	WorkID     string     `json:"work_id,omitempty"`
	ApprovalID string     `json:"approval_id,omitempty"`
}

type SignalInput struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Verifier struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config"`
	Secret  string          `json:"secret,omitempty"`
	Enabled bool            `json:"enabled"`
}

// VerificationEvidence is also the authenticated custom/event verifier contract.
// Absence is conclusive only when the previous invocation cannot still commit.
type VerificationEvidence struct {
	ID                     string          `json:"id"`
	Status                 string          `json:"status"`
	Observed               json.RawMessage `json:"observed,omitempty"`
	ExternalResultID       *string         `json:"external_result_id,omitempty"`
	EffectAbsent           bool            `json:"effect_absent,omitempty"`
	PriorAttemptTerminated bool            `json:"prior_attempt_terminated,omitempty"`
	Reason                 string          `json:"reason,omitempty"`
}

type DependencyInput struct {
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	TargetID     string `json:"target_id"`
	EntityType   string `json:"entity_type"`
	Relationship string `json:"relationship"`
}

type EnforcementRule struct {
	Operation string          `json:"operation"`
	Rule      string          `json:"rule"`
	Mode      EnforcementMode `json:"mode"`
}

type OperationVerifier struct {
	Operation        string          `json:"operation"`
	VerifierID       string          `json:"verifier_id"`
	ExpectedTemplate json.RawMessage `json:"expected_template"`
}
