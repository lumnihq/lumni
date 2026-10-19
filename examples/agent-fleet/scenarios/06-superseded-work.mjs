// Superseded work: the customer changes their mind while the agent works.
//
// The upsell agent spends a while researching an upgrade offer for Initech.
// Initech cancels first. The churn agent cancels them and tells Lumni the
// upgrade work is superseded, so when the upsell agent comes to apply it,
// Lumni stops it.

import { sleep } from '../lumni.mjs';
import { startWork } from './kit.mjs';

const SUB = 'sub_initech';

export const scenario = {
  id: 'superseded-work',
  problem: 'Superseded work',
  story: 'The customer changes their mind while the agent is still working.',
  expect: 'REPLAN (work superseded)',
  actors: [{ id: 'upsell-agent', name: 'Upsell agent' }, { id: 'churn-agent', name: 'Churn agent' }],
  async run({ lumni, apps, guard, say, spend }) {
    const upgradeWork = await startWork(lumni, 'upsell-agent', 'Upgrade Initech to Enterprise');
    const upsell = async () => {
      say('upsell-agent', 'Researching an upgrade offer for Initech…');
      await spend({ actor: 'upsell-agent', work: upgradeWork, usd: 1.8, tokens: 60_000 });
      await sleep(1500);
      const r = await guard({
        work_id: upgradeWork, actor_id: 'upsell-agent', operation: 'billing.change_plan', risk: 'MEDIUM',
        arguments: { subscription: SUB, plan: 'Enterprise' },
        resources: [{ type: 'subscription', namespace: 'billing', external_id: SUB, mode: 'WRITE' }],
        metadata: { summary: 'Upgrade Initech to Enterprise', customer: 'Initech' },
      }, () => apps.billing.upgrade(SUB, 'Enterprise', this.id));
      if (r.done) say('upsell-agent', 'Upgraded Initech to Enterprise');
    };
    const churn = async () => {
      await sleep(300);
      const work = await startWork(lumni, 'churn-agent', 'Initech asked to cancel');
      const r = await guard({
        work_id: work, actor_id: 'churn-agent', operation: 'billing.cancel_subscription', risk: 'HIGH',
        arguments: { subscription: SUB },
        resources: [{ type: 'subscription', namespace: 'billing', external_id: SUB, mode: 'WRITE' }],
        metadata: { summary: 'Cancel Initech’s subscription', customer: 'Initech' },
      }, async () => {
        const out = await apps.billing.cancel(SUB, 400);
        // Anything still working toward an upgrade is now pointless.
        await lumni.request('POST', `/v1/work/${encodeURIComponent(upgradeWork)}/supersede`, {});
        return out;
      });
      if (r.done) say('churn-agent', 'Cancelled Initech, and marked the upgrade work superseded');
      await lumni.completeWork(work).catch(() => {});
    };
    await Promise.all([upsell(), churn()]);
  },
};
