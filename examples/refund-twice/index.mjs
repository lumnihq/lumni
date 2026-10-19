// Two agents, one refund: the classic duplicate, and Lumni catching it.
//
//   npm install && npx lumniverse init     connect this folder (writes .env)
//   node --env-file=.env index.mjs         run it; run it again with Protect on
//
// New workspaces only watch: the copy comes back WOULD_DEDUPLICATE and still
// "refunds". Switch the workspace to Protect (Settings) and the copy comes back
// DEDUPLICATE, and is skipped.

import { LumniClient, shouldExecute } from '@lumniverse/sdk';

const lumni = new LumniClient(process.env.LUMNI_URL, process.env.LUMNI_API_KEY);
const order = process.argv[2] ?? `order_${Date.now().toString(36)}`;
const refunded = []; // stands in for Stripe: what actually went out

async function refund(agent) {
  await lumni.request('POST', '/v1/actors', { id: agent, type: 'AGENT', runtime: 'node' }, true);
  const workId = `work_${agent}_${order}`;
  await lumni.startWork({ id: workId, actor_id: agent, goal: `Refund ${order}` });

  const d = await lumni.checkAction({
    actor_id: agent,
    work_id: workId,
    operation: 'stripe.refund',
    effect_key: `refund:${order}`, // the same refund, whoever asks: it happens once
    resources: [{ type: 'order', external_id: order, mode: 'WRITE' }],
    metadata: { summary: `Refund $64 for ${order}`, amount: 64, customer: 'Maya' },
  });
  console.log(`  ${agent.padEnd(14)} ${d.decision}  ${d.reason_code ?? ''}`);
  if (!shouldExecute(d)) return console.log(`  ${''.padEnd(14)} skipped: Lumni stopped it`);

  await lumni.startAction(d.action_id, d.lease_token);
  refunded.push(agent); // your real call goes here, e.g. await stripe.refunds.create({ charge })
  await lumni.reportResult(d.action_id, { status: 'SUCCEEDED', lease_token: d.lease_token, output: { refunded: 64 } });
}

console.log(`\n  Two agents refund ${order}\n`);
await refund('support-agent');
await refund('billing-agent');
console.log(`\n  Refunds that went out: ${refunded.length} (${refunded.join(', ')})`);
console.log(refunded.length === 1 ? '  Paid once.\n' : '  Paid twice. In watch mode that’s expected: see Flow for what Lumni would have stopped.\n');
