// Cost waste: the agent spends $7 solving a task that was already solved.
//
// The quote bot is asked to send Hooli their renewal quote. The account bot
// already sent it this morning. Drafting a quote takes about $7 of model
// calls, so the quote bot asks Lumni first; Lumni says it's done, and the bot
// stops before spending anything.

import { startWork } from './kit.mjs';


export const scenario = {
  id: 'cost-waste',
  problem: 'Cost waste',
  story: 'The agent spends $7 solving a task that was already solved.',
  expect: 'DEDUPLICATE (before spending)',
  actors: [{ id: 'account-bot', name: 'Account bot' }, { id: 'quote-bot', name: 'Quote bot' }],
  async run({ lumni, apps, guard, say, spend, run }) {
    const EFFECT = `quote.send:hooli:renewal-2027:${run}`;
    const send = (actor, work) => ({
      work_id: work, actor_id: actor, operation: 'quotes.send', risk: 'LOW',
      arguments: { account: 'Hooli', kind: 'renewal' }, effect_key: EFFECT,
      resources: [{ type: 'quote', namespace: 'cpq', external_id: `hooli-renewal-2027-${run}`, mode: 'CREATE' }],
      metadata: { summary: 'Send Hooli their 2027 renewal quote', customer: 'Hooli' },
    });
    // This morning.
    const morning = await startWork(lumni, 'account-bot', 'Hooli renewal');
    if ((await guard(send('account-bot', morning), () => apps.quotes.send('Hooli', 'renewal', 'account-bot', this.id))).done) say('account-bot', '(this morning) Sent Hooli their renewal quote');
    await lumni.completeWork(morning).catch(() => {});

    const work = await startWork(lumni, 'quote-bot', 'Send Hooli their renewal quote');
    await spend({ actor: 'quote-bot', work, usd: 0.04, tokens: 1_500 });
    const r = await guard(send('quote-bot', work), async (d) => {
      say('quote-bot', 'Drafting the quote: pricing, terms, a cover note (about $7 of model calls)…');
      await spend({ actor: 'quote-bot', work, action: d.action_id, usd: 7.2, tokens: 240_000 });
      return apps.quotes.send('Hooli', 'renewal', 'quote-bot', this.id);
    });
    if (r.skipped) say('quote-bot', 'Stopped before drafting: $7.20 not spent');
    else if (r.done) say('quote-bot', 'Sent Hooli a renewal quote');
    await lumni.completeWork(work).catch(() => {});
  },
};
