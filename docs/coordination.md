# Lumni Coordination Runtime — Backend & API Architecture

**Status:** End-to-end backend design specification  
**Primary implementation language:** Go  
**Initial operating mode:** Observe / shadow analysis  
**Long-term operating mode:** Cross-runtime admission, coordination, verification, and FinOps  
**Primary deployment model:** Self-hosted / customer VPC with optional Lumni Cloud management plane  
**Initial license target for core runtime:** Apache-2.0  

---

# 1. Executive Summary

Lumni is an **independent coordination and transactional-correctness layer for production AI agents**.

It connects to heterogeneous agents, runtimes, workflows, humans, automation systems, repositories, and external resources and maintains a live model of:

- **who** is doing work,
- **what** they are trying to accomplish,
- **what state** they observed,
- **what actions** they intend to take,
- **what resources** those actions affect,
- **what other actors** are concurrently changing,
- **what effects** actually happened,
- **whether the intended outcome** is true,
- and **how much useful or wasted compute** was consumed.

Lumni is not primarily an agent framework, durable workflow engine, generic observability system, agent registry, generic governance product, or LLM evaluation platform.

Its core question is:

> **Given what this agent observed, what it intends to change, what other actors are doing, and what the real world currently looks like, should this action execute now — and did the intended effect actually happen?**

The system progresses through increasingly active modes:

```text
OBSERVE
  ↓
CORRELATE
  ↓
DETECT
  ↓
WARN
  ↓
REQUIRE APPROVAL
  ↓
WAIT / DEDUPLICATE / REPLAN
  ↓
VERIFY
  ↓
RECONCILE
  ↓
OPTIMIZE USEFUL WORK
```

The hard production path is deterministic. LLMs, embeddings, and semantic models may enrich understanding, propose relationships, identify likely duplicate goals, or explain incidents, but **must not independently hard-block consequential production actions**.

The central architecture is:

```text
                    AGENTS / HUMANS / RUNTIMES

 OpenAI Agents   Claude   LangGraph   CrewAI   Custom Agents
 Temporal        Restate  Cron        Humans   CI/CD
 MCP Clients     A2A      HTTP Apps   GitHub   Automation

                              │
                              │ events / observations /
                              │ proposed actions / results
                              ▼

┌──────────────────────────────────────────────────────────────────┐
│                    INGESTION / ACTION GATEWAYS                    │
│                                                                  │
│ MCP proxy │ SDK │ HTTP middleware │ OTel │ A2A │ webhooks        │
└──────────────────────────────┬───────────────────────────────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │ Event / Action      │
                    │ Normalization       │
                    └──────────┬──────────┘
                               │
             ┌─────────────────┼──────────────────────┐
             ▼                 ▼                      ▼
      Work Resolver     Resource Resolver      Cost Resolver
             │                 │                      │
             ▼                 ▼                      ▼
      ┌───────────────────────────────────────────────────────┐
      │              LIVE WORK / RESOURCE GRAPH               │
      │                                                       │
      │ Agents  Work  Actions  Resources  Versions  Effects   │
      │ Repo/PR Context  Dependencies  Outcomes  Costs        │
      └───────────────────────┬───────────────────────────────┘
                              │
                              ▼
                    ┌──────────────────────┐
                    │ COORDINATION KERNEL  │
                    │                      │
                    │ stale state          │
                    │ exact duplicates     │
                    │ conflicting actions  │
                    │ dependencies         │
                    │ supersession         │
                    │ policy / approvals   │
                    │ resource leases      │
                    │ idempotency          │
                    └──────────┬───────────┘
                               │
                 ┌─────────────┼───────────────┐
                 ▼             ▼               ▼
               ALLOW          WAIT           REPLAN
                 │             │
                 │             ├──────────────► runtime wakeup
                 │
                 ├────────────► DEDUPLICATE / JOIN
                 │
                 ├────────────► REQUIRE_APPROVAL
                 │
                 ▼
                    EXISTING EXECUTION RUNTIME
              Temporal / Restate / LangGraph / direct
                               │
                               ▼
                      External systems
                               │
                               ▼
                     ┌──────────────────┐
                     │ Result Recorder  │
                     └────────┬─────────┘
                              │
                              ▼
                     ┌──────────────────┐
                     │ Outcome Verifier │
                     └────────┬─────────┘
                              │
                              ▼
              versions / graph / cost / incidents updated
```

---

# 2. Product Thesis

Production agents increasingly perform consequential actions:

- refund payments,
- cancel subscriptions,
- modify orders,
- change customer entitlements,
- update CRM records,
- schedule or cancel appointments,
- create invoices or payouts,
- modify databases,
- deploy infrastructure,
- rollback services,
- change Kubernetes objects,
- merge pull requests,
- update tickets,
- send externally visible communication,
- operate back-office systems,
- approve workflows,
- or coordinate other agents.

The difficult production failures are often not simply model-quality failures. They are distributed-state failures.

```text
agent reads state
      ↓
agent reasons for 30 seconds
      ↓
state changes elsewhere
      ↓
agent acts on stale assumptions
```

or:

```text
agent A                agent B

refund payment:72      refund payment:72
      │                       │
      └───────── race ────────┘

→ duplicate effect
```

or:

```text
agent A                human

upgrade sub:91         cancel sub:91

→ conflicting business operations
```

or:

```text
Stripe update        SUCCESS
CRM update           FAILED
ticket close         SUCCESS

→ partially committed business workflow
```

or:

```text
tool result:
200 OK

system of record:
desired state missing

→ false completion
```

or:

```text
Agent A:
work goal = resolve account complaint

Agent B:
work goal = process refund request

Both ultimately attempt same refund.

→ duplicate autonomous work
```

The platform exists to provide a shared coordination model across runtimes rather than forcing every application team to independently implement:

- locks,
- resource ownership,
- version checks,
- idempotency,
- cross-agent conflict rules,
- duplicate-work detection,
- stale-state handling,
- approval boundaries,
- outcome verification,
- effect reconciliation,
- and cost attribution.

---

# 3. Core Product Boundary

## 3.1 Lumni owns

Lumni owns the following backend concepts:

- actor identity,
- agent identity,
- work identity,
- session/run correlation,
- action intent capture,
- resource identity,
- resource relationships,
- resource observations,
- resource versions,
- read/write/create/delete effect sets,
- exact duplicate-effect detection,
- idempotency identities,
- cross-runtime resource conflict detection,
- multi-resource reservations / leases,
- stale-state detection,
- explicit work/action dependencies,
- deterministic supersession,
- advisory semantic supersession,
- action admission,
- human approval decisions,
- action journaling,
- action result recording,
- outcome verification,
- partial-effect detection,
- reconciliation state,
- compensation metadata,
- decision receipts,
- incident records,
- live work/resource graph,
- historical work/resource graph,
- code/repository/PR enrichment,
- cost attribution,
- useful-work and wasted-work FinOps,
- replayable evidence,
- shadow-mode analysis,
- deterministic policy hooks.

## 3.2 Lumni does not own initially

Lumni does not initially own:

- LLM orchestration,
- prompt execution,
- agent planning,
- agent memory,
- RAG,
- generic durable workflow execution,
- generic RPC durability,
- generic retry engines,
- workflow authoring,
- workflow DSLs,
- generic queues,
- generic scheduling of arbitrary compute,
- generic LLM observability,
- generic model routing,
- identity governance as the main product,
- enterprise agent registry as the main product,
- model evaluation as the main product,
- full source-code intelligence as the main product,
- full knowledge graph of an organization,
- a generic authorization system,
- a replacement for Temporal,
- a replacement for Restate,
- a replacement for LangGraph,
- a replacement for OpenAI Agents SDK,
- a replacement for Kubernetes,
- a replacement for GitHub Actions.

Lumni integrates with those systems.

A durable runtime answers:

> **How do I make this work execute reliably?**

Lumni answers:

> **Should this work or action execute now, given shared state and other actors, and did it create the correct external effect?**

---

# 4. Primary Users and Workloads

## 4.1 Primary users

- developers building production AI-agent products,
- internal agent-platform teams,
- AI infrastructure teams,
- reliability engineers,
- platform engineers,
- engineering managers,
- heads of AI / agent engineering,
- support automation vendors,
- finance automation vendors,
- browser/operator-agent vendors,
- SRE/remediation-agent vendors,
- enterprise automation teams.

## 4.2 Ideal initial workload

```text
5+ consequential tools
+
multiple concurrent agent sessions
+
shared external systems
+
real mutations
+
humans / cron / services also modify the same state
+
custom retry / idempotency logic already appearing
```

## 4.3 Poor initial fit

- pure chatbots,
- summarization agents,
- read-only research agents,
- single-use prototypes,
- agents with no consequential side effects,
- applications where all state is already serialized inside one strongly controlled workflow,
- environments where no resource identity or state can be observed.

---

# 5. Problems Solved

Lumni should explicitly solve the following classes of problem.

## 5.1 Stale-state actions

```text
Agent A
READ subscription:91 @ v18
      ↓
reason
      ↓
Human CANCEL subscription:91
v18 → v19
      ↓
Agent A UPGRADE subscription:91
```

Decision:

```text
REPLAN
reason = STALE_OBSERVATION
```

## 5.2 Exact duplicate effects

```text
agent A → REFUND payment:72
agent B → REFUND payment:72
```

with the same effect identity:

```text
operation + canonical resource set + idempotency identity
```

Decision:

```text
DEDUPLICATE
JOIN
or
RETURN_PREVIOUS_RESULT
```

## 5.3 Conflicting concurrent actions

```text
DEPLOY service:payments
ROLLBACK service:payments
```

or:

```text
UPGRADE subscription:91
CANCEL subscription:91
```

Decision:

```text
WAIT
```

## 5.4 Duplicate autonomous work

Two different agents may have different prompts or task IDs but effectively pursue the same goal.

Example:

```text
Work A:
Resolve complaint #827

Work B:
Process refund for order #919
```

Both eventually target:

```text
payment:72
refund:$100
```

Exact resource/effect overlap may prove duplication.

Semantic similarity may produce:

```text
WARN_LIKELY_DUPLICATE_WORK
```

but not a hard block unless the effect identity is deterministic.

## 5.5 Superseded work

```text
Work A:
upgrade subscription

later:

Work B:
customer asks to cancel subscription
```

If explicit business state proves the original goal is no longer valid:

```text
SUPERSEDED
→ CANCEL / REPLAN
```

Semantic supersession remains advisory unless verified.

## 5.6 Dependencies

```text
refund
  ↓
CRM update
  ↓
ticket close
```

If closing a ticket depends on a verified refund:

```text
ticket-close action
→ WAIT
```

Explicit dependencies may be enforced. Inferred dependencies remain advisory initially.

## 5.7 Human-agent collisions

Humans are actors in the same resource graph.

```text
agent observed CRM account @ v11

human edited account → v12

agent writes based on v11

→ REPLAN
```

## 5.8 False completion

```text
tool:
200 OK

verifier:
expected effect missing
```

Result:

```text
EFFECT_UNVERIFIED
```

## 5.9 Partial effects

```text
payment mutation       COMMITTED
CRM mutation           FAILED
ticket mutation        COMMITTED
```

Result:

```text
PARTIALLY_COMMITTED
```

## 5.10 Unknown effects

The process crashes or the network fails after an external request.

Lumni must not blindly retry.

State:

```text
UNKNOWN_EFFECT
```

Resolution:

```text
verify
reconcile
or apply explicit retry policy
```

## 5.11 Wasted autonomous compute

Lumni should quantify compute wasted due to:

- duplicate work,
- duplicate actions,
- superseded work,
- stale work,
- work waiting on dependencies,
- retries after an effect already committed,
- work that produced no verified outcome,
- failed work followed by redundant retries.

