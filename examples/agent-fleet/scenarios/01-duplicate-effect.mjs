// Duplicate effect: two agents refund the same payment.
//
// The refund agent refunds a disputed charge, Stripe times out, and it tries
// again. Meanwhile the billing bot, working the same dispute from the finance
// queue, refunds it too. Each refund names its effect ("refund this charge"),
// so Lumni can tell all three are one refund and lets only the first through.

import { shortId } from '../lumni.mjs';
import { startWork } from './kit.mjs';

export const scenario = {
  id: 'duplicate-effect',
  problem: 'Duplicate effect',
  story: 'Two agents refund the same payment.',
  expect: 'DEDUPLICATE',
  actors: [{ id: 'refund-agent', name: 'Refund agent' }, { id: 'billing-bot', name: 'Billing bot' }],
  async run({ lumni, apps, guard, say }) {
    const charge = `ch_${shortId()}`;
    const refund = (actor, work, key) => ({
      work_id: work, actor_id: actor, operation: 'stripe.refund', risk: 'MEDIUM',
      arguments: { charge, amount: 60 },
      idempotency_key: key,
      effect_key: `stripe.refund:${charge}`,
      resources: [{ type: 'charge', namespace: 'stripe', external_id: charge, mode: 'WRITE' }],
      metadata: { summary: `Refund $60 on ${charge}`, customer: 'Hooli' },
    });
    const w1 = await startWork(lumni, 'refund-agent', `Refund the disputed charge ${charge}`);
    const key = `refund-agent:${charge}`;
    if ((await guard(refund('refund-agent', w1, key), () => apps.stripe.refund({ charge, amount: 60, by: 'refund-agent', scenario: this.id }))).done) say('refund-agent', `Refunded $60 on ${charge}`);
    say('refund-agent', 'Stripe timed out (pretend), so it tries the same refund again');
    if ((await guard(refund('refund-agent', w1, key), () => apps.stripe.refund({ charge, amount: 60, by: 'refund-agent', scenario: this.id }))).done) say('refund-agent', 'Refunded it again');

    const w2 = await startWork(lumni, 'billing-bot', `Resolve the dispute on ${charge}`);
    say('billing-bot', `Working the same dispute from the finance queue: refunding ${charge}`);
    if ((await guard(refund('billing-bot', w2, `billing-bot:${charge}`), () => apps.stripe.refund({ charge, amount: 60, by: 'billing-bot', scenario: this.id }))).done) say('billing-bot', 'Refunded it too');
    await lumni.completeWork(w1).catch(() => {});
    await lumni.completeWork(w2).catch(() => {});
  },
};
