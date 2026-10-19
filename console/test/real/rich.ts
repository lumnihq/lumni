// A fuller day for the fake API, so every console screen has real-shaped
// records to read: six AIs in teams, six apps (Gmail not checked), repeats,
// look-agains, waits on shared things, decisions waiting under two rules, rules
// only watching, things the app doesn't show, a burst of retries late in the
// morning, and one AI stuck in a retry loop right now.
//
// It also holds what the Console API reads: a version history for every write,
// read ages on version checks, app call timings (Stripe slow during the
// burst), the burst as an incident, a quote writer running away right now,
// leases with a holder and a queue, a billing plan, this morning's audit
// history, and seven finished days before today.
//
// Deterministic for a given `now`, so tests can count on it.

import { makeZone } from '../../src/domain/format';
import { approvalContext, audit, factOf, world, type DayHistory, type FakeAction, type FakeIncident, type FakeWorld, type TelemetryPoint, type VersionRow } from './fake-go';

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const iso = (t: number) => new Date(t).toISOString();

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const AIS = [
  { id: 'ai_refund', name: 'Refund agent', team: 'Support', w: 0.27 },
  { id: 'ai_triage', name: 'Ticket triage', team: 'Support', w: 0.2 },
  { id: 'ai_invoice', name: 'Invoice sender', team: 'Billing', w: 0.18 },
  { id: 'ai_quote', name: 'Quote writer', team: 'Sales', w: 0.13 },
  { id: 'ai_price', name: 'Price setter', team: 'Pricing', w: 0.12 },
  { id: 'ai_deploy', name: 'Deploy bot', team: 'Ops', w: 0.1 },
];

const NAMES = ['Omar Haddad', 'Priya Shah', 'Sam Lee', 'Dana Ruiz', 'Kai Rhee', 'Ben Ito', 'Zoe Hart', 'Nina Cole', 'Theo Park', 'Iris Bell', 'Hugo Lane', 'Mia Torres'];
const COMPANIES = ['Initech', 'Umbra', 'Hooli', 'Vandelay', 'Tyrell', 'Northwind'];

/** What each AI does, and the app it does it in. */
function work(ai: string, n: number): Pick<FakeAction, 'op' | 'meta' | 'resources' | 'cost'> {
  const person = NAMES[n % NAMES.length]!;
  const company = COMPANIES[n % COMPANIES.length]!;
  const amount = [20, 30, 45, 60, 75, 90, 120][n % 7]!;
  switch (ai) {
    case 'ai_refund':
      return { op: 'stripe.refund', meta: { app: 'stripe', summary: `Refund $${amount} to ${person}`, customer: person, amount }, resources: [{ id: `stripe:charge:ch_${n}`, mode: 'WRITE', type: 'charge', name: `${person}’s order`, version: String(2 + (n % 3)) }], cost: 80_000 };
    case 'ai_triage':
      return { op: 'zendesk.reply', meta: { app: 'zendesk', summary: `Reply to ${person}’s ticket`, customer: person }, resources: [{ id: `zendesk:ticket:t_${4300 + (n % 60)}`, mode: 'WRITE', type: 'ticket', name: `Ticket #${4300 + (n % 60)}`, version: String(1 + (n % 4)) }], cost: 50_000 };
    case 'ai_invoice':
      return { op: 'stripe.send_invoice', meta: { app: 'stripe', summary: `Send ${company} invoice #${2200 + (n % 90)}`, customer: company, amount: amount * 10 }, resources: [{ id: `stripe:invoice:in_${2200 + (n % 90)}`, mode: 'WRITE', type: 'invoice', name: `Invoice #${2200 + (n % 90)}`, version: '1' }], cost: 40_000 };
    case 'ai_quote':
      return { op: 'gmail.send', meta: { app: 'gmail', summary: `Email ${company} a quote`, customer: company }, resources: [{ id: `gmail:thread:q_${n}`, mode: 'CREATE', type: 'thread', name: `Quote for ${company}` }], cost: 190_000 };
    case 'ai_price':
      return { op: 'salesforce.update_price', meta: { app: 'salesforce', summary: `Update ${company}’s seat price`, customer: company }, resources: [{ id: 'salesforce:pricebook:enterprise', mode: 'WRITE', type: 'price list', name: 'Enterprise price list', version: String(28 + (n % 4)) }], cost: 120_000 };
    default:
      return { op: 'vercel.deploy', meta: { app: 'vercel', summary: 'Deploy a config change' }, resources: [{ id: 'vercel:site:checkout', mode: 'WRITE', type: 'site', name: 'Checkout site', version: String(200 + (n % 9)) }], cost: 140_000 };
  }
}