---

# 6. Design Principles

1. **Deterministic correctness path.**
2. **LLMs are advisory, never authoritative for hard blocking.**
3. **Coordinate resources and effects, not vendors.**
4. **Do not require a universal enterprise ontology.**
5. **Use a narrow live operational graph.**
6. **Start in shadow mode.**
7. **Every decision must have a stable reason code and evidence.**
8. **Every algorithm version must be recorded.**
9. **Never use eventually consistent semantic indexes for hard enforcement.**
10. **Hard decisions require deterministic or explicitly configured evidence.**
11. **Do not replace durable execution systems.**
12. **Customer-controlled deployment must be first class.**
13. **Cloud must not require raw sensitive payloads.**
14. **Minimize integration burden.**
15. **Protocol types should be open and framework-neutral.**
16. **Repository/code context is enrichment, not a runtime requirement.**
17. **Cost should be attributed to work and verified outcomes, not only model tokens.**

---

# 7. Core Domain Model

The public model should remain small and understandable.

## 7.1 Actor

An actor performs work or actions.

Types:

```text
AGENT
HUMAN
SERVICE
CRON
WEBHOOK
CI
WORKFLOW
SYSTEM
```

```go
type Actor struct {
    ID          string
    TenantID    string
    Type        ActorType
    Runtime     string
    DisplayName string
    Metadata    json.RawMessage
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

## 7.2 Agent

```go
type Agent struct {
    ID             string
    TenantID       string
    ActorID        string
    Name           string
    Framework      string
    ModelProvider  string
    Model          string
    Version        string
    RepositoryID   *string
    DeploymentID   *string
    Metadata       json.RawMessage
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

The same logical agent may create many sessions/runs.

## 7.3 Session

A runtime execution context.

```go
type Session struct {
    ID           string
    TenantID     string
    AgentID      *string
    ActorID      string
    Runtime      string
    ExternalID   *string
    Status       SessionStatus
    StartedAt    time.Time
    FinishedAt   *time.Time
    Metadata     json.RawMessage
}
```

Lumni does not own session durability; it correlates sessions.

## 7.4 Work

A `Work` object represents a goal being pursued.

```go
type Work struct {
    ID              string
    TenantID        string
    ActorID         string
    AgentID         *string
    SessionID       *string
    ParentWorkID    *string

    Goal            string
    NormalizedGoal  *string
    Status          WorkStatus
    Priority        int
    Risk            RiskLevel

    RepositoryID    *string
    IssueRef        *string
    PRRef           *string
    BaseCommit      *string

    StartedAt       time.Time
    CompletedAt     *time.Time
    CancelledAt     *time.Time
    SupersededAt    *time.Time

    Metadata        json.RawMessage
}
```

Statuses:

```text
PENDING
RUNNING
WAITING
BLOCKED
COMPLETED
FAILED
CANCELLED
SUPERSEDED
JOINED
```

## 7.5 Resource

A resource is something whose state can be observed or changed.

Examples:

```text
stripe/payment/pi_91
subscription/sub_123
customer/cus_81
postgres/main/orders/881
salesforce/account/001...
github/acme/backend/file/src/auth/session.go
github/acme/backend/pr/912
k8s/prod/default/deployment/payments
terraform/prod/aws_instance/api_1
```

```go
type Resource struct {
    ID                string
    TenantID          string
    Type              string
    ExternalID        string
    Namespace         string
    ParentResourceID  *string

    CurrentVersion    *string
    VersionSource     *string
    VersionConfidence Confidence

    Metadata          json.RawMessage
    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

## 7.6 Resource relationship

Examples:

```text
customer → subscription
customer → payment
order → payment
ticket → customer
deployment → service
repo → file
PR → commit
file → module
```

```go
type ResourceEdge struct {
    TenantID      string
    SourceID      string
    Relationship  string
    TargetID      string
    Confidence    float64
    EvidenceType  EvidenceType
    Source        string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

## 7.7 Observation

Records the state/version used while reasoning.

```go
type Observation struct {
    ID              string
    TenantID        string
    WorkID          string
    SessionID       *string
    ResourceID      string
    Version         string
    VersionSource   string
    ObservedAt      time.Time
    Confidence      float64
    EvidenceType    EvidenceType
    Metadata        json.RawMessage
}
```

Example:

```text
work_123 observed subscription:91 @ version 18
```

## 7.8 ActionIntent

A proposed consequential action.

```go
type ActionIntent struct {
    ID             string
    TenantID       string
    WorkID         string
    ActorID        string
    SessionID      *string

    Operation      string
    Tool           string
    Source         string
    ArgumentsHash  *string
    Arguments      json.RawMessage

    Resources      []ResourceIntent

    IdempotencyKey *string
    EffectKey      *string
    Risk           RiskLevel
    RequestedAt    time.Time

    Metadata       json.RawMessage
}
```

## 7.9 ResourceIntent

```go
type ResourceIntent struct {
    ResourceID      string
    ResourceType    string
    Access          AccessMode
    Operation       string
    ObservedVersion *string
    ExpectedVersion *string

    Confidence      float64
    EvidenceType    EvidenceType
    Evidence        json.RawMessage
}
```

Access modes:

```text
OBSERVE
READ
WRITE
CREATE
DELETE
IMPACT
```

## 7.10 Effect

An effect represents the business/system change the action attempts to produce.

```go
type Effect struct {
    ID              string
    TenantID        string
    ActionID        string
    Operation       string
    EffectKey       string
    Status          EffectStatus
    ResourceIDs     []string
    ExternalID      *string
    OccurredAt      *time.Time
    Metadata        json.RawMessage
}
```

Statuses:

```text
PROPOSED
ACTIVE
COMMITTED
FAILED
UNKNOWN
PARTIAL
VERIFIED
UNVERIFIED
COMPENSATED
```

## 7.11 ActionResult

```go
type ActionResult struct {
    ActionID          string
    Status            ActionStatus
    ExternalResultID  *string
    ResourceVersions  map[string]string
    CreatedResources  []ResourceRef
    Retryable         *bool
    Compensation      json.RawMessage
    ResultMetadata    json.RawMessage
    RecordedAt        time.Time
}
```

## 7.12 Outcome

Represents the verified business/system end state.

```go
type Outcome struct {
    ID            string
    TenantID      string
    WorkID        *string
    ActionID      *string
    EffectID      *string

    Type          string
    Status        OutcomeStatus
    VerifierID    *string

    Expected      json.RawMessage
    Observed      json.RawMessage
    Evidence      json.RawMessage

    VerifiedAt    *time.Time
    CreatedAt     time.Time
}
```

Statuses:

```text
UNKNOWN
PENDING
VERIFIED
MISMATCH
PARTIAL
FAILED
```

## 7.13 Decision

```go
type Decision struct {
    ID           string
    TenantID     string
    ActionID     string
    Type         DecisionType
    ReasonCode   string
    Confidence   float64
    Evidence     []Evidence
    RetryAfter   *time.Duration
    LeaseToken   *string
    CreatedAt    time.Time
}
```

Initial decisions:

```text
ALLOW
WARN
WAIT
REPLAN
DEDUPLICATE
JOIN
REQUIRE_APPROVAL
REJECT
CANCEL_WORK
```

Shadow equivalents:

```text
WOULD_ALLOW
WOULD_WAIT
WOULD_REPLAN
WOULD_DEDUPLICATE
WOULD_REQUIRE_APPROVAL
WOULD_REJECT
```

## 7.14 Approval

```go
type ApprovalRequest struct {
    ID          string
    TenantID    string
    ActionID    string
    PolicyID    *string
    Status      ApprovalStatus
    ReasonCode  string
    RequestedAt time.Time
    ExpiresAt   *time.Time
    ResolvedAt  *time.Time
    ResolvedBy  *string
    Metadata    json.RawMessage
}
```

Lumni owns the decision and state. Delivery may integrate with Slack, Teams, email, custom webhooks, or customer systems.

## 7.15 Incident

```go
type Incident struct {
    ID          string
    TenantID    string
    Type        IncidentType
    Severity    Severity
    Status      IncidentStatus

    WorkIDs     []string
    ActionIDs   []string
    ResourceIDs []string
    OutcomeIDs  []string

    ReasonCode  string
    Evidence    json.RawMessage

    DetectedAt  time.Time
    ResolvedAt  *time.Time
}
```

## 7.16 CostRecord

```go
type CostRecord struct {
    ID              string
    TenantID        string
    ActorID         *string
    AgentID         *string
    SessionID       *string
    WorkID          *string
    ActionID        *string

    Provider        string
    Model           *string
    CostType        CostType

    InputUnits      *int64
    OutputUnits     *int64
    ComputeUnits    *float64
    AmountMicrosUSD int64

    ObservedAt      time.Time
    Metadata        json.RawMessage
}
```

---

# 8. Confidence and Evidence Model

Every inferred mapping must carry provenance.

Evidence types:

```text
EXPLICIT
PROTOCOL_SCHEMA
OPENAPI
MCP_SCHEMA
DB_SCHEMA
GIT_STATE
GITHUB_STATE
RUNTIME_OBSERVED
VERIFIER
STATIC_CODE
TEST_ASSERTION
USER_CONFIRMED
STRUCTURAL_INFERENCE
SEMANTIC_INFERENCE
```

Suggested confidence convention:

```text
deterministic / explicit        = 1.0
strong structural              = 0.90–0.99
observed repeatedly            = 0.80–0.99
weak structural                = 0.60–0.89
semantic                       = model confidence
unknown                        = 0
```

Hard enforcement should require one of:

- explicit deterministic mapping,
- protocol-derived deterministic mapping,
- confirmed mapping,
- verified repeated structural mapping promoted through policy,
- or explicit administrator policy.

Semantic-only evidence must not independently hard-block.

---

# 9. Static Context Graph

Repo, PR, code, schemas, and tests enrich the runtime.

Sources:

- Git repositories,
- GitHub repositories,
- pull requests,
- commits,
- diffs,
- file paths,
- module/package metadata,
- OpenAPI,
- MCP schemas,
- protobuf / gRPC descriptors,
- SQL schema,
- ORM definitions,
- Terraform state/configuration,
- Kubernetes manifests,
- CI definitions,
- tests,
- fixtures,
- existing policy/config.

The static graph answers:

```text
What tools exist?
What resources might they touch?
What versions are available?
What changed in this PR?
Which workflows can this code affect?
Which verification surfaces exist?
```

It should not become a general source-code intelligence platform.

---

# 10. Live Operational Graph

The runtime graph is the core.

Nodes:

```text
Actor
Agent
Session
Work
Action
Resource
ResourceVersion
Observation
Effect
Outcome
Approval
Incident
Repository
PR
Commit
Deployment
CostRecord
```

Important edges:

```text
agent          OWNS             session
actor          EXECUTES         work
work           PARENT_OF        work
work           DEPENDS_ON       work
work           DUPLICATES       work
work           SUPERSEDES       work

work           OBSERVED         resource@version
work           AFFECTS          resource

action         BELONGS_TO       work
action         READS            resource
action         WRITES           resource
action         CREATES          resource
action         DELETES          resource
action         IMPACTS          resource

action         CONFLICTS_WITH   action
action         DUPLICATES       action
action         DEPENDS_ON       action

action         PRODUCED         effect
effect         AFFECTS          resource
effect         VERIFIED_BY      verifier
effect         RESULTED_IN      outcome

incident       INVOLVES         action
incident       INVOLVES         resource

PR             CHANGED          tool/code/resource mapping
commit         INTRODUCED       behavior mapping

work           INCURRED_COST    cost
action         INCURRED_COST    cost
outcome        ATTRIBUTED_COST  cost
```

Initial storage remains relational PostgreSQL. A graph database is not required until real query workloads prove it necessary.

---

# 11. Ingestion Architecture

## 11.1 Supported ingestion modes

Priority:

1. MCP gateway,
2. Go / TypeScript / Python SDK,
3. generic HTTP middleware,
4. OpenTelemetry,
5. Temporal adapter,
6. Restate adapter,
7. A2A adapter,
8. webhook/event ingestion,
9. GitHub webhooks/API,
10. local Git watcher/import,
11. generic JSON/JSONL trace import,
12. custom adapter SDK.

## 11.2 Ingestion events

Canonical event envelope:

```json
{
  "event_id": "evt_123",
  "tenant_id": "t_1",
  "type": "ACTION_PROPOSED",
  "source": "mcp",
  "occurred_at": "2026-08-28T12:00:00Z",
  "actor": {
    "id": "agent_support_1",
    "type": "agent"
  },
  "session_id": "sess_9",
  "work_id": "work_77",
  "payload": {}
}
```

All ingestion must be idempotent by `event_id` or source-specific dedupe identity.

## 11.3 Event ordering

Do not assume global event order.

Use:

- source timestamps,
- server receipt timestamps,
- monotonic sequence numbers where available,
- workflow sequence IDs,
- Git SHAs,
- vendor event IDs,
- resource versions.

Hard correctness decisions must depend on strongly consistent current control state, not arbitrary telemetry ordering.

---

# 12. MCP Gateway

Topology:

```text
Agent
  ↓
Lumni MCP Gateway
  ↓
Existing MCP Server
```

Gateway responsibilities:

1. identify tenant,
2. identify actor/agent/session/work,
3. capture tool schema,
4. normalize tool name and operation,
5. resolve affected resources,
6. capture relevant observed versions,
7. produce ActionIntent,
8. call coordination kernel,
9. in shadow mode, always forward,
10. in enforcement mode, enforce eligible deterministic decision,
11. capture response,
12. record result,
13. optionally verify outcome,
14. emit telemetry.

The MCP gateway must support:

- stdio proxy,
- HTTP/SSE transports where required,
- stream-safe forwarding,
- low-latency admission,
- transparent passthrough,
- payload redaction,
- selective tool interception,
- fail-open/fail-closed policies by risk.

---

# 13. SDKs

## 13.1 Go SDK

Primary SDK.

Core functions:

```go
work := lumni.StartWork(ctx, lumni.WorkInput{...})

obs := lumni.Observe(
    ctx,
    work.ID,
    lumni.Resource("subscription", subID),
    lumni.Version("18"),
)

decision, err := lumni.CheckAction(ctx, lumni.ActionInput{
    WorkID: work.ID,
    Operation: "subscription.cancel",
    Resources: []lumni.ResourceIntent{
        lumni.Write("subscription", subID),
    },
    IdempotencyKey: "cancel:" + subID,
})

err = lumni.ReportResult(ctx, decision.ActionID, result)
```

## 13.2 TypeScript SDK

Needed for Node/TS agent ecosystems.

## 13.3 Python SDK

Needed for Python agent ecosystems.

SDKs should remain thin clients around the open protocol.

---

# 14. OpenTelemetry Integration

Lumni should ingest OTel traces without becoming an observability platform.

Use OTel to derive:

- trace/run/session correlation,
- tool spans,
- model usage,
- retry behavior,
- external service calls,
- latency,
- cost metadata,
- error outcomes,
- resource attributes.

Recommended semantic attributes:

```text
lumni.actor.id
lumni.agent.id
lumni.session.id
lumni.work.id
lumni.action.id
lumni.operation
lumni.resource.id
lumni.resource.type
lumni.resource.mode
lumni.observed.version
lumni.idempotency.key
lumni.effect.key
lumni.outcome.status
```

Prefer alignment with existing OpenTelemetry GenAI semantic conventions when applicable rather than duplicating them.

---

# 15. Git / GitHub / PR Enrichment

Repository integration is optional but valuable.

## 15.1 Git inputs

Track:

```text
repository
branch
commit
file
directory
module/package
symbol where useful
migration
test
```

Versions:

```text
repository   = commit SHA
branch       = HEAD SHA
file         = blob SHA
PR           = head SHA
```

## 15.2 GitHub webhook inputs

- push,
- pull_request opened,
- pull_request synchronized,
- pull_request merged,
- pull_request closed,
- issue created,
- issue closed,
- issue assigned,
- review submitted,
- check completed,
- deployment events.

## 15.3 GitHub enrichment

Read API state as needed for:

- PR head/base SHA,
- changed files,
- linked issue,
- branch state,
- merge status,
- checks,
- authorship,
- deployment references.

## 15.4 Use cases

Example:

```text
PR adds retry to refund_payment
+
no idempotency mapping detected
→ correctness-risk finding
```

or:

```text
PR changes workflow from one external write
to Stripe + CRM + Zendesk
→ new multi-resource / partial-effect risk
```

or:

```text
production duplicate refund incident
→ map action implementation to PR/commit
→ incident RCA evidence
```

Repo data enriches understanding but is never required for basic runtime coordination.

---

# 16. Action Normalization

Different representations should normalize to stable operations.

Examples:

```text
stripe.subscriptions.cancel
cancel_subscription
DELETE /subscriptions/{id}
mcp://billing/cancel_subscription
```

may normalize to:

```text
subscription.cancel
```

Normalizer responsibilities:

- canonical operation name,
- actor identity,
- agent identity,
- work/session identity,
- transport/source,
- risk classification,
- arguments fingerprint,
- tenant,
- action ID,
- relevant cost correlation.

Normalization must be deterministic where possible.

---

# 17. Resource Effect Resolution

Given:

```text
operation: subscription.cancel
args:
  subscription_id: sub_91
```

resolve:

```text
WRITE subscription:sub_91
```

Potential enrichment:

```text
WRITE subscription:sub_91
IMPACT customer:cus_81
```

Resolution precedence:

1. explicit registered rule,
2. explicit SDK metadata,
3. protocol schema,
4. OpenAPI/schema parser,
5. known adapter,
6. static code analysis,
7. historical structural inference,
8. semantic inference.

Hard enforcement should normally stop at deterministic/confirmed levels.

Example registration:

```go
resolver.RegisterTool(
    "cancel_subscription",
    resolver.Rule{
        Operation: "subscription.cancel",
        Resources: []resolver.ResourceRule{
            resolver.Write("subscription", resolver.Arg("subscription_id")),
        },
    },
)
```

---

# 18. Version Manager

Supported version sources:

- integer version,
- `updated_at`,
- ETag,
- database version column,
- MVCC/xmin where safe,
- event sequence,
- Git commit SHA,
- Git blob SHA,
- PR head SHA,
- Kubernetes `resourceVersion`,
- Terraform state serial/resource version,
- custom resolver,
- runtime logical version.

A version is opaque within its source.

Do not compare unrelated version formats.

```go
type ResourceVersion struct {
    ResourceID  string
    Version     string
    Source      string
    ObservedAt  time.Time
    Confidence  float64
}
```

If no reliable version exists:

```text
version_confidence = unavailable
```

Lumni must not claim stale-state protection for that resource.

---

# 19. Coordination Kernel

The coordination kernel is the deterministic core.

Responsibilities:

- exact duplicate detection,
- idempotency,
- stale observation validation,
- active conflict detection,
- resource reservation,
- dependency enforcement,
- deterministic supersession,
- explicit policy,
- approval requirement,
- decision persistence,
- audit receipt creation.

Critical-path requirements:

- no LLM dependency,
- bounded resource set,
- strongly consistent storage,
- bounded DB transaction,
- deterministic output for identical state,
- stable reason codes,
- algorithm version in receipt,
- safe retries,
- tenant isolation.

Pseudo-flow:

```text
receive ActionIntent

→ normalize

→ resolve resources

→ resolve exact effect identity

→ exact completed duplicate?
    yes → DEDUPLICATE / RETURN_PREVIOUS_RESULT

→ exact active duplicate?
    yes → JOIN / WAIT

→ validate observed versions
    stale → REPLAN

→ explicit dependency incomplete?
    yes → WAIT

→ deterministic supersession?
    yes → CANCEL_WORK / REPLAN

→ load active resource leases/intents

→ incompatible mutation?
    yes → WAIT

→ policy requires approval?
    yes → REQUIRE_APPROVAL

→ atomically acquire required write leases
    failed → WAIT

→ persist decision + receipt

→ ALLOW
```

---

# 20. Conflict Engine

Base access compatibility:

| Existing | Proposed | Default |
|---|---|---|
| READ | READ | ALLOW |
| READ | WRITE | ALLOW_WITH_VERSION_CHECK |
| WRITE | READ | ALLOW_WITH_VERSION_CHECK |
| WRITE | WRITE | WAIT |
| DELETE | WRITE | WAIT |
| WRITE | DELETE | WAIT |
| DELETE | DELETE | DEDUPLICATE or WAIT |
| CREATE same identity | CREATE | DEDUPLICATE / WAIT |

Domain rules may refine semantics:

```text
CANCEL × UPGRADE       = conflict
REFUND × REFUND        = duplicate
DEPLOY × ROLLBACK      = conflict
CREATE_PR × CREATE_PR  = duplicate if same target goal
ADD_TAG × ADD_TAG      = commutative
INCREMENT × INCREMENT  = adapter-defined
```

Reason codes must be structured, not arbitrary prose.

---

# 21. Idempotency Engine

Exact identity should use:

```text
tenant
+
canonical operation
+
canonical resource set
+
caller idempotency key when available
+
domain effect key when available
```

Example:

```text
refund:payment:pi_72:amount:10000:currency:USD
```

States:

```text
ACTIVE
COMPLETED
FAILED_RETRYABLE
FAILED_FINAL
UNKNOWN_EFFECT
```

Behavior:

```text
ACTIVE duplicate
→ JOIN or WAIT

COMPLETED duplicate
→ DEDUPLICATE and optionally return previous result

FAILED_RETRYABLE
→ allow based on retry policy

UNKNOWN_EFFECT
→ VERIFY / RECONCILE before retry
```

Semantic similarity must never replace exact idempotency.

---

# 22. Multi-Resource Lease Manager

Example action:

```text
WRITE subscription:sub_91
WRITE invoice:in_82
READ customer:cus_81
```

Properties:

- all-or-nothing write reservation,
- deterministic resource order,
- lease TTL,
- server-side expiration,
- heartbeat,
- fencing token,
- no permanent locks,
- atomic decision persistence.

Initial implementation uses PostgreSQL transactions.

Pseudo-SQL:

```sql
BEGIN;

SELECT id
FROM resources
WHERE tenant_id = $1
  AND id = ANY($resource_ids)
ORDER BY id
FOR UPDATE;

SELECT *
FROM leases
WHERE tenant_id = $1
  AND resource_id = ANY($resource_ids)
  AND expires_at > clock_timestamp();

-- validate compatibility

INSERT INTO leases (...);
INSERT INTO decisions (...);

COMMIT;
```

Never rely on client clocks.

Fencing token should monotonically increase per resource or reservation domain.

---

# 23. Work Coordination

Action-level correctness is not enough.

Lumni should also maintain work-level semantics.

## 23.1 Exact duplicate work

Examples:

```text
same external task ID
same issue ID
same order/refund request
same workflow dedupe key
same explicit business idempotency key
```

May deterministically produce:

```text
JOIN
DEDUPLICATE
```

## 23.2 Semantic duplicate work

Pipeline:

```text
compact work representation
      ↓
embedding
      ↓
pgvector top-k active/recent work
      ↓
deterministic filters
      ↓
optional classifier
      ↓
likely duplicate recommendation
```

Input document:

```text
Goal
referenced business entity IDs
resource footprint
repository
issue / ticket
recent actions
```

Output:

```json
{
  "same_goal": true,
  "confidence": 0.91,
  "reason_code": "LIKELY_SAME_GOAL"
}
```

Initial behavior:

```text
WARN_LIKELY_DUPLICATE
```

not hard block.

## 23.3 Supersession

Deterministic examples:

- issue already closed by merged PR,
- requested cancellation recorded after older upgrade work,
- resource already reaches target state,
- newer explicit work supersedes older work ID.

Semantic supersession is advisory.

## 23.4 Work joining

Later:

```text
Work B determined exact duplicate of active Work A
→ JOIN Work A
→ share completion/result
```

The runtime adapter determines how waiting/joining is performed.

---

# 24. Dependencies

Initial dependencies should be explicit:

```text
work B depends_on work A
action B depends_on action A
```

Sources:

- SDK declaration,
- runtime workflow relationship,
- explicit application metadata,
- deterministic output/input binding,
- known adapter rule.

Semantic dependency inference should not be in the hard path.

Dependency statuses:

```text
PENDING
SATISFIED
FAILED
CANCELLED
SUPERSEDED
```

---

# 25. Human Approval

Policies may return:

```text
REQUIRE_APPROVAL
```

Example:

```text
refund > $5,000
→ REQUIRE_APPROVAL
```

Lumni should:

1. persist approval request,
2. release or retain resource reservation according to policy,
3. emit webhook/event,
4. allow external approval channel,
5. record approver identity,
6. revalidate stale state before execution after approval,
7. produce a new decision receipt if state changed.

Approval does not freeze the world.

After long approval delays, observations must be revalidated.

---

# 26. Outcome Verification

Purpose:

> distinguish tool success from business-effect success.

Verifier types:

- API read-back,
- database query,
- event/webhook observation,
- custom assertion,
- tool-specific verifier,
- composite state assertion,
- customer-provided callback.

Example:

```text
Action:
refund payment:pi_91

Tool:
HTTP 200

Verifier:
refund re_882 exists
status = succeeded

→ VERIFIED
```

Failure:

```text
→ EFFECT_UNVERIFIED
```

Verification may be:

```text
SYNC
ASYNC
EVENTUAL
MANUAL
```

```go
type Verifier struct {
    ID           string
    TenantID     string
    Operation    string
    Type         VerifierType
    Timeout      time.Duration
    RetryPolicy  RetryPolicy
    Config       json.RawMessage
}
```

---

# 27. Partial Effects and Reconciliation

Multi-step work may partially commit.

Lumni should record every effect separately.

Example:

```text
effect_1 refund       COMMITTED
effect_2 CRM update   FAILED
effect_3 close ticket COMMITTED
```

Work-level state:

```text
PARTIALLY_COMMITTED
```

Lumni initially does not invent compensations.

Developers may register:

- retryable effects,
- non-reversible effects,
- compensators,
- reconciliation handlers.

Example metadata:

```json
{
  "effect": "crm.update",
  "retryable": true,
  "compensator": null
}
```

Integration with Temporal/Restate may execute compensation/reconciliation.

---

# 28. Temporal Integration

Temporal remains the durable execution substrate.

Lumni adapter should support:

- workflow/run identity,
- activity identity,
- work correlation,
- action intents before consequential activities,
- WAIT as durable sleep/retry signal,
- REPLAN as application signal,
- approval state,
- result reporting,
- cost attribution.

Possible pattern:

```text
Temporal Workflow
      │
      ▼
Lumni CheckAction
      │
      ├── ALLOW → execute activity
      ├── WAIT → durable timer / signal
      ├── REPLAN → branch to re-read/replan
      └── REQUIRE_APPROVAL → await signal
```

Lumni must not duplicate Temporal history/replay semantics.

---

# 29. Restate Integration

Restate remains responsible for durable functions, keyed state, messaging, and execution.

Lumni adds:

- external resource versions,
- cross-runtime resource sets,
- cross-object conflict semantics,
- shared-resource work graph,
- effect identity,
- verified outcomes,
- cross-framework correlation.

Pattern:

```text
Lumni says WAIT
→ Restate owns durable suspension/wakeup

Lumni says REPLAN
→ application re-reads resource state

Lumni says COMPENSATION_REQUIRED
→ Restate durable handler executes configured reconciliation
```

---

# 30. Other Runtime Integrations

Adapters should be possible for:

- LangGraph,
- OpenAI Agents SDK,
- Claude tooling / MCP,
- CrewAI,
- custom Python,
- custom TypeScript,
- Go services,
- A2A,
- workflow products,
- HTTP services.

Adapter interface:

```go
type RuntimeAdapter interface {
    IdentifyActor(ctx context.Context, raw RawEvent) (ActorRef, error)
    IdentifySession(ctx context.Context, raw RawEvent) (*SessionRef, error)
    IdentifyWork(ctx context.Context, raw RawEvent) (*WorkRef, error)
    NormalizeAction(ctx context.Context, raw RawEvent) (ActionIntent, error)
    ApplyDecision(ctx context.Context, d Decision) error
}
```

---

# 31. Policy Engine

Policy is an input, not the core product.

Initial implementation:

- simple typed Go rules,
- optional OPA/Rego adapter.

Examples:

```text
production mutation + unresolved resource
→ WARN or REJECT

refund amount > threshold
→ REQUIRE_APPROVAL

critical action + coordinator unavailable
→ fail closed

low-risk read + coordinator unavailable
→ fail open
```

Do not invent a custom policy language early.

---

# 32. Semantic Layer

The semantic layer is asynchronous and advisory.

Uses:

- duplicate goal detection,
- likely supersession,
- inferred resource relationships,
- tool mapping suggestions,
- workflow summaries,
- incident explanation,
- candidate conflict-rule suggestions,
- anomaly grouping.

Architecture:

```text
new / updated work
      ↓
compact representation
      ↓
embedding
      ↓
pgvector candidates
      ↓
deterministic filters
      ↓
small classifier / LLM
      ↓
recommendation
```

Never:

```text
LLM says conflict
→ hard block
```

without deterministic evidence or explicit policy.

Primary backend can call model APIs directly from Go.

Python is optional for offline experimentation/training only and should not be required for runtime correctness.

---

# 33. FinOps

FinOps should be tied to **useful autonomous work**, not only model usage.

## 33.1 Cost ingestion

Sources:

- provider usage metadata,
- OTel GenAI attributes,
- runtime callbacks,
- customer pricing tables,
- custom compute costs,
- browser/action costs,
- external tool costs.

## 33.2 Attribution hierarchy

```text
provider usage
    ↓
session
    ↓
work
    ↓
action
    ↓
effect
    ↓
verified outcome
```

## 33.3 Metrics

```text
total agent spend
cost per work item
cost per completed work
cost per verified outcome
cost per effect
cost by agent
cost by model
cost by workflow
cost by resource type
cost by customer/account
```

## 33.4 Waste categories

```text
duplicate_work_cost
duplicate_action_cost
stale_work_cost
superseded_work_cost
failed_effect_cost
unverified_outcome_cost
retry_after_commit_cost
conflict_wait_cost
unnecessary_reasoning_cost
```

## 33.5 Useful Work Ratio

Example:

```text
Useful Work Ratio =
cost attributed to verified useful outcomes
/
total autonomous compute/tool cost
```

Do not present approximate cost attribution as exact if provider/tool data is incomplete.

---

# 34. Event Journal

Append-only event journal:

```text
ACTOR_REGISTERED
AGENT_DISCOVERED
SESSION_STARTED
SESSION_FINISHED

WORK_STARTED
WORK_WAITING
WORK_COMPLETED
WORK_FAILED
WORK_CANCELLED
WORK_SUPERSEDED
WORK_JOINED

OBSERVATION_RECORDED

ACTION_PROPOSED
ACTION_NORMALIZED
RESOURCE_RESOLVED

DECISION_ALLOW
DECISION_WARN
DECISION_WAIT
DECISION_REPLAN
DECISION_DEDUPLICATE
DECISION_REQUIRE_APPROVAL
DECISION_REJECT

LEASE_ACQUIRED
LEASE_HEARTBEAT
LEASE_RELEASED
LEASE_EXPIRED

ACTION_STARTED
ACTION_SUCCEEDED
ACTION_FAILED
ACTION_UNKNOWN

EFFECT_RECORDED
EFFECT_VERIFIED
EFFECT_UNVERIFIED
EFFECT_PARTIAL

RESOURCE_VERSION_UPDATED

APPROVAL_REQUESTED
APPROVAL_GRANTED
APPROVAL_DENIED
APPROVAL_EXPIRED

INCIDENT_OPENED
INCIDENT_RESOLVED

COST_RECORDED
```

Use transactional outbox semantics for publishing internal async events.

---

# 35. Consistency Model

## 35.1 Hard control state

Must be strongly consistent:

- active action status,
- exact idempotency identities,
- current enforcement resource versions,
- active leases,
- fencing tokens,
- decisions,
- approval state used for admission,
- exact duplicate state.

## 35.2 Soft state

May be eventually consistent:

- historical graphs,
- dashboards,
- FinOps aggregates,
- semantic duplicate candidates,
- embeddings,
- recommendations,
- analytics,
- explanations,
- code enrichment.

Never use an eventually consistent semantic store as source of truth for hard admission.

---

# 36. Storage Architecture

Initial stack:

```text
Go
PostgreSQL
pgvector optional
OpenTelemetry
object storage optional for large evidence blobs
```

Do not initially require:

- Kafka,
- Redis,
- Neo4j,
- Elasticsearch,
- distributed SQL,
- separate vector database.

Scale them in only when demonstrated workloads justify them.

---

# 37. PostgreSQL Schema

## 37.1 tenants

```text
id UUID PK
name TEXT
slug TEXT UNIQUE
settings JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

## 37.2 actors

```text
id TEXT
tenant_id UUID
type TEXT
runtime TEXT
display_name TEXT
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, id)
```

## 37.3 agents

```text
id TEXT
tenant_id UUID
actor_id TEXT
name TEXT
framework TEXT
model_provider TEXT
model TEXT
version TEXT
repository_id UUID NULL
deployment_id TEXT NULL
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, id)
```

## 37.4 sessions

```text
id TEXT
tenant_id UUID
agent_id TEXT NULL
actor_id TEXT
runtime TEXT
external_id TEXT NULL
status TEXT
started_at TIMESTAMPTZ
finished_at TIMESTAMPTZ NULL
metadata JSONB

