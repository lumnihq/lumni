// Resource contention: five agents start editing the same account.
//
// The SDR bot, renewal bot, support agent, billing bot and enrichment bot all
// update the Globex account in Salesforce at once. Lumni lets one in at a
// time, so no edit is lost.

import { startWork } from './kit.mjs';

const EDITORS = [
  ['sdr-bot', 'SDR bot', 'next_step', 'Book a demo'],
  ['renewal-bot', 'Renewal bot', 'renewal_date', '2027-01-01'],
  ['support-agent', 'Support agent', 'health', 'At risk'],
  ['billing-bot', 'Billing bot', 'billing_email', 'ap@globex.example'],
  ['enrich-bot', 'Enrichment bot', 'employees', '1,240'],
];

export const scenario = {
  id: 'resource-contention',
  problem: 'Resource contention',
  story: 'Five agents start editing the same account.',
  expect: 'WAIT',
  actors: EDITORS.map(([id, name]) => ({ id, name })),
  async run({ lumni, apps, guard, say }) {
    await Promise.all(EDITORS.map(async ([actor, , field, value], i) => {
      const work = await startWork(lumni, actor, `Update Globex: ${field}`);
      await new Promise((r) => setTimeout(r, i * 40));
      const r = await guard({
        work_id: work, actor_id: actor, operation: 'salesforce.update_account', risk: 'LOW',
        arguments: { account: 'Globex', field, value },
        resources: [{ type: 'account', namespace: 'salesforce', external_id: 'globex', mode: 'WRITE' }],
        metadata: { summary: `Globex ${field.replace(/_/g, ' ')}: ${value}`, customer: 'Globex' },
      }, () => apps.salesforce.editAccount('Globex', field, value, actor, this.id));
      if (r.done) say(actor, `Updated Globex ${field.replace(/_/g, ' ')}`);
      await lumni.completeWork(work).catch(() => {});
    }));
  },
};
