---
name: lumni
description: Add Lumni to an AI agent so each consequential action (refund, email, CRM write, deploy, ticket change) is checked before it runs, happens once, waits for people and other agents, and is proven afterwards. Use when the user mentions Lumni or lumniverse, asks to stop agents from duplicating, colliding or overwriting work, wants approvals for risky agent actions, or asks to wrap tool calls with checkAction / check_action / shouldExecute.
---

# Integrating Lumni into an agent

Lumni sits between AI agents and the systems they change. Before a consequential action runs, the agent asks Lumni; Lumni answers with a decision; the agent acts only when the decision allows it, then reports what happened. Lumni never runs the action itself and never uses an LLM to decide.

Only wrap actions that **change something outside the agent**: payments, refunds, emails, messages, CRM/DB writes, tickets, deploys. Leave reads, model calls and pure computation alone.

## 1. Connect

In the agent's project folder:

```bash
npx lumniverse init
```

It opens the browser to approve, writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env`, and prints the lines to add. Don't invent keys; if `.env` has no `LUMNI_API_KEY`, ask the user to run this (sign-up is at https://app.lumniverse.com; self-hosted consoles use `npx lumniverse init --console <url>`).

Install the SDK for the agent's language:

| Language | Install | Import |
|---|---|---|
| TypeScript / Node 22+ | `npm install @lumniverse/sdk` | `import { LumniClient, shouldExecute } from '@lumniverse/sdk'` |
| Python 3.10+ | `pip install lumni-sdk` | `from lumni import Client, should_execute` |
| Go | Source in `sdk/go` of github.com/lumnihq/lumni (package `lumni`; not yet `go get`-able, so vendor it) | `lumni.New(url, key)` |

## 2. Wrap each consequential action

TypeScript:

```ts
const lumni = new LumniClient(process.env.LUMNI_URL!, process.env.LUMNI_API_KEY!);

const d = await lumni.checkAction({
  actor_id: 'support-agent',            // which agent (stable name)
  work_id: ticket.id,                   // the task this belongs to
  operation: 'stripe.refund',           // app.verb
  effect_key: `refund:${order.id}`,     // the real-world effect: same key = same effect, happens once
  arguments: { charge: chargeId, amount: 64 },
  resources: [{ type: 'order', external_id: order.id, mode: 'WRITE' }],
  metadata: { summary: `Refund $64 for order ${order.id}`, amount: 64, customer: order.customerName },
});

if (shouldExecute(d)) {
  await lumni.startAction(d.action_id, d.lease_token);
  try {
    const refund = await stripe.refunds.create({ charge: chargeId });
    await lumni.reportResult(d.action_id, { status: 'SUCCEEDED', lease_token: d.lease_token, external_result_id: refund.id, output: { id: refund.id } });
  } catch (e) {
    await lumni.reportResult(d.action_id, { status: 'FAILED', lease_token: d.lease_token, output: { error: String(e) } });
    throw e;
  }
} else {
  // handle the decision: see section 3
}
```

Python:

```python
lumni = Client(os.environ["LUMNI_URL"], os.environ["LUMNI_API_KEY"])
d = lumni.check_action({
    "actor_id": "support-agent", "work_id": ticket_id, "operation": "stripe.refund",
    "effect_key": f"refund:{order_id}",
    "resources": [{"type": "order", "external_id": order_id, "mode": "WRITE"}],
    "metadata": {"summary": f"Refund $64 for order {order_id}", "amount": 64},
})
if should_execute(d):
    lumni.start_action(d["action_id"], d.get("lease_token", ""))
    refund = stripe.Refund.create(charge=charge_id)
    lumni.report_result(d["action_id"], {"status": "SUCCEEDED", "lease_token": d.get("lease_token"), "external_result_id": refund.id})