PK (tenant_id, id)
```

## 37.5 repositories

```text
id UUID PK
tenant_id UUID
provider TEXT
external_id TEXT
owner TEXT
name TEXT
default_branch TEXT
url TEXT NULL
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

UNIQUE (tenant_id, provider, external_id)
```

## 37.6 pull_requests

```text
id UUID PK
tenant_id UUID
repository_id UUID
external_number TEXT
state TEXT
head_sha TEXT
base_sha TEXT
title TEXT
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

UNIQUE (tenant_id, repository_id, external_number)
```

## 37.7 work

```text
id TEXT
tenant_id UUID
actor_id TEXT
agent_id TEXT NULL
session_id TEXT NULL
parent_work_id TEXT NULL

goal TEXT
normalized_goal TEXT NULL
goal_embedding VECTOR NULL

status TEXT
priority INT
risk TEXT

repository_id UUID NULL
issue_ref TEXT NULL
pr_ref TEXT NULL
base_commit TEXT NULL

started_at TIMESTAMPTZ
completed_at TIMESTAMPTZ NULL
cancelled_at TIMESTAMPTZ NULL
superseded_at TIMESTAMPTZ NULL

metadata JSONB

PK (tenant_id, id)
```

Indexes:

```text
(tenant_id, status, started_at)
(tenant_id, actor_id, started_at)
(tenant_id, repository_id, status)
pgvector index on goal_embedding when enabled
```

## 37.8 work_edges

```text
tenant_id UUID
source_work_id TEXT
relationship TEXT
target_work_id TEXT
confidence DOUBLE PRECISION
evidence_type TEXT
evidence JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, source_work_id, relationship, target_work_id)
```

Relationships:

```text
DEPENDS_ON
DUPLICATES
SUPERSEDES
JOINED_TO
PARENT_OF
RELATED_TO
```

## 37.9 resources

```text
id TEXT
tenant_id UUID
type TEXT
external_id TEXT
namespace TEXT
parent_resource_id TEXT NULL

