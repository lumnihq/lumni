# Console API

The APIs from the October 14, 2026 audit are implemented in the Go backend. There is no `console` integration in this change. PostgreSQL migrations 18–24 add controls, transactional audit events, proof storage, notification jobs, telemetry, corrected analytical aggregates, notification configuration history, an index for recent admissions, reason-specific resource rollups, onboarding, and the October 15 follow-ups. Service startup applies migrations through the existing migration runner.

All private routes use the existing bearer key or session cookie authentication. Session writes also require `X-CSRF-Token`. Read routes use `query`, control configuration uses `admin`, approval decisions retain `admission`, and verification retries retain `verification`. Errors use the existing error envelope and request ID. Money is integer micro-USD; service database pools use UTC.

| Audit item | API |
| --- | --- |
| A1 | `GET /v1/analytics/day` |
| A2 | Durable collections accept `from`, `to`, `limit`, `cursor`, and applicable status/actor/operation/action filters |
| A3 | `GET /v1/approvals?status=PENDING&include=action,rule`; `POST /v1/approvals/decide` |
| A4 | `GET /v1/actions/{id}/journey` |
| B1 | `POST /v1/actors/{id}/pause`, `/resume`; `POST /v1/config/pause` |
| B2 | `GET /v1/config/limits`; `PUT` / `DELETE /v1/config/limits/{id}` |
| B3 | `GET` / `PUT` / `DELETE /v1/config/spend-limit` |
| B4 | `GET /v1/config/autopilot`; `PUT` / `DELETE /v1/config/autopilot/{actor_id}` |
| B5 | `POST /v1/proofs`; `DELETE /v1/proofs/{id}`; public `GET /v1/public/proofs/{token}` and `/p/{token}` |
| B6 | `PATCH` / `DELETE /v1/config/policy-rules/{id}` |
| B7 | `DELETE /v1/config/enforcement` |
| B8 | `GET` / `PUT /v1/config/notifications`; `POST /v1/notifications/send` |
| B9 | `POST /v1/config/policy-rules/preview` |
| C1 | Resource freshness evaluation details; `GET /v1/analytics/freshness` |
| C2 | `GET /v1/resources/{id}/lease`; `GET /v1/resources/contended` |
| C3 | `GET /v1/resources/{id}/versions` |
| C4 | `GET /v1/analytics/daily` |
| C5 | `GET /v1/audit` |
| C6 | Stable waste keys, `avoided`, and `by_actor` on `GET /v1/finops/graph` |
| C7 | `GET /v1/apps/{app}/health` |
| C8 | Worker burst detection; `GET /v1/incidents?kind=BURST` |
| C9 | `GET /v1/billing/usage` |
| C10 | `GET /v1/analytics/anomalies?kinds=loop,runaway,drift` |
| D1 | `POST /v1/onboarding/codes`; public `POST /v1/onboarding/exchange`; admin `GET /v1/onboarding/codes/{code}/status` |
| D2 | Native `lumni init`; backend-only release packaging and manual SDK publication workflow |
| D3 | `GET /v1/actors?since=...&type=AGENT`; `GET /v1/graph/stream?types=actor.created,action.checked` |

Migration 23 adds durable onboarding credentials, actor filters, and onboarding event triggers. [Onboarding and releases](onboarding.md) covers these contracts, native CLI setup, and the remaining public registry publication step.

The complete request and response contracts are in [OpenAPI](../api/openapi.yaml). Collection responses keep `items` and add a nullable `next_cursor`. Cursors are scoped to the authenticated tenant, route, and filters. Preserve filters while following a cursor; page sizes can change. Time windows include `from` and exclude `to`. A list defaults to 200 rows and accepts 1–1000. Approvals can include old pending records without a time filter. The optional bulk decision accepts at most 200 IDs and handles each independently.

Graph snapshots retain the 500-node ceiling and add `next_cursor`. Transaction snapshots page by stable node identity. Focused graph inspection keeps its existing behavior. Edges can reference nodes on another page; combine pages by node and edge IDs to reconstruct the window. Legacy first-page graph views preserve their attention queue and can repeat nodes on the first continuation page, so deduplicate IDs.

Daily aggregates are updated transactionally from one fact per action. An outcome or recheck replaces the action’s prior contribution instead of incrementing its count. Fifteen-minute cells preserve local-hour boundaries in UTC, :30 and :45 zones. Partial boundary cells read authoritative facts. Reason-specific top resources use the same corrected rollup approach; sample actions stay within their app and effective outcome kind. With the ClickHouse profile, the worker exports replacement cells, including cells corrected to zero; PostgreSQL supplies unexported corrections and partial cells. The worker also creates the new ClickHouse table for an existing analytics installation. The default analytics range is local midnight through now, the maximum is seven days, and invalid ranges or dimensions return 400.

