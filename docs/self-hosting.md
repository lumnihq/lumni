# Self-hosting Lumni

Lumni Cloud ([app.lumniverse.com](https://app.lumniverse.com)) is the quickest way to use Lumni. To run it on your own infrastructure instead, use Docker Compose. Everything runs in four containers: the API (`lumnid`), its worker, the console, and Postgres.

Self-hosting is free under the [Functional Source License](../LICENSE): you can run Lumni for your own agents, in your company, and change it. You can't offer it to others as a competing hosted service.

## Requirements

- Docker with Compose v2 (`docker compose version`)
- 2 CPUs and 4 GB of memory for a small team; Postgres needs disk for your agents' history
- To reach it from other machines: a server with ports 80 and 443 open, and a domain

## 1. Get the files

```bash
git clone https://github.com/lumnihq/lumni.git
cd lumni/deploy
cp .env.example .env
```

## 2. Fill in the secrets

Open `.env` and set the four secrets. Generate each with `openssl rand -hex 32`:

```bash
sed -i.bak \
  -e "s/^LUMNI_ADMIN_KEY=$/LUMNI_ADMIN_KEY=lumni_$(openssl rand -hex 24)/" \
  -e "s/^LUMNI_SECRET_ENCRYPTION_KEY=$/LUMNI_SECRET_ENCRYPTION_KEY=$(openssl rand -hex 32)/" \
  -e "s/^LUMNI_CONSOLE_SECRET=$/LUMNI_CONSOLE_SECRET=$(openssl rand -hex 32)/" \
  -e "s/^POSTGRES_PASSWORD=$/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" \
  .env && rm .env.bak
```

| Secret | What it protects |
|---|---|
| `LUMNI_ADMIN_KEY` | The first workspace's admin key; the console uses it to give each person their own scoped key |
| `LUMNI_SECRET_ENCRYPTION_KEY` | Secrets the API stores (webhook and integration keys) |
| `LUMNI_CONSOLE_SECRET` | Keys the console stores for its accounts |
| `POSTGRES_PASSWORD` | The database |

Keep `.env` private and backed up: without `LUMNI_SECRET_ENCRYPTION_KEY` and `LUMNI_CONSOLE_SECRET`, stored keys can't be read back.

## 3. Start it

```bash
docker compose up -d
docker compose ps        # lumnid and postgres should be healthy
```

Open http://localhost:5200. The first visit creates the owner account; invite everyone else from **Settings → People**.

## 4. Connect an agent

In the agent's folder:

```bash
npx lumniverse init --console http://localhost:5200
```

It opens your browser to approve, then writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` in that folder. To see Lumni at work first, `npx lumniverse try --console http://localhost:5200` runs sample agents in your workspace.

## On a server, with a domain

1. Point two DNS names at the server, for example `app.example.com` (the console) and `api.example.com` (the API), and open ports 80 and 443.
2. In `.env`, use the "server with a domain" block: `COMPOSE_PROFILES=https`, the two URLs and the two hosts.
3. `docker compose up -d`. Caddy gets and renews HTTPS certificates by itself.

**Create the owner account straight away.** Until it exists, whoever opens the console first becomes the owner.

Already running nginx or another proxy on 80/443? Leave `COMPOSE_PROFILES` empty, keep `LUMNI_BIND=127.0.0.1`, and proxy `app.` to `127.0.0.1:5200` and `api.` to `127.0.0.1:8080`. Turn off response buffering for both, since the console's live view streams.

## Updating

```bash
git pull
docker compose pull
docker compose up -d
```

The API applies database migrations when it starts. Back up first (below). To stay on one version, set `LUMNI_VERSION` (for example `0.3.0`) instead of `latest`.

## Backups

```bash
docker compose exec -T postgres pg_dump -U lumni -d lumni | gzip > lumni-$(date +%F).sql.gz
docker compose exec -T console sh -c 'bun scripts/backup-db.ts /tmp/b.db && cat /tmp/b.db' > console-$(date +%F).db
```

The first is your agents' history and the workspace; the second is the console's accounts. Keep `.env` with them.

## Optional settings

All of these live in `.env`; apply changes with `docker compose up -d`.

- **Sign in with Google or GitHub:** create an OAuth app with the callback `<LUMNI_APP_URL>/console/auth/oauth/google/callback` (or `.../github/callback`), then set the client ID and secret.
- **Email alerts:** `LUMNI_SMTP_ADDR` (host:port, STARTTLS), `LUMNI_SMTP_FROM`, and a user and password.
- **Slack:** through Composio, with `LUMNI_COMPOSIO_API_KEY` and `LUMNI_COMPOSIO_SLACK_AUTH_CONFIG_ID`.
- **Analytics at large volume:** add `analytics` to `COMPOSE_PROFILES` (`https,analytics`), and set `LUMNI_ANALYTICS_BACKEND=clickhouse` and `CLICKHOUSE_PASSWORD`. Postgres stays the system of record.

## Troubleshooting

- `docker compose logs lumnid` (or `console`, `worker`, `postgres`) shows what each part is doing.
- `curl http://localhost:8080/v1/health/ready` should answer `{"status":"ready"}`.
- "required variable … is missing a value": a secret in `.env` is still empty.