current_version TEXT NULL
version_source TEXT NULL
version_confidence DOUBLE PRECISION

metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, id)
```

Indexes:

```text
(tenant_id, type, external_id)
(tenant_id, parent_resource_id)
```

## 37.10 resource_edges

```text
tenant_id UUID
source_resource_id TEXT
relationship TEXT
target_resource_id TEXT
confidence DOUBLE PRECISION
evidence_type TEXT
source TEXT
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, source_resource_id, relationship, target_resource_id)
```

## 37.11 resource_versions

Append-only version history.

```text
id UUID PK
tenant_id UUID
resource_id TEXT
version TEXT
version_source TEXT
observed_at TIMESTAMPTZ
source TEXT
confidence DOUBLE PRECISION
metadata JSONB
```

Index:

```text
(tenant_id, resource_id, observed_at DESC)
```

## 37.12 observations

```text
id UUID PK
tenant_id UUID
work_id TEXT
session_id TEXT NULL
resource_id TEXT
observed_version TEXT
version_source TEXT
source TEXT
confidence DOUBLE PRECISION
evidence_type TEXT
observed_at TIMESTAMPTZ
metadata JSONB
```

Indexes:

```text
(tenant_id, work_id, resource_id, observed_at DESC)
(tenant_id, resource_id, observed_at DESC)
```

## 37.13 actions

```text
id TEXT
tenant_id UUID
work_id TEXT
actor_id TEXT
session_id TEXT NULL

