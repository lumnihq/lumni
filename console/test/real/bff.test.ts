// The console’s own server against a fake Go API: signing in, passing calls
// through, and building every view from real-shaped records.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../../server/env';
import { createBff } from '../../server/index';
import type {
  AppsView, FrameView, LiveView, MapView, NeedsView, ReceiptDetail, ReceiptsView, RulesView, Session, SpendView, StoryView, TodayView, WatchView,
} from '../../src/domain/types';
import { busyDay, ZONE } from './day';
import { fakeFetch, type FakeWorld } from './fake-go';

function setUp(w: FakeWorld, vars: Record<string, string> = {}) {
  const env = readEnv({ LUMNI_FRONTFACE_DATA_MODE: 'real', LUMNI_API_URL: 'http://go.test', LUMNI_FRONTFACE_TEST_ADMIN_KEY: w.key, PORT: '5200', ...vars });
  const bff = createBff(env, fakeFetch(w));
  const jar = new Map<string, string>();
  let csrf = '';
  const send = async (path: string, init: { method?: string; body?: unknown; headers?: Record<string, string> } = {}) => {
    const headers: Record<string, string> = { host: 'localhost:5200', 'x-lumni-tz': ZONE, ...init.headers };
    if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
    if (init.method && init.method !== 'GET') {
      headers.origin = headers.origin ?? 'http://localhost:5200';
      if (csrf) headers['x-csrf-token'] = csrf;
    }
    const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { method: init.method ?? 'GET', headers, body: init.body === undefined ? undefined : JSON.stringify(init.body) })))!;
    for (const line of res.headers.getSetCookie()) {
      const [pair] = line.split(';');
      const [k, v] = pair!.split('=');
      if (/Max-Age=0/.test(line)) jar.delete(k!);
      else jar.set(k!, v!);
    }
    const text = await res.text();
    const data = text && (res.headers.get('content-type') ?? '').includes('json') ? JSON.parse(text) : undefined;
    if (path === '/v1/auth/session' && data?.csrf_token) csrf = data.csrf_token;
    return { status: res.status, data, text, res };
  };
  return { bff, send, jar, w };
}

describe('signing in with the admin key', () => {
  test('the first look signs in on its own, and the key never leaves the server', async () => {
    const { send, jar, w } = setUp(busyDay(10));
    const r = await send('/v1/auth/session');
    expect(r.status).toBe(200);
    const s = r.data as Session;
    expect(s.signed_in).toBe(true);
    expect(s.csrf_token).toBe('csrf_1');
    expect(s.tenant?.id).toBe(w.tenant);
    expect(jar.get('lumni_session')).toBe('sess_1');
    expect(r.text).not.toContain(w.key);
    const cookies = r.res.headers.getSetCookie().join('\n');
    expect(cookies).toContain('HttpOnly');
    expect(cookies).toContain('SameSite=Strict');
  });

  test('a reload keeps the session and still hands back its CSRF token', async () => {
    const { send, w } = setUp(busyDay(10));
    await send('/v1/auth/session');
    const r = await send('/v1/auth/session');
    expect((r.data as Session).csrf_token).toBe('csrf_1');
    expect(w.sessions.size).toBe(1);
  });

  test('an expired session signs in again instead of answering 401', async () => {
    const { send, w } = setUp(busyDay(10));
    await send('/v1/auth/session');
    w.sessions.clear();
    const r = await send('/v1/auth/session');
    expect(r.status).toBe(200);
    expect((r.data as Session).signed_in).toBe(true);
    expect(w.sessions.size).toBe(1);
  });

  test('signing out stays signed out until you sign in again', async () => {
    const { send, jar } = setUp(busyDay(10));
    await send('/v1/auth/session');
    const out = await send('/v1/auth/session', { method: 'DELETE' });
    expect(out.status).toBe(200);
    expect(jar.has('lumni_session')).toBe(false);
    expect(jar.get('lumni_signed_out')).toBe('1');
    const look = await send('/v1/auth/session');
    expect(look.status).toBe(200);
    expect((look.data as Session).signed_in).toBe(false);
    const back = await send('/v1/auth/session', { method: 'POST', body: {} });
    expect((back.data as Session).signed_in).toBe(true);
    expect(jar.has('lumni_signed_out')).toBe(false);
  });

  test('a wrong key is a plain error, never a 401 that would sign the page out', async () => {
    const { send } = setUp(busyDay(10), { LUMNI_FRONTFACE_TEST_ADMIN_KEY: 'not_the_key' });
    const r = await send('/v1/auth/session');
    expect(r.status).not.toBe(401);
    const pasted = await send('/v1/auth/session', { method: 'POST', body: { key: 'also_wrong' } });
    expect(pasted.status).toBe(400);
  });

  test('with auto sign-in off, the page asks', async () => {
    const { send } = setUp(busyDay(10), { LUMNI_FRONTFACE_AUTO_SIGN_IN: 'off' });
    const r = await send('/v1/auth/session');
    expect((r.data as Session).signed_in).toBe(false);
  });

  test('config says what the browser may know, and nothing secret', async () => {
    const { send, w } = setUp(busyDay(10));
    const r = await send('/console/config.json');
    expect(r.data).toEqual({ mode: 'real', realAvailable: true, workspace: null, autoSignIn: true, adminKey: true, auth: 'key', billing: false, apiUrl: 'http://go.test' });
    expect(r.text).not.toContain(w.key);
  });
});

