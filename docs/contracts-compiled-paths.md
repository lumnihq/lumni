# Contracts and compiled paths APIs

These APIs run in the Go API and worker. They add no `/v1/console/*` routes, SDK instrumentation or onboarding. With real data, the console's Compiled paths pages (`/compiled`, `/compile`) call the compiled-path APIs directly, through the console server's `/v1` pass-through. The contract pages (`/contracts`, `/contract-list`) do the same with the contract APIs: drafts are saved with `POST`/`PATCH /v1/contracts`, the reader is `POST /v1/contracts/classify`, and with real data the flow is Describe, Connect, Preview, Turn on. Connect is the binding editor: each rule shows the binding fields its kind needs (the table below), checked as you type against the same rules as `ValidateRules`, with `GET /v1/contracts/{id}/needs` as the final word on read grants and verifiers. Preview, the week and runs come from the API; numbers it doesn't measure, like money kept, show as a dash.

Migrations 25 and 26 install the storage. Normal API/worker startup applies them. Run the worker alongside the API for compilation, shadow comparison, independent Watch verification, drift detection, and retention cleanup.

## Authentication and common behavior

Use the existing bearer key or session cookie. Session writes require `X-CSRF-Token`. Reads use `query`, configuration/lifecycle writes use `admin`, traces use `ingest`, execution/reports use `runtime`, and evidence uses `verification`. Retained trace payloads, regression fixtures, and compiled outputs require `payload`. Admin keys retain the existing scope inheritance.

Errors use the standard error/request-ID envelope. Invalid input returns 400, unavailable resources 404, stale revisions/invalid transitions 409, and unconfigured compilation 503. Lists return `items` and `next_cursor`, default to 200, accept 1–1000, and bind cursors to tenant, route, and filters. Dates are RFC 3339 UTC; time windows include `from` and exclude `to`. Analytics accept an IANA `tz`, default UTC. Monetary telemetry is integer micro-USD; action predicates use integer minor units in an explicitly bound currency, with no implicit currency conversion.

The OpenAPI file is the endpoint/schema reference. No `/v1/console/*` routes are introduced.

## Contracts

- `GET/POST /v1/contracts`: list or create a draft. List filters include `mode`, `q`, `from`, `to`, `limit`, and `cursor`.
- `GET/PATCH/DELETE /v1/contracts/{id}`: retrieve, edit with `revision`, or stop future checks while retaining historical revisions and receipts.
- `PUT /v1/contracts/{id}/mode`: `{ "mode": "draft|watch|protect|paused", "revision": N }`. State changes increment the revision; `was` retains the mode to use when resuming.
- `POST /v1/contracts/classify`: `{ "text": "..." }`. Returns advisory readings, ranked probabilities, slots, guessed fields, cues, and `source`. Deterministic classification always remains available. Configured JEV is used only when the tenant's existing semantic opt-in is enabled and its privacy check succeeds; requests/results are audited in semantic evaluations.
- `GET /v1/contracts/{id}/needs`: configured read capabilities, required fields, missing bindings, and readiness. Capability provenance is explicit; it is not a claim that provider-wide permissions were inspected.
- `GET /v1/contracts/{id}/hook-status?since=...`: observed check/result activity per bound actor/operation. No observations means `not_observed`, not disconnected.
- `GET /v1/contracts/{id}/week`: seven local calendar-day buckets by default, observed checks, verified results, gaps, and stopped/would-stop counts. Unmeasured savings are null.
- `GET /v1/contracts/{id}/runs`: paginated action references, receipt IDs, decisions, and rule verification status.
- `POST /v1/contracts/preview`: replay draft rules against the preceding seven days or supplied `from`/`to`. Reads retained historical snapshots, never today's app state in place of old facts. The scan is bounded at 10,000 actions and reports unavailable evidence/truncation. A supplied `fixture` evaluates one rule without database or tool mutations.
- `GET /v1/contracts/{id}/tests?lang=ts|py&action=...`: exports a retained regression fixture and a standard-library HTTP test. It does not depend on a fictional fixtures SDK.
- `POST /v1/contracts/{id}/runs/{action_id}/evidence`: verification-scoped custom evidence for independent Watch assertions: `{ "rule_id": "r1", "evidence": { "id": "stable-delivery-id", "status": "VERIFIED", "observed": {} } }`. Identical deliveries are idempotent; changed content under the same ID conflicts.

Example draft and activation:

