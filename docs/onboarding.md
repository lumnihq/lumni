# Backend onboarding and releases

The Go backend implements D1 and D3 from the console audit. The native `lumni init` command implements the CLI part of D2 without Node. These APIs are independent of any console server. No frontend integration is required or included.

An administrator creates a short-lived code:

```http
POST /v1/onboarding/codes
Authorization: Bearer <admin-key>
Content-Type: application/json

{"scopes":["ingest","query","runtime","admission"],"expires_in_minutes":15,"actor_hint":"support-agent"}
```

The response is `201 {"code":"LUM-…","expires_at":"…"}`. Omitted scopes default to `ingest`, `query`, `runtime`, and `admission`; admission is needed for the hello check and normal action checks. Explicit scope lists are preserved. Expiry must be 1–15 minutes. Codes have 128 bits of randomness and only their SHA-256 hashes are stored. A code created by a subsequently revoked or expired administrator key cannot be exchanged.

Run the native CLI from the backend release archive:

```sh
lumni init --url https://api.example.com --code LUM-… --dir ./my-agent
```

The CLI exchanges the code at `POST /v1/onboarding/exchange`, detects Python, JavaScript/TypeScript, and Go projects, saves the returned key in an owner-only `.env`, adds `.env` to `.gitignore`, wraps stdio MCP servers when `gateway` is available, and records a local hello action. It never installs dependencies or modifies agent source code. Original MCP configuration is retained as an owner-only `.lumni-backup`; existing unrelated settings and upstream credentials are preserved. The binary archive places `gateway` beside `lumni`, so a PATH installation works without a separate gateway download.

Use `--dry-run` to inspect setup without file changes or network calls. `--gateway PATH`, `--actor ID`, `--no-mcp`, and `--no-hello` customize setup. Existing credentials are preserved; rerunning without a code reuses `.env`. Alternatively set `LUMNI_API_KEY` and `LUMNI_URL` in the environment. API keys are never printed by `init`. The newly exchanged credential is saved before further network calls so it remains recoverable after an interruption.

Exchange accepts `{"code":"LUM-…","host":"dev-laptop","project":"billing-agent"}` without bearer authentication. The response is `200 {"api_key":"lm_…","key_id":"…","tenant_name":"…","api_url":"…","actor_hint":"…"}`. A transaction locks the code, creates exactly one scoped key, and marks the code used. Unknown, used, expired, and revoked-issuer codes all return `404 NOT_FOUND`. Attempts are limited to ten per minute per transport peer, including failed exchanges; forwarded IP headers cannot bypass the limit. Set `LUMNI_PUBLIC_BASE_URL` to the externally reachable API origin when behind a proxy. Credential responses use `Cache-Control: no-store`.

Administrators can read `GET /v1/onboarding/codes/{code}/status`: `{"used":false,"used_at":null,"actor_id":null,"host":null,"project":null,"key_id":null}` before exchange. After exchange, it returns the use timestamp, hinted or registered actor, exact submitted `host` and `project`, and the issued `key_id`. Exchange saves these values atomically with key creation. Codes exchanged before migration 24 retain null host/project; their key names are not parsed to guess them. Status is tenant-scoped and requires admin scope; the code is redacted from access logs and responses are never cached.

`GET /v1/actors?since=<RFC3339>&type=AGENT` lists newly created agents with the existing `items`/`next_cursor` pagination. `since` aliases inclusive `from`; preserve filters while following a cursor. Supported types are `AGENT`, `HUMAN`, `SERVICE`, `CRON`, `WEBHOOK`, `CI`, `WORKFLOW`, and `SYSTEM`.

`GET /v1/graph/stream?types=actor.created,action.checked` filters the existing SSE stream. Both events are written transactionally with the originating insert. Actor updates do not emit another `actor.created`. Events retain the existing `invalidate` SSE envelope and identify the actor or action in `entity_id`. Resume with `Last-Event-ID` or `cursor`; the query parameter takes precedence when both are supplied, so a client can resume even when a proxy drops the header. An explicit `cursor=0` replays retained events. Without a cursor, the stream starts at the current tenant head. If retention has removed the requested position, a `reset` event moves the cursor immediately before the first retained event, then replay includes that event. Deleted events cannot be recovered; consumers can refresh their snapshot on reset. Existing unfiltered consumers remain supported.

## Packaging and publication

Public registry checks on October 15, 2026 returned HTTP 404 for `@lumniverse/sdk` on npm and `lumni-sdk` on PyPI. Source packages are version 0.2.0. Preparing artifacts does not publish them.

```sh
npm ci --prefix sdk/typescript
python3 -m pip install build
sh scripts/package-backend-release.sh
```

This produces Linux/macOS archives for amd64/arm64, the TypeScript SDK tarball, the Python wheel, and `SHA256SUMS` in `dist/backend-release`. Each archive contains `lumni`, `gateway`, `lumnid`, `worker`, and this guide. The script never builds or copies a frontend. `RELEASE_GO`, `RELEASE_PYTHON`, `RELEASE_TARGETS`, and `RELEASE_OUT_DIR` support local build environments and a single-platform smoke build.

The manual **Backend release** workflow prepares and validates the same artifacts by default. To publish, configure `NPM_TOKEN` and `PYPI_API_TOKEN` in the GitHub `release` environment, ensure the account owns the package names, and run the workflow with its `publish` input enabled. It publishes both SDKs and creates a draft GitHub release with gateway and CLI binaries. Registry publication is irreversible for a version; bump both SDK versions together for the next release. Public publication has not been performed as part of this backend implementation.
