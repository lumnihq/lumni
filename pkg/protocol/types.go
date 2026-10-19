// Package protocol contains Lumni's framework-neutral public wire types.
package protocol

import (
	"encoding/json"
	"time"
)

const SchemaVersion = "1.0"

type ActorType string

const (
	ActorAgent    ActorType = "AGENT"
	ActorHuman    ActorType = "HUMAN"
	ActorService  ActorType = "SERVICE"
	ActorCron     ActorType = "CRON"
	ActorWebhook  ActorType = "WEBHOOK"
	ActorCI       ActorType = "CI"
	ActorWorkflow ActorType = "WORKFLOW"
	ActorSystem   ActorType = "SYSTEM"
)

type WorkStatus string

const (
	WorkPending    WorkStatus = "PENDING"
	WorkRunning    WorkStatus = "RUNNING"
	WorkWaiting    WorkStatus = "WAITING"
	WorkBlocked    WorkStatus = "BLOCKED"
	WorkCompleted  WorkStatus = "COMPLETED"
	WorkFailed     WorkStatus = "FAILED"
	WorkCancelled  WorkStatus = "CANCELLED"
	WorkSuperseded WorkStatus = "SUPERSEDED"
	WorkJoined     WorkStatus = "JOINED"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "LOW"
	RiskMedium   RiskLevel = "MEDIUM"
	RiskHigh     RiskLevel = "HIGH"
	RiskCritical RiskLevel = "CRITICAL"
)

type AccessMode string

const (
	AccessObserve AccessMode = "OBSERVE"
	AccessRead    AccessMode = "READ"
	AccessWrite   AccessMode = "WRITE"
	AccessCreate  AccessMode = "CREATE"
	AccessDelete  AccessMode = "DELETE"
	AccessImpact  AccessMode = "IMPACT"
)

type DecisionType string

const (
	DecisionAllow           DecisionType = "ALLOW"
	DecisionWarn            DecisionType = "WARN"
	DecisionWait            DecisionType = "WAIT"
	DecisionReplan          DecisionType = "REPLAN"
	DecisionDeduplicate     DecisionType = "DEDUPLICATE"
	DecisionRequireApproval DecisionType = "REQUIRE_APPROVAL"
	DecisionReject          DecisionType = "REJECT"
	DecisionWouldAllow      DecisionType = "WOULD_ALLOW"
	DecisionWouldWait       DecisionType = "WOULD_WAIT"
	DecisionWouldReplan     DecisionType = "WOULD_REPLAN"
	DecisionWouldDedup      DecisionType = "WOULD_DEDUPLICATE"
	DecisionWouldApproval   DecisionType = "WOULD_REQUIRE_APPROVAL"
	DecisionWouldReject     DecisionType = "WOULD_REJECT"
)

type ReasonCode string

const (
	ReasonNoConflict                   ReasonCode = "NO_CONFLICT"
	ReasonExactDuplicateActive         ReasonCode = "EXACT_DUPLICATE_ACTIVE"
	ReasonEffectAlreadyCommitted       ReasonCode = "EXACT_EFFECT_ALREADY_COMMITTED"
	ReasonIdempotencyPayloadMismatch   ReasonCode = "IDEMPOTENCY_KEY_REUSED_DIFFERENT_PAYLOAD"
	ReasonStaleObservation             ReasonCode = "STALE_OBSERVATION"
	ReasonVersionUnavailable           ReasonCode = "VERSION_UNAVAILABLE"
	ReasonVersionSourceChanged         ReasonCode = "VERSION_SOURCE_CHANGED"
	ReasonConflictingActiveMutation    ReasonCode = "CONFLICTING_ACTIVE_MUTATION"
	ReasonWriteWriteConflict           ReasonCode = "WRITE_WRITE_CONFLICT"
	ReasonDeleteWriteConflict          ReasonCode = "DELETE_WRITE_CONFLICT"
	ReasonDomainOperationConflict      ReasonCode = "DOMAIN_OPERATION_CONFLICT"
	ReasonApprovalRequired             ReasonCode = "APPROVAL_REQUIRED"
	ReasonApprovalPending              ReasonCode = "APPROVAL_PENDING"
	ReasonApprovalDenied               ReasonCode = "APPROVAL_DENIED"
	ReasonApprovalExpired              ReasonCode = "APPROVAL_EXPIRED"
	ReasonResourceUnresolved           ReasonCode = "RESOURCE_UNRESOLVED"
	ReasonResourceMappingLowConfidence ReasonCode = "RESOURCE_MAPPING_LOW_CONFIDENCE"
	ReasonLeaseBusy                    ReasonCode = "LEASE_BUSY"
	ReasonLeaseExpired                 ReasonCode = "LEASE_EXPIRED"
	ReasonFencingTokenInvalid          ReasonCode = "FENCING_TOKEN_INVALID"
	ReasonActionResultUnknown          ReasonCode = "ACTION_RESULT_UNKNOWN"
	ReasonCoordinatorUnavailable       ReasonCode = "COORDINATOR_UNAVAILABLE"
	ReasonPolicyRejected               ReasonCode = "POLICY_REJECTED"
)

