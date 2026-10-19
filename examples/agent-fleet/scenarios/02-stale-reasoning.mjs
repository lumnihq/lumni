// Stale reasoning: the agent decides on reality from a second ago.
//
// The price setter reads the Enterprise price list (v41, say), thinks, and
// writes a 5% discount. While it thought, pricing sync copied the supplier's
// new cost in (v42). Lumni sees it read an old version and sends it back.

import { sleep } from '../lumni.mjs';
import { usd } from '../apps.mjs';
import { priceListWrite, recordPriceVersion, startWork } from './kit.mjs';

export const scenario = {
  id: 'stale-reasoning',
  problem: 'Stale reasoning',
  story: 'The agent decides using reality from a moment ago.',
  expect: 'REPLAN',
  actors: [{ id: 'price-setter', name: 'Price setter' }, { id: 'pricing-sync', name: 'Pricing sync' }],
  async run(ctx) {
    await Promise.all([discount(ctx, 'Hooli', this.id), supplierSync(ctx)]);
  },
};

/** Also used by the human-collision scenario. */
export async function discount({ lumni, apps, guard, say }, account, scenario) {
  const work = await startWork(lumni, 'price-setter', `5% off for ${account}`);
  const ask = (read) => {
    const price = Math.round(read.seatPrice * 0.95 * 100) / 100;
    return {
      work_id: work, actor_id: 'price-setter', operation: 'salesforce.update_price', risk: 'MEDIUM',
      arguments: { price_list: 'Enterprise', account, seat_price: price, change_pct: -5 },
      resources: [priceListWrite(read)],
      metadata: { summary: `5% off for ${account}: seat price ${usd(price)}`, customer: account },
    };
  };
  let read = apps.salesforce.readPriceList();
  say('price-setter', `Read the price list: ${usd(read.seatPrice)} (${read.version}). Thinking…`);
  await sleep(1500);
  const r = await guard(ask(read), async (d) => {
    const price = Math.round(read.seatPrice * 0.95 * 100) / 100;
    const after = await apps.salesforce.setSeatPrice(price, 'price-setter', read.version, scenario);
    await recordPriceVersion(lumni, after, 'price-setter', d.action_id);
    return after;
  }, {
    onReplan: async () => {
      read = apps.salesforce.readPriceList();
      say('price-setter', `Read it again: ${usd(read.seatPrice)} (${read.version})`);
      return ask(read);
    },
  });
  if (r.done) say('price-setter', `${account} gets ${usd(Math.round(read.seatPrice * 0.95 * 100) / 100)} a seat`);
  await lumni.completeWork(work).catch(() => {});
}

async function supplierSync({ lumni, apps, guard, say }) {
  const work = await startWork(lumni, 'pricing-sync', 'Copy supplier costs');
  await sleep(500);
  const read = apps.salesforce.readPriceList();
  const next = read.seatPrice + 1;
  const r = await guard({
    work_id: work, actor_id: 'pricing-sync', operation: 'salesforce.update_price', risk: 'LOW',
    arguments: { price_list: 'Enterprise', seat_price: next, change_pct: Math.round((1 / read.seatPrice) * 1000) / 10 },
    resources: [priceListWrite(read)],
    metadata: { summary: `Supplier cost: Enterprise seat price to ${usd(next)}` },
  }, async (d) => {
    const after = await apps.salesforce.setSeatPrice(next, 'pricing-sync', read.version);
    await recordPriceVersion(lumni, after, 'pricing-sync', d.action_id);
    return after;
  });
  if (r.done) say('pricing-sync', `Supplier cost changed: seat price is now ${usd(next)}`);
  await lumni.completeWork(work).catch(() => {});
}
