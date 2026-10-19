# Quick start

## Start Lumni

```bash
cp .env.example .env
docker compose up --build
```

The Compose profile starts PostgreSQL, applies migrations, bootstraps tenant `local`, and starts the API on `:8080`, OTLP/gRPC on `:4317`, and OTLP/HTTP on `:4318`. The example key is `lumni_local_change_me`; replace it for every non-local deployment.

Verify the process and database:

```bash
curl http://localhost:8080/v1/health/live
curl http://localhost:8080/v1/health/ready
```

Run `make demo` to exercise shadow stale detection, enforced admission, write contention, result recording, version advancement, and exact-effect deduplication.

For a clean local latency sample after the stack is ready:

```bash
docker compose exec lumnid /usr/local/bin/lumni benchmark \
  --url http://lumnid:8080 --key lumni_local_change_me
```

## Action lifecycle

1. Register an actor and start a work item.
2. Record the resource versions used while reasoning.
3. Call `POST /v1/actions/check` immediately before a consequential action.
4. In shadow mode, forward the action and retain the `WOULD_*` receipt.
5. In enforcement mode, execute only `ALLOW`; pass the returned fencing token to a supporting external system.
6. Call `/start`, heartbeat long actions, and always report a terminal result.
7. On `WAIT`, retry after the suggested interval. On `REPLAN`, re-read state. On approval, retry the same idempotent action after approval resolution.

Every write request requires `Authorization: Bearer ...`. API keys are tenant-bound; an optional `X-Lumni-Tenant` header must match the key.

## MCP gateway

Register a deterministic resolver rule before intercepting a tool whose request omits explicit Lumni resources:

```json
{
  "source": "mcp",
  "tool": "cancel_subscription",
  "operation": "subscription.cancel",
  "resources": [
    {
      "mode": "WRITE",
      "type": "subscription",
      "id_pointer": "/subscription_id",
      "observed_version_pointer": "/version",
      "version_source": "etag"
    }
  ]
}
```

For stdio:

```bash
LUMNI_API_KEY=... LUMNI_GATEWAY_ACTOR=support-agent \
  go run ./cmd/gateway --transport stdio --command ./mock-mcp-server
```

For Streamable HTTP:

```bash
LUMNI_API_KEY=... go run ./cmd/gateway \
  --transport http --listen :8090 --upstream http://existing-mcp:8080/mcp
```

The gateway relays unknown MCP methods transparently, intercepts `tools/call`, and reports the upstream result after it has streamed to the caller. Lumni context may be supplied as `_meta.lumni.{actor_id,session_id,work_id}`.

## SDKs

- Go: package `lumni/sdk/go` (normally imported with alias `lumni`).
- TypeScript: `@lumniverse/sdk` under `sdk/typescript`.
- Python: `lumni-sdk` under `sdk/python`.

All clients expose work, observation, action check/start/heartbeat/result, approval, event, and cost operations. Transport-safe requests retry bounded 5xx/network failures and include unique request IDs.
