# Configuration and deterministic rules

## Enforcement mode

Each tenant has one of `SHADOW`, `WARN`, or `ENFORCE`. `SHADOW` and `WARN` persist `WOULD_*` receipts without leases or blocking. P1 execution controls are active only in `ENFORCE`.

## Resource rules

Resource rules map one `(source, tool)` pair to a canonical operation and bounded resource list. Arguments are selected only with RFC 6901 JSON Pointers. The format has no arbitrary expressions:

```json
{
  "source": "mcp",
  "tool": "refund_payment",
  "operation": "payment.refund",
  "resources": [
    {"mode":"WRITE", "type":"payment", "id_pointer":"/payment_id"}
  ]
}
```

Explicit resources supplied by the SDK take precedence. Missing pointer targets produce `RESOURCE_UNRESOLVED`; semantic inference never enters enforcement.

## Conflict rules

Base write/write, write/delete, and same-identity create conflicts are deterministic. A conflict rule refines operation semantics for a resource type:

```json
{
  "left_operation":"deployment.deploy",
  "right_operation":"deployment.rollback",
  "resource_type":"deployment",
  "outcome":"WAIT",
  "reason_code":"DOMAIN_OPERATION_CONFLICT"
}
```

## Policy rules

P1 policies match an operation, resource type, minimum risk, and optionally a numeric argument selected by JSON Pointer. Outcomes are `WARN`, `REQUIRE_APPROVAL`, or `REJECT`. There is no custom policy language.

Approval creates no lease. An approved retry with the same idempotency identity rechecks current versions and conflicts before reservation.

## Advisory semantic evaluation (JEV)

Semantic evaluation is disabled by default and runs in a background loop that is separate from admission and durable-work maintenance. To make the OpenRouter adapter available to the worker, configure:

```env
LUMNI_SEMANTIC_PROVIDER=openrouter
LUMNI_OPENROUTER_API_KEY=...
LUMNI_JEV_MODEL=typesafe/jev-1.13
LUMNI_SEMANTIC_TIMEOUT=5s
LUMNI_SEMANTIC_MAX_RETRIES=3
LUMNI_SEMANTIC_MAX_CANDIDATES=20
```

The worker checks `GET https://openrouter.ai/api/v1/endpoints/zdr` before sending any state and disables itself whenever the selected model is absent. Use an OpenRouter account or guardrail with `enforce_zdr_other=true` as an additional control. The adapter never opts into prompt logging or metadata capture and sends no plugins or tools. [OpenRouter's ZDR documentation](https://openrouter.ai/docs/guides/features/zdr) explains that ZDR prevents provider retention; it does not prevent the provider from receiving the minimized state.

The pinned model is the production default; [`~typesafe/jev-latest`](https://openrouter.ai/~typesafe/jev-latest) is intended only for exploratory tests. OpenRouter currently routes JEV to one TypeSafe endpoint, so OpenRouter simplifies access and billing but is not provider redundancy.

Availability at the worker level does not opt in a tenant. Enable a tenant explicitly with an admin key:

```http
PUT /v1/config/semantic
Content-Type: application/json

{"enabled":true}
```

Only goals, statuses, operation and tool names, resource types and modes, declared risk, reason codes, and bounded redacted summaries are sent. Raw arguments, resource identifiers, outputs, metadata, and error messages are excluded. Outbound state is never persisted. The evaluation audit row stores only its hash, typed answers, model IDs, usage, cost, latency, and failures.

Accepted recommendations appear as ordinary findings and, for duplicate or supersession suggestions, `work_edges` with `evidence_type=SEMANTIC_INFERENCE`. They are advisory and never modify work, risk, approvals, verification, leases, or admission decisions.

Audit records are available from `GET /v1/semantic-evaluations` and can be filtered by `work_id`. They contain no outbound state.

Rollout should progress from fixture replay to internal shadow traffic and then tenant opt-in. Before broader use, evaluate at least 200 labeled examples per capability and require 90% precision for duplicate/supersession, 85% precision for risk/trace findings, binary expected-calibration error at most 0.10, no admission-latency regression, and no non-allowlisted outbound fields.