operation TEXT
tool TEXT
source TEXT
status TEXT
risk TEXT

arguments_hash TEXT NULL
arguments JSONB NULL

idempotency_key TEXT NULL
effect_key TEXT NULL

requested_at TIMESTAMPTZ
started_at TIMESTAMPTZ NULL
finished_at TIMESTAMPTZ NULL

metadata JSONB

PK (tenant_id, id)
```

Indexes:

```text
(tenant_id, work_id, requested_at)
(tenant_id, effect_key, requested_at)
(tenant_id, status, requested_at)
```

## 37.14 action_resources

```text
tenant_id UUID
action_id TEXT
resource_id TEXT
mode TEXT
operation TEXT
observed_version TEXT NULL
expected_version TEXT NULL
confidence DOUBLE PRECISION
evidence_type TEXT
evidence JSONB

PK (tenant_id, action_id, resource_id, mode)
```

Indexes:

```text
(tenant_id, resource_id, mode)
```

## 37.15 effects

```text
id TEXT
tenant_id UUID
action_id TEXT
operation TEXT
effect_key TEXT
status TEXT
external_id TEXT NULL
occurred_at TIMESTAMPTZ NULL
metadata JSONB
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ

PK (tenant_id, id)
```

Unique/index:

```text
(tenant_id, effect_key, status)
(tenant_id, action_id)
```

## 37.16 effect_resources

```text
tenant_id UUID
effect_id TEXT
resource_id TEXT
relationship TEXT

PK (tenant_id, effect_id, resource_id, relationship)
```

## 37.17 leases

```text
id UUID PK
tenant_id UUID
resource_id TEXT
action_id TEXT
mode TEXT
fencing_token BIGINT
expires_at TIMESTAMPTZ
heartbeat_at TIMESTAMPTZ
created_at TIMESTAMPTZ
```

Indexes:

```text
(tenant_id, resource_id, expires_at)
(tenant_id, action_id)
```

## 37.18 decisions

```text
id UUID PK
tenant_id UUID
action_id TEXT
decision TEXT
reason_code TEXT
confidence DOUBLE PRECISION
retry_after_ms BIGINT NULL
lease_token TEXT NULL
evidence JSONB
kernel_version TEXT
resolver_version TEXT
policy_version TEXT
created_at TIMESTAMPTZ
```

Index:

```text
(tenant_id, action_id, created_at DESC)
```

## 37.19 action_results

```text
tenant_id UUID
action_id TEXT
status TEXT
external_result_id TEXT NULL
retryable BOOLEAN NULL
resource_versions JSONB
created_resources JSONB
compensation JSONB
result_metadata JSONB
recorded_at TIMESTAMPTZ

PK (tenant_id, action_id)
```

## 37.20 verifiers

```text
id UUID PK
tenant_id UUID
operation TEXT
type TEXT
config JSONB
timeout_ms BIGINT
retry_policy JSONB
enabled BOOLEAN
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

## 37.21 outcomes

```text
id UUID PK
tenant_id UUID
work_id TEXT NULL
action_id TEXT NULL
effect_id TEXT NULL
type TEXT
status TEXT
verifier_id UUID NULL
expected JSONB
observed JSONB
evidence JSONB
verified_at TIMESTAMPTZ NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

## 37.22 approvals

```text
id UUID PK
tenant_id UUID
action_id TEXT
policy_id TEXT NULL
status TEXT
reason_code TEXT
requested_at TIMESTAMPTZ
expires_at TIMESTAMPTZ NULL
resolved_at TIMESTAMPTZ NULL
resolved_by TEXT NULL
metadata JSONB
```

## 37.23 incidents

```text
id UUID PK
tenant_id UUID
type TEXT
severity TEXT
status TEXT
reason_code TEXT
evidence JSONB
detected_at TIMESTAMPTZ
resolved_at TIMESTAMPTZ NULL
metadata JSONB
```

## 37.24 incident_entities

```text
tenant_id UUID
incident_id UUID
entity_type TEXT
entity_id TEXT

PK (tenant_id, incident_id, entity_type, entity_id)
```

## 37.25 costs

```text
id UUID PK
tenant_id UUID
actor_id TEXT NULL
agent_id TEXT NULL
session_id TEXT NULL
work_id TEXT NULL
action_id TEXT NULL

provider TEXT
model TEXT NULL
cost_type TEXT
input_units BIGINT NULL
output_units BIGINT NULL
compute_units DOUBLE PRECISION NULL
amount_micros_usd BIGINT

observed_at TIMESTAMPTZ
metadata JSONB
```

Indexes:

```text
(tenant_id, work_id, observed_at)
(tenant_id, action_id, observed_at)
(tenant_id, agent_id, observed_at)
```

## 37.26 events

```text
id TEXT
tenant_id UUID
type TEXT
entity_type TEXT
entity_id TEXT
source TEXT
occurred_at TIMESTAMPTZ
received_at TIMESTAMPTZ
payload JSONB
created_at TIMESTAMPTZ

PK (tenant_id, id)
```

Partition by time only when scale requires it.

## 37.27 outbox

```text
id BIGSERIAL PK
tenant_id UUID
topic TEXT
key TEXT
payload JSONB
created_at TIMESTAMPTZ
published_at TIMESTAMPTZ NULL
attempts INT
```

---

# 38. API Design

All APIs versioned under `/v1`.

Use JSON over HTTP initially.

gRPC may be added for internal/embedded high-throughput use later.

## 38.1 Tenant/runtime auth

Headers:

```text
Authorization: Bearer ...
X-Lumni-Tenant: ...
Idempotency-Key: ...
```

Service-to-service should support mTLS.

## 38.2 Actors

```http
POST /v1/actors
GET  /v1/actors/{id}
GET  /v1/actors
```

## 38.3 Agents

```http
POST /v1/agents
GET  /v1/agents/{id}
GET  /v1/agents
PATCH /v1/agents/{id}
```

## 38.4 Sessions

```http
POST /v1/sessions
POST /v1/sessions/{id}/complete
GET  /v1/sessions/{id}
```

## 38.5 Work

Create:

```http
POST /v1/work
```

Request:

```json
{
  "id": "work_77",
  "actor_id": "agent_support_5",
  "agent_id": "support-agent",
  "session_id": "sess_81",
  "goal": "Resolve customer refund request",
  "priority": 50,
  "risk": "high",
  "context": {
    "repository_id": null,
    "issue_ref": "zendesk:827"
  }
}
```

Other endpoints:

```http
GET  /v1/work/{id}
GET  /v1/work
POST /v1/work/{id}/complete
POST /v1/work/{id}/fail
POST /v1/work/{id}/cancel
POST /v1/work/{id}/dependencies
GET  /v1/work/{id}/graph
```

## 38.6 Observations

```http
POST /v1/observations
```

Request:

```json
{
  "work_id": "work_77",
  "session_id": "sess_81",
  "resource": {
    "type": "subscription",
    "id": "subscription:sub_91"
  },
  "version": "18",
  "version_source": "updated_at"
}
```

Bulk:

```http
POST /v1/observations/batch
```

## 38.7 Action check

```http
POST /v1/actions/check
```

Request:

```json
{
  "work_id": "work_77",
  "actor_id": "agent_5",
  "session_id": "sess_81",
  "operation": "subscription.cancel",
  "tool": "cancel_subscription",
  "resources": [
    {
      "type": "subscription",
      "id": "subscription:sub_91",
      "mode": "WRITE",
      "observed_version": "18"
    }
  ],
  "idempotency_key": "cancel:sub_91",
  "risk": "high"
}
```

ALLOW:

```json
{
  "action_id": "act_123",
  "decision": "ALLOW",
  "reason_code": "NO_CONFLICT",
  "lease_token": "lease_abc",
  "expires_at": "2026-08-28T12:00:30Z"
}
```

REPLAN:

```json
{
  "action_id": "act_123",
  "decision": "REPLAN",
  "reason_code": "STALE_OBSERVATION",
  "evidence": [
    {
      "resource_id": "subscription:sub_91",
      "observed_version": "18",
      "current_version": "19"
    }
  ]
}
```

WAIT:

```json
{
  "action_id": "act_123",
  "decision": "WAIT",
  "reason_code": "CONFLICTING_ACTIVE_MUTATION",
  "retry_after_ms": 5000,
  "conflicting_action_ids": ["act_119"]
}
```

DEDUPLICATE:

```json
{
  "action_id": "act_123",
  "decision": "DEDUPLICATE",
  "reason_code": "EXACT_EFFECT_ALREADY_COMMITTED",
  "existing_action_id": "act_99",
  "existing_result_id": "re_812"
}
```

Approval:

```json
{
  "action_id": "act_123",
  "decision": "REQUIRE_APPROVAL",
  "reason_code": "HIGH_VALUE_REFUND",
  "approval_id": "apr_81"
}
```

## 38.8 Record observed action

Shadow integrations that cannot gate inline:

```http
POST /v1/actions/observe
```

## 38.9 Report action start

```http
POST /v1/actions/{id}/start
```

## 38.10 Heartbeat

```http
POST /v1/actions/{id}/heartbeat
```

## 38.11 Result

```http
POST /v1/actions/{id}/result
```

Request:

```json
{
  "status": "succeeded",
  "external_result_id": "re_882",
  "resource_versions": {
    "subscription:sub_91": "19"
  },
  "retryable": false
}
```

## 38.12 Verification

```http
POST /v1/actions/{id}/verify
GET  /v1/actions/{id}/verification
```

## 38.13 Action inspection

```http
GET /v1/actions/{id}
GET /v1/actions
```

## 38.14 Approvals

```http
GET  /v1/approvals
GET  /v1/approvals/{id}
POST /v1/approvals/{id}/approve
POST /v1/approvals/{id}/deny
```

Approval resolution must trigger state revalidation.

## 38.15 Resources

```http
GET  /v1/resources/{id}
GET  /v1/resources
GET  /v1/resources/{id}/activity
GET  /v1/resources/{id}/graph
POST /v1/resources/resolve
POST /v1/resources/{id}/version
```

## 38.16 Graph

```http
GET /v1/graph/work/{id}
GET /v1/graph/resource/{id}
GET /v1/graph/live
```

Parameters:

```text
depth
relationship_types
active_only
since
until
```

## 38.17 Findings/incidents

```http
GET  /v1/incidents
GET  /v1/incidents/{id}
POST /v1/incidents/{id}/resolve
```

## 38.18 FinOps

```http
POST /v1/costs
GET  /v1/costs
GET  /v1/finops/summary
GET  /v1/finops/work/{id}
GET  /v1/finops/agents
GET  /v1/finops/waste
```

Example summary:

```json
{
  "period": "7d",
  "total_cost_usd": 23810.12,
  "verified_outcomes": 31229,
  "cost_per_verified_outcome_usd": 0.7624,
  "waste": {
    "duplicate_work_usd": 1921.20,
    "superseded_work_usd": 1204.10,
    "stale_work_usd": 612.80,
    "failed_effect_usd": 491.30,
    "retry_after_commit_usd": 282.10
  }
}
```

## 38.19 Repository/GitHub

```http
POST /v1/repositories
GET  /v1/repositories/{id}
POST /v1/repositories/{id}/analyze
POST /v1/webhooks/github
GET  /v1/repositories/{id}/pull-requests
GET  /v1/repositories/{id}/changes/{sha}
```

## 38.20 Configuration

```http
GET  /v1/config/resource-rules
POST /v1/config/resource-rules
GET  /v1/config/conflict-rules
POST /v1/config/conflict-rules
GET  /v1/config/verifiers
POST /v1/config/verifiers
```

---

# 39. API Error Model

Canonical error:

```json
{
  "error": {
    "code": "STALE_OBSERVATION",
    "message": "Observed resource version is no longer current.",
    "request_id": "req_123",
    "details": {}
  }
}
```

Stable machine codes are required.

HTTP examples:

```text
400 malformed input
401 unauthenticated
403 unauthorized
404 entity missing
409 coordination conflict / stale precondition where appropriate
422 unsupported mapping
429 admission/rate limit
500 internal
503 coordinator unavailable
```

Action decisions themselves should normally return `200` with explicit decision payload rather than misuse HTTP errors.

---

# 40. Reason Codes

Initial deterministic reason codes:

```text
NO_CONFLICT

