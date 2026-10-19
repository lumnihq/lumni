// Extra: a person decides. A $900 refund matches the rule “Refunds over $500
// need a person” (added by --admin-key), so it waits in Needs you until you
// approve or deny it. The agent waits up to two minutes.
// Not in the default run, because it waits for you: --scenario approval.

import { shortId } from '../lumni.mjs';
import { startWork } from './kit.mjs';

export const REFUND_RULE = {
  name: 'Refunds over $500 need a person', operation: 'stripe.refund', resource_type: '*',
  argument_pointer: '/amount', numeric_threshold: 500, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED',
};

export const scenario = {
  id: 'approval',
  problem: 'A person decides (extra)',
  story: 'A big refund waits for a person.',
  expect: 'REQUIRE_APPROVAL',
  extra: true,
  onlyWhenAsked: true,
  actors: [{ id: 'refund-agent', name: 'Refund agent' }],
  async run({ lumni, admin, apps, guard, say }) {
    if (admin) await admin.request('POST', '/v1/config/policy-rules', REFUND_RULE);
    const work = await startWork(lumni, 'refund-agent', 'Refund an annual plan');
    const charge = `ch_${shortId()}`;
    const r = await guard({
      work_id: work, actor_id: 'refund-agent', operation: 'stripe.refund', risk: 'HIGH',
      arguments: { charge, amount: 900 }, effect_key: `stripe.refund:${charge}`,
      resources: [{ type: 'charge', namespace: 'stripe', external_id: charge, mode: 'WRITE' }],
      metadata: { summary: `Refund the $900 annual plan on ${charge}`, customer: 'Initech' },
    }, () => apps.stripe.refund({ charge, amount: 900, by: 'refund-agent', scenario: this.id }));
    if (r.done) say('refund-agent', `Refunded $900 on ${charge}`);
    await lumni.completeWork(work).catch(() => {});
  },
};
