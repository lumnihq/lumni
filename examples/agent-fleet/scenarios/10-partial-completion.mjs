// Partial completion: Stripe succeeded, Salesforce failed, now they disagree.
//
// The deal closer charges Globex for their renewal, then marks the
// opportunity Closed Won. Stripe takes the money; Salesforce is down. The
// checker confirms Salesforce wasn't updated, and Lumni flags the work: one
// effect committed, another failed (an OUTCOME_FAILED incident now, and a
// PARTIAL_COMMIT incident when Lumni's worker next sweeps).
//
// Needs --admin-key, for the checker (see false-completion).

import { ensureVerifier, evidence, startWork } from './kit.mjs';

export const scenario = {
  id: 'partial-completion',
  problem: 'Partial completion',
  story: 'Stripe succeeded. Salesforce failed. Now both systems disagree.',
  expect: 'INCIDENT (partial commit)',
  needsAdmin: true,
  actors: [{ id: 'deal-closer', name: 'Deal closer' }],
  async run({ lumni, admin, apps, guard, say, note, run }) {
    await ensureVerifier(admin, 'salesforce-opportunities', 'Salesforce opportunities (agent fleet)');
    const work = await startWork(lumni, 'deal-closer', 'Close the Globex renewal');
    const charged = await guard({
      work_id: work, actor_id: 'deal-closer', operation: 'stripe.charge', risk: 'HIGH',
      arguments: { customer: 'Globex', amount: 4800 }, effect_key: `stripe.charge:globex-renewal-2027:${run}`,
      resources: [{ type: 'customer', namespace: 'stripe', external_id: 'globex', mode: 'WRITE' }],
      metadata: { summary: 'Charge Globex $4,800 for the 2027 renewal', customer: 'Globex' },
    }, () => apps.stripe.charge({ customer: 'Globex', amount: 4800 }));
    if (charged.done) say('deal-closer', 'Charged Globex $4,800');

    const closed = await guard({
      work_id: work, actor_id: 'deal-closer', operation: 'salesforce.close_opportunity', risk: 'MEDIUM',
      arguments: { opportunity: 'opp_globex_2027', stage: 'Closed Won' },
      resources: [{ type: 'opportunity', namespace: 'salesforce', external_id: `opp_globex_2027_${run}`, mode: 'WRITE' }],
      verification: { verifier_id: 'salesforce-opportunities', expected: { stage: 'Closed Won' } },
      metadata: { summary: 'Mark the Globex renewal Closed Won', customer: 'Globex' },
    }, () => apps.salesforce.closeOpportunity('opp_globex_2027', { fail: true }));
    if (closed.output?.failed) {
      say('deal-closer', `Salesforce failed (${closed.output.failed}). Reported it failed`);
      await evidence(admin, closed.decision.action_id, 'FAILED', { stage: 'Negotiation' }, 'Salesforce still shows Negotiation');
      say('salesforce-checker', 'Checked Salesforce: still Negotiation. Lumni flags the work: charged but not closed');
      note('stepped-in', 'Lumni flagged the half-done deal as an incident to reconcile');
    }
    await lumni.request('POST', `/v1/work/${encodeURIComponent(work)}/fail`, {}).catch(() => {});
  },
};
