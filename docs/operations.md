# Operations and failure semantics

## Health and telemetry

- `/v1/health/live`: process health.
- `/v1/health/ready`: PostgreSQL connectivity.
- `/metrics`: Prometheus action decision counts and latency histograms.
- OTLP traces: `:4317` gRPC and `:4318/v1/traces` HTTP protobuf/JSON.

OTLP spans deduplicate by tenant, trace ID, and span ID. Known `lumni.*` and GenAI attributes correlate actors, agents, sessions, work, actions, and cost without delaying admission.

## Graph freshness and incident operation

The Graph Explorer defaults to live plus 24 hours and caps a focused snapshot at 500 entities. `graph_changes` supplies monotonic tenant-filtered invalidations and is retained for seven days. A reconnect older than retained history receives a reset signal and reloads a complete snapshot.

Attention is ordered as unknown effect, failed/rejected action, pending approval, replan/stale state, wait/conflict, expiring lease, then attributed waste. Safe actions never force-release an active lease or mutate a resource version.

ClickHouse lag does not make the coordinator unready. Historical responses show their source and lag, and fall back to PostgreSQL where retained data permits. Alert on growing `analytics_outbox` pending count or oldest age; do not page on ClickHouse loss as a coordination failure.

Remain PostgreSQL-only until long-range cost or OTel queries compete with admission, or retained event volume becomes operationally expensive. Enable ClickHouse when 30–90 day multidimensional analytics cannot meet the console latency target without affecting the admission p95 target.

## Failure behavior

- A lost heartbeat expires its reservation. Started actions become `UNKNOWN`; unstarted reservations become `EXPIRED`.
- A gateway coordinator failure is fail-open for low/medium risk unless configured otherwise, and fail-closed for high/critical risk.
- A crash after an external effect must be resolved with the effect/idempotency identity before retry; Lumni never blindly replays it.
- Late resource versions remain in history. They replace authoritative current state only when their source sequence is newer, or their unsequenced observation time is not older.
- Webhooks use at-least-once delivery, signed delivery IDs, exponential retry, and idempotent outbox ownership.

## Rollout

Start in `SHADOW`, review stable reason codes and receipts, then enable exact-effect suppression, stale-version protection, exact write conflicts, domain rules, and approvals in that order. Monitor false-positive enforcement before expanding the protected resource set.
