# Integrations

## Optional Composio connector

The opt-in [Composio GitHub pilot](composio.md) supports account connection,
status and disconnection, fresh PR reads, governed close/reopen actions, durable
read-back verification and signed V3 event ingestion. Configure the API and
worker with a scoped project key and your GitHub auth config. Direct integrations
remain available. Live provider acceptance is required before production use.

## MCP gateway

The gateway supports subprocess stdio and Streamable HTTP on both sides. Unknown JSON-RPC methods and discovery payloads pass through byte-for-byte. `tools/call` is coordinated using `_meta.lumni` context first, then configured actor/session/work defaults. Missing identities are registered lazily.

Shadow and warning decisions are forwarded and their upstream success or failure is reported. Enforced blocking decisions return a structured JSON-RPC response without calling the upstream tool. Coordinator outages fail open for low/medium risk and fail closed for high/critical risk unless `--fail-closed` makes the policy stricter.

## OpenTelemetry

`lumnid` accepts authenticated OTLP traces on gRPC `:4317` and HTTP `:4318/v1/traces` using protobuf or JSON. Send the Lumni API key as a bearer token. Spans deduplicate by tenant, trace ID, and span ID. `lumni.*` and applicable GenAI attributes correlate actors, sessions, work, actions, results, and cost outside the admission transaction.

## Git and GitHub

The GitHub receiver verifies `X-Hub-Signature-256` with constant-time HMAC comparison and deduplicates `X-GitHub-Delivery`. It upserts repositories, pull requests, commits, changed-file resources, and version relationships. Set `LUMNI_GITHUB_WEBHOOK_SECRET` and send the tenant slug as the documented receiver query parameter.

`lumni repo-import --path .` records the repository, branch, commit, and blob identities without transmitting source contents by default. The worker owns asynchronous GitHub backfill hooks.

## Outbound webhooks

Admin endpoints under `/v1/webhooks` store encrypted endpoint secrets. Outbox delivery includes `X-Lumni-Delivery`, `X-Lumni-Event`, and `X-Lumni-Signature-256`, retries exponentially, and is at-least-once. Consumers must deduplicate delivery IDs.
