#!/usr/bin/env node
// A realistic slice of agent traffic against a real Lumni API, so every
// console screen has something true to show. Zero dependencies; Node 22.
//
//   node scripts/try-real.mjs                       # reads LUMNI_URL and LUMNI_API_KEY from env or ./.env
//   node scripts/try-real.mjs --rounds 5            # play it 5 times, a few seconds apart
//   node scripts/try-real.mjs --admin-key lumni_…   # also adds the "refunds over $500 need a person" rule
//
// Each round, four things happen that Lumni exists for:
//   1. A refund agent sends the same refund twice (a retry after a timeout).
//   2. Two deploy bots go for the same checkout site at once.
//   3. A price setter quotes from a price list that changed after it read it.
//   4. A refund agent tries a $900 refund, which a rule says needs a person.
// Plus some ordinary, clean actions, so the day isn't all trouble.
//
// The script prints what Lumni decided at each step. In watch-only mode
// (SHADOW, the default) decisions read WOULD_…: Lumni records what it would
// have done and lets everything through.

import { existsSync, readFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const ACTORS = [
  { id: 'refund-agent', display_name: 'Refund agent', runtime: 'python' },
  { id: 'price-setter', display_name: 'Price setter', runtime: 'node' },
  { id: 'deploy-bot', display_name: 'Deploy bot', runtime: 'go' },
  { id: 'release-bot', display_name: 'Release bot', runtime: 'go' },
  { id: 'support-agent', display_name: 'Support agent', runtime: 'python' },
];
const REFUND_RULE = {
  name: 'Refunds over $500 need a person', operation: 'stripe.refund', resource_type: '*',
  argument_pointer: '/amount', numeric_threshold: 500, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED',
};

let say = (s) => console.log(s);
const short = () => randomUUID().slice(0, 8);

/** Reads KEY=value lines; nothing fancier. */
export function readDotEnv(text) {
  const out = {};
  for (const line of text.split('\n')) {
    const m = /^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/.exec(line);
    if (m) out[m[1]] = m[2].replace(/^['"]|['"]$/g, '');
  }
  return out;
}

export function client(url, key, fetchFn = fetch) {
  return async (method, path, body) => {
    let res;
    try {
      res = await fetchFn(`${url}${path}`, {
        method,
        headers: { authorization: `Bearer ${key}`, accept: 'application/json', ...(body ? { 'content-type': 'application/json' } : {}) },
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch {
      throw new Error(`Lumni can’t be reached at ${url}. Is it running?`);
    }
    const text = await res.text();
    const data = text ? JSON.parse(text) : {};
    if (!res.ok) {
      const e = new Error(data?.error?.message ?? `${method} ${path} failed (${res.status})`);
      e.status = res.status;
      throw e;
    }
    return data;
  };
}

/** What a decision means, in plain words. */
const SAID = {
  ALLOW: 'went ahead',
  WARN: 'went ahead, with a warning',
  WAIT: 'waited its turn',
  REPLAN: 'was sent back to read the latest',
  DEDUPLICATE: 'was stopped: it was already done',
  REQUIRE_APPROVAL: 'is waiting for a person',
  REJECT: 'was refused',
};
const WOULD = {
  WAIT: 'would have waited its turn',
  REPLAN: 'would have been sent back to read the latest',
  DEDUPLICATE: 'would have been stopped as already done',
  REQUIRE_APPROVAL: 'would have waited for a person',
  REJECT: 'would have been refused',
};
export function words(d) {
  const raw = String(d.decision ?? '');
  const k = raw.replace(/^WOULD_/, '');
  if (raw.startsWith('WOULD_') && WOULD[k]) return `${WOULD[k]} (watch-only, so it went ahead)`;
  return SAID[k] ?? `got ${raw}`;
}

/** One action, end to end: check, then start and finish when there's a lease. */
async function act(call, input, { finish = true, status = 'SUCCEEDED', output = { ok: true } } = {}) {
  const d = await call('POST', '/v1/actions/check', input);
  if (finish && d.lease_token && /^(ALLOW|WARN)$/.test(d.decision)) {
    await call('POST', `/v1/actions/${encodeURIComponent(d.action_id)}/start`, { lease_token: d.lease_token });
    await call('POST', `/v1/actions/${encodeURIComponent(d.action_id)}/result`, { status, lease_token: d.lease_token, output });
  }
  return d;
}

const work = async (call, actor, goal) => {
  const id = `${actor}-${short()}`;
  await call('POST', '/v1/work', { id, actor_id: actor, goal, risk: 'MEDIUM' });
  return id;
};

export async function round(call, n) {
  const line = (who, what, d) => say(`  ${who.padEnd(14)} ${what}: ${words(d)}`);

  // 1. The same refund, twice: the agent retried after a timeout.
  const charge = `ch_${short()}`;
  const w1 = await work(call, 'refund-agent', `Refund order for ${charge}`);
  const refund = { work_id: w1, actor_id: 'refund-agent', operation: 'stripe.refund', arguments: { charge, amount: 60 }, idempotency_key: `refund-${charge}`, resources: [{ type: 'charge', external_id: charge, mode: 'WRITE' }], risk: 'MEDIUM' };
  line('Refund agent', `refund $60 on ${charge}`, await act(call, { ...refund, action_id: `${w1}-a` }));
  line('Refund agent', 'the same refund again', await act(call, { ...refund, action_id: `${w1}-b` }));

  // 2. Two deploy bots, one checkout site. The first holds it while it works.
  const site = { type: 'deployment', external_id: 'checkout-site', namespace: 'vercel', mode: 'WRITE' };
  const w2 = await work(call, 'deploy-bot', 'Ship the checkout fix');
  const first = await act(call, { action_id: `${w2}-a`, work_id: w2, actor_id: 'deploy-bot', operation: 'vercel.deploy', arguments: { project: 'checkout' }, resources: [site] }, { finish: false });
  if (first.lease_token) await call('POST', `/v1/actions/${encodeURIComponent(first.action_id)}/start`, { lease_token: first.lease_token });
  line('Deploy bot', 'deploy the checkout site', first);
  const w3 = await work(call, 'release-bot', 'Roll the weekly release');
  line('Release bot', 'deploy it at the same time', await act(call, { action_id: `${w3}-a`, work_id: w3, actor_id: 'release-bot', operation: 'vercel.deploy', arguments: { project: 'checkout' }, resources: [site] }, { finish: false }));
  if (first.lease_token) await call('POST', `/v1/actions/${encodeURIComponent(first.action_id)}/result`, { status: 'SUCCEEDED', lease_token: first.lease_token, output: { url: 'https://checkout.example.com' } });

  // 3. A stale read: the price list changes after the price setter read it.
  const list = { type: 'price_list', external_id: 'enterprise', namespace: 'salesforce' };
  const res = await call('POST', '/v1/resources/resolve', list);
  const before = `v${n * 2 + 1}`;
  const after = `v${n * 2 + 2}`;
  await call('POST', `/v1/resources/${encodeURIComponent(res.id)}/version`, { version: before, version_source: 'salesforce', summary: `Enterprise seat price $${40 + n}` });
  const w4 = await work(call, 'price-setter', 'Quote Hooli for 50 seats');
  await call('POST', `/v1/resources/${encodeURIComponent(res.id)}/version`, { version: after, version_source: 'salesforce', actor_id: 'support-agent', summary: `Enterprise seat price $${42 + n}` });
  line('Price setter', 'quote from the list it read earlier', await act(call, {
    action_id: `${w4}-a`, work_id: w4, actor_id: 'price-setter', operation: 'salesforce.update_price', arguments: { account: 'Hooli', change_pct: 4 },
    resources: [{ ...list, mode: 'WRITE', observed_version: before, version_source: 'salesforce' }],
  }));

  // 4. A big refund, which the rule says needs a person. It stays waiting in Needs you.
  const big = `ch_${short()}`;
  const w5 = await work(call, 'refund-agent', `Refund the annual plan on ${big}`);
  line('Refund agent', `refund $900 on ${big}`, await act(call, { action_id: `${w5}-a`, work_id: w5, actor_id: 'refund-agent', operation: 'stripe.refund', arguments: { charge: big, amount: 900 }, resources: [{ type: 'charge', external_id: big, mode: 'WRITE' }], risk: 'HIGH' }));

  // And ordinary work, which simply goes through.
  for (let i = 0; i < 3; i++) {
    const t = `${4300 + n * 3 + i}`;
    const w = await work(call, 'support-agent', `Answer ticket #${t}`);
    await act(call, { action_id: `${w}-a`, work_id: w, actor_id: 'support-agent', operation: 'zendesk.reply', arguments: { ticket: t }, resources: [{ type: 'ticket', external_id: t, namespace: 'zendesk', mode: 'WRITE' }] });
  }
  say('  Support agent  answered 3 tickets: went ahead');
}

function args(argv) {
  const o = {};
  for (let i = 0; i < argv.length; i++) {
    if (!argv[i].startsWith('--')) continue;
    const k = argv[i].slice(2);
    if (argv[i + 1] && !argv[i + 1].startsWith('--')) o[k] = argv[++i];
    else o[k] = true;
  }
  return o;
}

export async function main(argv, env, { fetchFn = fetch, log, cwd = process.cwd(), sleep = (ms) => new Promise((r) => setTimeout(r, ms)) } = {}) {
  if (log) say = log;
  const a = args(argv);
  const file = `${cwd}/.env`;
  const dot = existsSync(file) ? readDotEnv(readFileSync(file, 'utf8')) : {};
  const url = String(a.url ?? env.LUMNI_URL ?? dot.LUMNI_URL ?? 'http://localhost:8080').replace(/\/$/, '');
  const key = a.key ?? env.LUMNI_API_KEY ?? dot.LUMNI_API_KEY;
  if (!key) {
    say('No Lumni key. Run lumni init first (it writes LUMNI_API_KEY to .env), or pass --key.');
    return 1;
  }
  const call = client(url, key, fetchFn);
  const rounds = Math.max(1, Math.min(50, Number(a.rounds ?? 1) || 1));

  if (a['admin-key']) {
    const admin = client(url, a['admin-key'], fetchFn);
    await admin('POST', '/v1/config/policy-rules', REFUND_RULE);
    say(`Added the rule “${REFUND_RULE.name}”.`);
  }
  for (const x of ACTORS) await call('POST', '/v1/actors', { ...x, type: 'AGENT', metadata: { source: 'try-real' } });
  say(`Five agents are registered with Lumni at ${url}.`);

  for (let n = 0; n < rounds; n++) {
    say(`\nRound ${n + 1} of ${rounds}`);
    await round(call, n);
    if (n < rounds - 1) await sleep(Number(a.gap ?? 4000));
  }
  say('\nOpen the console: Today has the day, Needs you has the $900 refund, Tower has the checkout site, Context has the price list.');
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main(process.argv.slice(2), process.env).then((c) => process.exit(c), (e) => {
    console.error(e.message);
    process.exit(1);
  });
}
