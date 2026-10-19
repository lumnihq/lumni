// False completion: the agent says “done”, the app says no.
//
// The refund agent refunds $75. Stripe answers “succeeded”, then loses it.
// The agent reports success, as it should. A checker (Stripe's webhook, in
// real life) reads Stripe afterwards, finds no refund, and tells Lumni, which
// opens an incident. A second refund that did land is marked verified.
//
// Needs --admin-key: registering the checker and posting what it found need
// the `verification` scope, which agent keys don't have.

import { shortId } from '../lumni.mjs';
import { ensureVerifier, evidence, startWork } from './kit.mjs';

export const scenario = {
  id: 'false-completion',
  problem: 'False completion',
  story: 'The agent says “done.” The app says “no.”',
  expect: 'INCIDENT (outcome mismatch)',
  needsAdmin: true,
  actors: [{ id: 'refund-agent', name: 'Refund agent' }],
  async run({ lumni, admin, apps, guard, say, note }) {
    await ensureVerifier(admin, 'stripe-refunds', 'Stripe refunds (agent fleet)');
    const work = await startWork(lumni, 'refund-agent', 'Refund two late deliveries');
    for (const [amount, lost] of [[75, true], [30, false]]) {
      const charge = `ch_${shortId()}`;
      const r = await guard({
        work_id: work, actor_id: 'refund-agent', operation: 'stripe.refund', risk: 'MEDIUM',
        arguments: { charge, amount }, effect_key: `stripe.refund:${charge}`,
        resources: [{ type: 'charge', namespace: 'stripe', external_id: charge, mode: 'WRITE' }],
        verification: { verifier_id: 'stripe-refunds', expected: { charge, amount } },
        metadata: { summary: `Refund $${amount} on ${charge}`, customer: 'Hooli' },
      }, () => apps.stripe.refund({ charge, amount, by: 'refund-agent', drop: lost, scenario: this.id }));
      if (!r.done) continue;
      say('refund-agent', `Stripe said the $${amount} refund succeeded. Reported it done`);
      // The checker reads Stripe itself.
      const found = apps.stripe.refundsFor(charge);
      if (found.length) {
        await evidence(admin, r.decision.action_id, 'VERIFIED', { refunds: found.map((x) => x.id) }, 'Stripe shows the refund');
        say('stripe-checker', `Checked Stripe: the $${amount} refund is there. Verified`);
      } else {
        await evidence(admin, r.decision.action_id, 'MISMATCH', { refunds: [] }, 'Stripe has no refund for this charge');
        say('stripe-checker', `Checked Stripe: there is no $${amount} refund. Lumni opens an incident`);
        note('stepped-in', 'Lumni opened an incident for the lost refund; the real one is verified');
      }
    }
    await lumni.completeWork(work).catch(() => {});
  },
};