```bash
curl -fsS "$LUMNI_URL/v1/contracts" \
  -H "Authorization: Bearer $LUMNI_API_KEY" -H 'Content-Type: application/json' \
  -d '{"title":"One refund per order","text":"Never refund the same order twice","rules":[{"id":"r1","type":"once-per-key","slots":{},"binding":{"operation":"stripe.refund","key_pointer":"/order_id"}}]}'

# Use the id and revision returned above.
curl -fsS -X PUT "$LUMNI_URL/v1/contracts/CONTRACT_ID/mode" \
  -H "Authorization: Bearer $LUMNI_API_KEY" -H 'Content-Type: application/json' \
  -d '{"mode":"watch","revision":1}'
```

Rules require explicit operation/key mappings before activation. Other bindings:

| Kind | Additional configuration |
|---|---|
| `approval-over` | `slots.amount_minor`, `amount_pointer`, and currency; optionally `currency_pointer`. Approval requirements are pinned to rule revisions. |
| `end-state` | An enabled `verifier_id`, nonempty `expected`, and optional `within_seconds` (default 300). |
| `amount-cap` | Read binding, arguments mapped from JSON pointers, `value_pointer`, amount/currency mappings, and `slots.limit`. Static limits (`amount paid`, `order total`, `daily budget`) also account for committed amounts; remaining balances reserve pending/uncertain amounts. |
| `order-before` | `predecessor_operation`; the key pointer correlates verified predecessor arguments, including retained actions from before contract creation. An unavailable predecessor remains held. |
| `rate-limit` | Positive `slots.n` and `slots.window` of `minute`, `hour`, `day`, or `week`. Windows are rolling elapsed-time windows. |
| `human-wins` | Positive `slots.minutes`, read binding, `last_editor_pointer`, and RFC 3339 `last_edited_at_pointer`. Reader output must identify `human`/`person` or `agent`/`ai`/`system`; unknown kinds are unavailable evidence. |

Any rule may attach a verifier. Expected templates substitute only explicit `{"$arg":"/argument/pointer"}` objects. Read freshness defaults to 30 seconds and is checked with binding/grant versions at admission. Existing resource-version checks still apply.

Watch findings do not alter enforced leases or block managed work. Watch read-back assertions have independent storage and cannot downgrade canonical action outcomes. Protect follows workspace/operation enforcement; effective mode can be `MIXED` across operations. Protect assertions compose with existing verifier requirements, preserve the original native expected-effects identity, and keep independent verification deadlines. Core effect identities and manually configured policies/verifiers are not rewritten. Pending and uncertain effects participate in keyed reservations.

## Tool bindings and read capabilities

`GET /v1/tool-bindings`, `PUT /v1/tool-bindings/{id}`, and `DELETE /v1/tool-bindings/{id}` list/register/disable explicit transports. PUT supplies the current `version` (0 for creation); the result returns the incremented version. Historical definitions remain immutable; credentials are encrypted and never returned. Updates invalidate pinned compiled versions.

Bindings specify a fixed HTTP(S) URL, method, JSON input/output schemas, app, operation, and explicit capability. Redirects and remote schema loading are disabled. Read bindings use GET. Their `objects` declare response field paths (dotted paths or JSON pointers); returned data is projected to these fields, including nested collections. Mutation bindings require resource mappings, effect-key fields, and an authoritative verifier/expected template. Optional `idempotency_header` forwards the stable effect identity.

Read capabilities are registered through `POST /v1/integrations/{app}/grants` with `{ "binding_id": "...", "objects": { "orders": ["balance", "status"] } }`. GET lists grants; DELETE `.../grants/{id}` revokes one. Only registered fields can be granted. Responses identify these as `operator_configured` and set `provider_permissions_verified: false`. This API does not mint native provider restricted keys or launch OAuth flows. Initial coverage is the existing verifier adapters plus explicit HTTP/custom bindings.

## Compiled paths

Generation requires both `LUMNI_OPENROUTER_API_KEY` and `LUMNI_COMPILER_MODEL`; no model is selected automatically. `LUMNI_COMPILER_URL` optionally overrides the chat-completion endpoint. The adapter requests schema-constrained responses and ZDR routing, validates the program independently, redacts credential-shaped fields, and bounds provider requests/responses/retries. Tests use local fixtures, not paid provider calls.

Enable tenant compilation explicitly:

```bash
curl -fsS -X PUT "$LUMNI_URL/v1/config/compiler" \
  -H "Authorization: Bearer $LUMNI_API_KEY" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"cloud_processing_consent":true}'
```

Ingest complete normalized traces with `POST /v1/traces`; inspect metadata with GET and retained payloads with `GET /v1/traces/{id}/payload`. Supply `id`, `work_id`, `actor_id`, `handler_version`, `operation`, `input`, `output`, `completed_at`, and ordered `steps`. Step kinds are `llm`, `read`, or `call`; tool steps name their binding/version and input/output. Optional action/step/model-call references must belong to the same work and tenant. This endpoint does not instrument or change an SDK.

