// Human/agent collision: a person fixes something while an AI is still on it.
//
// Jess, in sales ops, corrects the Enterprise seat price in Salesforce by hand
// while the price setter is thinking. Salesforce tells Lumni about the new
// version, and the agent is sent back instead of writing over her fix.

import { sleep } from '../lumni.mjs';
import { usd } from '../apps.mjs';
import { recordPriceVersion } from './kit.mjs';
import { discount } from './02-stale-reasoning.mjs';

export const scenario = {
  id: 'human-collision',
  problem: 'Human/agent collision',
  story: 'A human fixes something while an AI is still trying to fix it.',
  expect: 'REPLAN',
  actors: [{ id: 'price-setter', name: 'Price setter' }, { id: 'jess', name: 'Jess Park', type: 'HUMAN' }],
  async run(ctx) {
    const jess = async () => {
      await sleep(600);
      const read = ctx.apps.salesforce.readPriceList();
      const fixed = read.seatPrice + 3;
      const after = await ctx.apps.salesforce.setSeatPrice(fixed, 'jess', read.version);
      // Salesforce's webhook tells Lumni; no agent involved.
      await recordPriceVersion(ctx.lumni, after, 'jess');
      ctx.say('jess', `(a person) Corrected the seat price in Salesforce to ${usd(fixed)}`);
    };
    await Promise.all([discount(ctx, 'Initech', this.id), jess()]);
  },
};