EXACT_DUPLICATE_ACTIVE
EXACT_EFFECT_ALREADY_COMMITTED
IDEMPOTENCY_KEY_REUSED_DIFFERENT_PAYLOAD

STALE_OBSERVATION
VERSION_UNAVAILABLE
VERSION_SOURCE_CHANGED

CONFLICTING_ACTIVE_MUTATION
WRITE_WRITE_CONFLICT
DELETE_WRITE_CONFLICT
DOMAIN_OPERATION_CONFLICT

DEPENDENCY_PENDING
DEPENDENCY_FAILED

WORK_ALREADY_COMPLETED
WORK_SUPERSEDED
EXACT_DUPLICATE_WORK

APPROVAL_REQUIRED
APPROVAL_PENDING
APPROVAL_DENIED
APPROVAL_EXPIRED

RESOURCE_UNRESOLVED
RESOURCE_MAPPING_LOW_CONFIDENCE

LEASE_BUSY
LEASE_EXPIRED
FENCING_TOKEN_INVALID

ACTION_RESULT_UNKNOWN
EFFECT_UNVERIFIED
OUTCOME_MISMATCH
PARTIAL_COMMIT

COORDINATOR_UNAVAILABLE
POLICY_REJECTED
```

Semantic/advisory:

```text
LIKELY_DUPLICATE_WORK
LIKELY_SUPERSEDED_WORK
LIKELY_RESOURCE_RELATIONSHIP
LIKELY_CONFLICT
```

---

# 41. Shadow Mode

Every production customer should begin in shadow mode.

Behavior:

- never blocks,
- computes all deterministic decisions,
- records what would have happened,
- correlates later outcomes,
- measures false positives,
- measures potential prevented waste,
- generates decision receipts.

Example:

```text
Last 7 days

91,220 actions

42 exact duplicate effects
18 stale observations
11 conflicting writes
6 unknown-effect retries
9 duplicate work items

Potentially avoidable cost: $482.11

No actions were blocked.
```

This is the primary trust-building path.

---

# 42. Enforcement Rollout

Recommended sequence:

```text
0. observe only
1. notify
2. exact duplicate-effect suppression
3. stale-version protection
4. exact same-resource write conflicts
5. explicit domain conflict rules
6. explicit dependencies
7. approval enforcement
8. verified effects
9. reconciliation hooks
10. exact duplicate-work joining
11. semantic recommendations remain advisory
```

Do not enable semantic hard blocks until independently validated and explicitly opted in.

---

# 43. Failure Handling

## 43.1 Coordinator unavailable

Policy by risk:

```text
LOW       fail open
MEDIUM    configurable
HIGH      fail closed / approval
CRITICAL  fail closed
```

Customer must control this policy.

## 43.2 Agent crashes

Lease expires.

Waiting work may re-evaluate after expiry.

## 43.3 Gateway crashes after external effect

Use:

- idempotency key,
- effect key,
- action journal,
- reconciliation,
- verifier.

Never blindly replay.

## 43.4 Missing result

Action:

```text
UNKNOWN_EFFECT
```

Then verify/reconcile before retry.

## 43.5 Lost heartbeat

Lease expires after TTL.

Fencing token protects against stale holder resuming.

## 43.6 Database unavailable

Shadow mode:

```text
pass through + record local degraded metric where possible
```

Enforcement:

apply configured fail-open/fail-closed policy.

## 43.7 Semantic service unavailable

No effect on safety kernel.

Semantic recommendations are skipped.

## 43.8 GitHub unavailable

Runtime coordination continues.

Static enrichment becomes stale/unavailable.

---

# 44. Concurrency and Transactions

Use PostgreSQL `READ COMMITTED` with explicit `SELECT ... FOR UPDATE` for bounded resource rows initially.

For operations requiring stronger invariants, use transaction-local locking and unique constraints.

Do not use application mutexes as distributed correctness primitives.

Important invariants:

```text
two incompatible active write leases cannot coexist
same exact committed effect cannot be committed twice through Lumni enforcement
decision receipt is persisted atomically with lease acquisition
fencing token is monotonic
event ingestion is idempotent
```

Use PostgreSQL advisory locks only where clearly justified; prefer row locks and constraints for explainability.

---

# 45. Performance Targets

Initial production goals:

```text
action check p50    < 10 ms
action check p95    < 30 ms
action check p99    < 75 ms

result record p95   < 50 ms
observation p95     < 50 ms
```

Assumes local-region PostgreSQL.

Hard path:

```text
normalize
resolve bounded resources
DB transaction
decision
```

No LLM.

No vector search.

No network call to Lumni Cloud if running customer-local enforcement.

---

# 46. Scaling Strategy

## Phase 1

```text
modular Go monolith
Postgres
optional pgvector
single region
```

## Phase 2

Separate stateless gateway replicas from control service.

## Phase 3

Scale async workers independently.

## Phase 4

Partition event/history workloads.

## Phase 5

Only if necessary:

- dedicated analytics store,
- streaming bus,
- dedicated graph system,
- multi-region coordination,
- customer-local regional cells.

Hard coordination state should remain localized wherever possible.

Cross-region global locking is expensive and should not be introduced without a proven requirement.

---

# 47. Security Model

The service may sit in front of sensitive production actions.

Requirements:

- customer-controlled deployment,
- no mandatory cloud data path,
- mTLS between control components,
- TLS externally,
- encryption at rest,
- tenant isolation,
- scoped API keys,
- workload identity where available,
- RBAC,
- payload minimization,
- secret masking,
- field-level redaction,
- configurable raw-payload retention,
- ability to store only IDs/hashes/metadata,
- audit logging,
- immutable decision receipts,
- configurable regional residency,
- connector least privilege,
- verifier read-only credentials wherever possible.

Never require production secrets in Lumni Cloud for self-hosted enforcement.

---

# 48. Multi-Tenancy

Every stateful table must include tenant scope.

All repository interfaces require tenant context.

Never query a resource by external ID alone.

Canonical interface:

```go
type TenantContext struct {
    TenantID string
    ActorID  *string
}
```

Database access must include tenant predicates.

Enterprise deployment may optionally use dedicated databases or schemas.

---

# 49. Privacy and Payload Strategy

Store only what is required.

Preferred hierarchy:

```text
resource IDs
hashes
versions
operation
reason codes
small evidence
```

Arguments/results may be:

- dropped,
- redacted,
- hashed,
- field-selected,
- encrypted,
- retained for limited TTL.

Semantic analysis should operate on minimized representations.

---

# 50. Observability of Lumni Itself

Metrics:

```text
lumni_actions_total
lumni_actions_checked_total
lumni_decisions_total
lumni_decision_latency_ms
lumni_conflicts_total
lumni_stale_observations_total
lumni_exact_duplicates_total
lumni_duplicate_work_candidates_total
lumni_leases_active
lumni_lease_wait_ms
lumni_unknown_effects_total
lumni_verification_failures_total
lumni_partial_effects_total
lumni_approvals_total
lumni_resource_resolution_failures_total
lumni_events_ingested_total
lumni_event_deduplications_total
lumni_cost_records_total
lumni_waste_cost_usd
```

Tracing:

```text
work
  ↓
ActionIntent
  ↓
resource resolution
  ↓
decision
  ↓
lease
  ↓
external action
  ↓
result
  ↓
verification
```

Logs must include:

- request ID,
- tenant ID,
- action ID,
- work ID,
- reason code,
- algorithm version.

Never log raw secrets.

---

# 51. Decision Receipts

Every decision must be explainable.

Example:

```json
{
  "decision": "REPLAN",
  "reason_code": "STALE_OBSERVATION",
  "action": "subscription.upgrade",
  "resource": "subscription:sub_91",
  "observed_version": "18",
  "current_version": "19",
  "evidence_type": "EXPLICIT",
  "confidence": 1.0,
  "kernel_version": "coord-v1.4.0"
}
```

WAIT:

```json
{
  "decision": "WAIT",
  "reason_code": "DOMAIN_OPERATION_CONFLICT",
  "proposed_action": "deployment.rollback",
  "existing_action": "deployment.deploy",
  "resource": "k8s/prod/deployment/payments",
  "rule": "DEPLOY_X_ROLLBACK",
  "confidence": 1.0
}
```

---

# 52. Algorithm Versioning

Version at minimum:

```text
action_normalizer_version
resource_resolver_version
version_manager_version
conflict_engine_version
idempotency_engine_version
work_duplicate_model_version
supersession_model_version
policy_engine_version
coordination_kernel_version
verifier_version
cost_attribution_version
```

Required for:

- replay,
- customer debugging,
- incident RCA,
- gradual rollout,
- shadow comparison,
- regression testing.

---

# 53. Replay

Every incident should be replayable at the decision level.

Replay input:

- action intent,
- observations,
- resource snapshot,
- active actions,
- leases,
- policies,
- mappings,
- algorithm versions.

Replay should answer:

```text
Given the same state, what decision would kernel version X produce?
```

This is not full external-world workflow replay; Temporal/Restate may provide workflow replay.

---

# 54. Testing Strategy

## 54.1 Unit tests

- resource normalization,
- version comparison,
- compatibility matrix,
- idempotency,
- decision precedence,
- policy,
- cost attribution.

## 54.2 Property tests

Examples:

```text
two incompatible write leases cannot both be active