`POST /v1/compiled-paths/discover` accepts `{}` and returns a job. Discovery groups actor/handler/tool sequences and schema fingerprints. A stable logical path survives binding revisions; compilation selects currently compatible retained traces. It requires two traces, selects up to 500 recent traces, and holds out the newest 20% (at least one) from synthesis.

Read APIs:

- `GET /v1/compiled-paths`, `/totals`, and `/suggestions`.
- `GET /v1/compiled-paths/{id}`: versions, immutable programs/hashes, drift, pause metadata, statistics, and server-computed `can` verbs.
- `GET /v1/compiled-paths/{id}/fallbacks?day=YYYY-MM-DD&tz=...`.
- `GET /v1/compiled-paths/{id}/race`: available only for an actual comparable recorded input, trace, and completed code run.
- `GET /v1/compiled-paths/{id}/versions/{v}/replay`: stored agreement/disagreement/uncovered counts and stable case IDs. It never initiates writes or runs live tools.

Lifecycle POSTs are `compile`, `shadow`, `promote`, `rollback`, `pause`, `resume`, and `recompile`. Supply the current `revision`; pause also accepts `why`. Compile/recompile return 202 jobs, including background replay. Other transitions return updated state. Poll `GET /v1/jobs/{id}`. Job claims are fenced and compilation resumes its existing artifact after a worker crash.

A first compile creates a candidate. Shadow compares future recorded traces without mutation calls. Promotion requires completed held-out replay, at least one covered shadow observation, current bindings/capabilities, and reviewed disagreements. `PUT /v1/compiled-paths/{id}/disagreements/{case_id}/label` accepts `{ "right": "llm|code" }`. Labels become later regression cases. There is no fixed agreement-percentage gate. Promotion retires the old live version; recompile preserves it while building a shadow replacement. Rollback restores the preceding retired version and reopens its drift.

Programs are bounded `path-v1` JSON instruction graphs interpreted in Go, not arbitrary JavaScript. Instructions are `let`, `read`, `call`, `guard`, `return`, and `fallback`; all edges point forward. Expressions use literals, `/input`/`/vars` pointers, object construction, and bounded arithmetic/comparison/string/date operations. Reads, guards, and fallback precede mutations. Limits are 256 nodes, 1 MiB program/input/output values, 2 MiB working variables, one second of evaluation time, and 15 seconds per tool call. Only registered versioned endpoints can be called.

## Invocation, reporting, and recovery

`POST /v1/compiled-paths/route` takes `{ "actor_id": "...", "operation": "..." }` and returns a path/version and `live`, `shadow`, or `none`. It does not call the agent's LLM.

```bash
curl -fsS "$LUMNI_URL/v1/compiled-paths/PATH_ID/execute" \
  -H "Authorization: Bearer $LUMNI_API_KEY" -H 'Content-Type: application/json' \
  -d '{"invocation_id":"stable-request-id","actor_id":"registered-agent","input":{"order_id":"order-42"}}'
```

Poll `GET /v1/compiled-runs/stable-request-id`; add `?include=output` with payload scope. The same invocation identity returns the existing run; changed content returns 409. Paused, shadow, drifted, and unsupported paths return a recorded fallback to the calling agent. Once an external mutation starts, execution suspends/reconciles and cannot fall back to a fresh agent execution. Native claims, step receipts, start-once boundaries, effect identities, leases, and verification prevent blind retries after lost responses.

`POST /v1/compiled-paths/{id}/runs` accepts caller-reported `llm`/`fallback` telemetry with a stable ID. It cannot claim server code execution, verify actions, create shadow evidence, or promote a version. Statistics identify reported counts separately; unavailable prices, comparisons, and savings remain null rather than borrowing demo values.

Binding/schema/policy changes and failed reviewed regression cases create drift records. Execution checks current bindings and grants before tool calls. Payload cleanup follows the worker's configured retention; immutable metadata and history remain.

## Verification

Run `make lint`, `make test-race`, `make test-integration`, and `make sdk-test`. Integration tests require an isolated `LUMNI_TEST_DATABASE_URL`. Tests cover direct HTTP API behavior, tenant/scopes/CSRF, concurrent key reservations, independent Watch verification, composed Protect assertions, mode transitions, replay isolation, labels/promotion/rollback, actual HTTP mutations, lost-response recovery, retention, and migration upgrade/down/up. Console coverage for compiled paths and contracts is `bun test` in `console/` (against the fake API in `console/test/real/fake-paths.ts` and `console/test/real/fake-contracts.ts`).
