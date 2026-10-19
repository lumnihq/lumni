# Running and testing Lumni

## Production-image smoke test

Start the recommended PostgreSQL-only topology from a new checkout:

```bash
cp .env.example .env
docker compose up --build -d
docker compose ps
curl -fsS http://localhost:8080/v1/health/ready
```

Every listed service should be running and PostgreSQL plus `lumnid` should be healthy. Open `http://localhost:5200` and create the owner account; the console uses the local bootstrap key `lumni_local_change_me` to mint each person's own key. The key is a development default only; replace it before any shared deployment.

If a host port is already occupied, override only the published side:

```bash
LUMNI_HTTP_PORT=18080 \
LUMNI_POSTGRES_PORT=15432 \
LUMNI_OTLP_GRPC_PORT=14317 \
LUMNI_OTLP_HTTP_PORT=14318 \
docker compose up --build -d
```

Run the deterministic scenario against the selected HTTP port:

```bash
LUMNI_URL=http://localhost:8080 make demo
```

The output must show `WOULD_REPLAN` in shadow mode, followed by enforced `ALLOW`, concurrent `WAIT`, and committed-effect `DEDUPLICATE`. Each receipt shows the six ordered evaluation stages without printing its lease secret. The demo also creates an agent, session, work item, actions, and resources so every operational graph lens has meaningful data. It leaves the tenant in `ENFORCE`; set it back to `SHADOW` when the test is complete if this is not a disposable environment.

In the console, verify:

- Live shows active/recent actions, resources, conflicts, and the attention queue.
- Agents shows `Demo Agent → demo-session → work → actions`.
- Transaction shows the receipt and six evaluation stages.
- Selecting an action opens its timeline and replay action.
- Table mode is keyboard navigable and provides the non-canvas accessibility view.

## Optional ClickHouse acceptance

Enable historical analytics without changing the PostgreSQL authority path:

```bash
docker compose --profile analytics up --build -d
docker compose --profile analytics ps
curl -fsS -u default:lumni --data-binary 'SELECT count() FROM lumni.analytics_facts FINAL' http://localhost:8123/
```

The first 24 hours continue to report `analytics.source=postgres`. A range longer than 24 hours reports `analytics.source=clickhouse` after the worker exports facts:

```bash
curl -fsS -H 'Authorization: Bearer lumni_local_change_me' \
  'http://localhost:8080/v1/graph/snapshot?lens=finops&from=2026-08-01T00%3A00%3A00Z'
```

To prove failure isolation, stop only ClickHouse and repeat a historical query:

```bash
docker compose --profile analytics stop clickhouse
curl -fsS http://localhost:8080/v1/health/ready
docker compose --profile analytics start clickhouse
```

Readiness remains green. Historical responses use PostgreSQL and set `analytics.fallback=true` while ClickHouse is down. New analytics facts remain pending in `analytics_outbox` and drain after restart; admission, leases, fencing, approvals, and resource versions never move out of PostgreSQL.

## Automated suites

With Go 1.27, Node 22+, Python 3.10+, and PostgreSQL available:

```bash
go test -race ./...
(cd console && bun test && bun run build)
cd sdk/typescript && npm ci && npm test
cd ../python && python3 -m pytest
```

Run PostgreSQL integration tests against a disposable database:

```bash
LUMNI_TEST_DATABASE_URL='postgres://lumni:lumni@localhost:5432/lumni?sslmode=disable' \
go test -count=1 -tags=integration ./internal/integration/...
```

With the analytics profile running, exercise actual ClickHouse replacement, tenant isolation, rollup replacement, and TTL configuration:

```bash
LUMNI_TEST_CLICKHOUSE_URL=http://localhost:8123 \
LUMNI_CLICKHOUSE_USER=default \
LUMNI_CLICKHOUSE_PASSWORD=lumni \
go test -count=1 -tags=integration -run TestClickHouseProfile ./internal/integration
```

CI additionally validates formatting, `go vet`, vulnerability scans, migration down/up, the race suite, PostgreSQL concurrency and tenancy tests, all SDKs, console tests/build, the installer against freshly built images, a real ClickHouse service, the production container build, and an image scan.

## Shutdown and data

`docker compose down` stops and removes project containers while retaining database volumes. `docker compose down -v` also deletes PostgreSQL and ClickHouse data; use it only for an explicitly disposable test environment.
