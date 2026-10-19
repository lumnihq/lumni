// Dependency: one agent finishes step 3 before another finishes step 2.
//
// A customer is owed a refund on ticket #5120. The support agent wants to
// close the ticket, but only once the refund agent's refund is done. It tells
// Lumni its work depends on the refund work, and Lumni holds the close until
// the refund completes.

import { shortId, sleep } from '../lumni.mjs';
import { startWork } from './kit.mjs';

const TICKET = 5120;

export const scenario = {
  id: 'dependency',
  problem: 'Dependency',
  story: 'One agent finishes step 3 before another finishes step 2.',
  expect: 'WAIT (dependency)',
  actors: [{ id: 'refund-agent', name: 'Refund agent' }, { id: 'support-agent', name: 'Support agent' }],
  async run({ lumni, apps, guard, say }) {
    const charge = `ch_ticket_${TICKET}_${shortId()}`;
    const refundWork = await startWork(lumni, 'refund-agent', `Refund for ticket #${TICKET}`);
    const closeWork = await startWork(lumni, 'support-agent', `Close ticket #${TICKET}`);
    await lumni.request('POST', '/v1/dependencies', { id: `dep_${closeWork}`, entity_type: 'work', source_id: closeWork, target_id: refundWork, relationship: 'DEPENDS_ON' });
    say('support-agent', `Told Lumni: closing #${TICKET} depends on the refund`);

    const refund = async () => {
      const r = await guard({
        work_id: refundWork, actor_id: 'refund-agent', operation: 'stripe.refund', risk: 'MEDIUM',
        arguments: { charge, amount: 120 }, effect_key: `stripe.refund:${charge}`,
        resources: [{ type: 'charge', namespace: 'stripe', external_id: charge, mode: 'WRITE' }],
        metadata: { summary: `Refund $120 for ticket #${TICKET}`, customer: 'Hooli' },
      }, () => apps.stripe.refund({ charge, amount: 120, by: 'refund-agent', ms: 1200, scenario: this.id }));
      if (r.done) say('refund-agent', 'Refunded $120 (Stripe was slow)');
      await lumni.completeWork(refundWork).catch(() => {});
    };
    const close = async () => {
      await sleep(200);
      const r = await guard({
        work_id: closeWork, actor_id: 'support-agent', operation: 'zendesk.close_ticket', risk: 'LOW',
        arguments: { ticket: TICKET },
        resources: [{ type: 'ticket', namespace: 'zendesk', external_id: String(TICKET), mode: 'WRITE' }],
        metadata: { summary: `Close ticket #${TICKET}` },
      }, () => apps.zendesk.close(TICKET, { refundFor: charge, scenario: this.id }));
      if (r.done) say('support-agent', `Closed ticket #${TICKET}`);
      await lumni.completeWork(closeWork).catch(() => {});
    };
    await Promise.all([refund(), close()]);
  },
};
