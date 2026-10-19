# Release benchmark record

Date: 2026-08-28

Profile: Docker Desktop on an ARM64 host; one distroless `lumnid` container and one PostgreSQL 17 Alpine container on the same Docker network; warmed existing database; 1,000 authenticated `POST /v1/actions/observe` requests; one explicit read resource per request; concurrency 4; structured access logging enabled.

Command:

```bash
docker compose exec -T lumnid /usr/local/bin/lumni benchmark \
  --url http://127.0.0.1:8080 --key "$LUMNI_API_KEY" \
  --requests 1000 --concurrency 4
```

Result:

```json
{"concurrency":4,"p50_ms":5.225,"p95_ms":6.376,"p99_ms":8.874,"requests":1000}
```

Acceptance: passed against targets p50 < 10 ms, p95 < 30 ms, and p99 < 75 ms. This record characterizes the documented single-host profile, not every production topology; rerun it with the release image and target-region database before deployment.
