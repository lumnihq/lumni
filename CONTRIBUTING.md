# Contributing

Use Go 1.27, Bun 1.2+, Node 22+, Python 3.11+ for the full framework suite, and Docker. The core Python SDK supports 3.10+. Keep correctness decisions deterministic and tenant-scoped. Public protocol changes begin in `api/openapi.yaml`, require compatibility tests, and must remain additive within `/v1`.

Before opening a change, run `make lint`, `make test-race`, `make test-integration`, and `make sdk-test`.

For a complete backend test environment without installing Go, run `docker compose -f compose.test.yaml up --build --abort-on-container-exit --exit-code-from tests`.

Console changes also need `make console-test` (tests and types), `make console-build`, and a look at the affected screens in both demo (`?data=mock`) and real (`?data=real`) data. Respect reduced-motion preferences, and keep missing data visibly distinct from the demo. Changes to `npx lumniverse` need `node --test packages/lumni/test/*.test.mjs`. Changes to `deploy/` should still pass `docker compose config` with a filled-in `.env`, and the self-hosting steps in `docs/self-hosting.md`.
