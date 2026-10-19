# Deployment and migrations

For a ready-made install (Docker Compose, HTTPS, updates, backups), see [self-hosting](self-hosting.md). This page covers the parts underneath.

The release is a single-region PostgreSQL-backed modular monolith. The multi-stage [`Dockerfile`](../Dockerfile) builds `lumnid`, `worker`, `gateway`, and `lumni`; the final image runs as a non-root user. [`compose.yaml`](../compose.yaml) is the runnable local topology. Production deployments should run at least one API instance, one worker, managed PostgreSQL, and ingress TLS termination.

Migrations are embedded but explicit. Run `lumni migrate --direction up` before starting a new application version. Validate `--direction down` or `--direction down-to-zero` only against a backup or disposable database; never use a down migration as an ad-hoc rollback of live effects. `lumni bootstrap --slug <tenant>` generates the initial admin key and prints it once; `--key` may supply an externally generated value. Replace the development bootstrap key, database password, GitHub secret, and `LUMNI_SECRET_ENCRYPTION_KEY`; rotating the encryption key requires re-encrypting stored webhook secrets.

Admission, ingestion, query, and worker pools are configured separately so telemetry and graph reads cannot consume admission capacity. Scale API instances horizontally within one region. Run workers concurrently only with the built-in `SKIP LOCKED` ownership. PostgreSQL is the fencing authority; do not place a lagging replica on the admission path.

## Operator console and optional analytics

The console is its own image (`console/Dockerfile`, `ghcr.io/lumnihq/lumni-console`) on its own host, `app.` next to the API's `api.`. Its server keeps people's accounts and trades each person's API key once through `POST /v1/auth/session`; subsequent browser requests use an HttpOnly, SameSite session cookie and CSRF token. Terminate TLS at ingress so the browser marks the cookie Secure.

The default stack needs only PostgreSQL:

```bash
docker compose up --build
```

If standard development ports are already occupied, override the host side without changing container-to-container addresses, for example `LUMNI_HTTP_PORT=18080 LUMNI_POSTGRES_PORT=15432 LUMNI_OTLP_GRPC_PORT=14317 LUMNI_OTLP_HTTP_PORT=14318 docker compose up --build`.

ClickHouse is an optional system of analysis:

```bash
docker compose --profile analytics up --build
```

With `LUMNI_ANALYTICS_BACKEND=auto`, each process activates ClickHouse only when it is healthy at startup. Use `clickhouse` to require it or `postgres` to disable it explicitly. Admission, leases, fencing, current versions, approvals, effects, and live entity inspection always use PostgreSQL.

The worker exports redacted immutable facts from `analytics_outbox`. Delivery is at least once with stable event IDs; ClickHouse uses `ReplacingMergeTree` identity keys and rollups are rebuilt from deduplicated facts. If ClickHouse fails, the worker retains pending facts and API analytics queries fall back to PostgreSQL without changing coordination decisions.

Raw facts and hourly rollups retain 180 days by default; daily rollups retain 730 days. Change those TTLs in the ClickHouse initialization schema before the first production deployment. Monitor `analytics_outbox` age and pending rows as the replication-lag signal.

Readiness requires PostgreSQL connectivity. Graceful shutdown stops accepting HTTP/gRPC work, drains requests, and closes pools. Configure lease TTL, body limits, HTTP timeouts, outbox retry, and payload retention for the deployment's risk envelope. Keep the default mode `SHADOW`; enable deterministic protection only after reviewing receipts for each resource class.

## Documented benchmark profile

The acceptance profile is one `lumnid` container and one PostgreSQL 17 container on the same Docker host, warmed database, shadow observations with one explicit read resource and no lease, 1,000 requests, and concurrency 4. Run:

```bash
lumni benchmark --url http://lumnid:8080 --key "$LUMNI_API_KEY" \
  --requests 1000 --concurrency 4
```

The command prints p50, p95, and p99 end-to-end HTTP latency in milliseconds. The P1 local-region targets are p50 below 10 ms, p95 below 30 ms, and p99 below 75 ms. The checked-in run record is in [`benchmark.md`](benchmark.md); CI correctness does not substitute for an environment-specific latency run.