describe('guarding the console', () => {
  test('another host name pointed here gets nothing', async () => {
    const { send } = setUp(busyDay(10));
    const r = await send('/v1/auth/session', { headers: { host: 'evil.test' } });
    expect(r.status).toBe(421);
  });

  test('a write from another site is refused', async () => {
    const { send } = setUp(busyDay(10));
    await send('/v1/auth/session');
    const r = await send('/v1/approvals/ap_1/approve', { method: 'POST', body: {}, headers: { origin: 'http://evil.test' } });
    expect(r.status).toBe(403);
  });

  test('console views need a session', async () => {
    const { send } = setUp(busyDay(10));
    const r = await send('/v1/console/frame');
    expect(r.status).toBe(401);
  });
});

describe('passing calls through', () => {
  test('approving sends the session and CSRF token, never an Authorization header', async () => {
    const { send, w } = setUp(busyDay(10));
    await send('/v1/auth/session');
    const r = await send('/v1/approvals/ap_1/approve', { method: 'POST', body: {}, headers: { authorization: 'Bearer stolen' } });
    expect(r.status).toBe(200);
    const sent = w.seen.find((x) => x.path === '/v1/approvals/ap_1/approve')!;
    expect(sent.headers.authorization).toBeUndefined();
    expect(sent.headers.cookie).toContain('lumni_session=sess_1');
    expect(sent.headers['x-csrf-token']).toBe('csrf_1');
    expect(w.approvals[0]!.status).toBe('APPROVED');
  });

  test('the change stream comes through as a stream', async () => {
    const { send } = setUp(busyDay(10));
    await send('/v1/auth/session');
    const r = await send('/v1/graph/stream');
    expect(r.res.headers.get('content-type')).toContain('text/event-stream');
    expect(r.text).toContain('event: ready');
  });
});

async function signedIn(w: FakeWorld) {
  const s = setUp(w);
  await s.send('/v1/auth/session');
  return s;
}

