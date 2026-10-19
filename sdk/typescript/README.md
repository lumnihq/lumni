# @lumniverse/sdk

The TypeScript client for [Lumni](https://lumniverse.com): your AI agents check with Lumni before they change anything, so each action happens once, the way you meant, and is proven afterwards.

```bash
npm install @lumniverse/sdk
```

The quickest start is `npx lumniverse init` in your agent's folder: it connects the agent, writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env`, and prints the lines below for your code.

```ts
import { LumniClient, shouldExecute } from '@lumniverse/sdk';

const lumni = new LumniClient(process.env.LUMNI_URL!, process.env.LUMNI_API_KEY!);

const d = await lumni.checkAction({
  actor_id: 'support-agent', work_id: runId, operation: 'stripe.refund',
  arguments: { charge: chargeId, amount: 60 },
  resources: [{ type: 'charge', external_id: chargeId, mode: 'WRITE' }],
});
if (shouldExecute(d)) await stripe.refunds.create({ charge: chargeId });
```

New workspaces start in shadow mode: Lumni records what it would have done, and `shouldExecute` lets everything through until you turn enforcement on.

Adapters: `@lumniverse/sdk/langgraph` and `@lumniverse/sdk/openai` (OpenAI Agents). Node 22+.

Docs: [github.com/lumnihq/lumni](https://github.com/lumnihq/lumni/blob/main/docs/sdk.md). License: Apache-2.0 (the SDK; Lumni’s server is FSL).