type ActionStatus string

const (
	ActionProposed         ActionStatus = "PROPOSED"
	ActionAwaitingApproval ActionStatus = "AWAITING_APPROVAL"
	ActionReserved         ActionStatus = "RESERVED"
	ActionActive           ActionStatus = "ACTIVE"
	ActionSucceeded        ActionStatus = "SUCCEEDED"
	ActionFailed           ActionStatus = "FAILED"
	ActionUnknown          ActionStatus = "UNKNOWN"
	ActionExpired          ActionStatus = "EXPIRED"
)

type EnforcementMode string

const (
	ModeShadow  EnforcementMode = "SHADOW"
	ModeWarn    EnforcementMode = "WARN"
	ModeEnforce EnforcementMode = "ENFORCE"
)

type Actor struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	ID            string          `json:"id"`
	Type          ActorType       `json:"type"`
	Runtime       string          `json:"runtime,omitempty"`
	DisplayName   string          `json:"display_name,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
	Paused        bool            `json:"paused"`
	PausedAt      *time.Time      `json:"paused_at,omitempty"`
	PausedBy      *string         `json:"paused_by,omitempty"`
	Until         *time.Time      `json:"until,omitempty"`
}

type Agent struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	ID            string          `json:"id"`
	ActorID       string          `json:"actor_id"`
	Name          string          `json:"name"`
	Framework     string          `json:"framework,omitempty"`
	ModelProvider string          `json:"model_provider,omitempty"`
	Model         string          `json:"model,omitempty"`
	Version       string          `json:"version,omitempty"`
	RepositoryID  *string         `json:"repository_id,omitempty"`
	DeploymentID  *string         `json:"deployment_id,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
}

type Session struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	ID            string          `json:"id"`
	AgentID       *string         `json:"agent_id,omitempty"`
	ActorID       string          `json:"actor_id"`
	Runtime       string          `json:"runtime,omitempty"`
	ExternalID    *string         `json:"external_id,omitempty"`
	Status        string          `json:"status,omitempty"`
	StartedAt     time.Time       `json:"started_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type Work struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	ID            string          `json:"id"`
	ActorID       string          `json:"actor_id"`
	AgentID       *string         `json:"agent_id,omitempty"`
	SessionID     *string         `json:"session_id,omitempty"`
	ParentWorkID  *string         `json:"parent_work_id,omitempty"`
	Goal          string          `json:"goal"`
	Status        WorkStatus      `json:"status,omitempty"`
	Priority      int             `json:"priority,omitempty"`
	Risk          RiskLevel       `json:"risk,omitempty"`
	RepositoryID  *string         `json:"repository_id,omitempty"`
	IssueRef      *string         `json:"issue_ref,omitempty"`
	PRRef         *string         `json:"pr_ref,omitempty"`
	BaseCommit    *string         `json:"base_commit,omitempty"`
	StartedAt     time.Time       `json:"started_at,omitempty"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type ResourceRef struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	ExternalID string          `json:"external_id,omitempty"`
	Namespace  string          `json:"namespace,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type Resource struct {
	SchemaVersion string `json:"schema_version,omitempty"`
	ResourceRef
	CurrentVersion    *string   `json:"current_version,omitempty"`
	VersionSource     *string   `json:"version_source,omitempty"`
	VersionConfidence float64   `json:"version_confidence,omitempty"`
	FencingToken      int64     `json:"fencing_token,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type ResourceIntent struct {
	ResourceRef
	Mode            AccessMode      `json:"mode"`
	Operation       string          `json:"operation,omitempty"`
	ObservedVersion *string         `json:"observed_version,omitempty"`
	ObservedAt      *time.Time      `json:"observed_at,omitempty"`
	ExpectedVersion *string         `json:"expected_version,omitempty"`
	VersionSource   string          `json:"version_source,omitempty"`
	Confidence      float64         `json:"confidence,omitempty"`
	EvidenceType    string          `json:"evidence_type,omitempty"`
	Evidence        json.RawMessage `json:"evidence,omitempty"`
}

