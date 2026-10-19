# Lumni console

The outcome-first console for Lumni: fifteen screens about what your AIs did, where Lumni stepped in, and what it was worth. Every screen runs on one of two kinds of data:

- **Demo:** a made-up world generated in the page, at three sizes of client (2.2k, 48k and 2.1M actions a day). It needs nothing else running.
- **Your workspace:** today's real records from your Lumni API. This console's own server reads them and builds the same object the demo builds, so every screen works the same on both.

Switch between them in the top bar, or with `?data=mock` / `?data=real`. Real data is only offered when the server can reach a Lumni API.

## Run it

**New here? [docs/getting-started.md](docs/getting-started.md)** walks through a real setup end to end: the Lumni API in Docker, this console, connecting an agent with a code, a realistic day of traffic (`scripts/try-real.mjs`), and every control.

```bash
cp .env.example .env     # point LUMNI_API_URL at your Lumni, add an admin key
bun serve.ts             # http://localhost:5200
```

Other commands:

| Command | What it does |
| --- | --- |
| `bun test` | Server, world and page tests, against a fake Lumni API |
| `bun run typecheck` | TypeScript, for the server and tests |
| `bun run build` | Builds `dist/`; `bun serve.ts --prod` serves it |
| `bun test/real/serve-fake.ts` | A fake Lumni API on :8080 with a busy day, for working on real mode without Docker |
| `bun scripts/capture.mjs` | Screenshots of every screen, demo and real, into `shots/` |

### Installing without the registry

`bun run seed` installs TypeScript from the local npm cache. That's the only dependency: the pages are plain HTML and JS, and Bun runs the server.

## Connecting an agent

In the agent's folder, run the one command from `/start`. It needs nothing first:

```bash
curl -fsSL https://console.example.com/init | node --input-type=module -
```

It shows a short code and opens the console's `/device` page, where you check the code matches and press **Connect** (a browser login, like `gh auth login`). For machines with no browser nearby, make a one-time code on Start and add `--code LUM-…`; the `lumni` binary from the release archive takes `--code` too:

```bash
lumni init --url https://api.example.com --code LUM-…
```

Either way, the code is traded with the API itself (`POST /v1/onboarding/exchange`), so no key ever appears in a shell, a browser or a screenshot. `/start` lights up as each step happens:
1. the command ran (on which machine, in which folder);
2. the agent connected;
3. its first check;
4. its first step-in or proof.

It hears about new agents and checks from Lumni's change stream (`GET /v1/graph/stream?types=actor.created,action.checked`) the moment they happen, and still polls slowly in case the stream drops.

Safety, all enforced by the API:
- A code works once.
- Only a hash of each code is kept.
- Exchanges are limited to 10 a minute per machine.
- Nothing is made until a code is used, so an unused code leaves no key behind.
- Agent keys get `ingest`, `query`, `runtime` and `admission` (admission is what lets them check actions).

**Older Lumni APIs**, without onboarding codes: this console's server makes the code and the key itself, and trades the code at `POST /console/onboarding/exchange`. Those codes are shorter (`LUM-XXXX-XXXX`) and live in the console's memory; unused ones have their key revoked. `lumni-init.mjs` tells the two kinds of code apart by length.

`/init` serves `scripts/lumni-init.mjs`. It needs only Node 22. In order, it:

1. trades the code with the API, or with this console for its short codes (or uses `LUMNI_API_KEY`);
2. checks the key;
3. reads the project (Python, TypeScript/JavaScript or Go, and its agent libraries);
4. adds `LUMNI_URL` and `LUMNI_API_KEY` to `.env`, without overwriting, and git-ignores `.env`;
5. wraps stdio MCP servers in Cursor, Claude Desktop and VS Code configs with Lumni's gateway, keeping a backup of each config;
6. registers the agent and sends one hello check;
7. prints the lines to add.

`--dry-run` changes nothing, and running it twice changes nothing the second time.

## The screens

| Group | Path | Screen |
| --- | --- | --- |
| Watch | `/start` | Connect an agent with one command, and watch it arrive |
| | `/flow` | The whole day as a living transit map: volume, step-ins, apps, proof |
| | `/today` | The day so far, the catch of the day, and what needs you |
| | `/day-map` | Who touched what, with a brush over the whole day |
| | `/journey?id=` | One action's trip through the four checks, the app and the proof |
| | `/runtime` | Vital signs for every AI: loops, runaways, and what's next |
| | `/context` | Shared facts, and which AIs read an old version |
| Coordinate | `/tower` | Air traffic control for shared things |
| | `/needs-you` | Decisions grouped by the rule that asked, with real approve and deny |
| | `/fixes` | Patterns Lumni found, and rules that fix them |
| | `/autonomy` | How strictly each AI's work is enforced, and whether it has earned more |
| Prove | `/money` | Cost per proven outcome, waste, and what Lumni gave back |
| | `/incident` | The worst minutes of the day, second by second |
| | `/without` | The same day with nothing stepping in |
| | `/wrapped` | The day or week as a story to share |
| | `/share` | Proof of one action |