/** Today from midnight to now, with `n` routine actions and every kind of step-in. */
export function richDay(n = 900, over: Partial<FakeWorld> = {}, zone = 'UTC', now = Date.now()): FakeWorld {
  const z = makeZone(zone);
  const start = z.dayStart(now);
  const span = Math.max(30 * MIN, now - start - MIN);
  const r = rng(Math.floor(start / 86_400_000));
  const w = world({
    actors: [
      ...AIS.map((a) => ({ id: a.id, type: 'AGENT', display_name: a.name, metadata: { team: a.team } })),
      { id: 'u_jess', type: 'HUMAN', display_name: 'Jess Park' },
      { id: 'u_ak', type: 'HUMAN', display_name: 'AK' },
    ],
    agents: [
      ...AIS.flatMap((a, i) => Array.from({ length: 1 + (i % 3) }, (_, k) => ({ id: `ag_${a.id}_${k}`, actor_id: a.id, name: `${a.name} ${k + 1}` }))),
    ],
    verifiers: [
      { id: 'v_stripe', name: 'Stripe events', kind: 'EVENT', version: '1', config: { app: 'stripe' }, enabled: true, created_at: iso(start) },
      { id: 'v_zendesk', name: 'Zendesk tickets', kind: 'READ_BACK', version: '1', config: { app: 'zendesk' }, enabled: true, created_at: iso(start) },
      { id: 'v_salesforce', name: 'Salesforce records', kind: 'READ_BACK', version: '1', config: { app: 'salesforce' }, enabled: true, created_at: iso(start) },
      { id: 'v_vercel', name: 'Vercel deploys', kind: 'EVENT', version: '1', config: { app: 'vercel' }, enabled: true, created_at: iso(start) },
      { id: 'v_gmail', name: 'Gmail sent mail', kind: 'READ_BACK', version: '1', config: { app: 'gmail' }, enabled: false, created_at: iso(start) },
    ],
    rules: [
      { id: 'pr_refunds', name: 'Refunds over $500 need a person', operation: 'stripe.refund', resource_type: '*', minimum_risk: null, argument_pointer: '/amount', numeric_threshold: 500, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', enabled: true, version: 3, created_at: iso(start - 9 * 86_400_000) },
      { id: 'pr_prices', name: 'Price changes over 10% need a person', operation: 'salesforce.update_price', resource_type: '*', minimum_risk: null, argument_pointer: '/change_pct', numeric_threshold: 10, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', enabled: true, version: 1, created_at: iso(start - 4 * 86_400_000) },
    ],
    enforcement: [
      { operation: 'stripe.refund', rule: '*', mode: 'ENFORCE' },
      { operation: 'stripe.send_invoice', rule: '*', mode: 'ENFORCE' },
      { operation: 'zendesk.reply', rule: '*', mode: 'ENFORCE' },
      { operation: 'vercel.deploy', rule: '*', mode: 'ENFORCE' },
      { operation: 'salesforce.update_price', rule: '*', mode: 'WARN' },
    ],
    mode: 'SHADOW',
    perSnapshot: 400,
    billing: { plan: 'team', price_per_action_micros_usd: 20_000 },
    connections: [{ id: 'ca_slack_1', toolkit: 'slack' }],
    ...over,
  });

  const pick = () => {
    let x = r();
    for (const a of AIS) {
      x -= a.w;
      if (x < 0) return a.id;
    }
    return AIS[0]!.id;
  };
  const actions: FakeAction[] = [];
  const add = (a: FakeAction) => {
    actions.push(a);
    const d = a.decision;
    if (d && d.d !== 'ALLOW') {
      w.incidents.push({ id: `finding_rcpt_${a.id}`, kind: d.r, action_id: a.id, work_id: a.work, status: 'OPEN', title: d.r, evidence: { receipt_id: `rcpt_${a.id}`, action_id: a.id, decision: d.d, reason_code: d.r, enforced: !d.d.startsWith('WOULD_') }, created_at: iso(a.at), updated_at: iso(a.at) });
    }
  };
  const PASS = [{ stage: 'resource_versions', outcome: 'PASS' }, { stage: 'idempotency', outcome: 'PASS' }, { stage: 'conflicts', outcome: 'PASS' }, { stage: 'policy', outcome: 'PASS' }];

  for (let i = 0; i < n; i++) {
    const at = start + Math.round(((i + r()) / n) * span);
    const ai = pick();
    const base: FakeAction = {
      id: `a_${i}`, at, actor: ai, work: `w_${i}`, status: 'SUCCEEDED', ...work(ai, i),
      decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: PASS },
      outcome: { status: 'VERIFIED', verifier: 'v_stripe' },
    };
    if (ai === 'ai_quote') base.outcome = { status: 'UNKNOWN', verifier: null };
    const roll = r();
    if (ai === 'ai_refund' && roll < 0.04) {
      base.decision = { d: 'DEDUPLICATE', r: 'EXACT_EFFECT_ALREADY_COMMITTED', evals: [PASS[0]!, { stage: 'exact_effect', outcome: 'DEDUPLICATE', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED' }] };
      base.status = 'CANCELLED';
      base.outcome = undefined;
    } else if (ai === 'ai_invoice' && roll < 0.025) {
      base.decision = { d: 'DEDUPLICATE', r: 'EXACT_EFFECT_ALREADY_COMMITTED', evals: [PASS[0]!, { stage: 'exact_effect', outcome: 'DEDUPLICATE', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED' }] };
      base.status = 'CANCELLED';
      base.outcome = undefined;
    } else if (ai === 'ai_price' && roll < 0.09) {
      base.decision = { d: 'REPLAN', r: 'STALE_OBSERVATION', evals: [{ stage: 'resource_versions', outcome: 'REPLAN', reason_code: 'STALE_OBSERVATION' }] };
    } else if (ai === 'ai_triage' && roll < 0.03) {
      base.decision = { d: 'REPLAN', r: 'STALE_OBSERVATION', evals: [{ stage: 'resource_versions', outcome: 'REPLAN', reason_code: 'STALE_OBSERVATION' }] };
    } else if (ai === 'ai_deploy' && roll < 0.12) {
      base.decision = { d: 'WAIT', r: 'WRITE_WRITE_CONFLICT', evals: [PASS[0]!, PASS[1]!, { stage: 'conflicts', outcome: 'WAIT', reason_code: 'WRITE_WRITE_CONFLICT' }] };
    } else if (ai === 'ai_triage' && roll < 0.05) {
      base.decision = { d: 'DEDUPLICATE', r: 'EXACT_DUPLICATE_ACTIVE', evals: [PASS[0]!, { stage: 'idempotency', outcome: 'DEDUPLICATE', reason_code: 'EXACT_DUPLICATE_ACTIVE' }] };
      base.status = 'CANCELLED';
      base.outcome = undefined;
    } else if (ai === 'ai_quote' && roll < 0.05) {
      base.decision = { d: 'WOULD_REQUIRE_APPROVAL', r: 'APPROVAL_REQUIRED' };
    } else if (ai === 'ai_refund' && roll < 0.055) {
      base.outcome = { status: 'MISMATCH', verifier: 'v_stripe' };
      w.incidents.push({ id: `outcome_out_${base.id}`, kind: 'OUTCOME_MISMATCH', action_id: base.id, work_id: base.work, status: 'OPEN', title: 'OUTCOME_MISMATCH', evidence: { status: 'MISMATCH', reason: 'there’s no refund in Stripe' }, created_at: iso(at + 3000), updated_at: iso(at + 3000) });
    }
    add(base);
  }

  // A few refunds the app never shows, spread through the day.
  for (let k = 0; k < 4; k++) {
    const at = start + Math.round(((k + 1) / 5) * span);
    const id = `a_missing_${k}`;
    add({ id, at, actor: 'ai_invoice', work: `w_missing_${k}`, status: 'SUCCEEDED', ...work('ai_refund', 700 + k), decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: PASS }, outcome: { status: 'MISMATCH', verifier: 'v_stripe', attempts: 3 } });
    w.incidents.push({ id: `outcome_out_${id}`, kind: 'OUTCOME_MISMATCH', action_id: id, work_id: `w_missing_${k}`, status: 'OPEN', title: 'OUTCOME_MISMATCH', evidence: { status: 'MISMATCH', reason: 'there’s no refund in Stripe' }, created_at: iso(at + 3000), updated_at: iso(at + 3000) });
  }

  // A burst late in the morning: the app timed out and the refund agent retried.
  const burstAt = start + 11 * HOUR + 36 * MIN;
  if (burstAt < now - 20 * MIN) {
    for (let k = 0; k < 42; k++) {
      const at = burstAt + Math.round((k / 42) * 9 * MIN + r() * 30_000);
      add({
        id: `a_burst_${k}`, at, actor: 'ai_refund', work: `w_burst_${k}`, status: 'CANCELLED', ...work('ai_refund', 500 + k),
        decision: { d: 'DEDUPLICATE', r: 'EXACT_EFFECT_ALREADY_COMMITTED', evals: [PASS[0]!, { stage: 'exact_effect', outcome: 'DEDUPLICATE', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED' }] },
      });
    }
  }

  // Stuck right now: the deploy bot restarting the same site.
  for (let k = 0; k < 9; k++) {
    add({
      id: `a_loop_${k}`, at: now - (9 - k) * 50_000, actor: 'ai_deploy', work: 'w_loop', status: 'CANCELLED', op: 'vercel.restart',
      meta: { app: 'vercel', summary: 'Restart the checkout site' }, resources: [{ id: 'vercel:site:checkout', mode: 'WRITE', type: 'site', name: 'Checkout site', version: '208' }], cost: 60_000,
      decision: { d: 'DEDUPLICATE', r: 'EXACT_DUPLICATE_ACTIVE', existing: 'a_loop_hold', evals: [PASS[0]!, { stage: 'idempotency', outcome: 'DEDUPLICATE', reason_code: 'EXACT_DUPLICATE_ACTIVE' }] },
    });
  }

  // Decisions waiting on you, under two rules.
  const asks = [
    { who: 'Dana Ruiz', amount: 620, mins: 31, op: 'stripe.refund', ai: 'ai_refund' },
    { who: 'Kai Rhee', amount: 540, mins: 24, op: 'stripe.refund', ai: 'ai_refund' },
    { who: 'Ben Ito', amount: 880, mins: 12, op: 'stripe.refund', ai: 'ai_refund' },
    { who: 'Mia Torres', amount: 7440, mins: 6, op: 'stripe.refund', ai: 'ai_refund' },
    { who: 'Hooli', amount: 18, mins: 18, op: 'salesforce.update_price', ai: 'ai_price' },
    { who: 'Tyrell', amount: 14, mins: 3, op: 'salesforce.update_price', ai: 'ai_price' },
  ];
  asks.forEach((x, k) => {
    const at = now - x.mins * MIN;
    const isRefund = x.op === 'stripe.refund';
    const id = `a_ask_${k}`;
    add({
      id, at, actor: x.ai, work: `w_ask_${k}`, op: x.op, status: 'AWAITING_APPROVAL',
      meta: isRefund ? { app: 'stripe', summary: `Refund $${x.amount} to ${x.who}`, customer: x.who, amount: x.amount } : { app: 'salesforce', summary: `Raise ${x.who}’s seat price ${x.amount}%`, customer: x.who, change_pct: x.amount },
      decision: { d: 'REQUIRE_APPROVAL', r: 'APPROVAL_REQUIRED', evals: [PASS[0]!, PASS[1]!, PASS[2]!, { stage: 'policy', outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' }] },
      outcome: { status: 'PENDING', verifier: isRefund ? 'v_stripe' : 'v_salesforce' },
      resources: [{ id: isRefund ? `stripe:charge:ch_ask_${k}` : 'salesforce:pricebook:enterprise', mode: 'WRITE', type: isRefund ? 'charge' : 'price list', name: isRefund ? `${x.who}’s order` : 'Enterprise price list' }],
      cost: 60_000,
    });
    w.approvals.push({ id: `ap_${k}`, action_id: id, status: 'PENDING', reason_code: 'APPROVAL_REQUIRED', requested_at: iso(at), expires_at: iso(at + 86_400_000), resolved_at: null, policy_id: isRefund ? 'pr_refunds' : 'pr_prices' });
  });

  consoleData(w, actions, { start, now, burstAt, n, span, z: zone });
  w.actions = actions.sort((a, b) => a.at - b.at);
  return w;
}

const CHECKS = (details?: Record<string, unknown>) => [
  { stage: 'resource_versions', outcome: 'PASS', ...(details ? { details } : {}) },
  { stage: 'idempotency', outcome: 'PASS' },
  { stage: 'conflicts', outcome: 'PASS' },
  { stage: 'policy', outcome: 'PASS' },
];

/** A short, stable hex hash, standing in for a value hash. */
function hash(s: string) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193) >>> 0;
  return h.toString(16).padStart(8, '0');
}

/** What the Console API reads beyond the actions themselves. Uses its own random numbers, so the day above stays as it was. */
function consoleData(w: FakeWorld, actions: FakeAction[], o: { start: number; now: number; burstAt: number; n: number; span: number; z: string }) {
  const { start, now, burstAt, n, span } = o;
  const r = rng(Math.floor(start / 86_400_000) * 7 + 3);

  // Live: the restart the deploy bot keeps repeating, deploys queued behind it,
  // a price change holding the price list and one waiting for it.
  const site = { id: 'vercel:site:checkout', mode: 'WRITE', type: 'site', name: 'Checkout site' };
  const prices = { id: 'salesforce:pricebook:enterprise', mode: 'WRITE', type: 'price list', name: 'Enterprise price list' };
  const holdAt = now - 500_000;
  actions.push({
    id: 'a_loop_hold', at: holdAt, actor: 'ai_deploy', work: 'w_loop', status: 'ACTIVE', op: 'vercel.restart',
    meta: { app: 'vercel', summary: 'Restart the checkout site' }, resources: [{ ...site }], cost: 60_000, decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: CHECKS() },
  });
  const priceAt = now - 70_000;
  actions.push({
    id: 'a_price_live', at: priceAt, actor: 'ai_price', work: 'w_price_live', status: 'ACTIVE', op: 'salesforce.update_price',
    meta: { app: 'salesforce', summary: 'Update Umbra’s seat price', customer: 'Umbra' }, resources: [{ ...prices }], cost: 120_000, decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: CHECKS() },
  });
  const waits = [
    { id: 'a_wait_site_0', at: now - 150_000, actor: 'ai_deploy', op: 'vercel.deploy', meta: { app: 'vercel', summary: 'Deploy a config change' }, res: site },
    { id: 'a_wait_site_1', at: now - 40_000, actor: 'ai_deploy', op: 'vercel.deploy', meta: { app: 'vercel', summary: 'Deploy a config change' }, res: site },
    { id: 'a_wait_price_0', at: now - 25_000, actor: 'ai_price', op: 'salesforce.update_price', meta: { app: 'salesforce', summary: 'Update Hooli’s seat price', customer: 'Hooli' }, res: prices },
  ];
  for (const x of waits) {
    actions.push({
      id: x.id, at: x.at, actor: x.actor, work: `w_${x.id}`, status: 'PROPOSED', op: x.op, meta: x.meta, resources: [{ ...x.res }], cost: 0,
      decision: { d: 'WAIT', r: 'WRITE_WRITE_CONFLICT', evals: [CHECKS()[0]!, CHECKS()[1]!, { stage: 'conflicts', outcome: 'WAIT', reason_code: 'WRITE_WRITE_CONFLICT' }] },
    });
  }
  w.leases.push(
    { resource_id: site.id, action_id: 'a_loop_hold', actor_id: 'ai_deploy', since: iso(holdAt), expires_at: iso(holdAt + 15 * MIN), fencing_token: 4127 },
    { resource_id: prices.id, action_id: 'a_price_live', actor_id: 'ai_price', since: iso(priceAt), expires_at: iso(priceAt + 5 * MIN), fencing_token: 912 },
  );
  w.leaseStats[site.id] = { handoffs_last_hour: 7, refused_late_writes_today: 2 };
  w.leaseStats[prices.id] = { handoffs_last_hour: 4, refused_late_writes_today: 1 };

  // A refund a person approved this morning.
  const doneAt = start + 9 * HOUR + 58 * MIN;
  if (doneAt + 7 * MIN < now) {
    actions.push({
      id: 'a_ask_done', at: doneAt, actor: 'ai_refund', work: 'w_ask_done', op: 'stripe.refund', status: 'SUCCEEDED',
      meta: { app: 'stripe', summary: 'Refund $650 to Sam Lee', customer: 'Sam Lee', amount: 650 },
      decision: { d: 'REQUIRE_APPROVAL', r: 'APPROVAL_REQUIRED', evals: [...CHECKS().slice(0, 3), { stage: 'policy', outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' }] },
      outcome: { status: 'VERIFIED', verifier: 'v_stripe' }, resources: [{ id: 'stripe:charge:ch_ask_done', mode: 'WRITE', type: 'charge', name: 'Sam Lee’s order' }], cost: 60_000,
    });
    w.approvals.push({ id: 'ap_done', action_id: 'a_ask_done', status: 'APPROVED', reason_code: 'APPROVAL_REQUIRED', requested_at: iso(doneAt), expires_at: iso(doneAt + DAY), resolved_at: iso(doneAt + 7 * MIN), resolved_by: 'u_ak', policy_id: 'pr_refunds' });
  }

  // The quote writer running away: several times its usual hourly pace, this hour.
  const usual = (0.13 * n) / Math.max(1, span / HOUR);
  const rush = Math.max(10, Math.round(5.5 * usual));
  const hourStart = Math.floor(now / HOUR) * HOUR;
  let ws = Math.max(hourStart + 30_000, now - 15 * MIN);
  const we = now - 20_000;
  if (we - ws < MIN) ws = now - 15 * MIN;
  for (let k = 0; k < rush; k++) {
    const company = COMPANIES[k % COMPANIES.length]!;
    actions.push({
      id: `a_rush_${k}`, at: Math.round(ws + ((k + 0.5) / rush) * (we - ws)), actor: 'ai_quote', work: `w_rush_${k}`, status: 'SUCCEEDED', op: 'gmail.send',
      meta: { app: 'gmail', summary: `Email ${company} a quote`, customer: company }, resources: [{ id: `gmail:thread:q_rush_${k}`, mode: 'CREATE', type: 'thread', name: `Quote for ${company}` }], cost: 190_000,
      decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: CHECKS() }, outcome: { status: 'UNKNOWN', verifier: null },
    });
  }
  actions.sort((a, b) => a.at - b.at || (a.id < b.id ? -1 : 1));

  // Every write makes a version; reads say how old the version they saw was.
  const current = new Map<string, number>();
  const last = new Map<string, VersionRow>();
  const tracked = new Set(['ai_price', 'ai_triage', 'ai_deploy']);
  const price = (id: string) => 36 + (Number(/\d+/.exec(id)?.[0] ?? 0) % 10);
  const summary = (a: FakeAction, version: number) => {
    if (a.op === 'salesforce.update_price') return `${a.meta?.customer ?? 'Enterprise'} seat price $${price(a.id)}`;
    if (a.op === 'vercel.deploy') return `Config deploy, build ${version}`;
    if (a.op === 'vercel.restart') return 'Restarted';
    return String(a.meta?.summary ?? a.op);
  };
  for (const a of actions) {
    const d = a.decision?.d ?? 'ALLOW';
    for (const res of a.resources ?? []) {
      if (!current.has(res.id)) current.set(res.id, res.mode === 'CREATE' ? 0 : Number(res.version ?? 1) || 1);
      const cur = current.get(res.id)!;
      const ev = a.decision?.evals?.[0];
      if (ev?.stage === 'resource_versions' && tracked.has(a.actor) && res.mode === 'WRITE') {
        const writer = last.get(res.id);
        const stale = ev.outcome === 'REPLAN';
        const observed = stale ? Math.max(1, cur - 1 - Math.floor(r() * 2)) : cur;
        const age = stale ? Math.round(3 * MIN + r() * 12 * MIN) : Math.round(2_000 + r() * 38_000);
        const details = {
          resources: [{
            resource_id: res.id, observed_version: String(observed), current_version: String(cur), observed_at: iso(a.at - age), age_ms: age,
            changed_by_actor_id: stale ? (writer?.actor_id ?? null) : null,
          }],
        };
        a.decision!.evals = [{ ...ev, details }, ...a.decision!.evals!.slice(1)];
      }
      if (res.mode === 'CREATE' || res.mode === 'WRITE') res.version = String(cur);
      const wrote = (KIND_ALLOW.has(d) || d.startsWith('WOULD_')) && ['SUCCEEDED', 'ACTIVE'].includes(a.status) && (res.mode === 'WRITE' || res.mode === 'CREATE');
      if (!wrote) continue;
      const v = cur + 1;
      current.set(res.id, v);
      const row: VersionRow = { version: String(v), at: iso(a.at + 1500), actor_id: a.actor, action_id: a.id, summary: summary(a, v), value_hash: hash(`${res.id}@${v}`) };
      (w.versions[res.id] ??= []).push(row);
      last.set(res.id, row);
    }
  }

  // The burst, as the worker records it: ten-minute windows well over the hour before.
  w.incidents.push(...bursts(actions, now));

  // How each app answered, minute by minute; Stripe crawled around the burst.
  const APPS: Record<string, [number, number]> = { stripe: [180, 420], zendesk: [240, 600], salesforce: [320, 900], vercel: [850, 2400], gmail: [150, 380] };
  const slowFrom = burstAt - 4 * MIN;
  const slowTo = burstAt + 12 * MIN;
  for (const [app, [p50, p95]] of Object.entries(APPS)) {
    const series: TelemetryPoint[] = [];
    for (let t = start; t + MIN <= now; t += MIN) {
      const jitter = 0.85 + r() * 0.3;
      const slow = app === 'stripe' && t >= slowFrom && t < slowTo && burstAt < now - 20 * MIN;
      series.push(
        slow
          ? { at: t, p50_ms: Math.round(2_300 + r() * 600), p95_ms: Math.round(8_600 + r() * 800), errors: 1 + Math.floor(r() * 4), timeouts: 2 + Math.floor(r() * 6) }
          : { at: t, p50_ms: Math.round(p50 * jitter), p95_ms: Math.round(p95 * jitter), errors: r() < 0.02 ? 1 : 0, timeouts: 0 },
      );
    }
    w.telemetry[app] = series;
  }

  // This morning's changes, in the order they were made.
  const history: [number, () => void][] = [
    [start + 8 * HOUR + 40 * MIN, () => {
      const rule = w.rules.find((x) => x.id === 'pr_refunds');
      if (rule) audit(w, 'policy_rule.updated', { id: rule.id, operation: rule.operation }, { ...rule, numeric_threshold: 250, version: rule.version - 1 }, rule, 'Jess Park', start + 8 * HOUR + 40 * MIN);
    }],
    [start + 9 * HOUR + 12 * MIN, () => {
      audit(w, 'enforcement.set', { operation: 'salesforce.update_price', rule: '*' }, { operation: 'salesforce.update_price', rule: '*', mode: 'SHADOW' }, { operation: 'salesforce.update_price', rule: '*', mode: 'WARN' }, 'Jess Park', start + 9 * HOUR + 12 * MIN);
    }],
    [doneAt + 7 * MIN, () => {
      const ap = w.approvals.find((x) => x.id === 'ap_done');
      if (ap) audit(w, 'approval.decided', { id: ap.id, action_id: ap.action_id, ...approvalContext(w, ap.action_id) }, { ...ap, status: 'PENDING', resolved_at: null, resolved_by: null }, ap, 'AK', doneAt + 7 * MIN);
    }],
  ];
  for (const [at, write] of history) if (at < now) write();

  // Seven finished days, at about today's pace; weekends are quieter.
  const rate = n / Math.max(1, span / HOUR);
  const zone = makeZone(o.z);
  for (let d = 1; d <= 7; d++) {
    const day = zone.dayStart(start - d * DAY + 12 * HOUR);
    const weekday = new Date(day + zone.offset(day) + 12 * HOUR).getUTCDay();
    const mult = weekday === 0 || weekday === 6 ? 0.55 : 0.9 + r() * 0.2;
    const hours = Array.from({ length: 24 }, () => Math.round(rate * mult * (0.85 + r() * 0.3)));
    const total = hours.reduce((s, x) => s + x, 0);
    const part = (f: number) => Math.round(total * f * (0.8 + r() * 0.4));
    const kinds = { stop: part(0.055), wait: part(0.013), recheck: part(0.016), ask: part(0.004), bad: part(0.012), blind: part(0.13) };
    const row: DayHistory = {
      days_ago: d, hours, kinds: { ...kinds, allow: total - kinds.stop - kinds.wait - kinds.recheck - kinds.ask - kinds.bad },
      kept_micros_usd: Math.round(kinds.stop * 0.6 * 62) * 1_000_000, cost_micros_usd: Math.round(total * (95_000 + r() * 15_000)), verified: Math.round(total * (0.76 + r() * 0.06)),
    };
    w.history.push(row);
  }
}

const KIND_ALLOW = new Set(['ALLOW', 'REQUIRE_APPROVAL']);

/** Burst incidents as the worker writes them (analytics/incidents.go), over the whole day. */
function bursts(actions: FakeAction[], now: number): FakeIncident[] {
  const steps = actions.filter((a) => a.at <= now).map((a) => ({ a, f: factOf(a) })).filter(({ f }) => f.kind !== 'allow' || ['MISMATCH', 'PARTIAL', 'FAILED'].includes(f.outcome));
  const cells = new Map<string, typeof steps>();
  for (const s of steps) {
    const bucket = Math.floor(s.a.at / (10 * MIN)) * 10 * MIN;
    const k = `${s.f.app}|${bucket}`;
    cells.set(k, [...(cells.get(k) ?? []), s]);
  }
  const out: FakeIncident[] = [];
  for (const [k, xs] of cells) {
    const [app, b] = k.split('|');
    const bucket = Number(b);
    if (xs.length < 10) continue;
    const before = steps.filter((s) => s.f.app === app && s.a.at >= bucket - HOUR && s.a.at < bucket).length / 6;
    if (xs.length < 5 * Math.max(1, before)) continue;
    xs.sort((x, y) => x.a.at - y.a.at);
    const perMinute = new Map<number, number>();
    for (const s of xs) perMinute.set(Math.floor(s.a.at / MIN) * MIN, (perMinute.get(Math.floor(s.a.at / MIN) * MIN) ?? 0) + 1);
    const count = (kind: string) => xs.filter((s) => s.f.kind === kind).length;
    const evidence = {
      app, started_at: iso(xs[0]!.a.at), ended_at: iso(xs.at(-1)!.a.at), pages: 0,
      totals: {
        stepins: xs.length, stop: count('stop'), wait: count('wait'), recheck: count('recheck'), ask: count('ask'), kept_micros_usd: xs.reduce((s, x) => s + x.f.kept, 0),
        customers: new Set(xs.map((s) => s.a.meta?.customer).filter((c) => c != null)).size, actors: new Set(xs.map((s) => s.a.actor)).size,
      },
      per_minute: [...perMinute].sort((x, y) => x[0] - y[0]).map(([at, c]) => ({ at: iso(at), stepins: c })),
      events: xs.slice(0, 12).map((s) => ({ at: iso(s.a.at), kind: s.f.kind, action_id: s.a.id, text: `${s.a.op} ${s.f.reason}` })),
    };
    out.push({ id: `inc_burst:${app}:${bucket / 1000}`, kind: 'BURST', action_id: null, work_id: null, status: 'OPEN', title: `${app} activity burst`, evidence, created_at: iso(bucket), updated_at: iso(Math.min(now, bucket + 10 * MIN)) });
  }
  return out;
}
