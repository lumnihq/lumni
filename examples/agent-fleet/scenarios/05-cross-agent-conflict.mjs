// Cross-agent conflict: two agents change the same customer differently.
//
// Umbrella asks to cancel. The churn agent starts cancelling their
// subscription, and at the same moment the upsell agent tries to upgrade it.
// Lumni makes the upgrade wait until the cancellation is done; the upsell
// agent then sees the subscription is cancelled and drops the upgrade.

import { sleep } from '../lumni.mjs';
import { startWork } from './kit.mjs';

const SUB = 'sub_umbrella';
const subscription = { type: 'subscription', namespace: 'billing', external_id: SUB, mode: 'WRITE' };

export const scenario = {
  id: 'cross-agent-conflict',
  problem: 'Cross-agent conflict',
  story: 'Two agents try to change the same customer differently.',
  expect: 'WAIT',
  actors: [{ id: 'churn-agent', name: 'Churn agent' }, { id: 'upsell-agent', name: 'Upsell agent' }],
  async run({ lumni, apps, guard, say }) {
    const cancel = async () => {
      const work = await startWork(lumni, 'churn-agent', 'Umbrella asked to cancel');
      const r = await guard({
        work_id: work, actor_id: 'churn-agent', operation: 'billing.cancel_subscription', risk: 'HIGH',
        arguments: { subscription: SUB }, resources: [subscription],
        metadata: { summary: 'Cancel Umbrella’s subscription', customer: 'Umbrella' },
      }, () => apps.billing.cancel(SUB));
      if (r.done) say('churn-agent', 'Cancelled Umbrella’s subscription');
      await lumni.completeWork(work).catch(() => {});
    };
    const upgrade = async () => {
      const work = await startWork(lumni, 'upsell-agent', 'Upgrade Umbrella to Enterprise');
      await sleep(200);
      const r = await guard({
        work_id: work, actor_id: 'upsell-agent', operation: 'billing.change_plan', risk: 'MEDIUM',
        arguments: { subscription: SUB, plan: 'Enterprise' }, resources: [subscription],
        metadata: { summary: 'Upgrade Umbrella to Enterprise', customer: 'Umbrella' },
      }, async () => {
        // It acts on the subscription as it is now, which is why waiting matters.
        const now = apps.billing.subscription(SUB);
        if (now.status === 'cancelled') return { dropped: 'cancelled' };
        return apps.billing.upgrade(SUB, 'Enterprise', this.id);
      });
      if (r.output?.dropped) say('upsell-agent', 'Umbrella has cancelled, so it drops the upgrade');
      else if (r.done) say('upsell-agent', 'Upgraded Umbrella to Enterprise');
      await lumni.completeWork(work).catch(() => {});
    };
    await Promise.all([cancel(), upgrade()]);
  },
};
