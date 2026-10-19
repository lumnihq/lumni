# API contract

[`api/openapi.yaml`](../api/openapi.yaml) is the canonical HTTP contract. Versioned event schemas live under [`api/schemas`](../api/schemas). Additive response fields are allowed; clients must ignore fields they do not understand.

## Authentication and tenancy

Every `/v1` endpoint except health and the signed GitHub receiver requires `Authorization: Bearer <key>`. Keys are random bearer credentials stored only as SHA-256 hashes, are bound to one tenant, and carry `admin`, `ingest`, `admission`, or `query` scopes. The raw value returned by `POST /v1/api-keys` is shown once. If `X-Lumni-Tenant` is sent, it must equal the key's tenant.

The browser console sends the bearer key once to `POST /v1/auth/session`. The response sets `lumni_session` as an HttpOnly, SameSite cookie and returns a CSRF token. Cookie-authenticated `POST`, `PUT`, `PATCH`, and `DELETE` requests must include that token in `X-CSRF-Token`. Sessions idle after eight hours and have a 24-hour absolute lifetime by default. `DELETE /v1/auth/session` revokes the session.

## Operational graph

- `GET /v1/graph/snapshot` returns one focused graph for `live`, `agents`, `work`, `resources`, `transaction`, or `finops`. Filters include RFC3339 `from`/`to`, focus entity, depth, status, risk, and a hard limit of 500 nodes.
- `GET /v1/graph/entities/{type}/{id}` returns an inspector with the current entity, immediate topology, redacted details, timeline, decision stages, and only the safe actions valid for its state.
- `GET /v1/graph/stream` is a tenant-authenticated SSE invalidation stream. Resume with `Last-Event-ID`; a `reset` event means the client must fetch a complete snapshot.
- `GET /v1/finops/graph` returns cost and waste series, provider/model/waste breakdowns, graph nodes, and an analytics freshness watermark.

Graph responses expose `analytics.source`, `analytics.as_of`, `analytics.lag_seconds`, and fallback information. Graph invalidations never carry authoritative entity payloads; clients refetch a snapshot after an invalidation.

Decision receipts include ordered `evaluations[]` for idempotency, exact effect, resource versions, conflicts, policy, and leases. This is additive to the existing decision and reason code.

The only graph-specific mutation is `POST /v1/actions/{id}/cancel-reservation`, which succeeds only for an unstarted `RESERVED` action. It cannot release an active action.

## Admission

`POST /v1/actions/check` returns HTTP 200 for every valid coordination outcome. `ALLOW`, `WARN`, `WAIT`, `REPLAN`, `DEDUPLICATE`, `REQUIRE_APPROVAL`, and `REJECT` are application decisions, not transport errors. Shadow checks use the corresponding `WOULD_*` decision, set `enforced` to false, acquire no leases, and permit execution.

An enforced `ALLOW` includes an opaque lease token and one monotonically increasing fencing token per write resource. The caller posts the lease token to `/start`, heartbeats while executing, and posts a terminal `/result`. A result advances resource versions and releases all leases in one transaction. `WAIT` exposes conflicting actions and retry guidance; `REPLAN` means the version source is comparable and stale; `DEDUPLICATE` references the committed effect; approval retries revalidate state before reserving.

Callers may provide both `action_id` and `idempotency_key`. Reusing a key with a different canonical payload is deterministically rejected. Coordination snapshots record kernel, resolver, and policy versions; `GET /v1/replay/{actionID}` reruns only a registered historical kernel and otherwise returns an explicit unsupported-version error.

Malformed input, authentication, authorization, and storage failures use the canonical envelope:

```json
{"error":{"code":"MALFORMED_JSON","message":"Request body is invalid","request_id":"req_...","details":{}}}
```

Events are idempotent on `(tenant_id,event_id)`. Resource identity is `(tenant,namespace,type,external_id)` and resolves to a stable opaque ID. Observations are historical evidence; authoritative versions are maintained separately and compared only when version sources match.
