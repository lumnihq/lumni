# Run one complete workflow

Lumni checks whether an agent should perform an action, records the call, and independently verifies its effect. Start with a local fixture before connecting a provider account.

## 1. Start the service

From the repository root, with Docker running:

```sh
docker compose up --build -d --wait
```

Open **http://localhost:5200** and create the owner account. The API is on http://localhost:8080; its development key is `lumni_local_change_me`, unless you set `LUMNI_BOOTSTRAP_ADMIN_KEY` in your environment or `.env`. The console's **Start** page explains the flow and connects your first agent. Development Compose ports bind to loopback.

To run Lumni from published images instead of a checkout, see [self-hosting](self-hosting.md).

If a port is occupied, for example:

```sh
LUMNI_HTTP_PORT=18089 LUMNI_POSTGRES_PORT=55439 \
LUMNI_OTLP_HTTP_PORT=14318 LUMNI_OTLP_GRPC_PORT=14317 \
  docker compose up --build -d --wait
```

Use the matching HTTP port in the following commands. Compose keeps PostgreSQL data across `docker compose stop` and subsequent starts. The first build needs network access for base images and dependencies.

## 2. Run a real API workflow

With Node 22+ installed:

```sh
node scripts/try-workflow.mjs
```

For a different service URL or key, supply `LUMNI_URL` and `LUMNI_API_KEY`. For a self-hosted install, the key is `LUMNI_ADMIN_KEY` in `deploy/.env`.

This script uses the real backend and PostgreSQL, plus a separate local fixture payment ledger. It:

1. Registers an example agent, work item, verifier and one operation-specific enforcement rule. It does not change your workspace's global mode.
2. Admits and executes a simulated $100 refund, creating exactly one external ledger record.
3. Demonstrates that a successful tool call is not automatically verified.
4. Reads the ledger separately and records authenticated observer evidence.
5. Submits a duplicate action and asserts that no second ledger mutation occurs.
6. Prints links to the persistent task, findings and verified outcomes in your console.

Expected ending: **PASS**, **External mutations: 1**, and independently verified outcomes. Every invocation has unique fixture IDs, so it can be run repeatedly. No real payment provider, model credential or agent framework is required. Example records remain in the selected workspace so you can inspect them. The script uses EVENT evidence; production refunds should use the authoritative Stripe verifier and correlation described in [durable runtime](durable-runtime.md).

## 3. Run the recovery and correctness suite

This command builds the test toolchain inside Docker and creates a separate PostgreSQL database. It needs no Go installation and publishes no host ports:

```sh
docker compose -f compose.test.yaml up --build \
  --abort-on-container-exit --exit-code-from tests
```

It runs Go race, unit and Postgres integration tests, including the six scenarios, unknown-call recovery, native worker replay, verification and migration tests. The first compile can take several minutes. The command exits nonzero if a test fails. Its PostgreSQL data is temporary; it does not use your application database.

To clean up the test containers:

```sh
docker compose -f compose.test.yaml down
```

Console and SDK suites are separate:

```sh
(cd console && bun test)
npm ci --prefix sdk/typescript
npm test --prefix sdk/typescript
python3 -m venv .venv
.venv/bin/python -m pip install pytest pytest-asyncio './sdk/python[langgraph,openai]'
.venv/bin/python -m pytest sdk/python
```

Use Python 3.11+ for all framework tests. Restate-server and ClickHouse-profile tests need the environment described in their integration tests; CI configures those separately. [Release validation](release-validation.md) records which environments were actually tested.

## Console development and the demo

```sh
cd console && bun serve.ts
```

Open `http://localhost:5200`. It signs in with the admin key in `console/.env` and switches between your workspace (`?data=real`) and a made-up day (`?data=mock`). `npx lumniverse demo` serves the demo alone, with no API. See [console/README.md](../console/README.md).

## Connect a real workflow

Start with one operation in a provider's test environment. Route existing MCP/HTTP tools through the gateway, or register a versioned Go/Python/TypeScript SDK worker. Keep Temporal, Restate, LangGraph or OpenAI Agents as the owner of orchestration when using an external-runtime adapter.

In Setup, confirm the resource and effect identity, configure an authoritative verifier, and bind expected values to the operation. For a refund, include amount, currency and correlation with the provider's refund record. Observe traffic in shadow mode, then selectively enforce the operation after testing duplicate, delayed and timeout-after-write cases.

Lumni can coordinate accurately identified effects. It does not infer reliable identities from arbitrary prose, guarantee correctness of uninstrumented calls, or prove absence from a temporary missing read-back. Unknown mutations remain held until evidence or a supported upstream idempotency contract makes retry safe. Review [OSS readiness](oss-readiness.md) before a production rollout.