describe('views from real records', () => {
  test('the frame counts what needs you and marks what isn’t available', async () => {
    const { send } = await signedIn(busyDay(60));
    const f = (await send('/v1/console/frame')).data as FrameView;
    expect(f.demo).toBe(false);
    expect(f.caps.pause).toBe(false);
    expect(f.caps.playground).toBe(false);
    // One ask and three actions that didn’t happen.
    expect(f.needs).toBe(1 + 3);
    expect(f.protecting).toBe(1);
    expect(f.watching).toBe(1);
    expect(f.tabs.today.stepins).toBeGreaterThan(0);
  });

  test('today counts every action even when snapshots cut off', async () => {
    // Thirteen actions are still open, and every snapshot carries those too.
    const w = busyDay(240, { perSnapshot: 30 });
    const { send } = await signedIn(w);
    const t = (await send('/v1/console/today')).data as TodayView;
    expect(t.done).toBe(241);
    // Repeats, the waiting one, and the ask; the watched ones only would have.
    expect(t.tallies.stop).toBe(12);
    expect(t.tallies.wait).toBe(12);
    expect(t.tallies.ask).toBe(1);
    expect(t.wouldHave).toBe(12);
    expect(t.bad).toBe(12);
    expect(t.needs?.kind).toBe('approval');
    expect(t.needs?.title).toContain('Support AI wants to refund $250 to Dana Lee');
    expect(t.ribbon.quarters.length).toBeGreaterThan(0);
    expect(Math.max(...t.ribbon.quarters)).toBeLessThanOrEqual(1);
    expect(t.steps[0]!.at).toBeGreaterThanOrEqual(t.steps.at(-1)!.at);
  });

  test('an empty workspace still draws its day', async () => {
    const { send } = await signedIn(busyDay(0));
    const t = (await send('/v1/console/today')).data as TodayView;
    expect(t.done).toBe(1);
    const r = (await send('/v1/console/receipts')).data as ReceiptsView;
    expect(r.items.length).toBe(1);
    const sp = (await send('/v1/console/spend')).data as SpendView;
    expect(Number.isFinite(sp.forecast)).toBe(true);
  });

  test('receipts filter, search and page', async () => {
    const { send } = await signedIn(busyDay(150));
    const all = (await send('/v1/console/receipts')).data as ReceiptsView;
    expect(all.total).toBe(151);
    expect(all.items.length).toBe(60);
    expect(all.matched).toBe(151);
    expect(all.ais.map((a) => a.name)).toEqual(['Billing AI', 'Sales AI', 'Support AI']);
    const bad = (await send('/v1/console/receipts?filter=bad')).data as ReceiptsView;
    expect(bad.matched).toBe(all.tallies.bad);
    expect(bad.items.every((r) => r.proof.kind === 'bad')).toBe(true);
    const one = (await send('/v1/console/receipts?ai=ai_sales')).data as ReceiptsView;
    expect(one.items.every((r) => r.who.id === 'ai_sales')).toBe(true);
    const found = (await send('/v1/console/receipts?q=dana')).data as ReceiptsView;
    expect(found.items.map((r) => r.id)).toEqual(['a_ask']);
    const more = (await send('/v1/console/receipts?limit=120')).data as ReceiptsView;
    expect(more.items.length).toBe(120);
  });

  test('a receipt that didn’t happen offers a real retry, and marks what isn’t available', async () => {
    const { send } = await signedIn(busyDay(30));
    const d = (await send('/v1/console/receipts/a_11')).data as ReceiptDetail;
    expect(d.shows.tone).toBe('off');
    const retry = d.actions.find((a) => a.kind === 'retry')!;
    expect(retry.post).toBe('/v1/outcomes/out_a_11/retry');
    expect(d.actions.find((a) => a.kind === 'handled')?.unavailable).toBeDefined();
  });

  test('a story tells the checks, whatever stages the kernel ran', async () => {
    const { send } = await signedIn(busyDay(30));
    const s = (await send('/v1/console/story/a_3')).data as StoryView;
    expect(s.step).toBe('stop');
    const step = s.events.find((e) => e.id === 'step')!;
    expect(step.checks?.map((c) => c.stage)).toEqual(['FRESHNESS', 'IDEMPOTENCY', 'LEASE', 'POLICY']);
    expect(step.checks?.find((c) => c.hit)?.stage).toBe('IDEMPOTENCY');
    expect(s.head.after).toContain('Lumni stopped a repeat of');
    const missing = await send('/v1/console/story/a_0');
    expect(missing.status).toBe(404);
  });

  test('needs you lists the ask with what the AI wants to do, and the mismatches with a retry', async () => {
    const { send } = await signedIn(busyDay(30));
    const n = (await send('/v1/console/needs-you')).data as NeedsView;
    const ask = n.items.find((i) => i.kind === 'approval')!;
    expect(ask.id).toBe('ap_1');
    expect(ask.ask?.did).toBe('Refund $250 to Dana Lee');
    expect(ask.ask?.facts.find((f) => f.label === 'Amount')?.value).toBe('$250');
    expect(ask.why.rule?.id).toBe('pr_refunds');
    const mm = n.items.filter((i) => i.kind === 'mismatch');
    expect(mm.length).toBe(1);
    expect(mm[0]!.mismatch?.post).toBe('/v1/outcomes/out_a_11/retry');
    expect(n.tallies.waiting).toBe(2);
  });

  test('rules read as sentences in the mode their operation runs in', async () => {
    const { send } = await signedIn(busyDay(60));
    const r = (await send('/v1/console/rules')).data as RulesView;
    const refunds = r.rules.find((x) => x.id === 'pr_refunds')!;
    expect(refunds.text).toBe('Ask a person before a Stripe refund over $100');
    expect(refunds.mode).toBe('ENFORCE');
    expect(refunds.tokens[0]!.options.map((o) => o.label)).toContain('$100');
    const email = r.rules.find((x) => x.id === 'pr_email')!;
    expect(email.mode).toBe('SHADOW');
    expect(email.today.would).toBe(3);
    expect(r.templates.some((t) => t.added)).toBe(true);
  });

  test('the watch report has the rules that only watch', async () => {
    const { send } = await signedIn(busyDay(60));
    const v = (await send('/v1/console/watch-report')).data as WatchView;
    expect(v.areas.map((a) => a.ruleId)).toEqual(['pr_email']);
    expect(v.week.days).toHaveLength(7);
    expect(v.overheadMs).toBeNull();
  });

  test('spend follows the cost records, with no limit to set', async () => {
    const { send } = await signedIn(busyDay(60));
    const s = (await send('/v1/console/spend')).data as SpendView;
    expect(s.limit).toBeNull();
    expect(s.spent).toBeCloseTo((60 * 40_000) / 1e6 - (12 * 0) / 1e6, 2);
    expect(s.curve[0]).toEqual([0, 0]);
    expect(s.byAi.length).toBeGreaterThan(0);
  });

  test('apps say which are checked, and which can be switched back on', async () => {
    const { send } = await signedIn(busyDay(60));
    const a = (await send('/v1/console/apps')).data as AppsView;
    const stripe = a.apps.find((x) => x.id === 'stripe')!;
    const gmail = a.apps.find((x) => x.id === 'gmail')!;
    expect(stripe.state).toBe('connected');
    expect(gmail.state).toBe('off');
    expect(gmail.canConnect).toBe(true);
    expect(a.catalog).toEqual([]);
  });

  test('live lines up who waits on what', async () => {
    const { send } = await signedIn(busyDay(60));
    const l = (await send('/v1/console/live')).data as LiveView;
    expect(l.lanes.length).toBeGreaterThan(0);
    expect(l.waiting).toBeGreaterThan(0);
  });

  test('a map shows the action and what it touched', async () => {
    const { send } = await signedIn(busyDay(30));
    const m = (await send('/v1/console/map/a_11')).data as MapView;
    expect(m.nodes.find((n) => n.id === 'act')?.actor?.name).toBe('Sales AI');
    const thing = m.nodes.find((n) => n.app)!;
    expect(thing.proof.kind).toBe('bad');
    expect(thing.fix).toEqual({ kind: 'retry', post: '/v1/outcomes/out_a_11/retry', label: 'Check Stripe again', done: 'Checking Stripe again' });
    expect(m.tallies.bad).toBe(1);
  });

  test('days start at your midnight', async () => {
    const w = busyDay(48, {}, 'Asia/Kolkata');
    const s = setUp(w);
    await s.send('/v1/auth/session');
    const t = (await s.send('/v1/console/today', { headers: { 'x-lumni-tz': 'Asia/Kolkata' } })).data as TodayView;
    expect(t.done).toBe(49);
  });

  test('no response ever carries the admin key', async () => {
    const w = busyDay(30);
    const { send } = await signedIn(w);
    for (const p of ['/v1/console/frame', '/v1/console/today', '/v1/console/receipts', '/v1/console/needs-you', '/v1/console/rules', '/v1/console/apps', '/v1/console/spend', '/v1/console/live', '/v1/console/map']) {
      const r = await send(p);
      expect(r.status).toBe(200);
      expect(r.text).not.toContain(w.key);
    }
  });
});

