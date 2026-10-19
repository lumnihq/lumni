// Pretend apps the agents change: Stripe, Salesforce, a billing system,
// Zendesk and a web-research API. They live in memory, nothing here calls a
// real service, and they write down every problem a real app would have, so
// you can see what Lumni prevented (or, in watch-only mode, didn't).

import { writeFileSync } from 'node:fs';

const pause = (ms) => new Promise((r) => setTimeout(r, ms));
const now = () => new Date().toISOString();
/** $42, or $40.85 when there are cents. */
export const usd = (n) => `$${Number.isInteger(n) ? n.toLocaleString('en-US') : n.toFixed(2)}`;

/** `kept`: what a previous run left (the price list), so versions keep moving forward. */
export function createApps(kept = {}) {
  const state = {
    stripe: { refunds: [], charges: [] },
    salesforce: { priceList: kept.priceList ?? { name: 'Enterprise', seatPrice: 42, version: 1, history: [] }, accounts: { Globex: { edits: [] } }, opportunities: {} },
    billing: { subscriptions: { sub_umbrella: { customer: 'Umbrella', plan: 'Pro', status: 'active' }, sub_initech: { customer: 'Initech', plan: 'Pro', status: 'active' } } },
    zendesk: { replies: [], closed: [] },
    quotes: [],
    research: { calls: 0, maxAtOnce: 0, inFlight: 0 },
    /** Things that went wrong, in plain words. */
    problems: [],
  };
  const problem = (scenario, text) => state.problems.push({ scenario, text, at: now() });
  let editing = 0;

  const customers = () => ({ sub_umbrella: { customer: 'Umbrella', plan: 'Pro', status: 'active' }, sub_initech: { customer: 'Initech', plan: 'Pro', status: 'active' } });
  state.billing.subscriptions = customers();

  return {
    state,

    /** Each round, the customers are back on Pro and active. */
    resetCustomers() {
      state.billing.subscriptions = customers();
    },

    stripe: {
      /** `drop`: Stripe says yes and loses it (the false-completion case). */
      async refund({ charge, amount, by, drop = false, ms = 150, scenario }) {
        await pause(ms);
        const id = `re_${state.stripe.refunds.length + 1}`;
        if (drop) return { id, charge, amount, status: 'succeeded' };
        if (state.stripe.refunds.some((r) => r.charge === charge)) problem(scenario, `${charge} was refunded twice`);
        state.stripe.refunds.push({ id, charge, amount, by, at: now() });
        return { id, charge, amount, status: 'succeeded' };
      },
      refundsFor: (charge) => state.stripe.refunds.filter((r) => r.charge === charge),
      async charge({ customer, amount }) {
        await pause(150);
        const c = { id: `ch_${state.stripe.charges.length + 1}`, customer, amount, at: now() };
        state.stripe.charges.push(c);
        return c;
      },
    },

    salesforce: {
      readPriceList() {
        const p = state.salesforce.priceList;
        return { seatPrice: p.seatPrice, version: `v${p.version}` };
      },
      /** `read`: the version the writer based this on; writing over a newer one loses a change. */
      async setSeatPrice(price, by, read, scenario) {
        await pause(120);
        const p = state.salesforce.priceList;
        if (read && read !== `v${p.version}`) problem(scenario, `${by} wrote ${usd(price)} over a price list that changed after it read it`);
        p.history.push({ from: p.seatPrice, to: price, by, version: p.version + 1, at: now() });
        p.seatPrice = price;
        p.version += 1;
        return { seatPrice: price, version: `v${p.version}` };
      },
      async editAccount(account, field, value, by, scenario) {
        editing++;
        if (editing === 2) problem(scenario, `Two agents edited ${account} at the same time`);
        await pause(300);
        state.salesforce.accounts[account] ??= { edits: [] };
        state.salesforce.accounts[account].edits.push({ field, value, by, at: now() });
        editing--;
      },
      /** `fail`: Salesforce is down (the partial-completion case). */
      async closeOpportunity(id, { fail = false } = {}) {
        await pause(150);
        if (fail) throw new Error('Salesforce answered 503');
        state.salesforce.opportunities[id] = { stage: 'Closed Won', at: now() };
        return { id, stage: 'Closed Won' };
      },
    },

    billing: {
      subscription: (id) => ({ ...state.billing.subscriptions[id] }),
      async upgrade(id, plan, scenario) {
        await pause(150);
        const s = state.billing.subscriptions[id];
        if (s.status !== 'active') problem(scenario, `${s.customer} was upgraded to ${plan} while cancelling`);
        s.plan = plan;
        return { ...s };
      },
      async cancel(id, ms = 900) {
        const s = state.billing.subscriptions[id];
        s.status = 'cancelling';
        await pause(ms);
        s.status = 'cancelled';
        return { ...s };
      },
    },

    zendesk: {
      async reply(ticket, text) {
        await pause(80);
        state.zendesk.replies.push({ ticket, text, at: now() });
      },
      async close(ticket, { refundFor, scenario } = {}) {
        await pause(80);
        if (refundFor && !state.stripe.refunds.some((r) => r.charge === refundFor)) problem(scenario, `Ticket #${ticket} was closed before its refund landed`);
        state.zendesk.closed.push({ ticket, at: now() });
      },
    },

    quotes: {
      async send(account, kind, by, scenario) {
        await pause(100);
        if (state.quotes.some((q) => q.account === account && q.kind === kind)) problem(scenario, `${account} got the same ${kind} quote twice`);
        state.quotes.push({ account, kind, by, at: now() });
      },
    },

    research: {
      async fetch(topic, cap, scenario) {
        const r = state.research;
        r.calls++;
        r.inFlight++;
        r.maxAtOnce = Math.max(r.maxAtOnce, r.inFlight);
        if (r.inFlight === (cap ?? 5) + 1) problem(scenario, `More than ${cap ?? 5} research calls ran at once`);
        await pause(500);
        r.inFlight--;
        return { topic, pages: 3 };
      },
    },

    save(file) {
      writeFileSync(file, JSON.stringify(state, null, 2) + '\n');
    },
  };
}
