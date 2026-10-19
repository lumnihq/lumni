# Getting started: a real Lumni, end to end

This takes you from nothing to a console full of real data, on your own machine, in about 15 minutes. You'll:

1. run the Lumni API;
2. run this console against it;
3. connect an agent with a one-time code;
4. play a realistic day of agent traffic;
5. watch Lumni in watch-only mode, then turn protection on and see it step in for real;
6. use the console's controls (approve, pause, hold, limits, proof links).

Every number you see along the way is real. Nothing in real mode is made up.

**You need:** Docker (for the API, Postgres and ClickHouse), [Bun](https://bun.sh) 1.1 or newer (for the console), and Node 22 (for the agent-side scripts).

---

## 1. Run the Lumni API

From the repository root (`lumni/`):

```bash
docker compose up -d
```

This starts Postgres, ClickHouse, the API (`lumnid`) and the worker. When it's ready, this answers `200`:

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/v1/health/ready
```

The compose file makes one workspace, "Local Development", with an admin key **`lumni_local_change_me`**. It starts in **watch-only mode** (`LUMNI_MODE: SHADOW`): Lumni records what it would have done and lets every action through. That's the safe way to start with real agents, and step 5 shows how to switch.

To use your own admin key, set `LUMNI_BOOTSTRAP_ADMIN_KEY` before the first `docker compose up`. Set `LUMNI_SECRET_ENCRYPTION_KEY` too for anything that isn't your laptop.

## 2. Run the console against it

```bash
cd console
cp .env.example .env
```

Edit `.env`:

```bash
LUMNI_API_URL=http://localhost:8080
LUMNI_CONSOLE_ADMIN_KEY=lumni_local_change_me
LUMNI_CONSOLE_DATA_MODE=real
LUMNI_CONSOLE_WORKSPACE_NAME=Local Development
```

Then start it:

```bash
bun serve.ts
```

Open <http://localhost:5200>. The console signs in with the admin key on its own server; the key never reaches the browser. The top bar shows **Your workspace** selected. If it only offers **Demo**, the console can't reach the API: check step 1, then wait 15 seconds (it re-checks the API's health every 15 s).

The screens are quiet for now, because no agent has done anything yet.

## 3. Connect an agent

Pick a folder for an agent. Any project works; an empty one is fine for this walkthrough:

```bash
mkdir -p ~/lumni-try && cd ~/lumni-try
```

In the agent's folder, run the command from **Start** (Watch menu). It needs no code:

```bash
curl -fsSL http://localhost:5200/init | node --input-type=module -
```

It prints a short code like `WDJB-MJHT` and opens the console in your browser. Check the code matches, name the agent, and press **Connect**. The terminal carries on by itself. Any browser where you're signed in to the console works, even on another machine, so this works over SSH too.

No browser at all (CI, a script)? Press **Make a one-time code** on Start and add `--code LUM-…` to the command.

No Node? The **Lumni CLI** tab shows the same thing for the `lumni` binary from Lumni's release archive (`sh scripts/package-backend-release.sh` builds it):

```bash
lumni init --url http://localhost:8080 --code LUM-…
```

**Using Claude Code, Codex or Cursor?** Pick the **Coding agent** tab instead. It gives you a prompt to paste into your coding agent. The coding agent runs the same command, then reads `/init/guide.md` and wraps each place your agents change something with a Lumni check.

The command:

1. trades the code with the API for a fresh agent key (scopes `ingest`, `query`, `runtime` and `admission`), once;
2. checks the key against the API;
3. looks at the project (language, agent libraries, MCP configs);
4. writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env`, without changing anything already there, and git-ignores `.env`;
5. wraps any stdio MCP servers with Lumni's gateway, keeping a backup of each config;
6. registers the agent and sends one hello check.

Watch the Start page while it runs: each step lights up as it happens, including which machine and folder the code was used from.

`--dry-run` shows what it would do and changes nothing. Running it twice changes nothing the second time.

**About the code:**
- It works once and lasts 15 minutes. No key exists until it's used, so an unused code leaves nothing behind.
- If it says the code isn't valid, make a new one.
- The agent trades the code with the API directly, so it must reach the API at the address `/start` shows (`LUMNI_CONSOLE_PUBLIC_API_URL`, else `LUMNI_API_URL`). The API's `LUMNI_PUBLIC_BASE_URL` decides the address it hands back.
- The key appears in Settings, under Agents and keys, as "<folder>, set up on <machine>". You can revoke it there.

## 4. Play a realistic day

**Want real agent code instead?** [`examples/agent-fleet`](../../examples/agent-fleet/README.md) acts out all twelve problems Lumni solves (duplicate effects and work, stale reads, human collisions, contention, conflicts, superseded work, dependencies, false and partial completion, cost waste, runaway fleets) with small agents that call Lumni through the SDK. Run `lumni init` in that folder, then `node run.mjs --admin-key lumni_local_change_me`. The script below is the quicker, scripted version.

Real agents take a while to produce an interesting day, so `scripts/try-real.mjs` plays one. It uses the key `lumni init` just wrote, through the same public API your agents use. Each round:

| What happens | What Lumni should do |
|---|---|
| The refund agent sends the same $60 refund twice (a retry after a timeout) | Stop the second one: it's already done |
| Two deploy bots go for the checkout site at once | Let one in; the other waits its turn |
| The price setter quotes from a price list that changed after it read it | Send it back to read the latest |
| The refund agent tries a $900 refund | Hold it for a person (with the rule below) |
| The support agent answers three tickets | Let them through |

Still in the agent's folder:

```bash
node ~/path/to/lumni/console/scripts/try-real.mjs --rounds 5 --admin-key lumni_local_change_me
```

- `--admin-key` adds one policy rule first, "Refunds over $500 need a person". An agent key can't change rules, which is why this uses the admin key. Leave it out if you'd rather add the rule from the console.
- `--rounds 5` plays the day five times, 4 seconds apart (`--gap` changes the spacing). More rounds make fuller charts.

It prints what Lumni decided at each step. In watch-only mode you'll see lines like:

```
Refund agent   the same refund again: would have been stopped as already done (watch-only, so it went ahead)
Release bot    deploy it at the same time: would have waited its turn (watch-only, so it went ahead)
```

## 5. Read the day in the console

Refresh the console (or press **Read it again** in the strip under the top bar). The strip says how many times rules were only watching. Then try these screens:

**Watch**
- **Today:** the day so far, in plain sentences, with what Lumni stepped in on.
- **Flow:** every action, grouped by what Lumni did with it.
- **Day map:** which AI touched what, and when.
- **Journey:** one action, check by check. Open any action from another screen to land here.
- **Runtime:** which AIs are looping, running away or acting on old reads.
- **Context:** the Enterprise price list's versions, and the price setter acting on an old one.

**Coordinate**
- **Tower:** the checkout site, who held it and who waited.
- **Needs you:** the $900 refunds (in watch-only mode, as decisions it would have asked you about).
- **Fixes:** findings from today, and rules to back-test on the last 7 days.
- **Autonomy:** each AI's level and record.

**Prove**
- **Money:** spend, waste and the return.
- **Incident:** the worst minutes of the day.
- **Without Lumni:** the same day, with nothing stepping in.
- **Wrapped:** the week as a story.
- **Share:** a proof of one action.

### Turn protection on

Open **Settings**, and under Workspace choose **Protect** (`PUT /v1/config/mode` `ENFORCE`). You can also move one AI at a time on **Autonomy**. Then play the day again:

```bash
node ~/path/to/lumni/console/scripts/try-real.mjs --rounds 3
```

Now the decisions are real:

```
Refund agent   the same refund again: was stopped: it was already done
Release bot    deploy it at the same time: waited its turn
Price setter   quote from the list it read earlier: was sent back to read the latest
Refund agent   refund $900 on ch_…: is waiting for a person
```

## 6. Use the controls

Every control writes to the real API, and every write lands in the activity log (Settings, at the bottom). Things that remove or stop something ask for a second click.

| Try | Where | Then check |
|---|---|---|
| Approve or deny the $900 refunds, one or all | Needs you | The decision appears in the activity log |
| Pause the refund agent | Runtime | Play a round: its checks are refused (`ACTOR_PAUSED`). Resume it after |
| Pause every AI | Settings, Workspace | A banner appears on every screen |
| Hold everything on the checkout site, then let it go | Tower | Deploys wait while it's held |
| Raise the checkout site's limit to 2 | Tower | Both deploy bots go in together |
| Cap the deploy bot at one action a minute | Runtime | Limits and holds, in Settings |
| Back-test the refund rule at $250 instead of $500, then save it | Fixes | The rule's version and the activity log |
| Give the support agent Autopilot, then take it back | Autonomy (once it's on Protect) | The Autonomy history chart |
| Set a daily spend limit | Settings, Spend | Money shows when it would be reached |
| Make a proof link for one action | Share (or **Share proof** on Journey) | Open the link in a private window |
| Send alerts to Slack or email | Settings, Notifications | Use **Send a test** |
| Revoke the agent's key | Settings, Agents and keys | The next try-real run is refused |

**What some controls need:**
- **Proof links:** the API needs `LUMNI_PUBLIC_BASE_URL` (in compose) set to an address the reader can open.
- **Slack:** a Composio Slack connection (`LUMNI_COMPOSIO_API_KEY` and `LUMNI_COMPOSIO_SLACK_AUTH_CONFIG_ID`).
- **Email:** SMTP on the worker (`LUMNI_SMTP_ADDR`, `LUMNI_SMTP_FROM` and the rest).
- **Money's return:** set `LUMNI_CONSOLE_PRICE_PER_ACTION` in the console's `.env` if your API doesn't report billing.

## 7. Your real agents

`try-real.mjs` is only a stand-in. In your own agents, the pattern is the same three calls around anything with side effects:

1. **Check:** `POST /v1/actions/check` with the actor, the operation, its arguments and the resources it touches (with `observed_version` for anything it read first). Do the action only if the decision is `ALLOW` or `WARN`, or a `WOULD_…` decision in watch-only mode.
2. **Start:** `POST /v1/actions/{id}/start` with the lease token.
3. **Result:** `POST /v1/actions/{id}/result`, saying how it went.

`lumni init` prints a snippet for your language at the end. The SDKs aren't on npm or PyPI yet: install them from the release archive (`npm install ./lumni-sdk-0.2.0.tgz`, `pip install ./lumni_sdk-0.2.0-py3-none-any.whl`), or call the API directly, as `scripts/try-real.mjs` does. The archive also has the `gateway` binary next to `lumni`.

---

## When something's off

| What you see | Why, and what to do |
|---|---|
| Only **Demo** in the top bar | The console can't reach `LUMNI_API_URL`. Check `curl …/v1/health/ready`, then wait 15 s |
| Sign-in fails, or Your workspace asks for a key | `LUMNI_CONSOLE_ADMIN_KEY` is empty or doesn't match the API's `LUMNI_BOOTSTRAP_ADMIN_KEY` |
| "That code isn't valid" from `lumni init` | It was used, or it's older than 15 minutes. Make a new one |
| "Too many onboarding attempts" from `lumni init` | 10 tries a minute from one machine. Wait a minute |
| `lumni init` can't reach Lumni | Agents use `LUMNI_CONSOLE_PUBLIC_API_URL` (or `LUMNI_API_URL`). If the console reaches the API by a Docker-internal name, set the public one |
| Today says “Not live” | The console's server is older than the page: stop `bun serve.ts` and start it again. Pages reload from disk; the server doesn't |
| Screens stay empty after try-real | Press **Read it again**. Today is the console's local day; actions from before midnight are on yesterday |
| Controls are disabled, titled "Needs a newer Lumni API" | The API predates the Console API. The console still reads the day, from snapshots, but can't write |
| The source bar says "3 new checks, read again" | New activity arrived on Lumni's change stream since the screen was read. Press it to read again; screens never re-read under you |

## Before you share it with anyone

- Change `LUMNI_BOOTSTRAP_ADMIN_KEY` and `LUMNI_SECRET_ENCRYPTION_KEY` from the compose defaults.
- Set `LUMNI_CONSOLE_PUBLIC_URL` (where agents reach this console, for codes) and `LUMNI_CONSOLE_PUBLIC_API_URL` (where agents reach the API).
- Set `LUMNI_PUBLIC_BASE_URL` on the API, for proof links.
- Against an API from before onboarding codes, run one console instance per workspace: its fallback codes live in memory.
- Build once and serve the built files: `bun run build`, then `bun serve.ts --prod`.
