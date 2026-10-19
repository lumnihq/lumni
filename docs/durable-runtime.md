# Native execution and verified outcomes

Lumni 0.2 adds an optional runtime to the existing `/v1` API. PostgreSQL owns work, steps, immutable attempts, leases and fencing, durable waits, verification jobs, and the journal. Application functions run in Go, Python, or TypeScript workers operated by the customer. Existing work remains `EXTERNAL`; gateway and framework integrations do not require native replay state.

## Native workers

Register a handler name and version, submit an identified work item, and run a worker. Each orchestration rerun must use the same stable step keys and inputs. Completed steps return encrypted stored outputs. A handler version or step input mismatch blocks replay. Do external I/O inside `llm`, `tool`, or `step`; orchestration outside these boundaries must be deterministic. `step` is for explicitly safe local/custom operations; consequential mutations must use `tool` with an ActionIntent. Python's core runtime supports Python 3.10+, while the asynchronous LangGraph adapter requires Python 3.11+.

```typescript
import {LumniClient, RuntimeClient, Worker} from '@lumniverse/sdk';
const api = new LumniClient(process.env.LUMNI_URL!, process.env.LUMNI_API_KEY!);
const runtime = new RuntimeClient(api);
const worker = new Worker(runtime).register('hello', '1', async (ctx, input) => {
  const result = await ctx.step('calculate', input, async () => ({message: 'hello'}));
  await ctx.sleep('pause', 1000);
  return result;
});
await runtime.submit({id:'hello-1', actor_id:'hello-agent', handler:'hello', handler_version:'1', input:{}});
await worker.run();
```

Equivalent SDK operations cover submission, claims, heartbeats, results, cancellation, signals, verifiers, evidence, dependencies, and action rechecks. Context primitives include `llm`, `tool`, `step`, `wait`, `sleep`, `approval`, `child`, and `handoff`. A timer created with `sleep(key, duration)` records its deadline at the first execution and returns the stored result after restart. Waits release worker capacity. Signals have immutable IDs and are consumed by one wait. A resolved approval or resource wait causes a fresh admission check before tool execution.

A work item can declare `expected_effects`, keyed by step key, with `verifier_id` and `expected`. Each corresponding tool action automatically receives that assertion. Work cannot complete until those exact expectations are verified. Child failures propagate by default. Set `handle_child_failure` only when orchestration explicitly handles the failure.

## Uncertain effects

The worker persists a call-start marker before invoking a tool. If it dies before that marker, a new attempt may resume. If it dies afterwards, the attempt is unknown. Expiring a lease never proves a mutation did not commit. The unresolved-effect hold survives lease expiry, cancellation, and worker replacement.

Transport results and authoritative outcomes are separate. `SUCCEEDED` means the invocation returned successfully. It does not mean `VERIFIED`. Unknown transport receipts remain immutable after later evidence proves the effect committed. A verifier may reconstruct a lost result from authoritative evidence. Otherwise deduplication returns `RESULT_UNAVAILABLE` and never repeats the mutation.

The default maximum is three explicitly safe retries with exponential backoff. A failed tool call is unknown unless the worker can establish that no effect occurred. Retrying an uncertain consequential mutation requires both authoritative absence and proof that the old attempt can no longer commit. A temporary empty lookup is insufficient. For upstream idempotency, pass the same upstream idempotency key in every provider request and retain its provider contract; Lumni does not assume that every tool supports it.

## Verifiers and mappings

Register immutable versioned verifiers using Setup or `POST /v1/verifiers`. Supported kinds:

| Kind | Configuration and expected evidence |
|---|---|
| HTTP | `config.url`, GET read-back; expected object subset or JSON-pointer `assertions` |
| POSTGRES | `config.query`, connection URL in encrypted `secret`; `expected.params` bound as SQL parameters; one JSON result column, read-only transaction |
| STRIPE | Optional `config.base_url`; secret API key; expected amount/currency and `refund_id`, or charge/payment_intent plus `correlation_id` matching `metadata.lumni_effect_id`; paginated authoritative lookup |
| GITHUB | Optional API base URL; repository, number, state/head SHA/merged assertion |
| SHOPIFY | Versioned GraphQL `config.url`; encrypted access token; order_id and order fields or assertions, including `cancelled`, refunds, transaction evidence |
| EVENT / CUSTOM | An authenticated service with `verification` scope appends identified evidence |
| COMPOSITE | `config.verifiers` lists registered IDs; expected can contain separate child assertions under those IDs |