type ObservationInput struct {
	StepID        *string         `json:"step_id,omitempty"`
	SchemaVersion string          `json:"schema_version,omitempty"`
	ID            string          `json:"id,omitempty"`
	WorkID        string          `json:"work_id"`
	SessionID     *string         `json:"session_id,omitempty"`
	Resource      ResourceRef     `json:"resource"`
	Version       string          `json:"version"`
	VersionSource string          `json:"version_source"`
	ObservedAt    *time.Time      `json:"observed_at,omitempty"`
	Confidence    float64         `json:"confidence,omitempty"`
	EvidenceType  string          `json:"evidence_type,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type ResourceVersionInput struct {
	Version       string          `json:"version"`
	VersionSource string          `json:"version_source"`
	ObservedAt    *time.Time      `json:"observed_at,omitempty"`
	Source        string          `json:"source,omitempty"`
	Sequence      *int64          `json:"sequence,omitempty"`
	Confidence    float64         `json:"confidence,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	ActorID       *string         `json:"actor_id,omitempty"`
	ActionID      *string         `json:"action_id,omitempty"`
	Summary       *string         `json:"summary,omitempty"`
	ValueHash     *string         `json:"value_hash,omitempty"`
}

type ActionCheckInput struct {
	StepID         *string          `json:"step_id,omitempty"`
	Verification   *OutcomeSpec     `json:"verification,omitempty"`
	RecheckID      string           `json:"-"`
	SchemaVersion  string           `json:"schema_version,omitempty"`
	ActionID       string           `json:"action_id,omitempty"`
	WorkID         string           `json:"work_id"`
	ActorID        string           `json:"actor_id"`
	SessionID      *string          `json:"session_id,omitempty"`
	Operation      string           `json:"operation"`
	Tool           string           `json:"tool,omitempty"`
	Source         string           `json:"source,omitempty"`
	Arguments      json.RawMessage  `json:"arguments,omitempty"`
	Resources      []ResourceIntent `json:"resources,omitempty"`
	IdempotencyKey *string          `json:"idempotency_key,omitempty"`
	EffectKey      *string          `json:"effect_key,omitempty"`
	Risk           RiskLevel        `json:"risk,omitempty"`
	Metadata       json.RawMessage  `json:"metadata,omitempty"`
	ObserveOnly    bool             `json:"observe_only,omitempty"`
}

