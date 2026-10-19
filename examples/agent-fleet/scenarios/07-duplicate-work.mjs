// Duplicate work: two agents unknowingly solve the same problem.
//
// One email about a damaged order lands in two inboxes. The support agent and
// the triage agent both pick it up. Both name the work after the goal
// ("refund order 8812"), so the second one finds the first already owns it in
// Lumni, and joins it instead of starting again.
//
// Lumni's part is the shared record of who is on what (the work id). Joining
// is the agent's choice; the API has no JOIN decision. If it didn't join,
// the refund's effect key would still stop the second refund (duplicate effect).

import { sleep } from '../lumni.mjs';

const ORDER = 8812;

export const scenario = {
  id: 'duplicate-work',
  problem: 'Duplicate work',
  story: 'Two agents unknowingly solve the same problem.',
  expect: 'JOIN (shared work id)',
  actors: [{ id: 'support-agent', name: 'Support agent' }, { id: 'triage-agent', name: 'Triage agent' }],
  async run({ lumni, apps, guard, say, note, run }) {
    const workId = `order-${ORDER}-refund-${run}`;
    const handle = async (actor, delay) => {
      await sleep(delay);
      const w = await lumni.startWork({ id: workId, actor_id: actor, goal: `Refund damaged order #${ORDER}`, risk: 'MEDIUM' });
      if (w.actor_id && w.actor_id !== actor) {
        say(actor, `${w.actor_id} already owns “Refund damaged order #${ORDER}”: joining instead of starting again`);
        note('joined', `${actor} joined ${w.actor_id}’s work instead of doing it twice`);
        return;
      }
      say(actor, `Picked up order #${ORDER}`);
      const charge = `ch_order_${ORDER}_${run}`;
      const r = await guard({
        work_id: workId, actor_id: actor, operation: 'stripe.refund', risk: 'MEDIUM',
        arguments: { charge, amount: 45 }, effect_key: `stripe.refund:${charge}`,
        resources: [{ type: 'charge', namespace: 'stripe', external_id: charge, mode: 'WRITE' }],
        metadata: { summary: `Refund damaged order #${ORDER}`, customer: 'Soylent' },
      }, () => apps.stripe.refund({ charge, amount: 45, by: actor, scenario: this.id }));
      if (r.done) say(actor, `Refunded order #${ORDER}`);
    };
    await Promise.all([handle('support-agent', 0), handle('triage-agent', 250)]);
    await lumni.completeWork(workId).catch(() => {});
  },
};
