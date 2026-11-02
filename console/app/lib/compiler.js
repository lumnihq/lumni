// Compiled paths: the steps an agent reasons through again and again, turned
// into a small program it calls instead. The LLM still answers anything off
// the path. After arXiv 2607.08010: compile from live traces, check against
// past runs, keep versions, watch for drift, and let a person promote.
//
//   const st = Compiler.seed();                // the demo's starting state
//   const list = Compiler.paths(w, st);        // every path, with today's numbers
//   const next = Compiler.promote(st, 'lead-routing');  // a new state, or null
//
// Everything here is the demo's: numbers come from the world `w` so they agree
// with the other screens. Nothing here touches the DOM.

(() => {
  function hash(s) {
    let h = 2166136261;
    for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
    return h >>> 0;
  }
  function rng(seed) {
    let s = seed >>> 0;
    return () => {
      s = (s + 0x6d2b79f5) >>> 0;
      let t = s;
      t = Math.imul(t ^ (t >>> 15), t | 1);
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  const money = (n) => `$${Number(n).toLocaleString('en-US', { maximumFractionDigits: n < 10 ? 2 : 0 })}`;

  // What a run costs when the program answers it: the app calls, no LLM.
  const CODE_COST = 0.0003;

  // ─── the paths Lumni has seen ───────────────────────
  //
  // steps: what the agent reasons through each time (LLM calls, time).
  // branches: where runs leave the path; those still go to the LLM.
  // code: the program, in blocks, each tagged with the step it replaces.
  // fields: what it reads from the app, grounded in the app's real schema.
  // versions: how it got here. start: where the demo begins.

  const PATHS = [
    {
      id: 'cancellation', name: 'Cancel a subscription', agent: 'refund', share: 0.22, app: 'stripe', perCall: 0.0275, codeMs: 820, variance: 0.071,
      ask: 'Please cancel my plan',
      answer: 'Cancelled, full refund',
      steps: [
        { id: 'look', t: 'Look up the subscription', d: 'Find it in Stripe from the customer’s email', calls: 2, ms: 1500 },
        { id: 'tenure', t: 'Work out plan and tenure', d: 'Monthly or yearly, and days since it renewed', calls: 1, ms: 800 },
        { id: 'policy', t: 'Read the refund policy', d: 'Which refund applies at this point', calls: 2, ms: 1300 },
        { id: 'decide', t: 'Choose the refund', d: 'Full, prorated or none', calls: 1, ms: 600 },
        { id: 'act', t: 'Cancel and refund', d: 'Now, or at the end of the period', calls: 1, ms: 700 },
      ],
      branches: [
        { at: 'look', t: 'Two subscriptions on one email', share: 0.031 },
        { at: 'policy', t: 'The customer disputes a charge', share: 0.024 },
        { at: 'decide', t: 'Asked to pause, not cancel', share: 0.017 },
      ],
      fields: ['status', 'current_period_start', 'interval', 'latest_charge', 'customer'],
      code: [
        { step: null, lines: ['export function cancelSubscription(req) {'] },
        { step: 'look', lines: ['  const subs = stripe.subscriptions.list({ customer: req.customer })', '  if (subs.length !== 1) return fallback(\'more than one subscription\')', '  const sub = subs[0]'] },
        { step: 'tenure', lines: ['  const days = daysSince(sub.current_period_start)', '  const yearly = sub.plan.interval === \'year\''] },
        { step: 'policy', lines: ['  if (hasDispute(sub.latest_charge)) return fallback(\'disputed charge\')', '  const window = yearly ? 30 : 14'] },
        { step: 'decide', lines: ['  const refund = days > window ? \'none\' : yearly ? \'prorated\' : \'full\''] },
        { step: 'act', lines: ['  stripe.subscriptions.cancel(sub.id, { at_period_end: refund === \'none\' })', '  if (refund !== \'none\') stripe.refunds.create({ charge: sub.latest_charge, amount: owed(sub, refund) })', '  return { cancelled: true, refund }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: 'sub_9QfA, monthly, 13.6 days in', llm: 'No refund', code: 'Full refund', right: 'code', why: 'The policy says 14 days. The LLM counted from the invoice, not the renewal.' },
        { input: 'sub_2LmX, yearly, day 31', llm: 'Prorated refund', code: 'No refund', right: 'code', why: 'Day 31 is past the 30-day window for yearly plans.' },
        { input: 'sub_7TzK, yearly, day 12, 20% coupon', llm: 'Prorated, less the coupon', code: 'Prorated', right: 'llm', why: 'The coupon should come off the refund. Kept as a test for the next version.' },
      ],
      versions: [
        { v: 1, status: 'retired', agree: 0.958, cases: 480, at: 23, note: 'First compile, from 1,410 runs' },
        { v: 2, status: 'retired', agree: 0.981, cases: 496, at: 16, note: 'Counts days from the renewal, not the invoice' },
        { v: 3, status: 'live', agree: 0.994, cases: 512, at: 6, note: 'Hands two subscriptions on one email to the LLM', by: 'Dana Ruiz' },
      ],
      drift: [],
    },
    {
      id: 'refund-eligibility', name: 'Is this refund allowed?', agent: 'refund', share: 0.48, app: 'stripe', perCall: 0.0225, codeMs: 460, variance: 0.054,
      ask: 'Refund order #4471',
      answer: 'Allowed, $60',
      steps: [
        { id: 'order', t: 'Find the order and its charge', d: 'Stripe charge and the order behind it', calls: 2, ms: 1300 },
        { id: 'prior', t: 'Check for an earlier refund', d: 'Has this order been refunded before', calls: 1, ms: 700 },
        { id: 'window', t: 'Check the refund window', d: 'Days since the charge, by plan', calls: 1, ms: 650 },
        { id: 'limit', t: 'Check the amount', d: 'Over $500 asks a person', calls: 1, ms: 500 },
      ],
      branches: [
        { at: 'order', t: 'A charge split across two orders', share: 0.022 },
        { at: 'limit', t: 'A partial refund with no amount given', share: 0.038 },
      ],
      fields: ['amount', 'amount_refunded', 'created', 'metadata.order_id'],
      code: [
        { step: null, lines: ['export function refundAllowed(req) {'] },
        { step: 'order', lines: ['  const charge = stripe.charges.search({ \'metadata.order_id\': req.order })', '  if (!charge) return fallback(\'no charge for this order\')'] },
        { step: 'prior', lines: ['  if (charge.amount_refunded > 0) return { allowed: false, why: \'already refunded\' }'] },
        { step: 'window', lines: ['  if (daysSince(charge.created) > policy.windowDays(charge)) return { allowed: false, why: \'past the window\' }'] },
        { step: 'limit', lines: ['  const amount = req.amount ?? charge.amount', '  if (amount > 500_00) return { allowed: \'ask\', who: \'a person\' }', '  return { allowed: true, amount }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: 'Order #5120, refunded once already', llm: 'Allowed, $40', code: 'Not allowed', right: 'code', why: 'The earlier refund was in a different currency, and the LLM missed it.' },
        { input: 'Order #4988, day 30 exactly', llm: 'Not allowed', code: 'Allowed', right: 'code', why: 'The window includes day 30.' },
      ],
      versions: [
        { v: 1, status: 'retired', agree: 0.971, cases: 500, at: 19, note: 'First compile, from 3,920 runs' },
        { v: 2, status: 'live', agree: 0.989, cases: 500, at: 11, note: 'Reads the refund window from the policy, by plan', by: 'Lee Sato' },
        { v: 3, status: 'shadow', agree: 0.992, cases: 500, at: 1, note: 'Monthly plans now have 14 days, not 30' },
      ],
      drift: [
        { id: 'd-window', kind: 'policy', t: 'The refund policy changed: monthly plans now have 14 days, not 30', field: 'windowDays', step: 'window', hits: 0.18, at: 2, open: true, fixedBy: 3 },
      ],
    },
    {
      id: 'ticket-triage', name: 'Route a new ticket', agent: 'triage', share: 0.61, app: 'zendesk', perCall: 0.015, codeMs: 290, variance: 0.093,
      ask: 'My invoice is wrong',
      answer: 'Billing queue, normal',
      steps: [
        { id: 'read', t: 'Read the ticket', d: 'Subject, first message, the customer’s plan', calls: 1, ms: 900 },
        { id: 'topic', t: 'Pick the topic', d: 'Billing, bug, account or how-to', calls: 1, ms: 750 },
        { id: 'urgent', t: 'Judge how urgent it is', d: 'From the plan and the words used', calls: 1, ms: 600 },
        { id: 'route', t: 'Send it to a queue', d: 'And tag it', calls: 1, ms: 400 },
      ],
      branches: [
        { at: 'topic', t: 'Two topics in one ticket', share: 0.064 },
        { at: 'read', t: 'Not in English', share: 0.029 },
      ],
      fields: ['subject', 'description', 'priority', 'organization.plan', 'tags'],
      code: [
        { step: null, lines: ['export function routeTicket(t) {'] },
        { step: 'read', lines: ['  const text = `${t.subject}\\n${t.description}`', '  if (!isEnglish(text)) return fallback(\'not in English\')'] },
        { step: 'topic', lines: ['  const topic = TOPICS.match(text)', '  if (topic.length !== 1) return fallback(\'more than one topic\')'] },
        { step: 'urgent', lines: ['  const urgent = t.priority === \'urgent\' || t.organization.plan === \'enterprise\''] },
        { step: 'route', lines: ['  zendesk.tickets.update(t.id, { group: QUEUES[topic[0]], tags: [topic[0]] })', '  return { queue: topic[0], urgent }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: '#88231 “refund AND the app crashes”', llm: 'Bug', code: 'Sent to the LLM', right: 'code', why: 'Two topics are off the path, so the LLM gets it, as it should.' },
        { input: '#88104 enterprise, “whenever you can”', llm: 'Normal', code: 'Urgent', right: 'llm', why: 'Enterprise isn’t always urgent. Kept as a test.' },
      ],
      versions: [
        { v: 1, status: 'retired', agree: 0.944, cases: 500, at: 41, note: 'First compile, from 6,800 runs' },
        { v: 2, status: 'retired', agree: 0.962, cases: 500, at: 33, note: 'Hands tickets not in English to the LLM' },
        { v: 3, status: 'retired', agree: 0.977, cases: 500, at: 20, note: 'Enterprise tickets go to the urgent queue' },
        { v: 4, status: 'live', agree: 0.986, cases: 500, at: 9, note: 'Hands tickets with two topics to the LLM', by: 'Priya Nair' },
      ],
      drift: [
        { id: 'd-urgency', kind: 'schema', t: 'Zendesk renamed priority to urgency on your account', field: 'priority', to: 'urgency', step: 'urgent', hits: 0.41, at: 1, open: true },
      ],
    },
    {
      id: 'dunning-retry', name: 'Retry a failed payment', agent: 'dunning', share: 0.74, app: 'stripe', perCall: 0.02, codeMs: 380, variance: 0.038,
      ask: 'Payment failed for inv_3381',
      answer: 'Retry Friday, email sent',
      steps: [
        { id: 'why', t: 'Read why it failed', d: 'The decline code from the bank', calls: 1, ms: 800 },
        { id: 'count', t: 'Count earlier retries', d: 'How many times we have tried', calls: 1, ms: 600 },
        { id: 'when', t: 'Pick when to retry', d: 'After payday, never on weekends', calls: 1, ms: 700 },
        { id: 'tell', t: 'Tell the customer', d: 'The right email for the reason', calls: 1, ms: 900 },
      ],
      branches: [
        { at: 'why', t: 'A decline code it hasn’t seen', share: 0.012 },
      ],
      fields: ['last_payment_error.decline_code', 'attempt_count', 'next_payment_attempt'],
      code: [
        { step: null, lines: ['export function retryPayment(inv) {'] },
        { step: 'why', lines: ['  const code = inv.last_payment_error.decline_code', '  if (!RETRYABLE.has(code)) return code in HARD ? stopDunning(inv) : fallback(\'new decline code\')'] },
        { step: 'count', lines: ['  if (inv.attempt_count >= 4) return stopDunning(inv)'] },
        { step: 'when', lines: ['  const at = nextWeekday(afterPayday(now()))', '  stripe.invoices.update(inv.id, { next_payment_attempt: at })'] },
        { step: 'tell', lines: ['  gmail.send(TEMPLATES[code], inv.customer_email)', '  return { retryAt: at }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: 'inv_2210, insufficient_funds, try 3', llm: 'Retry Saturday', code: 'Retry Monday', right: 'code', why: 'No retries on weekends.' },
      ],
      versions: [
        { v: 1, status: 'retired', agree: 0.983, cases: 500, at: 28, note: 'First compile, from 2,260 runs' },
        { v: 2, status: 'live', agree: 0.996, cases: 500, at: 14, note: 'Stops after four tries', by: 'Dana Ruiz' },
      ],
      drift: [],
    },
    {
      id: 'lead-routing', name: 'Assign a new lead', agent: 'router', share: 0.83, app: 'salesforce', perCall: 0.0175, codeMs: 340, variance: 0.061,
      ask: 'New lead: Hooli, 240 seats',
      answer: 'Enterprise, West, Dana',
      steps: [
        { id: 'size', t: 'Size the company', d: 'Seats and revenue from the form', calls: 1, ms: 700 },
        { id: 'region', t: 'Find the region', d: 'From the country and state', calls: 1, ms: 500 },
        { id: 'owner', t: 'Pick an owner', d: 'Whose turn it is, in that segment', calls: 2, ms: 1100 },
      ],
      branches: [
        { at: 'size', t: 'An existing customer’s new team', share: 0.044 },
        { at: 'region', t: 'No country given', share: 0.021 },
      ],
      fields: ['NumberOfEmployees', 'Country', 'State', 'OwnerId'],
      code: [
        { step: null, lines: ['export function assignLead(lead) {'] },
        { step: 'size', lines: ['  if (accounts.matchDomain(lead.Email)) return fallback(\'existing customer\')', '  const segment = lead.NumberOfEmployees >= 200 ? \'enterprise\' : \'smb\''] },
        { step: 'region', lines: ['  if (!lead.Country) return fallback(\'no country\')', '  const region = REGIONS.of(lead.Country, lead.State)'] },
        { step: 'owner', lines: ['  const owner = rota(segment, region).next()', '  salesforce.leads.update(lead.Id, { OwnerId: owner.id })', '  return { segment, region, owner: owner.name }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: 'Lead 00Q8…, 199 seats', llm: 'Enterprise', code: 'SMB', right: 'code', why: 'Enterprise starts at 200 seats.' },
        { input: 'Lead 00Q3…, Puerto Rico', llm: 'LATAM', code: 'US East', right: 'llm', why: 'Puerto Rico is LATAM for sales. Kept as a test.' },
      ],
      versions: [
        { v: 1, status: 'shadow', agree: 0.978, cases: 500, at: 3, note: 'First compile, from 5,100 runs' },
      ],
      drift: [],
    },
    {
      id: 'address-change', name: 'Update a billing address', agent: 'accounts', share: 0.37, app: 'salesforce', perCall: 0.02, codeMs: 410, variance: 0.047,
      ask: 'We moved to 12 Hale St',
      answer: 'Address updated in both',
      steps: [
        { id: 'parse', t: 'Read the new address', d: 'From the customer’s message', calls: 1, ms: 900 },
        { id: 'check', t: 'Check it’s a real address', d: 'Against the postal service', calls: 1, ms: 700 },
        { id: 'write', t: 'Update it everywhere', d: 'Salesforce account and Stripe customer', calls: 1, ms: 800 },
      ],
      branches: [
        { at: 'parse', t: 'The message has two addresses', share: 0.058 },
        { at: 'check', t: 'Outside the US', share: 0.071 },
      ],
      fields: ['BillingStreet', 'BillingCity', 'BillingPostalCode', 'address'],
      code: [
        { step: null, lines: ['export function updateAddress(msg) {'] },
        { step: 'parse', lines: ['  const found = addresses.parse(msg.body)', '  if (found.length !== 1) return fallback(\'not one address\')'] },
        { step: 'check', lines: ['  const a = usps.verify(found[0])', '  if (!a) return fallback(\'not a US address\')'] },
        { step: 'write', lines: ['  salesforce.accounts.update(msg.account, { BillingStreet: a.street, BillingCity: a.city, BillingPostalCode: a.zip })', '  stripe.customers.update(msg.customer, { address: a })', '  return { updated: a }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: '“Ship to 4 Elm, bill to 9 Oak”', llm: 'Billing: 4 Elm', code: 'Sent to the LLM', right: 'code', why: 'Two addresses are off the path. The LLM picked the wrong one.' },
      ],
      versions: [
        { v: 1, status: 'candidate', agree: 0.969, cases: 500, at: 0, note: 'First compile, from 1,180 runs' },
      ],
      drift: [],
    },
    {
      id: 'plan-upgrade', name: 'Upgrade a plan', agent: 'pricer', share: 0.18, app: 'stripe', perCall: 0.035, codeMs: 520, variance: 0.066,
      ask: 'Move us to Team',
      answer: 'Upgraded, $41 prorated',
      steps: [
        { id: 'current', t: 'Find the current plan', d: 'And when it renews', calls: 1, ms: 800 },
        { id: 'price', t: 'Price the new plan', d: 'Seats, discount, proration', calls: 2, ms: 1400 },
        { id: 'switch', t: 'Switch it', d: 'With the prorated charge', calls: 1, ms: 700 },
      ],
      branches: [
        { at: 'price', t: 'A custom enterprise price', share: 0.08 },
      ],
      fields: ['items.price', 'quantity', 'discount', 'current_period_end'],
      code: [
        { step: null, lines: ['export function upgradePlan(req) {'] },
        { step: 'current', lines: ['  const sub = stripe.subscriptions.retrieve(req.subscription)'] },
        { step: 'price', lines: ['  if (sub.discount?.custom) return fallback(\'custom price\')', '  const price = PRICES[req.plan]', '  const owed = prorate(price, sub.quantity, sub.current_period_end)'] },
        { step: 'switch', lines: ['  stripe.subscriptions.update(sub.id, { items: [{ price: price.id }], proration_behavior: \'always_invoice\' })', '  return { plan: req.plan, owed }'] },
        { step: null, lines: ['}'] },
      ],
      disagree: [
        { input: 'sub_5Hd2, 12 seats, 10% off', llm: 'Charged $44', code: 'Charged $39.60', right: 'code', why: 'The discount carries over to the new plan.' },
      ],
      versions: [
        { v: 1, status: 'live', agree: 0.988, cases: 500, at: 12, note: 'First compile, from 980 runs', by: 'Lee Sato' },
      ],
      drift: [],
      paused: { by: 'Lee Sato', why: 'Prices change on Monday. The LLM handles it until then.', at: 1 },
    },
    // Seen often, not compiled yet.
    {
      id: 'invoice-resend', name: 'Resend an invoice', agent: 'invoice', share: 0.41, app: 'stripe', perCall: 0.0175, codeMs: 300, variance: 0.052, watching: true, runs: 2840,
      ask: 'Can you resend March?', answer: 'Sent inv_0312',
      steps: [
        { id: 'find', t: 'Find the invoice', d: 'By month and customer', calls: 2, ms: 1200 },
        { id: 'paid', t: 'Check it’s still the latest', d: 'Not replaced or voided', calls: 1, ms: 600 },
        { id: 'send', t: 'Send it', d: 'To the billing contact', calls: 1, ms: 700 },
      ],
      branches: [{ at: 'find', t: 'No month given', share: 0.09 }],
      fields: ['period_start', 'status', 'customer_email'],
      code: [], disagree: [], versions: [], drift: [], compileAgree: 0.972,
    },
    {
      id: 'ledger-booking', name: 'Book a payout', agent: 'ledger', share: 0.66, app: 'quickbooks', perCall: 0.025, codeMs: 360, variance: 0.044, watching: true, runs: 1960,
      ask: 'Stripe payout po_8812', answer: 'Booked to 1200',
      steps: [
        { id: 'payout', t: 'Read the payout', d: 'Charges, fees and refunds in it', calls: 2, ms: 1400 },
        { id: 'accounts', t: 'Pick the accounts', d: 'Revenue, fees, refunds', calls: 1, ms: 800 },
        { id: 'book', t: 'Book the entry', d: 'One journal entry', calls: 1, ms: 600 },
      ],
      branches: [{ at: 'payout', t: 'A payout in another currency', share: 0.06 }],
      fields: ['amount', 'fee', 'balance_transaction'],
      code: [], disagree: [], versions: [], drift: [], compileAgree: 0.981,
    },
    {
      id: 'quote-discount', name: 'Approve a discount', agent: 'quote', share: 0.29, app: 'salesforce', perCall: 0.0475, codeMs: 420, variance: 0.12, watching: true, runs: 1240,
      ask: '15% off for Umbra?', answer: 'Approved, under 20%',
      steps: [
        { id: 'deal', t: 'Read the deal', d: 'Size, term and stage', calls: 2, ms: 1500 },
        { id: 'rule', t: 'Check the discount rules', d: 'By deal size and term', calls: 2, ms: 1300 },
        { id: 'reply', t: 'Approve or ask', d: 'Over 20% asks the sales lead', calls: 1, ms: 900 },
      ],
      branches: [{ at: 'deal', t: 'A multi-year deal', share: 0.11 }],
      fields: ['Amount', 'Term__c', 'StageName'],
      code: [], disagree: [], versions: [], drift: [], compileAgree: 0.958,
    },
  ];
  const def = (id) => PATHS.find((p) => p.id === id);

  // ─── state ──────────────────────────────────────────
  //
  // What can change: each path's versions, its open drift, whether it's paused.
  // Every change returns a new state, so a page can keep it and redraw.

  const copy = (x) => JSON.parse(JSON.stringify(x));

  function seed() {
    const paths = {};
    for (const p of PATHS) paths[p.id] = { versions: copy(p.versions), drift: copy(p.drift), paused: p.paused ? copy(p.paused) : null, labels: {} };
    return { paths };
  }

  const liveOf = (ps) => ps.versions.find((v) => v.status === 'live') ?? null;
  const shadowOf = (ps) => ps.versions.find((v) => v.status === 'shadow') ?? null;
  const candidateOf = (ps) => ps.versions.find((v) => v.status === 'candidate') ?? null;
  const openDrift = (ps) => ps.drift.filter((d) => d.open);

  /** One word for where a path is: watching, candidate, shadow, live, drifted or paused. */
  function status(ps) {
    if (!ps.versions.length) return 'watching';
    if (ps.paused) return 'paused';
    if (liveOf(ps)) return openDrift(ps).some((d) => !d.fixedBy) ? 'drifted' : 'live';
    if (shadowOf(ps)) return 'shadow';
    return 'candidate';
  }

  /** What a person can do to a path now. */
  function can(st, id) {
    const ps = st.paths[id];
    if (!ps) return [];
    const s = status(ps);
    const out = [];
    if (s === 'watching') out.push('compile');
    if (candidateOf(ps)) out.push('shadow');
    if (shadowOf(ps) && !ps.paused) out.push('promote');
    if (liveOf(ps) && ps.versions.some((v) => v.status === 'retired') && !ps.paused) out.push('rollback');
    if (liveOf(ps)) out.push(ps.paused ? 'resume' : 'pause');
    if (openDrift(ps).some((d) => !d.fixedBy) && !shadowOf(ps) && !candidateOf(ps)) out.push('recompile');
    return out;
  }

  function change(st, id, verb, f) {
    if (!can(st, id).includes(verb)) return null;
    const next = copy(st);
    f(next.paths[id], def(id));
    return next;
  }
  const nextV = (ps) => Math.max(0, ...ps.versions.map((v) => v.v)) + 1;

  /** Turn a watched path into its first program, to review. */
  const compile = (st, id) => change(st, id, 'compile', (ps, d) => {
    ps.versions.push({ v: 1, status: 'candidate', agree: d.compileAgree ?? 0.97, cases: 500, at: 0, note: `First compile, from ${(d.runs ?? 1000).toLocaleString('en-US')} runs` });
  });
  /** Run the candidate next to the LLM, answering nothing yet. */
  const shadow = (st, id) => change(st, id, 'shadow', (ps) => {
    const c = candidateOf(ps);
    c.status = 'shadow';
    c.at = 0;
  });
  /** A person makes the shadow version the one that answers. */
  const promote = (st, id) => change(st, id, 'promote', (ps) => {
    const s = shadowOf(ps);
    const l = liveOf(ps);
    if (l) l.status = 'retired';
    s.status = 'live';
    s.at = 0;
    s.by = 'You';
    for (const d of ps.drift) if (d.fixedBy === s.v) d.open = false;
  });
  /** Back to the version before. The newer one is kept, marked rolled back. */
  const rollback = (st, id) => change(st, id, 'rollback', (ps) => {
    const l = liveOf(ps);
    const prev = ps.versions.filter((v) => v.status === 'retired').sort((a, b) => b.v - a.v)[0];
    l.status = 'rolled-back';
    prev.status = 'live';
    prev.at = 0;
    prev.by = 'You';
    // What the newer version fixed is broken again on the older one.
    for (const d of ps.drift) if (d.fixedBy === l.v) { d.open = true; d.fixedBy = null; }
  });
  const pause = (st, id) => change(st, id, 'pause', (ps) => { ps.paused = { by: 'You', why: 'Paused from the console. The LLM answers every run.', at: 0 }; });
  const resume = (st, id) => change(st, id, 'resume', (ps) => { ps.paused = null; });
  /** Build the next version against what changed; it starts in shadow. */
  const recompile = (st, id) => change(st, id, 'recompile', (ps) => {
    const v = nextV(ps);
    const drifts = openDrift(ps).filter((d) => !d.fixedBy);
    const l = liveOf(ps);
    ps.versions.push({ v, status: 'shadow', agree: Math.min(0.997, (l?.agree ?? 0.98) + 0.004), cases: 500, at: 0, note: drifts.map(fixNote).join('; ') });
    for (const d of drifts) d.fixedBy = v;
  });
  const fixNote = (d) => (d.kind === 'schema' && d.to ? `Reads ${d.to} (was ${d.field})` : d.kind === 'schema' ? `Follows the new ${d.field} field` : 'Follows the new policy');

  /** Mark who was right in a disagreement; it becomes a test. */
  function label(st, id, i, who) {
    const next = copy(st);
    next.paths[id].labels[i] = who;
    return next;
  }

  // ─── numbers ────────────────────────────────────────

  function agentOf(p, w) {
    const f = w.fleet?.find((x) => x.id === p.agent);
    return { name: f?.name ?? p.agent, n: f?.n ?? Math.round((w.total ?? 1000) * 0.1) };
  }

  /**
   * A path's day: runs, how many the program answered, LLM calls and time
   * before and after, money saved. `as` lets the list project a watched path.
   */
  function stats(p, ps, w, as) {
    const s = as ?? status(ps);
    const agent = agentOf(p, w);
    const runs = Math.max(12, Math.round(agent.n * p.share));
    const main = 1 - p.branches.reduce((t, b) => t + b.share, 0);
    const drifted = openDrift(ps).reduce((t, d) => t + d.hits, 0);
    const answering = s === 'live' || s === 'drifted' || s === 'projected';
    const hit = answering ? Math.max(0, main * (1 - Math.min(1, drifted))) : 0;
    const calls = p.steps.reduce((t, x) => t + x.calls, 0);
    const ms = p.steps.reduce((t, x) => t + x.ms, 0);
    const costBefore = calls * p.perCall;
    const served = Math.round(runs * hit);
    const callsAfter = (1 - hit) * calls;
    // Typical time: most runs take the program's time once it answers most.
    const msAfter = hit >= 0.5 ? p.codeMs : ms;
    const costAfter = hit * CODE_COST + (1 - hit) * (costBefore + CODE_COST);
    return {
      status: s, agent: agent.name, runs, main, hit, fallback: 1 - hit, served, llm: runs - served,
      calls, callsAfter, ms, msAfter, msMean: hit * p.codeMs + (1 - hit) * (ms + 40),
      costBefore, costAfter, saved: answering ? runs * (costBefore - costAfter) : 0, callsSaved: Math.round(served * calls),
      timeSaved: served * (ms - p.codeMs), variance: p.variance, varianceAfter: p.variance * (1 - hit),
    };
  }

  /** Seven days of runs the program answered, oldest first, for sparklines. */
  function week(p, ps, w) {
    const st = stats(p, ps, w);
    const r = rng(hash(p.id + w.key));
    // Answering since the oldest version that went live, and not while paused.
    const since = Math.max(-1, ...ps.versions.filter((v) => v.status !== 'candidate' && v.status !== 'shadow').map((v) => v.at));
    return Array.from({ length: 7 }, (_, i) => {
      const ago = 6 - i;
      const runs = st.runs * (i === 6 ? 1 : 0.82 + r() * 0.3);
      if (ago > since || (ps.paused && ago <= ps.paused.at)) return 0;
      const drift = openDrift(ps).filter((d) => d.at >= ago).reduce((t, d) => t + d.hits, 0);
      return Math.round(runs * st.main * (1 - Math.min(1, drift)));
    });
  }

  /** Every path that has a program, with its numbers. */
  function paths(w, st) {
    return PATHS.filter((p) => st.paths[p.id].versions.length).map((p) => view(p, st.paths[p.id], w));
  }
  function view(p, ps, w) {
    return { ...p, state: ps, ...stats(p, ps, w), live: liveOf(ps), shadow: shadowOf(ps), candidate: candidateOf(ps), drifts: openDrift(ps), week: week(p, ps, w), can: null };
  }
  /** One path, or null. */
  function path(w, st, id) {
    const p = def(id);
    return p && st.paths[id] ? { ...view(p, st.paths[id], w), can: can(st, id) } : null;
  }

  /** Paths Lumni keeps seeing but hasn't compiled, with what compiling would save. */
  function suggest(w, st) {
    return PATHS.filter((p) => !st.paths[p.id].versions.length).map((p) => {
      const s = stats(p, st.paths[p.id], w, 'projected');
      return { id: p.id, name: p.name, app: p.app, agent: s.agent, steps: p.steps, runs: s.runs, main: s.main, saved: s.saved, msBefore: s.ms, msAfter: p.codeMs, calls: s.calls };
    });
  }

  /** Totals for the list's tiles. */
  function totals(list) {
    const sum = (f) => list.reduce((t, x) => t + x[f], 0);
    const live = list.filter((x) => x.status === 'live' || x.status === 'drifted');
    const runs = sum('runs');
    const before = live.reduce((t, x) => t + x.ms * x.served, 0) / Math.max(1, sum('served'));
    const after = live.reduce((t, x) => t + x.codeMs * x.served, 0) / Math.max(1, sum('served'));
    return { runs, served: sum('served'), callsSaved: sum('callsSaved'), saved: sum('saved'), timeSaved: sum('timeSaved'), msBefore: before, msAfter: after, live: live.length };
  }

  /**
   * Check a version on past runs. Off-path runs aren't the program's to answer;
   * the rest agree with what the LLM decided, or don't. `tiles` is a sample in
   * order, for the page to drop into bins.
   */
  function replay(p, version) {
    const n = version.cases;
    const main = 1 - p.branches.reduce((t, b) => t + b.share, 0);
    const out = Math.round(n * (1 - main));
    const inScope = n - out;
    const agree = Math.round(inScope * version.agree);
    const disagree = inScope - agree;
    const r = rng(hash(`${p.id}:${version.v}`));
    const tiles = [];
    const k = 60;
    const want = { a: Math.round((k * agree) / n), d: Math.max(disagree ? 1 : 0, Math.round((k * disagree) / n)), o: 0 };
    want.o = k - want.a - want.d;
    const bag = [...'a'.repeat(want.a), ...'d'.repeat(want.d), ...'o'.repeat(Math.max(0, want.o))];
    for (let i = bag.length - 1; i > 0; i--) { const j = Math.floor(r() * (i + 1)); [bag[i], bag[j]] = [bag[j], bag[i]]; }
    tiles.push(...bag);
    return { n, agree, disagree, out, rate: agree / Math.max(1, inScope), tiles, samples: p.disagree.slice(0, Math.max(1, Math.min(p.disagree.length, disagree))) };
  }

  /** Why runs went to the LLM today, most first. */
  function fallbacks(p, ps, w) {
    const s = stats(p, ps, w);
    const live = s.status === 'live' || s.status === 'drifted';
    const rows = p.branches.map((b) => ({ t: b.t, at: b.at, n: live ? Math.round(s.runs * b.share) : 0, kind: 'off-path' }));
    for (const d of openDrift(ps)) rows.push({ t: d.kind === 'schema' ? `Reads ${d.field}, which changed` : 'Hit the changed policy', at: d.step, n: live ? Math.round(s.runs * s.main * d.hits) : 0, kind: 'drift' });
    return rows.sort((a, b) => b.n - a.n);
  }

  /** The one request the page races: LLM lane vs program lane. */
  function race(p) {
    return { ask: p.ask, answer: p.answer, llm: p.steps.map((x) => ({ t: x.t, calls: x.calls, ms: x.ms })), codeMs: p.codeMs };
  }

  globalThis.Compiler = {
    PATHS, def, seed, status, can, compile, shadow, promote, rollback, pause, resume, recompile, label,
    stats, week, paths, path, suggest, totals, replay, fallbacks, race, hash, money,
  };
})();