```

Go: `d, err := client.CheckAction(ctx, protocol.ActionCheckInput{...})`, then `lumni.RequireExecution(d)`, `client.StartAction`, `client.ReportResult`.

### Choosing the fields well (this is what makes Lumni correct)

- **`effect_key`**: name the *effect in the world*, not the attempt. Two agents refunding the same order must produce the same key (`refund:<order>`); two different refunds must not. Never put a random ID, timestamp or the agent's name in it.
- **`resources`**: every record the action reads or writes, with `mode` `READ` or `WRITE`. Use the external system's real ID (`external_id`). This is how Lumni sees two agents (or an agent and a person) on the same record.
- **Stale data**: when the agent decided based on a value it read, send `observed_version` (an ETag, `updated_at`, revision number) and `version_source` on that resource. Lumni returns `REPLAN` if it changed since.
- **`work_id`**: one per task/run/ticket, so related actions group together.
- **`metadata`**: optional `summary`, `amount` (dollars), `customer`, `app`; the console uses them.
- Keep `action_id` stable across retries of the *same* attempt (the SDK generates one if omitted); a retry with a new `action_id` is a new attempt.
- Always report a terminal result (`SUCCEEDED`, `FAILED` or `UNKNOWN`) after you started. `UNKNOWN` is right when the call timed out and you can't tell.

## 3. Handle every decision

`shouldExecute(d)` is true for `ALLOW`, `WARN` and every `WOULD_*` decision. New workspaces start in **shadow mode**: decisions come back as `WOULD_*`, everything is let through, and the console shows what Lumni would have stopped. Write the handling below anyway; it applies once the user turns enforcement on.

| Decision | Meaning | What the agent should do |
|---|---|---|
| `ALLOW` / `WARN` | Go ahead | Start, act, report result. Pass `lease_token` on start/result. |
| `DEDUPLICATE` | This effect already happened (`existing_action_id`) | Don't act. Treat as done; don't report a result for this action. |
| `WAIT` | Someone else (agent or person) holds the record, or a dependency isn't done | Sleep `retry_after_ms` (bounded), then check the same action again. |
| `REPLAN` | Data the agent used is stale, or the work was superseded | Re-read the resource and re-decide; send a fresh check. If `reason_code` is `WORK_SUPERSEDED`, `WORK_CANCELLED` or `ACTION_SUPERSEDED`, stop instead. |
| `REQUIRE_APPROVAL` | A person must decide (`approval_id`) | Poll `approval(approval_id)` until not `PENDING` (or resume on an event). If `APPROVED`, check the **same** `action_id` again; otherwise don't act. |
| `REJECT` | Not allowed by policy | Don't act; surface `reason_code` to the user/logs. |

Bound every loop (retries, wait time, approval wait). Never retry a refused action by changing its `effect_key`.

## 4. Framework adapters (prefer these when the project uses the framework)

- **LangGraph (TS)**: `import { langgraphTool } from '@lumniverse/sdk/langgraph'`; wrap the side-effecting function: `langgraphTool(new ExternalClient(lumni), input => action, input => doIt(input))`. Needs a checkpointer; pending decisions become LangGraph interrupts.
- **OpenAI Agents (TS)**: `import { openaiTool } from '@lumniverse/sdk/openai'`; `openaiTool(tool, new ExternalClient(lumni), (input, callId) => action)` uses the SDK's `needsApproval` boundary.
- **Python**: `pip install "lumni-sdk[langgraph]"` or `"lumni-sdk[openai]"` for the same adapters.
- **Temporal / Restate (Go)**: adapters in `adapters/temporal` and `adapters/restate` map decisions to durable timers and signals.
- **MCP**: put the Lumni gateway in front of an MCP server instead of changing agent code; see `docs/integrations.md` in github.com/lumnihq/lumni.

## 5. Check your work

1. Run the agent once; the action should appear in the console (app.lumniverse.com → Flow) with a `WOULD_*` or `ALLOW` decision.
2. Trigger the same effect twice (run the step again): the second should be `DEDUPLICATE` (or `WOULD_DEDUPLICATE` in shadow mode).
3. To see every decision type without touching the user's agent: `npx lumniverse try`.

Docs: https://github.com/lumnihq/lumni (docs/sdk.md, docs/api.md, docs/getting-started.md). Full reference for LLMs: https://lumniverse.com/llms-full.txt