describe('writes with real data', () => {
  test('a rule’s mode goes to the enforcement endpoint, on its operation and reason', async () => {
    const { send, w } = await signedIn(busyDay(10));
    const r = await send('/v1/console/rules/pr_email/mode', { method: 'PUT', body: { mode: 'ENFORCE' } });
    expect(r.status).toBe(200);
    expect(w.enforcement.some((e) => e.operation === 'gmail.send' && e.rule === 'APPROVAL_REQUIRED' && e.mode === 'ENFORCE')).toBe(true);
  });

  test('trying a word waits for save, then updates the rule by name', async () => {
    const { send, w } = await signedIn(busyDay(10));
    await send('/v1/console/rules/pr_refunds/edit', { method: 'POST', body: { token: 'threshold', value: '250', action: 'preview' } });
    const v = (await send('/v1/console/rules')).data as RulesView;
    expect(v.rules.find((r) => r.id === 'pr_refunds')!.draft?.label).toBe('$250');
    expect(w.rules.find((r) => r.id === 'pr_refunds')!.numeric_threshold).toBe(100);
    await send('/v1/console/rules/pr_refunds/edit', { method: 'POST', body: { action: 'save' } });
    expect(w.rules.find((r) => r.id === 'pr_refunds')!.numeric_threshold).toBe(250);
  });

  test('a new rule starts out only watching', async () => {
    const { send, w } = await signedIn(busyDay(10));
    const r = await send('/v1/console/rules', { method: 'POST', body: { template: 'deploy-risk' } });
    expect(r.status).toBe(200);
    expect(w.rules.some((x) => x.name === 'No high-risk deploys')).toBe(true);
    expect(w.enforcement.some((e) => e.operation === 'vercel.deploy' && e.rule === 'POLICY_REJECTED' && e.mode === 'SHADOW')).toBe(true);
  });

  test('a checker switches on by posting its verifier again', async () => {
    const { send, w } = await signedIn(busyDay(10));
    const r = await send('/v1/console/apps/gmail/checker', { method: 'POST', body: { on: true } });
    expect(r.status).toBe(200);
    expect(w.verifiers.find((v) => v.id === 'v_gmail')!.enabled).toBe(true);
    const none = await send('/v1/console/apps/notion/checker', { method: 'POST', body: { on: true } });
    expect(none.status).toBe(501);
  });

  test('what the API can’t do yet says so', async () => {
    const { send } = await signedIn(busyDay(10));
    for (const [method, path] of [['POST', '/v1/console/pause'], ['PUT', '/v1/console/spend-limit'], ['PUT', '/v1/console/notify'], ['POST', '/v1/console/needs/x/handled']] as const) {
      const r = await send(path, { method, body: {} });
      expect(r.status).toBe(501);
      expect(r.data.error.code).toBe('NOT_AVAILABLE');
    }
  });
});
