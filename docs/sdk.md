
# SDKs and durable runtimes

The Go package at [`sdk/go`](../sdk/go), TypeScript package at [`sdk/typescript`](../sdk/typescript), and Python package at [`sdk/python`](../sdk/python) implement the same lifecycle: work start/complete, observation, version update, check/start/heartbeat/result, approval polling, events, costs, and context propagation. Each client adds request IDs and bounds retries to transport-safe operations.

Decision handling is explicit. Forward `ALLOW`, `WARN`, and every `WOULD_*` result. On `WAIT`, durably delay by `retry_after_ms`; on `REPLAN`, discard stale reasoning and read the resource again; on `REQUIRE_APPROVAL`, persist the approval ID and poll or resume from an approval event; on `DEDUPLICATE`, use only the referenced committed result metadata.

Go uses `lumni/sdk/go`. TypeScript builds with `npm ci && npm test` from `sdk/typescript`. Python installs from `sdk/python` with `pip install -e .`. The examples under [`examples`](../examples) contain minimal agents in all three languages.

The native Go CLI supports `lumni init --url <api-origin> --code <one-time-code> --dir <agent-project>` without Node. It detects SDK languages, configures credential files and MCP gateways, and records a hello check. See [backend onboarding and release packaging](onboarding.md); source SDKs are available locally, while public npm/PyPI publication remains a separate release operation.

[`adapters/temporal`](../adapters/temporal) maps decisions to durable timers, application failures, and signals. [`adapters/restate`](../adapters/restate) maps them to durable suspension and retry instructions. These adapters return runtime-native instructions; they do not execute or own the user's workflow.

# Console metadata conventions

Agents can supply these fields through the existing SDK inputs. Use plain text for summaries and names; the console does not need extra endpoints for them.

| Input | Field | Meaning |
| --- | --- | --- |
| Actor metadata | `team` | Team name used to group AIs |
| Action metadata | `app` | App slug; otherwise the operation’s first segment is used |
| Action metadata | `summary` | Plain-language description of the action |
| Action metadata | `customer` | Customer name for approvals and journeys |
| Action metadata | `amount` | Amount in dollars, used for displayed amounts and money kept from repeats |
| Resource metadata | `name` | Human-readable resource name |

When checking an action with an observed resource version, send `resources[].observed_at` as an RFC 3339 timestamp to preserve read age, including across rechecks. Otherwise the backend uses a matching work/resource/version observation when available. Resource-version updates optionally accept `actor_id`, `action_id`, `summary`, and `value_hash`; provide hashes instead of raw values. [Console API documentation](console-api.md) covers aggregate reads, paging, journeys, runtime controls, proofs, and notifications.
