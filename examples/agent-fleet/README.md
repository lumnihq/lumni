# Agent fleet: the twelve problems, acted out

Small agents doing real-looking work in pretend apps: Stripe, Salesforce, a billing system, Zendesk and a web-research API. Each agent checks with Lumni before it changes anything. There is one scenario for each problem Lumni exists to solve. Each scenario prints what Lumni decided, and the apps write down every problem that slips through.

No LLM, no real services and no dependencies: Node 22 and the Lumni SDK in this repository (`sdk/typescript`). Once `@lumniverse/sdk` is on npm, it uses that instead.

## The twelve

| # | Problem | The story | What Lumni does | How (API) |
|---|---|---|---|---|
| 1 | Duplicate effect | The refund agent retries after a timeout, and the billing bot refunds the same payment too | **DEDUPLICATE**: one refund | `effect_key` (`stripe.refund:<charge>`) and `idempotency_key` |
| 2 | Stale reasoning | The price setter reads v41, thinks, and writes; pricing sync made it v42 meanwhile | **REPLAN**: read again first | `observed_version`, plus the new version recorded by the writer |
| 3 | Human/agent collision | Jess fixes the price in Salesforce by hand while the price setter is thinking | **REPLAN** | A version recorded by a person (`jess`, type `HUMAN`) |
| 4 | Resource contention | Five agents start editing the Globex account | **WAIT**: one at a time | A `WRITE` on the same resource |
| 5 | Cross-agent conflict | The churn agent cancels Umbrella while the upsell agent upgrades them | **WAIT**; then the upsell agent sees the cancellation and drops it | A `WRITE` on the same subscription |
| 6 | Superseded work | Initech cancels while the upsell agent is still researching their upgrade | **REPLAN** `WORK_SUPERSEDED`: the agent stops | `POST /v1/work/{id}/supersede` by the churn agent |
| 7 | Duplicate work | One email lands in two inboxes; the support and triage agents both pick it up | The second **joins** the first's work | A work id named after the goal; `POST /v1/work` returns its owner |
| 8 | Dependency | The support agent wants to close a ticket before its refund is done | **WAIT** `DEPENDENCY_INCOMPLETE` | `POST /v1/dependencies` (`DEPENDS_ON`) |
| 9 | False completion | Stripe says a refund succeeded, then loses it; the agent reports done | **Incident**: outcome mismatch | A verifier, and evidence from a checker |
| 10 | Partial completion | Stripe charged Globex; Salesforce was down and the deal isn't closed | **Incident**: partial commit | Evidence (`FAILED`) in the same work as a committed effect |
| 11 | Cost waste | The quote bot is about to spend $7 drafting a quote that went out this morning | **DEDUPLICATE** before it spends | The same `effect_key`, checked first; costs via `POST /v1/costs` |
| 12 | Unbounded fleet | One research task fans out to 40 sub-agents | **WAIT** `CONCURRENCY_LIMIT`: 5 at a time | A limit (`PUT /v1/config/limits/…`) |
| + | Retry loop | The support agent sends the same reply again and again | Shows on **Runtime** | Loop detection in analytics |
| + | A person decides | A $900 refund matches "Refunds over $500 need a person" | **REQUIRE_APPROVAL**: waits for you | A policy rule; `--scenario approval` |

Some honest notes on what each one needs:

- **Duplicate work (7):** joining is the agent's choice. Lumni gives every agent the same record of who owns which goal (the work id); there's no JOIN decision in the API. If the second agent didn't join, the refund's effect key would still stop the second refund (problem 1).
- **False and partial completion (9, 10):** these need a checker that reads the app afterwards and tells Lumni what it found. In real life that's Stripe's webhook or a verifier Lumni calls. Here, the fleet plays the checker with your admin key, because posting evidence needs the `verification` scope and agent keys don't have it. Without `--admin-key` these two are skipped. The partial-commit incident is added by Lumni's worker on its next sweep, so it can take a moment to appear.
- **Unbounded fleet (12):** the limit is added for you with `--admin-key`, or you can set it on Runtime or Tower. Without a limit, all 40 run at once.
- **Watch-only mode:** every decision, limits included, is only recorded. Agents go ahead, and the apps show the damage.