Verifier calls execute outside admission transactions. Durable jobs use bounded backoff; outages leave evidence pending and preserve holds. Each job has a fenced claim and a bounded overall timeout. A verifier definition referenced by outcomes cannot be silently replaced; register a new ID/version. Explicit verification retry requeues evidence gathering, never the mutation.

Confirm operation mappings with `POST /v1/discovery/confirm`. Include canonical operation, resource identity pointers and effect identity pointers. Refund amount and currency belong in effect identity. Resource overlap indicates possible conflict, not exact duplication. Historical ambiguous fingerprints remain held until an operator supplies confirming evidence to `/v1/effects/resolve-identity`.

Bind verification to gateway operations with `PUT /v1/config/operation-verifiers`:

```json
{"operation":"payment.refund","verifier_id":"stripe-refund-v1","expected_template":{"charge":"$args:/charge","amount":"$args:/amount","currency":"$args:/currency","correlation_id":"$args:/correlation_id"}}
```

Registered reconciliation/compensation handlers execute as new work through normal admission and verification. Successful effects remain visible after partial completion or cancellation. Partial work produces `PARTIAL_COMMIT` evidence.

## Payloads and permissions

Replay inputs/results are encrypted using `LUMNI_SECRET_ENCRYPTION_KEY` in customer-controlled PostgreSQL. Back up this key with the database. Active payloads are retained; terminal payloads expire after `LUMNI_PAYLOAD_RETENTION` (default `720h`). Missing payloads block recovery. Ordinary metadata queries exclude encrypted payloads; retained-result endpoints require `payload` scope. Workers use `runtime`, callbacks use `verification`, admission uses `admission`, and discovery/model ingestion uses `ingest`. Administrative configuration requires `admin`.

Do not put secrets in public metadata, expected assertions, or callback evidence. These are operator-visible evidence. Secret verifier credentials have a separate encrypted field. Query MCP tools never intercept their own read-only requests.

## External runtimes

Temporal Activities and workflow helpers live in `adapters/temporal`; the executable worker is `examples/temporal-agent`. Restate durable handlers are in `adapters/restate`, with an executable service in `examples/restate-agent`. Both retain orchestration history in their own runtime and use Lumni's one-shot action-start boundary before a consequential callback.

TypeScript exports `@lumniverse/sdk/langgraph` and `@lumniverse/sdk/openai`; Python exports `lumni.langgraph` and `lumni.openai`. LangGraph uses tasks, interrupts, and a persistent checkpointer. Resume with the framework's `Command(resume=...)`. OpenAI wraps FunctionTools at `needsApproval` and invocation boundaries. Retain the framework RunState before approval, resolve the Lumni approval, approve the corresponding framework interruption, and resume that saved state. Revalidation can suspend again when resource state changed; retain the pre-resume state until tool completion is durably recorded.

## Gateways and inspection

`gateway --transport stdio --command <server> -- <args>` wraps stdio MCP. `--transport http --upstream URL` supports Streamable HTTP and SSE. `--transport openapi --openapi spec.json --upstream URL` maps HTTP routes from operation IDs. `--transport a2a` only enforces explicit `message.metadata.lumni.action` resource metadata; opaque task text is observational. `--transport model` opts into Responses/Chat capture, including complete stream reconstruction. Interrupted model streams remain incomplete.

An unspecified gateway mode follows saved service policy. `--mode SHADOW` explicitly forwards with coverage evidence. Enforced consequential calls fail closed on unavailable admission; before a managed gateway has obtained policy, its outage behavior is conservative. Session-scoped MCP request IDs prevent cross-session result attribution.

Use `lumni discover`, `lumni gateway connect`, `lumni runtime`, `lumni verify`, and `lumni replay`. Dry replay reconstructs receipts only. Explicit execution replay requires a new work submission through normal admission. `GET /v1/mcp` is not required: the stateless query MCP endpoint accepts JSON-RPC POSTs for seven read-only tools documented in `internal/api/mcp.go`.
