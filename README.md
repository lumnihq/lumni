# Lumni

**Your AI agents act. Lumni makes sure each action happens once, the way you meant, and proves it did.**

Lumni sits between agents and the things they change: refunds, emails, tickets, deploys. Before an action runs, it checks the action against what's true now and what other agents are doing, so duplicates, stale decisions and collisions are stopped. After it runs, Lumni verifies the result independently instead of trusting the tool call. The console shows what your agents did, where Lumni stepped in, and what that was worth.

Source available and self-hostable ([FSL](LICENSE); the SDKs and CLI are Apache-2.0). New workspaces start in shadow mode: Lumni watches and reports before it enforces anything.

## Get started

**1. See it work.** Sample agents make the twelve classic mistakes (double refunds, stale writes, agents overwriting people) in a sample workspace of your own, kept apart from your real one, and you watch Lumni catch them live:

```bash
npx lumniverse try
```

It opens your browser; if you don't have an account yet, you create one there (14 days free, no card).

**2. Connect your real agent.** In your agent's folder:

```bash
npx lumniverse init
```

It writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` and prints the lines to add where your agent acts. Or let your coding agent wire it in (next section).

**Run it yourself.** With Docker Compose; see the [self-hosting guide](docs/self-hosting.md):

```bash
git clone https://github.com/lumnihq/lumni.git && cd lumni/deploy
cp .env.example .env    # fill in the four secrets
docker compose up -d    # console on http://localhost:5200
```

## Let your coding agent do it

Install the Lumni skill and ask your coding agent to "add Lumni to this agent":

```bash
npx skills add lumnihq/lumni                  # Claude Code, Cursor, Codex, and other agents
```

In Claude Code you can also run `/plugin marketplace add lumnihq/lumni`, then `/plugin install lumni@lumni`. For LLMs, the docs in one file: [lumniverse.com/llms.txt](https://lumniverse.com/llms.txt).

## What's here

| Folder | What it is |
|---|---|
| `cmd/`, `internal/` | The Go API (`lumnid`), worker, gateway and CLI |
| `console/` | The console: plain HTML and JS pages, served by a small Bun server that also handles accounts |
| `sdk/` | Go, TypeScript and Python SDKs |
| `packages/lumni` | `npx lumniverse`: connect, try and demo |
| `skills/` | The Lumni skill for coding agents (Claude Code, Cursor, Codex and others) |
| `deploy/` | Docker Compose for self-hosting, and its Caddy config |
| `examples/` | Example agents, including `agent-fleet`, a set of agents that make realistic mistakes |
| `docs/` | Everything else |

## Develop

```bash
docker compose up --build -d --wait     # API on :8080, console on :5200
node scripts/try-workflow.mjs           # one complete workflow against it
```

For the console alone: `cd console && bun serve.ts` (see [console/README.md](console/README.md)). For the backend test suite without installing Go:

```bash
docker compose -f compose.test.yaml up --build --abort-on-container-exit --exit-code-from tests
```

[CONTRIBUTING.md](CONTRIBUTING.md) lists the checks to run before a change.

## Documentation

- [Getting started](docs/getting-started.md): one complete workflow, end to end.
- [Self-hosting](docs/self-hosting.md): Docker Compose on your machine or a server, HTTPS, updates and backups.
- [API](docs/api.md), [SDKs](docs/sdk.md), [integrations](docs/integrations.md) (MCP, OTLP, GitHub, webhooks).
- [Configuration](docs/configuration.md), [operations](docs/operations.md), [testing](docs/testing.md).
- [How coordination works](docs/coordination.md) and [durable execution](docs/durable-runtime.md).

## Design constraints

- Deterministic hard path: no LLM or vector store decides whether an action runs.
- Tenant-scoped Postgres state and bounded transactions.
- External runtimes (Temporal, Restate, LangGraph, OpenAI Agents) keep their own execution history.
- Correctness depends on explicit effect identity and authoritative verification. Lumni doesn't guess identities from prose.

## License

Lumni's server and console are under the [Functional Source License](LICENSE) (FSL-1.1, Apache-2.0 future license): use it, change it and self-host it for your own agents; you can't offer it as a competing hosted service. Each release becomes Apache-2.0 two years after it ships. The SDKs (`sdk/`), the `lumniverse` CLI (`packages/lumni`), the skills (`skills/`) and the examples are Apache-2.0.

Security reports: see [SECURITY.md](SECURITY.md).