## Try it

1. Run Lumni and the console (`console/docs/getting-started.md`, steps 1–3).
2. In the console, open **Start** and press **Make a code**. Run its command **in this folder**. It writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` here (git-ignored).

   ```bash
   curl -fsSL http://localhost:5200/init | node --input-type=module - --code LUM-…
   ```

3. Run all twelve. The admin key lets the fleet play the checker and set the research limit:

   ```bash
   node run.mjs --admin-key lumni_local_change_me
   ```

A new workspace only watches. Every agent goes ahead, and you see what Lumni would have stopped:

```
▸ Dependency  One agent finishes step 3 before another finishes step 2.
  support-agent      Lumni would have made it wait: what it depends on isn’t finished (watch-only, so it goes ahead)
  support-agent      Closed ticket #5120
  ✗ Ticket #5120 was closed before its refund landed

Problems in the apps: 10
```

Now, in the console, open **Settings** and set the workspace to **Protect**. Run it again:

```
▸ Superseded work  The customer changes their mind while the agent is still working.
  upsell-agent       Researching an upgrade offer for Initech…
  churn-agent        Cancelled Initech, and marked the upgrade work superseded
  upsell-agent       Lumni stopped it: the work it was doing was superseded
  ● Lumni stepped in: replan (the work it was doing was superseded)

Coverage
  ● Duplicate effect           Lumni stepped in: deduplicate (it was already done)
  ● Stale reasoning            Lumni stepped in: replan (what it read is out of date)
  …
  ● Unbounded agent fleet      Lumni stepped in: wait (as many as you allow are already running)

Problems in the apps: none
```

## Options

```bash
node run.mjs --list                          # the scenarios
node run.mjs --scenario dependency           # one (comma-separate for more)
node run.mjs --scenario approval             # the $900 refund; approve it in Needs you
node run.mjs --rounds 3                      # again, three times
node run.mjs --forever                       # a round every 20 s until Ctrl-C (good for watching Flow)
node run.mjs --fresh                         # forget the apps' saved price list
```

Without `lumni init`, set `LUMNI_URL` and `LUMNI_API_KEY` in the environment instead. Agent keys need the scopes `ingest`, `query` and `admission`.

Each run uses fresh order, quote and charge ids, so you can run it as often as you like against the same Lumni. The price list is kept in `state.json`, so its versions keep moving forward.

## Where to look in the console

| Screen | What you'll see |
|---|---|
| Today, Flow | The day: repeats stopped, waits, stale reads, decisions |
| Needs you | The $900 refund (`--scenario approval`) |
| Tower | Globex and the subscriptions: who held them, who waited |
| Context | The Enterprise price list's versions, including Jess's fix |
| Runtime | The retry loop, and the research limit |
| Money | What the agents spent; the quote bot's $7.20 only shows up in watch-only |
| Journey | Any one action, check by check |

## How it's built

- `lumni.mjs`: the SDK, `.env`, and `guard()`, the one place that turns Lumni's decision into what the agent does (following `docs/sdk.md`). It also has `spend()`, for model costs.
- `apps.mjs`: the pretend apps. They note every real problem: a double refund, a lost change, an early close.
- `scenarios/`: one file per problem. Each exports `{ id, problem, story, actors, run(ctx) }`.
- `run.mjs`: registers the actors, runs the scenarios, and prints the coverage table.
- `stand-in.mjs` and `fleet.test.mjs`: the whole fleet against a stand-in Lumni that decides the way the Go kernel does, in Protect and in watch-only (`node --test`). It's tested against that stand-in, not your server, so your first real run is the real test.

To add a scenario, copy one from `scenarios/`, wrap each side effect in `ctx.guard()`, and add it to `SCENARIOS` in `run.mjs`.