`/sign-in` signs you in to your workspace. `/` goes to Today.

## How it's built

```
app/                 one HTML file per screen, plain JS, no build step
  lib/shell.js       top bar, which data, sign-in, the world loader, writes
  lib/world-mock.js  the demo world at three volumes
  lib/base.css       tokens and shared parts
server/              this console's own server (Bun)
  index.ts           /console/config.json, sign-in, /v1/console/*, and a pass-through for the rest of /v1
  app.ts             pages at clean paths, and the lib
  console/views/world.ts    GET /v1/console/world: the real world, from the Lumni API
  console/views/journey.ts  GET /v1/console/journey/:id: one real action's trip
  console/…          the other views, loader, rules and writes
test/                bun tests; test/real has the fake Lumni API and its days
scripts/             build, screenshots, and the offline installer
docs/                the design rules (BRIEF.md) and the guide for building a screen (builders.md)
```

### The world

Every screen calls `Shell.boot({ nav, onWorld })` and draws from one object, `w`:

- **Totals and kinds:** step-ins by kind, and "would have" step-ins from rules that are only watching.
- **The day:** the reasons behind step-ins, hour by hour.
- **Who and where:** teams, apps, and the fleet of AIs.
- **Money:** spend and waste.
- **Decisions:** waiting decisions, grouped by the rule that asked.
- **Coordination:** contended things and shared facts.
- **Incident:** the day's worst burst, if one stood out.

`test/shape.test.ts` keeps the demo and real worlds the same shape.

The real world comes from the Console API's exact aggregates (`server/console/views/world2.ts`): `GET /v1/analytics/day`, approvals with `include=action,rule`, FinOps, billing, the spend limit, burst incidents, anomalies, freshness, contended resources and their leases, version history, Autopilot grants and `analytics/daily`. It's exact at any volume. On an older API without them, it falls back to reading snapshots (`views/world.ts`), which is built from:
- the day's actions, joined with their decisions, outcomes, approvals and costs;
- actors and agents, checkers (verifiers), policy rules and enforcement;
- FinOps.

Where the API has no source for something, real mode leaves it empty and the screen says so. Examples: how old each AI's reads were, Lumni's own price, history before today. It never invents a number.

### Writes

In real mode, buttons do the real thing through `Shell.write`:
- **Needs you:** approve or deny decisions, one at a time or many at once (`POST /v1/approvals/decide`).
- **Fixes:** back-test a rule on the last 7 days, edit its threshold (with a version check), protect it, or remove it. Add rule templates. Switch an app's checker on.
- **Autonomy:** set an operation's level (Watch, Warn or Enforce) and undo back to the workspace default. Grant Autopilot and take it back. History comes from the audit log.
- **Runtime:** pause or resume one AI, cap it at one action a minute, or protect its operation.
- **Tower:** hold everything on a shared thing, let it go, and raise or lower how many AIs can work on it at once.
- **Share:** make proof links, revoke them, and send them by Slack or email.
- **Settings:**
  - pause every AI;
  - list and revoke keys;
  - set a daily spend limit (slow down or hold);
  - set where notifications go;
  - list limits and Autopilot grants;
  - read the activity log.
- **Retry outcomes.**

Writes carry the session's CSRF token, and the server only accepts them from the console's own origin. Destructive ones (deny all, pause every AI, remove a rule, revoke a key, hold everything) ask for a second click. If a write fails, `Shell.lastError` keeps the reason for the screen to show.

Against an older Lumni API (`w.source === 'snapshots'`), these controls stay visible but disabled, titled "Needs a newer Lumni API".

### Signing in

`LUMNI_CONSOLE_AUTH` picks how:

- **`accounts`** (the Docker image's default): people have their own accounts, in a SQLite file (`LUMNI_CONSOLE_DB`). The first visit creates the owner. Owners and admins invite others from Settings → People, as owner, admin, member or viewer, and can send a password-reset link. GitHub and Google sign-in turn on with `LUMNI_CONSOLE_GITHUB_CLIENT_ID`/`_SECRET` and `LUMNI_CONSOLE_GOOGLE_CLIENT_ID`/`_SECRET`. Everyone gets their own API key, scoped to their role and revoked when they leave; the console mints it with the workspace's admin key, which, like every stored key, is sealed with `LUMNI_CONSOLE_SECRET`. `LUMNI_CONSOLE_SIGNUP=open` lets anyone sign up into a new workspace (Lumni Cloud; needs `LUMNI_CONSOLE_PLATFORM_KEY` and the API's `POST /v1/tenants`).
- **`key`** (the default for `bun serve.ts`): the first visit signs in with the admin key in `.env` (set `LUMNI_CONSOLE_AUTO_SIGN_IN=false` to ask instead), and people can paste their own API key on `/sign-in`. For development and tests.

Either way, keys never reach the browser: the server trades them for the API's session cookie, scoped to this origin.

## Configuration

See `.env.example`. Variables are `LUMNI_CONSOLE_*`. To show Lumni's return on Money, set `LUMNI_CONSOLE_PRICE_PER_ACTION`.
