# Put Lumni in front of this project's AI agents

You're a coding agent (Claude Code, Codex, Cursor or similar) setting up Lumni in this repository. Lumni checks each action an AI agent takes *before* it changes anything outside the program, and stops repeats, stale writes, clashes between agents, and work that's no longer wanted. Follow these steps in order. Don't skip the verification at the end.

Lumni's API: `__LUMNI_URL__`. Console: `__LUMNI_CONSOLE__`.

## 1. Connect (once)

If `.env` doesn't already have `LUMNI_URL` and `LUMNI_API_KEY`, run the command the person gave you. It looks like this:

```sh
curl -fsSL __LUMNI_CONSOLE__/init | node --input-type=module - --code LUM-…
```

It writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` (git-ignored) and registers the agent. Never print, log or commit the key.

## 2. Find the side effects

List every place an agent changes something outside this program: payments and refunds, CRM and database writes, emails and messages, tickets, deploys, file uploads, anything with a real-world effect. Reads don't need a check. Show the list to the person before changing code if there are more than ten.

## 3. Wrap each one: check, then do, then report

Use the SDK in this repository's language. Until the packages are on npm and PyPI, install from Lumni's release archive (`npm install ./lumni-sdk-0.2.0.tgz`, `pip install ./lumni_sdk-0.2.0-py3-none-any.whl`), or call the HTTP API directly (`POST /v1/actions/check`, `/v1/actions/{id}/start`, `/v1/actions/{id}/result`, with `Authorization: Bearer $LUMNI_API_KEY`).

TypeScript:

```ts
import { LumniClient } from '@lumniverse/sdk';
const lumni = new LumniClient(process.env.LUMNI_URL!, process.env.LUMNI_API_KEY!);

const d = await lumni.checkAction({
  actor_id: 'refund-agent',              // a stable id per agent
  work_id: ticketId,                      // the task this belongs to
  operation: 'stripe.refund',             // app.verb
  arguments: { charge: chargeId, amount },
  effect_key: `stripe.refund:${chargeId}`, // the same real-world effect gets the same key
  resources: [{ type: 'charge', namespace: 'stripe', external_id: chargeId, mode: 'WRITE' }],
  metadata: { summary: `Refund $${amount} on ${chargeId}` }, // one plain sentence: the console shows it
});
```

Python:

```python
from lumni import Client
lumni = Client(os.environ["LUMNI_URL"], os.environ["LUMNI_API_KEY"])
d = lumni.check_action({"actor_id": "refund-agent", "work_id": ticket_id, "operation": "stripe.refund",
    "arguments": {"charge": charge_id, "amount": amount}, "effect_key": f"stripe.refund:{charge_id}",
    "resources": [{"type": "charge", "namespace": "stripe", "external_id": charge_id, "mode": "WRITE"}],
    "metadata": {"summary": f"Refund ${amount} on {charge_id}"}})
```

Then act on the decision, exactly like this:

| Decision | What the code does |
|---|---|
| `ALLOW`, `WARN`, or any `WOULD_…` | Do it: `startAction(id, lease_token)`, perform the side effect, then `reportResult(id, { status: 'SUCCEEDED' \| 'FAILED', lease_token, output })` |
| `WAIT` | Sleep `retry_after_ms`, then check again with the same `action_id` |
| `REPLAN` | Read the thing again and decide again. If the reason is `WORK_SUPERSEDED` or `WORK_CANCELLED`, stop: the work is no longer wanted |
| `REQUIRE_APPROVAL` | Poll `approval(approval_id)` until it's `APPROVED` (check again with the same `action_id`) or `DENIED` (don't do it) |
| `DEDUPLICATE` | Don't do it. It already happened |
| `REJECT` | Don't do it |

`WOULD_…` decisions mean the workspace only watches: go ahead, Lumni is recording what it would have done.

Put this in one helper (for example `guard(input, act)`) and call it at every side effect, rather than repeating the switch.

## 4. Give Lumni what it needs to tell things apart

- **`effect_key`:** name the real-world effect, the same for every attempt at it: `stripe.refund:<charge>`, `email.send:<message-id>`. Without one, Lumni identifies an action by its operation, resources and arguments, so make sure arguments carry the request or ticket id.
- **`observed_version`:** if the agent read something before deciding (a price, a record, a plan), send the version or `updated_at` it read on that resource. Lumni sends it back to read again if the thing changed since.
- **`resources`:** every thing the action writes, with `mode: 'WRITE'` (or `CREATE`, `DELETE`). Lumni lets one writer in at a time.
- **`work_id`:** one id per task, named after the goal when two agents might pick up the same one (`order-8812-refund`). If `startWork` returns a different `actor_id`, another agent already owns it: join it instead of starting again.
- **Never** put secrets, tokens or whole documents in `arguments` or `metadata`.

## 5. Verify

1. Run the project's tests.
2. Trigger one real action and check it appears on the console's Today page (`__LUMNI_CONSOLE__/today`), with the summary you wrote.
3. Tell the person which side effects you wrapped, which you didn't and why, and what they should switch on next (Settings, then Protect, once they've watched a day).

## Optional: let your coding agent ask Lumni questions

Lumni has a read-only MCP endpoint, so you can ask why an action was stopped, or what happened in an incident:

```sh
claude mcp add --transport http lumni __LUMNI_URL__/v1/mcp --header "Authorization: Bearer $LUMNI_API_KEY"
```

Tools: `lumni_explain_action`, `lumni_recent_findings`, `lumni_explain_incident`, `lumni_get_resource_activity`, `lumni_get_outcome`, `lumni_get_conflicts`, `lumni_replay_incident`. They read evidence only, and never act.