Pause, holds, concurrency caps, rate limits, and budgets run in admission. Matching concurrency/rate scopes take transaction advisory locks before counting admitted actions. Rate windows use each action's most recent admission time, including older actions admitted after approval and actions that have already completed. Reservations that expire stop consuming concurrency; active and uncertain actions continue consuming it. `ACTOR_PAUSED`, `CONCURRENCY_LIMIT`, `RATE_LIMITED`, and `BUDGET_PACING` are protocol reason codes. Shadow mode produces the corresponding `WOULD_*` decisions. Workspace and actor budgets both apply and consume recorded costs. Spend-limit PUT accepts an optional IANA `tz` (default `UTC`); reads return that zone, `day_start`, and `resets_at`. Both reporting and enforcement use the configured local day, including 23- and 25-hour days. `SLOW` spaces checks when observed spend is ahead of its daily allowance; `HOLD` rejects after the allowance is consumed. `slow_at` projects budget exhaustion at the current spend pace.

`GET /v1/config/limits?resource=<id>` returns only rules with a nonempty resource scope that matches the ID, including suffix wildcards. Workspace, actor-only and operation-only limits remain available in the unfiltered list. `GET /v1/resources/contended` embeds the same ordered queue as the lease endpoint (up to 1,000 waiting actions per resource) and loads its bounded resource set in one database query. Resource IDs in paths can be percent-encoded, including colons, slashes, and literal percent signs.

New `approval.decided` audit entries snapshot `target.summary`, `target.operation`, and `target.actor_id` from the action in the decision transaction. Summary uses the explicit `metadata.summary` string, falling back to the operation; arguments and unrelated metadata are excluded. Historical audit entries remain unchanged.

Autopilot skips only the configured approval rules for the configured actor, operation, and amount guard. The guard requires a JSON number; missing, null, string, boolean, array, or object values retain approval. It records `autopilot: true` in policy evaluations. A stale read or bad outcome counts as a slip. The most recent `per` actions since grant time form the guard window. Reaching `slips` revokes the grant and writes an audit event. Other checks and other approval rules still run. Preview reads retained argument payloads without saving a rule. When retention has removed arguments needed for a numeric predicate, it reports `unavailable_arguments` and `truncated: true`. Approved examples contribute to the false-positive estimate; this is an estimate rather than ground truth.

Public proof tokens contain 256 bits of randomness and are stored as SHA-256 hashes. The URL needed for delivery is stored encrypted. Proof responses use a strict projection: no arguments, arbitrary metadata, policy text, prices, verifier evidence, or credentials. Actor names appear only when explicitly enabled. Unknown and expired tokens return the same 404; revocation returns 410. Public reads are limited to 60 per minute per transport peer. Set `LUMNI_PUBLIC_BASE_URL` to the externally reachable API/proof origin before sharing links.

Notification delivery uses durable jobs and the existing worker. A send request returns 202 with a job ID; proof recipient delivery is also queued. Configure channels and routes through the API. Email additionally requires `LUMNI_SMTP_ADDR`, `LUMNI_SMTP_FROM`, and optionally `LUMNI_SMTP_USER` / `LUMNI_SMTP_PASSWORD`; remote SMTP requires STARTTLS. Slack uses a tenant-owned Composio connection created with `toolkit: "slack"` and cloud-processing consent, plus `LUMNI_COMPOSIO_SLACK_AUTH_CONFIG_ID`. Its session is restricted to `SLACK_SEND_MESSAGE`. See [Composio toolkits](https://docs.composio.dev/docs/toolkits) for that tool’s role. Weekly schedules use `DAY HH:MM IANA-zone`, such as `MON 09:00 Asia/Kolkata`, and enqueue once on that local date. Approval and incident routes enqueue transactionally with the originating record. No external channel is enabled by default.

Call health is measured from connector execution and verifier read-back calls. Empty telemetry returns an empty series. Bursts are deterministic ten-minute incident records exceeding five times the preceding hourly baseline, with a minimum of ten step-ins. Loop detection uses sliding ten-minute windows, including lookback before `from` when the loop continues inside the requested window. Runaways require at least ten actions in an hour and more than three times the mean of active hours in the preceding six hours; the candidate hour never contributes to its own baseline. Drift requires at least twenty actions and more than five percent rechecks. Runaway and drift totals honor exact `from`/`to` boundaries. Billing reads a workspace’s `billing_plans` row, with the default team price of 20,000 micro-USD per checked action when no override exists. Audit history starts when the migration is installed; configuration writes and events commit together, including notification routing changes.

Run backend checks with `go test ./...` and `go vet ./...`. With an isolated `LUMNI_TEST_DATABASE_URL`, run `go test -tags=integration ./internal/integration ./internal/store`. The opt-in scale fixture uses `LUMNI_TEST_SCALE_ACTIONS=2000000 go test -tags=integration ./internal/integration -run '^TestConsoleAnalyticsScale$' -count=1 -v -timeout=15m`. It requires a test database role able to bulk load fixtures with `session_replication_role`, measures all seven dimensions for aligned and partial-day windows, and verifies exact totals and resource touches. The October 15 local run with two million actions measured 129 ms for the slowest of six reads: 5–33 ms for aligned windows and 124–129 ms for partial-day windows. Notification transport and analytics export failure tests use local fixtures.