same event processed twice produces same control state

completed exact effect + same effect key cannot be re-admitted

stale fencing token cannot mutate active lease state
```

Use Go property-testing/fuzzing.

## 54.3 Integration tests

Use ephemeral PostgreSQL via containers.

Scenarios:

- duplicate refund,
- timeout after commit,
- stale subscription,
- concurrent cancel/upgrade,
- deploy/rollback,
- multi-resource lease,
- approval then stale state,
- unknown effect reconciliation,
- partial effect,
- webhook reordering,
- event duplicate.

## 54.4 Git integration tests

Temporary repositories:

- concurrent same-file changes,
- PR merged while work active,
- branch advances,
- force push,
- migration collision,
- issue closed while duplicate work active.

## 54.5 Adapter contract tests

Every adapter must pass a canonical suite.

## 54.6 Replay fixtures

Every production bug should become a test fixture when safely reproducible.

---

# 55. Go Project Structure

Use a modular monolith first.

```text
lumni/
├── cmd/
│   ├── lumnid/                    # main control/API service
│   │   └── main.go
│   ├── gateway/                   # MCP/HTTP gateway
│   │   └── main.go
│   ├── worker/                    # async jobs
│   │   └── main.go
│   └── lumni/                     # CLI
│       └── main.go
│
├── internal/
│   ├── actor/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── agent/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── session/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── work/
│   │   ├── model.go
│   │   ├── service.go
│   │   ├── lifecycle.go
│   │   ├── duplicate.go
│   │   ├── supersession.go
│   │   ├── dependency.go
│   │   └── repository.go
│   ├── action/
│   │   ├── model.go
│   │   ├── normalize.go
│   │   ├── fingerprint.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── resource/
│   │   ├── model.go
│   │   ├── identity.go
│   │   ├── resolver.go
│   │   ├── registry.go
│   │   ├── version.go
│   │   ├── relationships.go
│   │   └── repository.go
│   ├── observation/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── effect/
│   │   ├── model.go
│   │   ├── identity.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── conflict/
│   │   ├── engine.go
│   │   ├── compatibility.go
│   │   ├── overlap.go
│   │   ├── domain_rules.go
│   │   └── evidence.go
│   ├── stale/
│   │   ├── detector.go
│   │   ├── versions.go
│   │   ├── git.go
│   │   └── evidence.go
│   ├── idempotency/
│   │   ├── engine.go
│   │   ├── key.go
│   │   └── repository.go
│   ├── lease/
│   │   ├── model.go
│   │   ├── manager.go
│   │   ├── fencing.go
│   │   └── repository.go
│   ├── dependency/
│   │   ├── model.go
│   │   ├── engine.go
│   │   └── repository.go
│   ├── policy/
│   │   ├── model.go
│   │   ├── engine.go
│   │   ├── rules.go
│   │   └── repository.go
│   ├── approval/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── decision/
│   │   ├── model.go
│   │   ├── kernel.go
│   │   ├── precedence.go
│   │   ├── receipt.go
│   │   └── service.go
│   ├── result/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── verify/
│   │   ├── model.go
│   │   ├── verifier.go
│   │   ├── api_verifier.go
│   │   ├── db_verifier.go
│   │   ├── event_verifier.go
│   │   └── repository.go
│   ├── reconcile/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── compensator.go
│   ├── incident/
│   │   ├── model.go
│   │   ├── detector.go
│   │   ├── service.go
│   │   └── repository.go
│   ├── graph/
│   │   ├── model.go
│   │   ├── live.go
│   │   ├── query.go
│   │   └── repository.go
│   ├── cost/
│   │   ├── model.go
│   │   ├── ingest.go
│   │   ├── attribution.go
│   │   ├── waste.go
│   │   └── repository.go
│   ├── semantic/
│   │   ├── embedder.go
│   │   ├── duplicate.go
│   │   ├── supersession.go
│   │   ├── provider.go
│   │   ├── prompts.go
│   │   └── cache.go
│   ├── event/
│   │   ├── model.go
│   │   ├── ingest.go
│   │   ├── dedupe.go
│   │   ├── outbox.go
│   │   └── consumer.go
│   ├── replay/
│   │   ├── model.go
│   │   ├── engine.go
│   │   └── evaluator.go
│   ├── shadow/
│   │   ├── analyzer.go
│   │   ├── comparator.go
│   │   └── metrics.go
│   ├── repo/
│   │   ├── analyzer.go
│   │   ├── mapping.go
│   │   ├── codegraph.go
│   │   └── language.go
│   ├── git/
│   │   ├── parser.go
│   │   ├── diff.go
│   │   ├── ancestry.go
│   │   ├── blob.go
│   │   └── watcher.go
│   ├── github/
│   │   ├── webhook.go
│   │   ├── client.go
│   │   ├── repository.go
│   │   └── resolver.go
│   ├── mcp/
│   │   ├── gateway.go
│   │   ├── protocol.go
│   │   ├── normalize.go
│   │   └── middleware.go
│   ├── otel/
│   │   ├── ingest.go
│   │   ├── attributes.go
│   │   └── correlate.go
│   ├── runtime/
│   │   ├── adapter.go
│   │   ├── temporal.go
│   │   ├── restate.go
│   │   ├── langgraph.go
│   │   └── generic.go
│   ├── api/
│   │   ├── server.go
│   │   ├── routes.go
│   │   ├── handlers/
│   │   ├── middleware/
│   │   ├── request.go
│   │   ├── response.go
│   │   └── errors.go
│   ├── auth/
│   │   ├── principal.go
│   │   ├── apikey.go
│   │   ├── mtls.go
│   │   └── middleware.go
│   ├── tenant/
│   │   ├── model.go
│   │   └── context.go
│   ├── store/
│   │   ├── postgres.go
│   │   ├── tx.go
│   │   ├── sqlc/
│   │   └── migrations.go
│   ├── config/
│   │   └── config.go
│   └── telemetry/
│       ├── metrics.go
│       ├── tracing.go
│       └── logging.go
│
├── adapters/
│   ├── mcp/
│   ├── http/
│   ├── openapi/
│   ├── github/
│   ├── postgres/
│   ├── kubernetes/
│   ├── terraform/
│   ├── temporal/
│   ├── restate/
│   ├── langgraph/
│   ├── openai/
│   └── generic/
│
├── sdk/
│   ├── go/
│   ├── typescript/
│   └── python/
│
├── pkg/
│   ├── protocol/
│   │   ├── actor.go
│   │   ├── work.go
│   │   ├── observation.go
│   │   ├── action.go
│   │   ├── resource.go
│   │   ├── effect.go
│   │   ├── decision.go
│   │   ├── outcome.go
│   │   └── event.go
│   └── schema/
│
├── migrations/
├── testdata/
│   ├── replay/
│   ├── traces/
│   ├── git/
│   └── scenarios/
├── examples/
├── docs/
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

Avoid generic `utils`, `helpers`, and `common` dumping grounds.

---

# 56. Language and Technology Choices

## Go

Use Go for:

- control service,
- MCP gateway,
- HTTP middleware,
- API server,
- event ingestion,
- coordination kernel,
- resource resolver,
- lease manager,
- idempotency engine,
- policy engine,
- Git/GitHub integration,
- runtime adapters,
- FinOps aggregation,
- CLI,
- workers,
- reconciliation orchestration.

Reason:

- excellent concurrency,
- network services,
- low-latency APIs,
- static binaries,
- operational simplicity,
- strong typing,
- good PostgreSQL ecosystem.

## TypeScript

Use for:

- TS/Node SDK,
- optional JS framework adapters,
- ecosystem-specific wrappers where direct implementation is easier.

## Python

Use for:

- Python SDK,
- optional offline ML experiments,
- evaluation/training of semantic classifiers if needed.

Python is not required in the production safety kernel.

## Repo analysis

Prefer Go plus:

- Tree-sitter bindings where practical,
- Git plumbing,
- SCIP/LSIF ingestion where available,
- language-specific parsers only when necessary.

Avoid building a universal compiler platform.

---

# 57. Recommended Go Libraries

Conservative choices:

```text
HTTP:
net/http or chi

Postgres:
pgx

SQL generation:
sqlc

Migrations:
goose or atlas

CLI:
cobra

Telemetry:
OpenTelemetry Go

Logging:
slog

UUID:
google/uuid

Config:
environment + small explicit config package

JSON:
stdlib encoding/json unless profiling proves otherwise
```

Avoid dependency-heavy application frameworks.

---

# 58. Coding Conventions

Prefer:

- explicit interfaces at boundaries,
- concrete internal types,
- context propagation,
- typed reason codes,
- domain errors,
- constructor injection,
- explicit transactions,
- small packages,
- immutable-ish decision inputs,
- pure deterministic decision functions,
- no hidden global state.

Kernel shape:

```go
type Kernel struct {
    stale        StaleDetector
    duplicates   DuplicateEngine
    conflicts    ConflictEngine
    dependencies DependencyEngine
    policies     PolicyEngine
    leases       LeaseManager
    store        Store
}
```

Pure decision snapshot:

```go
type DecisionInput struct {
    Action         ActionIntent
    ResourceState  []ResourceState
    Observations   []Observation
    ActiveActions  []ActiveAction
    Dependencies   []DependencyState
    Duplicate      *DuplicateEvidence
    Supersession   *SupersessionEvidence
    PolicyResults  []PolicyResult
}
```

---

# 59. Detection vs Decision vs Enforcement

Keep these separate.

Detection:

```text
Action overlaps another active write.
```

Decision:

```text
This overlap requires WAIT.
```

Enforcement:

```text
Do not forward tool call.
```

This separation prevents transport code from owning domain correctness.

---

# 60. Onboarding Flow — Backend Perspective

## Step 1 — Run

```bash
docker run lumni/lumni
```

or:

```bash
go install github.com/.../cmd/lumni
```

## Step 2 — Connect one live source

Recommended first choices:

```text
MCP gateway
SDK
OpenTelemetry
Temporal
Restate
```

## Step 3 — Optional enrichment

```text
GitHub repo
OpenAPI
DB schema
tests
PR data
```

## Step 4 — Discover

Backend outputs:

```text
Agents discovered          7
Tools discovered          41
Consequential writes      18
Deterministic mappings    13
Observed mappings          3
Unresolved                 2
```

## Step 5 — Run shadow mode

No blocking.

## Step 6 — Historical/live findings

```text
stale actions
exact duplicate effects
conflicting writes
duplicate work
superseded work
outcome mismatches
estimated wasted spend
```

## Step 7 — Enable deterministic protections

Selectively.

---

# 61. Initial OSS Scope

OSS should include enough functionality to be genuinely useful.

Apache-2.0 target:

- open protocol types,
- Go server,
- CLI,
- Postgres schema,
- MCP gateway,
- SDKs,
- OTel ingestion,
- resource model,
- observation/version model,
- exact duplicate detection,
- exact conflict engine,
- stale-state detector,
- shadow analyzer,
- decision receipts,
- basic work graph,
- Git/GitHub enrichment,
- Temporal/Restate adapters,
- cost ingestion and basic waste calculation,
- adapter SDK.

Principle:

> **Correctness primitives are open source.**

---

# 62. Commercial / Cloud Boundary

Commercial value comes from operating the system at organizational scale:

- managed control plane,
- fleet-wide graph,
- hosted history,
- BYOC management,
- enterprise RBAC,
- multi-region deployments,
- organization policy distribution,
- large-scale analytics,
- cross-runtime visibility,
- managed adapters,
- managed verifiers,
- managed approval integrations,
- advanced FinOps,
- semantic recommendations,
- advanced duplicate/supersession models,
- compliance/audit workflows,
- centralized incident management,
- enterprise support/SLA.

