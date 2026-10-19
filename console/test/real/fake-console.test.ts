// The fake Lumni API's Console API, called directly: every endpoint answers in
// the OpenAPI shape, works its numbers out from the world's actions, and
// remembers what writes changed.

import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { factOf, fakeFetch, type FakeWorld } from './fake-go';
import { richDay } from './rich';

// Late afternoon, so the morning burst is in the day.
const NOW = Date.UTC(2026, 9, 14, 16, 20);
const realNow = Date.now;
beforeEach(() => {
  Date.now = () => NOW;
});
afterEach(() => {
  Date.now = realNow;
});

type Any = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

function api(w: FakeWorld, auth = true) {
  const f = fakeFetch(w);
  return async (path: string, method = 'GET', body?: unknown, headers: Record<string, string> = {}) => {
    const res = await f(`http://go.test${path}`, {
      method, headers: { ...(auth ? { authorization: `Bearer ${w.key}` } : {}), 'content-type': 'application/json', host: 'go.test', ...headers },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    const data = text && (res.headers.get('content-type') ?? '').includes('json') ? JSON.parse(text) : text;
    return { status: res.status, data: data as Any, res };
  };
}

const day = () => richDay(900, {}, 'UTC', NOW);
const midnight = Date.UTC(2026, 9, 14);
const today = (w: FakeWorld) => w.actions.filter((a) => a.at >= midnight && a.at < NOW);

/** Every row of a paged list, following next_cursor. */
async function all(send: ReturnType<typeof api>, path: string, limit: number) {
  const out: Any[] = [];
  let cursor: string | null = null;
  let pages = 0;
  do {
    const sep = path.includes('?') ? '&' : '?';
    const r = await send(`${path}${sep}limit=${limit}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    expect(r.status).toBe(200);
    expect(r.data.items.length).toBeLessThanOrEqual(limit);
    out.push(...r.data.items);
    cursor = r.data.next_cursor;
    pages++;
  } while (cursor);
  return { items: out, pages };
}

describe('auth', () => {
  test('private routes need the key or a session; health and public proofs stay open', async () => {
    const w = day();
    expect((await api(w, false)('/v1/analytics/day')).status).toBe(401);
    expect((await api(w, false)('/v1/health/ready')).status).toBe(200);
    expect((await api(w, false)('/v1/public/proofs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')).status).toBe(404);
  });

  test('session writes need the CSRF token', async () => {
    const w = day();
    const f = fakeFetch(w);
    const s = await f('http://go.test/v1/auth/session', { method: 'POST', headers: { authorization: `Bearer ${w.key}` } });
    const cookie = s.headers.get('set-cookie')!.split(';')[0]!;
    const { csrf_token } = (await s.json()) as { csrf_token: string };
    const put = (h: Record<string, string>) => f('http://go.test/v1/config/pause', { method: 'POST', headers: { cookie, ...h }, body: JSON.stringify({ paused: true }) });
    expect((await put({})).status).toBe(403);
    expect((await put({ 'x-csrf-token': csrf_token })).status).toBe(200);
  });
});

describe('GET /v1/analytics/day', () => {
  test('counts the day from the actions', async () => {
    const w = day();
    const r = await api(w)('/v1/analytics/day?tz=UTC');
    expect(r.status).toBe(200);
    const d = r.data;
    const acts = today(w);
    expect(d.total).toBe(acts.length);
    expect(d.tz).toBe('UTC');
    expect(d.from).toBe(new Date(midnight).toISOString());
    expect(d.truncated).toBe(false);
    expect(d.kinds.stop).toBe(acts.filter((a) => ['DEDUPLICATE', 'REJECT'].includes(a.decision?.d ?? '')).length);
    expect(d.kinds.stop).toBeGreaterThan(40);
    expect(d.kinds.wait).toBe(acts.filter((a) => a.decision?.d === 'WAIT').length);
    expect(d.kinds.recheck).toBe(acts.filter((a) => a.decision?.d === 'REPLAN').length);
    expect(d.kinds.ask).toBe(acts.filter((a) => a.decision?.d === 'REQUIRE_APPROVAL').length);
    expect(d.kinds.bad).toBeGreaterThan(0);
    expect(d.kinds.blind).toBe(acts.filter((a) => (a.outcome?.status ?? 'UNKNOWN') === 'UNKNOWN').length);
    expect(d.would.ask).toBe(acts.filter((a) => a.decision?.d === 'WOULD_REQUIRE_APPROVAL').length);
    const kindSum = ['allow', 'stop', 'wait', 'recheck', 'ask', 'bad'].reduce((s, k) => s + d.kinds[k], 0);
    expect(kindSum).toBe(d.total);
    expect(d.cost_micros_usd).toBe(acts.reduce((s, a) => s + (a.cost ?? 0), 0));
    expect(d.kept_micros_usd).toBe(acts.reduce((s, a) => s + factOf(a).kept, 0));
    expect(d.kept_micros_usd).toBeGreaterThan(0);
    expect(d.hours).toHaveLength(24);
    expect(d.hours.reduce((s: number, h: Any) => s + h.total, 0)).toBe(d.total);
    expect(d.hours[11].kinds.stop).toBeGreaterThan(40);
    expect(d.actor_count).toBe(6);
    expect(d.app_count).toBe(5);
    expect(d.resource_count).toBeUndefined();
  });

  test('breaks the day down by AI, app and reason', async () => {
    const d = (await api(day())('/v1/analytics/day')).data;
    const refund = d.by_actor.find((a: Any) => a.actor_id === 'ai_refund');
    expect(refund.operation).toBe('stripe.refund');
    expect(refund.app).toBe('stripe');
    expect(refund.kinds.stop).toBeGreaterThan(40);
    expect(refund.verified).toBeGreaterThan(0);
    expect(refund.cost_micros_usd).toBeGreaterThan(0);
    expect(Date.parse(refund.last_at)).toBeLessThan(NOW);
    expect(typeof refund.streak).toBe('number');
    expect(refund.hours).toHaveLength(24);
    // The quote writer never steps in and is never checked, so its streak is all of it.
    const quote = d.by_actor.find((a: Any) => a.actor_id === 'ai_quote');
    expect(quote.streak).toBeGreaterThan(0);
    const apps = Object.fromEntries(d.by_app.map((a: Any) => [a.app, a]));
    expect(apps.gmail.checked).toBe(false);
    expect(apps.stripe.checked).toBe(true);
    const top = d.by_reason[0];
    expect(top).toMatchObject({ kind: 'stop', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED', operation: 'stripe.refund', actor_id: 'ai_refund', app: 'stripe' });
    expect(top.count).toBe(top.total);
    expect(top.kept_micros_usd).toBeGreaterThan(0);
    expect(top.hours[11]).toBeGreaterThan(40);
    expect(top.sample_action_id).toMatch(/^a_/);
    expect(top.top_resource).toMatch(/^stripe:charge:/);
    expect(d.by_reason.every((b: Any) => b.kind !== 'allow')).toBe(true);
  });

  test('groups by operation and resource on request, and folds the rest into other', async () => {
    const d = (await api(day())('/v1/analytics/day?group_by=operation,resource&top=3')).data;
    expect(d.hours).toBeUndefined();
    expect(d.by_operation).toHaveLength(4);
    expect(d.by_operation[3].operation).toBe('other');
    expect(d.resource_count).toBeGreaterThan(3);
    expect(d.by_resource.at(-1).resource_id).toBe('other');
    const site = (await api(day())('/v1/analytics/day?group_by=resource&top=50')).data.by_resource.find((r: Any) => r.resource_id === 'vercel:site:checkout');
    expect(site.name).toBe('Checkout site');
    expect(site.stepins.wait).toBeGreaterThan(0);
    expect(site.stepins.stop).toBe(9);
    expect(site.hours.wait).toHaveLength(24);
    expect(site.actors).toBe(1);
    expect(site.by_actor[0]).toEqual({ actor_id: 'ai_deploy', touches: site.touches });
  });

  test('buckets hours in the zone asked for', async () => {
    const d = (await api(day())(`/v1/analytics/day?tz=Asia/Kolkata&from=${new Date(midnight).toISOString()}&to=${new Date(NOW).toISOString()}`)).data;
    expect(d.tz).toBe('Asia/Kolkata');
    // 11:36 UTC is 17:06 in Kolkata.
    expect(d.hours[17].kinds.stop).toBeGreaterThan(40);
  });

  test('refuses bad windows and dimensions', async () => {
    const send = api(day());
    const at = (t: number) => new Date(t).toISOString();
    for (const q of [`from=${at(NOW)}&to=${at(NOW - 1000)}`, `from=${at(NOW - 8 * 86_400_000)}&to=${at(NOW)}`, 'tz=Mars/Base', 'from=yesterday']) {
      const r = await send(`/v1/analytics/day?${q}`);
      expect(r.status).toBe(400);
      expect(r.data.error.code).toBe('INVALID_TIME_RANGE');
    }
    expect((await send('/v1/analytics/day?group_by=kind,colour')).data.error.code).toBe('INVALID_GROUP_BY');
    expect((await send('/v1/analytics/day?top=0')).status).toBe(400);
  });
});

describe('GET /v1/analytics/daily', () => {
  test('seven finished days and today, comparable at the same hour', async () => {
    const w = day();
    const r = await api(w)('/v1/analytics/daily?days=8&at_hour=16');
    expect(r.status).toBe(200);
    const days = r.data.days as Any[];
    expect(days).toHaveLength(8);
    expect(days.at(-1)!.date).toBe('2026-10-14');
    expect(days[0]!.date).toBe('2026-10-07');
    expect(days.at(-1)!.total).toBe(today(w).length);
    for (const d of days.slice(0, 7)) {
      expect(d.total).toBeGreaterThan(300);
      expect(d.until_hour.total).toBeLessThan(d.total);
      expect(d.kinds.stop).toBeGreaterThan(0);
      expect(d.verified).toBeGreaterThan(0);
    }
    const morning = (await api(w)('/v1/analytics/daily?days=2&at_hour=9')).data.days;
    expect(morning[1].until_hour.total).toBe(today(w).filter((a) => new Date(a.at).getUTCHours() <= 9).length);
    // Older than the history: honest zeros.
    expect((await api(w)('/v1/analytics/daily?days=30')).data.days[0].total).toBe(0);
  });

  test('refuses bad parameters', async () => {
    const send = api(day());
    expect((await send('/v1/analytics/daily?days=0')).status).toBe(400);
    expect((await send('/v1/analytics/daily?at_hour=24')).status).toBe(400);
    expect((await send('/v1/analytics/daily?tz=Nowhere')).data.error.code).toBe('INVALID_TIME_RANGE');
  });
});

describe('GET /v1/analytics/freshness and anomalies', () => {
  test('read ages per AI, with stale reads counted', async () => {
    const r = await api(day())('/v1/analytics/freshness');
    const price = r.data.by_actor.find((a: Any) => a.actor_id === 'ai_price');
    expect(price.reads).toBeGreaterThan(20);
    expect(price.stale).toBeGreaterThan(0);
    expect(price.median_age_ms).toBeGreaterThan(1000);
    expect(price.p90_age_ms).toBeGreaterThanOrEqual(price.median_age_ms);
    expect(r.data.by_actor.find((a: Any) => a.actor_id === 'ai_quote')).toBeUndefined();
  });

  test('the live deploy loop, the quote writer running away, and price checks drifting', async () => {
    const r = await api(day())('/v1/analytics/anomalies');
    expect(r.status).toBe(200);
    const loop = r.data.items.find((a: Any) => a.kind === 'loop' && a.operation === 'vercel.restart');
    expect(loop).toMatchObject({ actor_id: 'ai_deploy', resource_id: 'vercel:site:checkout', count: 10, open: true });
    expect(loop.interval_ms).toBeGreaterThan(40_000);
    const runaway = r.data.items.find((a: Any) => a.kind === 'runaway' && a.actor_id === 'ai_quote');
    expect(runaway.open).toBe(true);
    expect(runaway.factor).toBeGreaterThan(4.5);
    expect(runaway.factor).toBeLessThan(8);
    expect(r.data.items.find((a: Any) => a.kind === 'drift' && a.actor_id === 'ai_price')).toBeDefined();
    const loopsOnly = await api(day())('/v1/analytics/anomalies?kinds=loop');
    expect(loopsOnly.data.items.every((a: Any) => a.kind === 'loop')).toBe(true);
    expect((await api(day())('/v1/analytics/anomalies?kinds=spiral')).status).toBe(400);
  });
});

describe('approvals', () => {
  test('pending ones with their action and rule, a page at a time', async () => {
    const send = api(day());
    const r = await send('/v1/approvals?status=PENDING&include=action,rule');
    expect(r.status).toBe(200);
    expect(r.data.items).toHaveLength(6);
    expect(r.data.next_cursor).toBeNull();
    const first = r.data.items[0];
    expect(first.action.operation).toBeDefined();
    expect(first.rule.id).toMatch(/^pr_/);
    expect(first.policy_id).toBeUndefined();
    const { items, pages } = await all(send, '/v1/approvals?status=PENDING', 4);
    expect(pages).toBe(2);
    expect(new Set(items.map((a) => a.id)).size).toBe(6);
    expect((await send('/v1/approvals?status=APPROVED')).data.items.map((a: Any) => [a.id, a.resolved_by])).toEqual([['ap_done', 'AK']]);
  });

  test('cursors and filters are checked', async () => {
    const send = api(day());
    const r = await send('/v1/approvals?status=PENDING&limit=2');
    expect((await send(`/v1/approvals?status=APPROVED&limit=2&cursor=${r.data.next_cursor}`)).status).toBe(400);
    expect((await send('/v1/approvals?cursor=garbage')).status).toBe(400);
    expect((await send('/v1/approvals?include=everything')).status).toBe(400);
    expect((await send('/v1/approvals?limit=1001')).status).toBe(400);
    expect((await send('/v1/approvals?action_id=a_ask_2')).data.items.map((a: Any) => a.id)).toEqual(['ap_2']);
  });

  test('deciding many at once, each on its own', async () => {
    const w = day();
    const send = api(w);
    const r = await send('/v1/approvals/decide', 'POST', { ids: ['ap_0', 'ap_1', 'ap_nope', 'ap_done'], decision: 'approve', note: 'checked the orders' });
    expect(r.status).toBe(200);
    expect(r.data.results).toEqual([
      { id: 'ap_0', status: 'APPROVED' }, { id: 'ap_1', status: 'APPROVED' }, { id: 'ap_nope', error: { code: 'NOT_FOUND' } }, { id: 'ap_done', error: { code: 'REQUEST_FAILED' } },
    ]);
    expect((await send('/v1/approvals?status=PENDING')).data.items).toHaveLength(4);
    expect(w.approvals.find((a) => a.id === 'ap_0')!.metadata).toEqual({ note: 'checked the orders' });
    const audit = (await send('/v1/audit?type=approval.decided')).data.items;
    expect(audit).toHaveLength(3);
    expect(audit[0].target).toMatchObject({ id: 'ap_1', action_id: 'a_ask_1' });
    // Go snapshots who asked and what for into the entry.
    for (const k of ['actor_id', 'operation', 'summary']) expect(audit[0].target[k]).toBeTruthy();
    expect(audit[0].after.status).toBe('APPROVED');
    expect((await send('/v1/approvals/decide', 'POST', { ids: [], decision: 'approve' })).status).toBe(400);
    expect((await send('/v1/approvals/decide', 'POST', { ids: ['ap_2'], decision: 'maybe' })).status).toBe(400);
  });

  test('a pending approval past its expiry reads as expired', async () => {
    const w = day();
    w.approvals[0]!.expires_at = new Date(NOW - 1000).toISOString();
    const send = api(w);
    expect((await send('/v1/approvals?status=EXPIRED')).data.items.map((a: Any) => a.id)).toEqual(['ap_0']);
    expect((await send('/v1/approvals?status=PENDING')).data.items).toHaveLength(5);
  });
});

describe('actions', () => {
  test('page through every action with filters', async () => {
    const w = day();
    const send = api(w);
    const { items, pages } = await all(send, '/v1/actions', 300);
    expect(items).toHaveLength(w.actions.length);
    expect(pages).toBe(Math.ceil(w.actions.length / 300));
    expect(new Set(items.map((a) => a.id)).size).toBe(w.actions.length);
    expect(Date.parse(items[0]!.requested_at)).toBeGreaterThanOrEqual(Date.parse(items.at(-1)!.requested_at));
    const refunds = await all(send, '/v1/actions?actor_id=ai_refund&operation=stripe.refund', 50);
    expect(refunds.items.length).toBe(w.actions.filter((a) => a.actor === 'ai_refund' && a.op === 'stripe.refund').length);
    expect((await send('/v1/actions?status=AWAITING_APPROVAL')).data.items).toHaveLength(6);
    const at = (t: number) => new Date(t).toISOString();
    const morning = await all(send, `/v1/actions?from=${at(midnight + 11 * 3_600_000)}&to=${at(midnight + 12 * 3_600_000)}`, 1000);
    expect(morning.items.length).toBe(w.actions.filter((a) => a.at >= midnight + 11 * 3_600_000 && a.at < midnight + 12 * 3_600_000).length);
    expect((await send('/v1/actions?limit=0')).status).toBe(400);
    expect((await send(`/v1/actions?from=${at(NOW)}&to=${at(NOW - 1)}`)).status).toBe(400);
  });

  test('a journey has the action, decision, approval, outcome and what a repeat repeated', async () => {
    const send = api(day());
    const loop = (await send('/v1/actions/a_loop_3/journey')).data;
    expect(loop.action.id).toBe('a_loop_3');
    expect(loop.action.resources).toEqual([{ action_id: 'a_loop_3', resource_id: 'vercel:site:checkout', mode: 'WRITE' }]);
    expect(loop.decision).toMatchObject({ decision: 'DEDUPLICATE', reason_code: 'EXACT_DUPLICATE_ACTIVE', receipt_id: 'rcpt_a_loop_3', enforced: true, existing_action_id: 'a_loop_hold' });
    expect(loop.duplicate_of).toBe('a_loop_hold');
    expect(loop.earlier).toEqual([]);
    expect(loop.approval).toBeNull();
    const ask = (await send('/v1/actions/a_ask_0/journey')).data;
    expect(ask.approval).toMatchObject({ id: 'ap_0', status: 'PENDING' });
    expect(ask.outcome.status).toBe('PENDING');
    expect(ask.attempts).toHaveLength(1);
    const missing = (await send('/v1/actions/a_missing_0/journey')).data;
    expect(missing.attempts).toHaveLength(3);
    expect((await send('/v1/actions/nope/journey')).status).toBe(404);
  });

  test('a stale read says how old it was and who changed it', async () => {
    const w = day();
    const stale = w.actions.find((a) => a.decision?.d === 'REPLAN' && a.actor === 'ai_price')!;
    const j = (await api(w)(`/v1/actions/${stale.id}/journey`)).data;
    const r = j.decision.evaluations[0].details.resources[0];
    expect(r.resource_id).toBe('salesforce:pricebook:enterprise');
    expect(Number(r.observed_version)).toBeLessThan(Number(r.current_version));
    expect(r.age_ms).toBeGreaterThan(60_000);
    expect(r.changed_by_actor_id).toBe('ai_price');
  });
});

describe('controls', () => {
  test('pausing and resuming an AI', async () => {
    const w = day();
    const send = api(w);
    const r = await send('/v1/actors/ai_deploy/pause', 'POST', { reason: 'loop' });
    expect(r.status).toBe(200);
    expect(r.data).toMatchObject({ actor_id: 'ai_deploy', paused: true, paused_by: w.principal, until: null });
    expect((await send('/v1/actors')).data.items.find((a: Any) => a.id === 'ai_deploy').paused).toBe(true);
    expect((await send('/v1/actors/ai_deploy')).data.paused).toBe(true);
    expect((await send('/v1/actors/ai_deploy/pause', 'POST', {})).status).toBe(409);
    expect((await send('/v1/actors/ai_deploy/resume', 'POST', {})).data.paused).toBe(false);
    expect((await send('/v1/actors/ai_deploy/resume', 'POST', {})).data.error.code).toBe('NOT_PAUSED');
    expect((await send('/v1/actors/ai_deploy/pause', 'POST', { until: new Date(NOW - 1).toISOString() })).status).toBe(400);
    expect((await send('/v1/actors/ghost/pause', 'POST', {})).status).toBe(404);
    expect((await send('/v1/audit?type=actor.paused,actor.resumed')).data.items.map((e: Any) => e.type)).toEqual(['actor.resumed', 'actor.paused']);
    // A pause that has run out reads as not paused.
    await send('/v1/actors/ai_quote/pause', 'POST', { until: new Date(NOW + 60_000).toISOString() });
    Date.now = () => NOW + 120_000;
    expect((await send('/v1/actors/ai_quote')).data.paused).toBe(false);
  });

  test('pausing the whole workspace', async () => {
    const w = day();
    const r = await api(w)('/v1/config/pause', 'POST', { paused: true });
    expect(r.data).toEqual({ paused: true });
    expect(w.paused).toBe(true);
    expect((await api(w)('/v1/config/pause', 'POST', {})).status).toBe(400);
  });

  test('limits are stored, counted and removable', async () => {
    const w = day();
    const send = api(w);
    const put = await send('/v1/config/limits/site_cap', 'PUT', { scope: { resource: 'vercel:site:*' }, max_in_flight: 1 });
    expect(put.status).toBe(200);
    expect(put.data).toMatchObject({ id: 'site_cap', mode: 'ENFORCE', max_in_flight: 1, per_minute: null, hold: false, in_flight_now: 1, waiting_now: 2 });
    await send('/v1/config/limits/refund_rate', 'PUT', { scope: { operation: 'stripe.refund' }, per_minute: 30, mode: 'SHADOW' });
    const list = await send('/v1/config/limits');
    expect(list.data.items.map((l: Any) => l.id).sort()).toEqual(['refund_rate', 'site_cap']);
    expect(list.data.next_cursor).toBeNull();
    // A limit with no resource in its scope covers every resource.
    expect((await send('/v1/config/limits?resource=vercel:site:checkout')).data.items.map((l: Any) => l.id).sort()).toEqual(['refund_rate', 'site_cap']);
    expect((await send('/v1/config/limits/site_only', 'PUT', { scope: { resource: 'stripe:charge:*' }, hold: true })).data.hold).toBe(true);
    expect((await send('/v1/config/limits?resource=vercel:site:checkout')).data.items.map((l: Any) => l.id)).not.toContain('site_only');
    expect((await send('/v1/config/limits/site_only', 'DELETE')).status).toBe(204);
    expect((await send('/v1/resources/vercel%3Asite%3Acheckout/lease')).data.limit).toEqual({ max_in_flight: 1, in_flight: 1 });
    expect((await send('/v1/config/limits/bad', 'PUT', { scope: {} })).status).toBe(400);
    expect((await send('/v1/config/limits/bad', 'PUT', { scope: { resource: 'vercel:*:checkout' }, hold: true })).status).toBe(400);
    expect((await send('/v1/config/limits/site_cap', 'DELETE')).status).toBe(204);
    expect((await send('/v1/config/limits/site_cap', 'DELETE')).status).toBe(404);
    expect((await send('/v1/audit?type=limit.set')).data.items).toHaveLength(5);
  });

  test('a spend limit paces against what was spent today', async () => {
    const w = day();
    const send = api(w);
    expect((await send('/v1/config/spend-limit')).data).toBeNull();
    const r = await send('/v1/config/spend-limit', 'PUT', { daily_micros_usd: 50_000_000, when_reached: 'SLOW' });
    expect(r.status).toBe(200);
    const spent = today(w).reduce((s, a) => s + (a.cost ?? 0), 0);
    expect(r.data.spent_today_micros_usd).toBe(spent);
    expect(r.data.pace_micros_usd).toBeGreaterThan(spent);
    // At this pace $50 runs out before midnight.
    expect(Date.parse(r.data.slow_at)).toBeLessThan(midnight + 86_400_000);
    expect((await send('/v1/config/spend-limit')).data.daily_micros_usd).toBe(50_000_000);
    const quote = await send('/v1/config/spend-limit', 'PUT', { daily_micros_usd: 900_000_000, when_reached: 'HOLD', scope: { actor_id: 'ai_quote' } });
    expect(quote.data.scope).toEqual({ actor_id: 'ai_quote' });
    expect(quote.data.slow_at).toBeNull();
    expect((await send('/v1/config/spend-limit?actor_id=ai_quote')).data.when_reached).toBe('HOLD');
    expect((await send('/v1/config/spend-limit', 'PUT', { daily_micros_usd: 0, when_reached: 'SLOW' })).status).toBe(400);
    expect((await send('/v1/config/spend-limit', 'PUT', { daily_micros_usd: 5, when_reached: 'SLOW', scope: { actor_id: 'ghost' } })).status).toBe(404);
    expect((await send('/v1/config/spend-limit', 'DELETE')).status).toBe(204);
    expect((await send('/v1/config/spend-limit')).data).toBeNull();
    expect((await send('/v1/audit?type=spend_limit.set')).data.items).toHaveLength(3);
  });

  test('autopilot grants and revokes', async () => {
    const w = day();
    const send = api(w);
    expect((await send('/v1/config/autopilot')).data).toEqual({ items: [], next_cursor: null });
    const grant = { operation: 'stripe.refund', skip_rules: ['pr_refunds'], below: { pointer: '/amount', value: 1000 }, revert_after: { slips: 2, per: 50 } };
    const r = await send('/v1/config/autopilot/ai_refund', 'PUT', grant);
    expect(r.status).toBe(200);
    expect(r.data).toMatchObject({ actor_id: 'ai_refund', active: true, slips: 0, reverted_at: null, skip_rules: ['pr_refunds'] });
    expect((await send('/v1/config/autopilot')).data.items).toHaveLength(1);
    expect((await send('/v1/config/autopilot/ai_refund', 'PUT', { ...grant, skip_rules: ['pr_prices'] })).data.error.code).toBe('INVALID_RULE');
    expect((await send('/v1/config/autopilot/ai_refund', 'PUT', { ...grant, revert_after: { slips: 5, per: 2 } })).status).toBe(400);
    // Two bad outcomes after the grant revoke it.
    for (const k of [0, 1]) w.actions.push({ id: `a_slip_${k}`, at: NOW + 1 + k, actor: 'ai_refund', work: 'w', op: 'stripe.refund', status: 'SUCCEEDED', decision: { d: 'ALLOW', r: 'NO_CONFLICT' }, outcome: { status: 'MISMATCH' } });
    const after = (await send('/v1/config/autopilot')).data.items[0];
    expect(after).toMatchObject({ active: false, slips: 2 });
    expect((await send('/v1/audit?type=autopilot.reverted')).data.items[0].by).toBe('system');
    expect((await send('/v1/config/autopilot/ai_refund', 'DELETE')).status).toBe(204);
    expect((await send('/v1/config/autopilot/ai_refund', 'DELETE')).status).toBe(404);
  });

  test('policy rules: patch with a version, delete softly', async () => {
    const w = day();
    const send = api(w);
    const r = await send('/v1/config/policy-rules/pr_prices', 'PATCH', { numeric_threshold: 15 }, { 'if-match': '"1"' });
    expect(r.status).toBe(200);
    expect(r.data).toMatchObject({ id: 'pr_prices', numeric_threshold: 15, version: 2 });
    expect((await send('/v1/config/policy-rules/pr_prices', 'PATCH', { enabled: false }, { 'if-match': '1' })).status).toBe(409);
    expect((await send('/v1/config/policy-rules/pr_prices', 'PATCH', { enabled: false, version: 2 })).data.enabled).toBe(false);
    expect((await send('/v1/config/policy-rules/pr_prices', 'PATCH', {})).status).toBe(400);
    expect((await send('/v1/config/policy-rules/pr_prices', 'PATCH', { name: ' ' })).status).toBe(400);
    expect((await send('/v1/config/policy-rules/pr_refunds', 'DELETE')).status).toBe(204);
    expect((await send('/v1/config/policy-rules')).data.items.map((x: Any) => x.id)).toEqual(['pr_prices']);
    expect((await send('/v1/config/policy-rules/pr_refunds', 'PATCH', { enabled: true })).status).toBe(404);
    expect((await send('/v1/config/policy-rules/pr_refunds', 'DELETE')).status).toBe(404);
    const types = (await send('/v1/audit?type=policy_rule.updated,policy_rule.deleted')).data.items.map((e: Any) => e.type);
    expect(types).toEqual(['policy_rule.deleted', 'policy_rule.updated', 'policy_rule.updated', 'policy_rule.updated']);
  });

  test('previewing a rule against the day', async () => {
    const w = day();
    const at = (t: number) => new Date(t).toISOString();
    const rule = { operation: 'stripe.refund', argument_pointer: '/amount', numeric_threshold: 100, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' };
    const r = await api(w)('/v1/config/policy-rules/preview', 'POST', { rule, from: at(midnight), to: at(NOW), examples: 5 });
    expect(r.status).toBe(200);
    const expected = w.actions.filter((a) => a.op === 'stripe.refund' && a.at >= midnight && a.at < NOW && typeof a.meta?.amount === 'number' && (a.meta.amount as number) > 100);
    expect(r.data.matched).toBe(expected.length);
    // The six refunds that asked already ask under this rule.
    expect(r.data.already_handled).toBe(5);
    expect(r.data.would_change).toBe(r.data.matched - 5);
    expect(r.data.false_positive_estimate).toBe(1);
    expect(r.data.examples).toHaveLength(5);
    expect(r.data.examples[0].decision_with_rule).toBe('REQUIRE_APPROVAL');
    expect(r.data.by_hour).toHaveLength(24);
    expect(r.data.by_day).toEqual([{ date: '2026-10-14', matched: expected.length }]);
    expect(r.data.truncated).toBe(false);
    expect((await api(w)('/v1/config/policy-rules/preview', 'POST', { rule: { ...rule, outcome: 'ALLOW' }, from: at(midnight), to: at(NOW) })).status).toBe(400);
    expect((await api(w)('/v1/config/policy-rules/preview', 'POST', { rule: { ...rule, numeric_threshold: undefined }, from: at(midnight), to: at(NOW) })).status).toBe(400);
    expect((await api(w)('/v1/config/policy-rules/preview', 'POST', { rule, from: at(NOW - 31 * 86_400_000), to: at(NOW) })).data.error.code).toBe('INVALID_TIME_RANGE');
  });

  test('enforcement overrides set and clear', async () => {
    const w = day();
    const send = api(w);
    const r = await send('/v1/config/enforcement', 'PUT', { operation: 'gmail.send', rule: '*', mode: 'WARN' });
    expect(r.data).toEqual({ operation: 'gmail.send', rule: '*', mode: 'WARN', effective_mode: 'WARN' });
    expect((await send('/v1/config/enforcement', 'DELETE', { operation: 'gmail.send' })).status).toBe(204);
    expect(w.enforcement.find((e) => e.operation === 'gmail.send')).toBeUndefined();
    expect((await send('/v1/config/enforcement', 'DELETE', { operation: 'gmail.send' })).status).toBe(404);
    expect((await send('/v1/config/enforcement', 'DELETE', {})).status).toBe(400);
    const types = (await send('/v1/audit?type=enforcement.set,enforcement.cleared')).data.items.map((e: Any) => [e.type, e.by]);
    expect(types).toEqual([['enforcement.cleared', w.principal], ['enforcement.set', w.principal], ['enforcement.set', 'Jess Park']]);
  });
});

describe('proofs', () => {
  test('a public link shows only what was chosen, and stops when revoked', async () => {
    const w = day();
    const send = api(w);
    const verified = w.actions.find((a) => a.outcome?.status === 'VERIFIED' && a.actor === 'ai_refund')!;
    const r = await send('/v1/proofs', 'POST', { action_id: verified.id, show: { what: true, checks: true, actor: true } });
    expect(r.status).toBe(201);
    expect(r.data.url).toBe(`http://go.test/p/${r.data.token}`);
    expect(r.data.token.length).toBeGreaterThanOrEqual(43);
    expect(r.data.sent_to).toEqual([]);
    const open = api(w, false);
    const pub = await open(`/v1/public/proofs/${r.data.token}`);
    expect(pub.status).toBe(200);
    expect(pub.res.headers.get('cache-control')).toBe('no-store');
    expect(pub.data).toEqual({
      workspace: 'Acme', status: 'proven', receipt: `rcpt_${verified.id}`, what: { summary: 'stripe refund', at: new Date(verified.at).toISOString(), app: 'stripe' },
      checks: [{ t: 'App outcome verified', ok: true }], actor: 'Refund agent',
    });
    expect(JSON.stringify(pub.data)).not.toContain(String(verified.meta!.customer));
    const page = await open(`/p/${r.data.token}`);
    expect(page.res.headers.get('content-type')).toContain('text/html');
    expect(page.data).toContain('Proof status: proven');
    expect((await send(`/v1/proofs/${r.data.id}`, 'DELETE')).status).toBe(204);
    expect((await open(`/v1/public/proofs/${r.data.token}`)).status).toBe(410);
    expect((await open('/v1/public/proofs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')).status).toBe(404);
    expect((await send('/v1/proofs/prf_nope', 'DELETE')).status).toBe(404);
    expect((await send('/v1/proofs', 'POST', { action_id: 'nope', show: {} })).status).toBe(404);
    expect((await send('/v1/proofs', 'POST', { action_id: verified.id, show: {}, expires_in_hours: 0 })).status).toBe(400);
    const audit = (await send('/v1/audit?type=proof.created')).data.items;
    expect(audit).toHaveLength(2);
    expect(JSON.stringify(audit)).not.toContain(r.data.token);
  });

  test('a proof can show the repeat it stopped, and expires', async () => {
    const w = day();
    const send = api(w);
    const r = await send('/v1/proofs', 'POST', { action_id: 'a_loop_hold', show: { stopped_repeat: true }, expires_in_hours: 1 });
    const pub = (await api(w, false)(`/v1/public/proofs/${r.data.token}`)).data;
    expect(pub.status).toBe('pending');
    expect(pub.stopped_repeat.at).toBe(new Date(w.actions.find((a) => a.id === 'a_loop_0')!.at).toISOString());
    expect(pub.what).toBeUndefined();
    expect(pub.actor).toBeUndefined();
    Date.now = () => NOW + 2 * 3_600_000;
    expect((await api(w, false)(`/v1/public/proofs/${r.data.token}`)).status).toBe(404);
  });
});

describe('notifications', () => {
  test('channels and routes are stored, checked, and sends are queued', async () => {
    const w = day();
    const send = api(w);
    expect((await send('/v1/config/notifications')).data).toEqual({ channels: [], routes: [] });
    const config = {
      channels: [{ id: 'ops', kind: 'slack', target: '#ops', connection_id: 'ca_slack_1' }, { id: 'jess', kind: 'email', target: 'jess@acme.test' }],
      routes: [{ event: 'approval.requested', channels: ['ops'] }, { event: 'digest.weekly', channels: ['jess'], schedule: 'MON 09:00 Asia/Kolkata' }],
    };
    const r = await send('/v1/config/notifications', 'PUT', config);
    expect(r.status).toBe(200);
    expect((await send('/v1/config/notifications')).data).toEqual(config);
    for (const bad of [
      { ...config, routes: [{ event: 'digest.weekly', channels: ['jess'], schedule: 'someday' }] },
      { ...config, channels: [{ id: 'x', kind: 'slack', target: '#x', connection_id: 'not_ours' }], routes: [] },
      { ...config, routes: [{ event: 'approval.requested', channels: ['nobody'] }] },
      { channels: [{ id: 'm', kind: 'email', target: 'not an email' }], routes: [] },
    ]) {
      const b = await send('/v1/config/notifications', 'PUT', bad);
      expect(b.status).toBe(400);
      expect(b.data.error.code).toBe('INVALID_NOTIFICATIONS');
    }
    const burst = w.incidents.find((i) => i.kind === 'BURST')!;
    const sent = await send('/v1/notifications/send', 'POST', { channel_id: 'ops', kind: 'incident', ref_id: burst.id });
    expect(sent.status).toBe(202);
    expect(sent.data).toEqual({ queued: true, id: 'ntf_1' });
    expect(w.notificationJobs).toHaveLength(1);
    expect((await send('/v1/notifications/send', 'POST', { channel_id: 'nope', kind: 'incident', ref_id: burst.id })).status).toBe(404);
    expect((await send('/v1/notifications/send', 'POST', { channel_id: 'ops', kind: 'proof', ref_id: 'prf_nope' })).status).toBe(404);
    expect((await send('/v1/notifications/send', 'POST', { channel_id: 'ops', kind: 'fax', ref_id: 'x' })).status).toBe(400);
    // With an email channel, proof recipients are queued.
    const proof = await send('/v1/proofs', 'POST', { action_id: 'a_0', show: { what: true }, recipients: ['dana@example.com'] });
    expect(proof.data.sent_to).toEqual(['dana@example.com']);
    expect((await send('/v1/audit?type=notifications.updated')).data.items).toHaveLength(1);
  });
});

describe('resources', () => {
  test('the checkout site: who holds it, who waits, and the hand-offs', async () => {
    const send = api(day());
    const r = await send('/v1/resources/vercel%3Asite%3Acheckout/lease');
    expect(r.status).toBe(200);
    expect(r.data.holder).toMatchObject({ action_id: 'a_loop_hold', actor_id: 'ai_deploy', fencing_token: 4127 });
    expect(r.data.queue.map((x: Any) => [x.action_id, x.position])).toEqual([['a_wait_site_0', 1], ['a_wait_site_1', 2]]);
    expect(r.data.limit).toEqual({ max_in_flight: null, in_flight: 1 });
    expect(r.data.handoffs_last_hour).toBe(7);
    expect(r.data.refused_late_writes_today).toBe(2);
    const prices = (await send('/v1/resources/salesforce%3Apricebook%3Aenterprise/lease')).data;
    expect(prices.holder.action_id).toBe('a_price_live');
    expect(prices.queue).toHaveLength(1);
    expect((await send('/v1/resources/nope/lease')).status).toBe(404);
  });

  test('the most contended things, with their queues', async () => {
    const r = await api(day())('/v1/resources/contended?top=5');
    expect(r.status).toBe(200);
    expect(r.data.items.length).toBeLessThanOrEqual(5);
    const site = r.data.items.find((x: Any) => x.resource_id === 'vercel:site:checkout');
    expect(site.holder.fencing_token).toBe(4127);
    expect(site.queue.map((x: Any) => x.position)).toEqual(site.queue.map((_: Any, i: number) => i + 1));
    expect(r.data.items.some((x: Any) => x.resource_id === 'salesforce:pricebook:enterprise')).toBe(true);
  });

  test('version history, newest first, a page at a time', async () => {
    const w = day();
    const send = api(w);
    const first = await send('/v1/resources/salesforce%3Apricebook%3Aenterprise/versions');
    expect(first.data.items).toHaveLength(20);
    expect(first.data.next_cursor).not.toBeNull();
    const v = first.data.items[0];
    expect(v.summary).toMatch(/seat price \$\d+$/);
    expect(v.actor_id).toBe('ai_price');
    expect(v.value_hash).toMatch(/^[0-9a-f]{8}$/);
    const { items } = await all(send, '/v1/resources/salesforce%3Apricebook%3Aenterprise/versions', 25);
    expect(items).toHaveLength(w.versions['salesforce:pricebook:enterprise']!.length);
    const numbers = items.map((x) => Number(x.version));
    expect(numbers).toEqual([...numbers].sort((a, b) => b - a));
    expect((await send('/v1/resources/nope/versions')).status).toBe(404);
  });
});

describe('audit', () => {
  test("this morning's changes are there, filtered by type and person", async () => {
    const send = api(day());
    const r = await send('/v1/audit');
    expect(r.data.items.map((e: Any) => e.type)).toEqual(['approval.decided', 'enforcement.set', 'policy_rule.updated']);
    expect(r.data.items[1]).toMatchObject({ by: 'Jess Park', target: { operation: 'salesforce.update_price', rule: '*' }, before: { mode: 'SHADOW' }, after: { mode: 'WARN' } });
    expect((await send('/v1/audit?actor=AK')).data.items.map((e: Any) => e.type)).toEqual(['approval.decided']);
    const paged = await all(send, '/v1/audit', 1);
    expect(paged.pages).toBe(3);
  });
});

describe('telemetry, bursts, billing and money', () => {
  test('Stripe slowed down around the burst', async () => {
    const send = api(day());
    const at = (t: number) => new Date(t).toISOString();
    const burst = await send(`/v1/apps/stripe/health?from=${at(midnight + 11 * 3_600_000 + 30 * 60_000)}&to=${at(midnight + 11 * 3_600_000 + 50 * 60_000)}`);
    expect(burst.status).toBe(200);
    expect(burst.data.app).toBe('stripe');
    expect(burst.data.series).toHaveLength(20);
    expect(Math.max(...burst.data.series.map((x: Any) => x.p95_ms))).toBeGreaterThan(8500);
    expect(burst.data.series.reduce((s: number, x: Any) => s + x.timeouts, 0)).toBeGreaterThan(10);
    const calm = await send(`/v1/apps/stripe/health?from=${at(midnight + 14 * 3_600_000)}&to=${at(midnight + 15 * 3_600_000)}&step=5m`);
    expect(calm.data.series).toHaveLength(12);
    expect(Math.max(...calm.data.series.map((x: Any) => x.p95_ms))).toBeLessThan(1000);
    expect((await send('/v1/apps/stripe/health?step=1h')).status).toBe(400);
    expect((await send('/v1/apps/nothing/health')).data.series).toEqual([]);
  });

  test('the morning burst is an incident with the evidence shape', async () => {
    const r = await api(day())('/v1/incidents?kind=BURST');
    expect(r.status).toBe(200);
    const items = r.data.items as Any[];
    expect(items.length).toBeGreaterThan(0);
    expect(items.every((i) => i.kind === 'BURST')).toBe(true);
    const stripe = items.filter((i) => i.app === 'stripe');
    // Ten-minute windows: the burst from 11:36 spans two.
    expect(stripe.map((i) => i.started_at.slice(11, 16)).sort()[0]).toMatch(/^11:3\d$/);
    const total = stripe.reduce((s, i) => s + i.totals.stepins, 0);
    expect(total).toBeGreaterThanOrEqual(42);
    const e = stripe[0]!;
    expect(e.evidence.totals).toEqual(e.totals);
    expect(Object.keys(e.totals).sort()).toEqual(['actors', 'ask', 'customers', 'kept_micros_usd', 'recheck', 'stepins', 'stop', 'wait']);
    expect(e.totals.kept_micros_usd).toBeGreaterThan(0);
    expect(e.per_minute.reduce((s: number, m: Any) => s + m.stepins, 0)).toBe(e.totals.stepins);
    expect(e.events.length).toBeLessThanOrEqual(12);
    expect(e.events[0]).toMatchObject({ kind: 'stop', text: 'stripe.refund EXACT_EFFECT_ALREADY_COMMITTED' });
    expect(e.pages).toBe(0);
    const w = day();
    const first = (await api(w)('/v1/incidents?limit=50')).data;
    expect(first.items).toHaveLength(50);
    expect(first.next_cursor).not.toBeNull();
    expect((await all(api(w), '/v1/incidents', 50)).items).toHaveLength(w.incidents.length);
  });

  test('billing counts checked actions at the plan price', async () => {
    const w = day();
    const r = await api(w)('/v1/billing/usage');
    expect(r.data).toEqual({ checked_actions: today(w).length, price_per_action_micros_usd: 20_000, cost_micros_usd: today(w).length * 20_000, plan: 'team' });
  });

  test('finops: stable waste keys, what was avoided, and cost by AI', async () => {
    const w = day();
    const r = await api(w)(`/v1/finops/graph?from=${new Date(midnight).toISOString()}&to=${new Date(NOW).toISOString()}`);
    const keys = r.data.by_waste.map((x: Any) => x.key);
    for (const k of keys) expect(['repeat_stopped', 'stale_redo', 'retry_loop', 'superseded', 'never_happened', 'other']).toContain(k);
    const byKey = Object.fromEntries(r.data.by_waste.map((x: Any) => [x.key, x]));
    expect(byKey.repeat_stopped.avoided).toBe(true);
    expect(byKey.retry_loop).toMatchObject({ avoided: true, count: 9 });
    expect(byKey.stale_redo.avoided).toBe(false);
    expect(byKey.never_happened.count).toBeGreaterThan(0);
    expect(r.data.waste_micros_usd).toBe(r.data.by_waste.reduce((s: number, x: Any) => s + x.waste_micros_usd, 0));
    const quote = r.data.by_actor.find((a: Any) => a.actor_id === 'ai_quote');
    expect(quote.actions).toBe(w.actions.filter((a) => a.actor === 'ai_quote' && a.at >= midnight && a.at <= NOW).length);
    expect(r.data.by_actor.reduce((s: number, a: Any) => s + a.cost_micros_usd, 0)).toBe(r.data.total_cost_micros_usd);
    expect(r.data.analytics.source).toBe('postgres');
  });
});

describe('richDay', () => {
  test('is the same day twice for the same now', () => {
    const a = richDay(900, {}, 'UTC', NOW);
    const b = richDay(900, {}, 'UTC', NOW);
    expect(JSON.stringify({ ...a, sessions: 0, csrf: 0 })).toBe(JSON.stringify({ ...b, sessions: 0, csrf: 0 }));
    expect(a.history).toHaveLength(7);
    expect(a.autopilot).toEqual([]);
  });
});