type Evidence struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// DecisionEvaluation is one deterministic stage in an admission receipt.
// Details must contain only redacted, operator-safe values.
type DecisionEvaluation struct {
	Stage      string         `json:"stage"`
	Outcome    string         `json:"outcome"`
	ReasonCode ReasonCode     `json:"reason_code,omitempty"`
	SubjectID  string         `json:"subject_id,omitempty"`
	DurationUS int64          `json:"duration_us,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

type LeaseGrant struct {
	ResourceID   string    `json:"resource_id"`
	FencingToken int64     `json:"fencing_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Decision struct {
	SchemaVersion        string               `json:"schema_version"`
	ActionID             string               `json:"action_id"`
	ReceiptID            string               `json:"receipt_id"`
	Decision             DecisionType         `json:"decision"`
	ReasonCode           ReasonCode           `json:"reason_code"`
	Enforced             bool                 `json:"enforced"`
	Confidence           float64              `json:"confidence"`
	RetryAfterMS         *int64               `json:"retry_after_ms,omitempty"`
	LeaseToken           *string              `json:"lease_token,omitempty"`
	ExpiresAt            *time.Time           `json:"expires_at,omitempty"`
	Leases               []LeaseGrant         `json:"leases,omitempty"`
	Evidence             []Evidence           `json:"evidence,omitempty"`
	Evaluations          []DecisionEvaluation `json:"evaluations,omitempty"`
	ExistingActionID     *string              `json:"existing_action_id,omitempty"`
	ExternalResultID     *string              `json:"existing_result_id,omitempty"`
	ConflictingActionIDs []string             `json:"conflicting_action_ids,omitempty"`
	ApprovalID           *string              `json:"approval_id,omitempty"`
	KernelVersion        string               `json:"kernel_version"`
	ResolverVersion      string               `json:"resolver_version"`
	PolicyVersion        string               `json:"policy_version"`
	CreatedAt            time.Time            `json:"created_at"`
}

type ActionResultInput struct {
	Output           json.RawMessage   `json:"output,omitempty"`
	Status           string            `json:"status"`
	LeaseToken       *string           `json:"lease_token,omitempty"`
	ExternalResultID *string           `json:"external_result_id,omitempty"`
	ResourceVersions map[string]string `json:"resource_versions,omitempty"`
	CreatedResources []ResourceRef     `json:"created_resources,omitempty"`
	Retryable        *bool             `json:"retryable,omitempty"`
	Compensation     json.RawMessage   `json:"compensation,omitempty"`
	ResultMetadata   json.RawMessage   `json:"result_metadata,omitempty"`
}

type EventEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	EventID       string          `json:"event_id"`
	Type          string          `json:"type"`
	Source        string          `json:"source"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Actor         *Actor          `json:"actor,omitempty"`
	SessionID     *string         `json:"session_id,omitempty"`
	WorkID        *string         `json:"work_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type CostInput struct {
	StepID          *string         `json:"step_id,omitempty"`
	ID              string          `json:"id,omitempty"`
	ActorID         *string         `json:"actor_id,omitempty"`
	AgentID         *string         `json:"agent_id,omitempty"`
	SessionID       *string         `json:"session_id,omitempty"`
	WorkID          *string         `json:"work_id,omitempty"`
	ActionID        *string         `json:"action_id,omitempty"`
	Provider        string          `json:"provider"`
	Model           *string         `json:"model,omitempty"`
	CostType        string          `json:"cost_type"`
	InputUnits      *int64          `json:"input_units,omitempty"`
	OutputUnits     *int64          `json:"output_units,omitempty"`
	ComputeUnits    *float64        `json:"compute_units,omitempty"`
	AmountMicrosUSD int64           `json:"amount_micros_usd"`
	ObservedAt      *time.Time      `json:"observed_at,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type GraphLens string

const (
	GraphLensLive        GraphLens = "live"
	GraphLensAgents      GraphLens = "agents"
	GraphLensWork        GraphLens = "work"
	GraphLensResources   GraphLens = "resources"
	GraphLensTransaction GraphLens = "transaction"
	GraphLensFinOps      GraphLens = "finops"
)

type AnalyticsFreshness struct {
	Source     string    `json:"source"`
	AsOf       time.Time `json:"as_of"`
	LagSeconds int64     `json:"lag_seconds"`
	Fallback   bool      `json:"fallback,omitempty"`
	Message    string    `json:"message,omitempty"`
}

type GraphNode struct {
	ID         string          `json:"id"`
	EntityID   string          `json:"entity_id"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Subtitle   string          `json:"subtitle,omitempty"`
	Status     string          `json:"status,omitempty"`
	Risk       RiskLevel       `json:"risk,omitempty"`
	AgentState string          `json:"agent_state,omitempty"`
	CostMicros int64           `json:"cost_micros_usd,omitempty"`
	OccurredAt *time.Time      `json:"occurred_at,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type GraphLink struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"`
	Target   string          `json:"target"`
	Type     string          `json:"type"`
	Label    string          `json:"label,omitempty"`
	Status   string          `json:"status,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type AttentionItem struct {
	ID          string     `json:"id"`
	EntityType  string     `json:"entity_type"`
	EntityID    string     `json:"entity_id"`
	Priority    int        `json:"priority"`
	Category    string     `json:"category"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status,omitempty"`
	Risk        RiskLevel  `json:"risk,omitempty"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}

type GraphTrendPoint struct {
	Bucket         time.Time `json:"bucket"`
	Actions        int64     `json:"actions"`
	Decisions      int64     `json:"decisions"`
	Failures       int64     `json:"failures"`
	CostMicrosUSD  int64     `json:"cost_micros_usd"`
	WasteMicrosUSD int64     `json:"waste_micros_usd"`
}

type GraphSnapshot struct {
	SchemaVersion string             `json:"schema_version"`
	Lens          GraphLens          `json:"lens"`
	Nodes         []GraphNode        `json:"nodes"`
	Edges         []GraphLink        `json:"edges"`
	Attention     []AttentionItem    `json:"attention"`
	Trends        []GraphTrendPoint  `json:"trends,omitempty"`
	AsOf          time.Time          `json:"as_of"`
	Cursor        int64              `json:"cursor"`
	Truncated     bool               `json:"truncated"`
	NextCursor    *string            `json:"next_cursor"`
	Limit         int                `json:"limit"`
	Analytics     AnalyticsFreshness `json:"analytics"`
}

type GraphTimelineEvent struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type SafeAction struct {
	Type      string `json:"type"`
	Label     string `json:"label"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	CSRF      bool   `json:"csrf_required"`
	Dangerous bool   `json:"dangerous"`
}

type EntityInspector struct {
	SchemaVersion string               `json:"schema_version"`
	Entity        GraphNode            `json:"entity"`
	RelatedNodes  []GraphNode          `json:"related_nodes"`
	RelatedEdges  []GraphLink          `json:"related_edges"`
	Timeline      []GraphTimelineEvent `json:"timeline"`
	Evaluations   []DecisionEvaluation `json:"evaluations,omitempty"`
	Details       json.RawMessage      `json:"details,omitempty"`
	SafeActions   []SafeAction         `json:"safe_actions"`
	AsOf          time.Time            `json:"as_of"`
}

type FinOpsBreakdown struct {
	Key             string `json:"key"`
	Label           string `json:"label"`
	AmountMicrosUSD int64  `json:"amount_micros_usd"`
	WasteMicrosUSD  int64  `json:"waste_micros_usd"`
	Count           int64  `json:"count"`
	Avoided         bool   `json:"avoided"`
}

type FinOpsGraph struct {
	SchemaVersion string             `json:"schema_version"`
	PeriodStart   time.Time          `json:"period_start"`
	PeriodEnd     time.Time          `json:"period_end"`
	Nodes         []GraphNode        `json:"nodes"`
	Edges         []GraphLink        `json:"edges"`
	Series        []GraphTrendPoint  `json:"series"`
	ByProvider    []FinOpsBreakdown  `json:"by_provider"`
	ByModel       []FinOpsBreakdown  `json:"by_model"`
	ByWaste       []FinOpsBreakdown  `json:"by_waste"`
	ByActor       []ActorCost        `json:"by_actor"`
	TotalMicros   int64              `json:"total_cost_micros_usd"`
	WasteMicros   int64              `json:"waste_micros_usd"`
	Analytics     AnalyticsFreshness `json:"analytics"`
}

type APIKeyCreateInput struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type APIKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Secret    string     `json:"secret,omitempty"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

type List[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor,omitempty"`
}