---

# 63. Deployment Topologies

## 63.1 Local developer

```text
Agent
  ↓
Lumni Gateway
  ↓
lumnid
  ↓
Postgres
```

## 63.2 Customer VPC

```text
CUSTOMER VPC

Agents / Runtimes
      │
      ▼
Gateway replicas
      │
      ▼
Lumni Control Service
      │
      ▼
Postgres HA
      │
      ├── local async workers
      │
      └── optional aggregate/redacted telemetry
                    │
                    ▼
                Lumni Cloud
```

## 63.3 Embedded

For low-volume/low-latency deployments:

```text
agent process
    │
    ├── embedded Go SDK
    │
    └── shared Postgres
```

## 63.4 Managed cloud

Suitable only where customer accepts cloud control path.

For critical enterprise use, local-region/customer-VPC mode is preferred.

---

# 64. Background Jobs

Async workers handle:

- semantic embeddings,
- semantic duplicate analysis,
- supersession suggestions,
- outcome verification retries,
- reconciliation,
- cost rollups,
- graph enrichment,
- GitHub backfill,
- historical correlation,
- incident grouping,
- outbox publishing,
- retention cleanup.

Do not put critical admission behind async workers.

---

# 65. Outbox and Async Delivery

Use transactional outbox.

Flow:

```text
DB transaction
  ├── mutate control state
  └── insert outbox row

worker
  ↓
publish webhook/internal event
  ↓
mark outbox published
```

At-least-once delivery is acceptable for consumers because event application must be idempotent.

---

# 66. Webhooks

Outbound webhook event types:

```text
decision.created
action.waiting
action.replan
duplicate.detected
conflict.detected
stale.detected
approval.requested
approval.resolved
outcome.mismatch
partial_effect.detected
incident.created
work.superseded
finops.waste_detected
```

Webhook delivery:

- signed payload,
- retries,
- exponential backoff,
- delivery IDs,
- idempotent consumer expectation,
- secret rotation.

---

# 67. Rate Limits and Backpressure

Do not allow analytics traffic to starve admission.

Separate pools/limits:

```text
admission API
result/observation writes
query APIs
historical import
semantic jobs
```

Admission gets highest priority.

If soft subsystems overload:

- shed semantic work,
- defer graph enrichment,
- defer analytics.

Do not shed hard state updates silently.

---

# 68. Data Retention

Configurable by class:

```text
hard decision receipts       long retention
resource versions            medium/long
action metadata              configurable
raw arguments/results        short or disabled
OTel spans                    external / configurable
semantic embeddings          configurable
cost records                 finance policy
audit records                enterprise policy
```

Support deletion by tenant and legal/compliance requirements.

---

# 69. Schema Evolution

Public protocol must be backward compatible.

Use:

- additive JSON fields,
- explicit API versioning,
- DB migrations,
- adapter capability negotiation.

Protocol events include `schema_version`.

---

# 70. Adapter Capability Model

Adapters declare capabilities:

```json
{
  "adapter": "temporal",
  "capabilities": [
    "session_identity",
    "work_identity",
    "action_interception",
    "decision_enforcement",
    "result_reporting",
    "cost_reporting"
  ]
}
```

Resource adapters may declare:

```text
version_resolution
relationship_resolution
verification
compensation
domain_conflicts
```

---

# 71. Reconciliation State Machine

```text
ACTION_STARTED
      ↓
RESULT_UNKNOWN
      ↓
VERIFYING
   ┌──┴───────────┐
   ▼              ▼
VERIFIED       NOT_FOUND
   │              │
COMMITTED      retry policy?
                  │
          ┌───────┴───────┐
          ▼               ▼
       RETRY          ESCALATE
```

For partial effects:

```text
PARTIAL
  ↓
RECONCILING
  ├── retry missing effects
  ├── invoke registered compensation
  └── human escalation
```

---

# 72. FinOps Aggregation Model

Rollups:

```text
hourly
daily
monthly
```

Dimensions:

- tenant,
- agent,
- framework,
- provider,
- model,
- workflow/work type,
- resource type,
- customer/business account,
- outcome,
- waste reason.

Use incremental SQL rollups initially.

Do not introduce a separate OLAP system until required.

---

# 73. Useful-Work Semantics

A work item may be:

```text
VERIFIED_USEFUL
COMPLETED_UNVERIFIED
FAILED
DUPLICATE
SUPERSEDED
PARTIAL
NO_EFFECT
```

Cost attribution should not automatically assume every `COMPLETED` work item is useful.

Preferred highest-confidence useful status:

```text
verified outcome
```

Secondary:

```text
customer-provided success event
explicit completion state
```

---

# 74. Incident Detection Rules

Initial incidents:

```text
EXACT_DUPLICATE_EFFECT_COMMITTED
STALE_ACTION_EXECUTED
CONFLICTING_ACTIONS_EXECUTED
OUTCOME_MISMATCH
PARTIAL_COMMIT
UNKNOWN_EFFECT_RETRIED
IDEMPOTENCY_VIOLATION
FENCING_VIOLATION
```

Shadow-mode findings may not be incidents until actual effect confirms harm.

---

# 75. Example End-to-End Flow — Stale Production Action

```text
1. Agent reads subscription:91 @ v18
2. SDK records Observation
3. Agent reasons for 40 sec
4. Human cancels subscription
5. webhook/version resolver records v19
6. Agent proposes upgrade
7. ActionIntent resolves WRITE subscription:91
8. Kernel compares observed v18 vs current v19
9. Decision = REPLAN
10. receipt stored
11. adapter returns replan signal
12. agent re-reads state
```

No LLM.

---

# 76. Example End-to-End Flow — Exact Duplicate

```text
1. Agent A proposes refund payment:72
2. effect key = refund:payment72:100USD
3. ALLOW
4. refund commits
5. result recorded
6. Agent B proposes same effect
7. idempotency engine finds committed effect
8. DEDUPLICATE
9. previous external result returned
```

---

# 77. Example End-to-End Flow — Cross-Runtime Conflict

```text
Temporal Agent:
UPGRADE subscription:91

LangGraph Agent:
CANCEL subscription:91
```

Both map to same canonical resource.

First action acquires write lease.

Second:

```text
domain rule:
UPGRADE × CANCEL = conflict
```

Decision:

```text
WAIT
```

Temporal/Restate/LangGraph adapter owns how waiting resumes.

---

# 78. Example End-to-End Flow — Partial Effect

```text
Work:
refund + CRM update + close ticket

refund     committed
CRM        failed
ticket     committed

→ PARTIAL_COMMIT incident
→ configured CRM reconciliation can retry
→ work outcome remains PARTIAL until verified
```

---

# 79. Example End-to-End Flow — Repo/PR Enrichment

```text
PR #812 changes refund workflow

repo analyzer:
retry added
idempotency key absent

runtime history:
6 timeout-after-request events

Lumni finding:
HIGH correctness risk

after deploy:
runtime tracks duplicate-effect identity
```

Repo analysis informs risk; runtime evidence proves production behavior.

---

# 80. Example End-to-End Flow — FinOps Waste

```text
Work A cost: $0.92
Work B cost: $1.11

Both exact same customer refund effect.

Work A verified.
Work B deduplicated after $0.47 already spent reasoning.

waste:
$0.47 duplicate_work_cost
```

This is more actionable than provider-level token reporting.

---

# 81. P0 Build Scope

Build first:

```text
Go modular monolith
Postgres
optional pgvector

protocol types
actor/session/work registry
resource registry
observations
ActionIntent
resource resolver
version manager
event journal

MCP gateway
Go SDK
TypeScript SDK
Python SDK
OTel ingestion

shadow-mode exact duplicate detector
shadow-mode stale detector
shadow-mode exact conflict detector

live work/resource graph
Git/GitHub enrichment
basic cost ingestion
basic waste attribution

HTTP API
CLI
OpenTelemetry
decision receipts
replay
```

No blocking in P0.

---

# 82. P1 — Deterministic Admission

Add:

- `/actions/check`,
- exact idempotency enforcement,
- lease manager,
- fencing,
- WAIT,
- REPLAN,
- deterministic conflict rules,
- approval state,
- Temporal/Restate enforcement adapters,
- outcome result journaling.

---

# 83. P2 — Verified Effects and Reconciliation

Add:

- verifier registry,
- API/DB/event verifiers,
- unknown-effect reconciliation,
- partial-effect state,
- registered compensators,
- incidents,
- cost-per-verified-outcome.

---

# 84. P3 — Work-Level Coordination

Add:

- exact duplicate work,
- work joining,
- explicit dependencies,
- deterministic supersession,
- priority,
- cross-runtime coordination analytics.

---

# 85. P4 — Semantic Intelligence

Add:

- work embeddings,
- likely duplicate goals,
- semantic supersession,
- relationship suggestions,
- conflict-rule suggestions,
- explanations.

Remain advisory by default.

---

# 86. P5 — Organization-Scale Control Plane

Add:

- multi-team/fleet graph,
- distributed policy,
- enterprise RBAC,
- managed approvals,
- large-scale historical analytics,
- advanced FinOps,
- BYOC,
- multi-region where required,
- organization-wide coordination.

---

# 87. Success Metrics

## Activation

```text
time to first agent discovered
time to first consequential action mapped
time to first resource mapping
time to first live conflict/stale/duplicate finding
```

## Reliability

```text
exact duplicate-effect rate
stale-action rate
conflicting-write rate
unknown-effect retry rate
partial-completion rate
outcome mismatch rate
false-positive hard-block rate
additional admission latency
```

Most important enforcement metric:

> **False-positive hard-block rate must be extremely low.**

## Coordination

```text
duplicate-work rate
superseded-work rate
work wait time
work join rate
resource contention rate
prevented conflicting actions
```

## FinOps

```text
cost per verified outcome
useful work ratio
duplicate-work waste
stale-work waste
superseded-work waste
retry-after-commit waste
failed-effect cost
```

---

# 88. Explicit Non-Goals

Do not build initially:

- generic chat UI,
- frontend dashboard in this backend spec,
- generic prompt management,
- generic LLM tracing,
- generic model evaluation,
- generic model router,
- custom workflow language,
- custom durable execution engine,
- generic agent memory,
- broad enterprise knowledge graph,
- agent marketplace,
- full enterprise IAM,
- generic agent registry,
- universal scheduler,
- distributed consensus system,
- cross-region global locks,
- Kafka-first architecture,
- Neo4j-first architecture,
- dozens of vendor-specific integrations before demand,
- autonomous compensation invented by an LLM.

---

# 89. Final Architectural Principle

Everything reduces to:

```text
WORK
"What is this actor trying to accomplish?"

OBSERVE
"What state did it reason from?"

ACTION
"What does it intend to do?"

RESOURCE
"What real objects will change?"

COORDINATE
"What else is happening to those objects?"

DECIDE
"Should this execute now?"

EXECUTE
"Let the existing runtime perform the work."

VERIFY
"Did the intended effect actually happen?"

ACCOUNT
"How much useful or wasted work did this consume?"

UPDATE
"Refresh the live operational graph."
```

The hard runtime is therefore:

```text
canonical identities
+
resource versions
+
effect identities
+
strongly consistent active state
+
deterministic conflict/idempotency rules
+
leases/fencing
+
explicit policy
+
verified outcomes
```

with:

```text
repo intelligence
embeddings
LLM classification
semantic relationship discovery
incident explanation
```

as enrichment.

The long-term product is not “another agent framework.”

It is the **independent coordination and correctness substrate for autonomous work across heterogeneous agent runtimes, human operators, traditional services, and shared real-world systems**.
