// Lumni Cloud's billing: a trial for each workspace made by sign-up, Stripe
// Checkout and the portal, Stripe's signed events, plans put into effect on
// the API, trials ending in watch-only, and usage reported to Stripe's meter.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../server/env';
import { createBff } from '../server/index';
import { Billing, TRIAL_DAYS } from '../server/accounts/billing';
import { Store } from '../server/accounts/store';
import { fakeFetch, world } from './real/fake-go';
import { busyDay } from './real/day';

const STRIPE = {
  LUMNI_CONSOLE_STRIPE_SECRET_KEY: 'sk_test_lumni', LUMNI_CONSOLE_STRIPE_WEBHOOK_SECRET: 'whsec_test_secret',
  LUMNI_CONSOLE_STRIPE_PRICE_PLAN: 'price_team_monthly', LUMNI_CONSOLE_STRIPE_PRICE_USAGE: 'price_team_checks',
};
const DAY = 86_400_000;

/** Stripe, as far as the console uses it: every call recorded. */
function fakeStripe() {
  const calls: { path: string; form: URLSearchParams }[] = [];
  let failMeter = false;
  const fetchFn = (url: string, init?: RequestInit): Promise<Response> | null => {
    if (!url.startsWith('https://api.stripe.com/')) return null;
    const path = url.replace('https://api.stripe.com/v1', '');
    calls.push({ path, form: new URLSearchParams(String(init?.body ?? '')) });
    if (path === '/checkout/sessions') return Promise.resolve(Response.json({ id: 'cs_1', url: 'https://checkout.stripe.com/c/cs_1' }));
    if (path === '/billing_portal/sessions') return Promise.resolve(Response.json({ id: 'bps_1', url: 'https://billing.stripe.com/p/bps_1' }));
    if (path === '/billing/meter_events') return Promise.resolve(failMeter ? Response.json({ error: { message: 'down' } }, { status: 500 }) : Response.json({ identifier: 'x' }));
    return Promise.resolve(Response.json({ error: { message: `no ${path}` } }, { status: 404 }));
  };
  return { calls, fetchFn, fail: (v: boolean) => (failMeter = v) };
}

/** A Stripe-Signature header for a body, as Stripe makes it. */
async function signed(body: string, secret = STRIPE.LUMNI_CONSOLE_STRIPE_WEBHOOK_SECRET, at = Math.floor(Date.now() / 1000)) {
  const key = await crypto.subtle.importKey('raw', new TextEncoder().encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const sig = Array.from(new Uint8Array(await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(`${at}.${body}`))), (b) => b.toString(16).padStart(2, '0')).join('');
  return `t=${at},v1=${sig}`;
}

function setUp() {
  const w = busyDay(5);
  w.platformKey = 'platform_test_key';
  const stripe = fakeStripe();
  const fake = fakeFetch(w);
  const fetchFn = async (url: string, init?: RequestInit) => (stripe.fetchFn(url, init) ?? fake(url, init));
  const bff = createBff(readEnv({
    LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, LUMNI_CONSOLE_AUTH: 'accounts', LUMNI_CONSOLE_SECRET: 'test-secret-0123456789', LUMNI_CONSOLE_DB: ':memory:',
    LUMNI_CONSOLE_DATA_MODE: 'real', PORT: '5200', LUMNI_CONSOLE_SIGNUP: 'open', LUMNI_CONSOLE_PLATFORM_KEY: 'platform_test_key', ...STRIPE,
  }), fetchFn as never);
  const browser = () => {
    const jar = new Map<string, string>();
    let csrf = '';
    const send = async (path: string, method = 'GET', body?: unknown, extra: Record<string, string> = {}, raw?: string) => {
      const headers: Record<string, string> = { host: 'localhost:5200', ...extra };
      if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
      if (method !== 'GET') { headers['content-type'] = 'application/json'; if (csrf) headers['x-csrf-token'] = csrf; }
      const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { method, headers, body: raw ?? (body === undefined ? undefined : JSON.stringify(body)) })))!;
      for (const line of res.headers.getSetCookie()) {
        const [k, v] = line.split(';')[0]!.split('=');
        if (/Max-Age=0/.test(line)) jar.delete(k!); else jar.set(k!, v!);
      }
      const text = await res.text();
      const data = text ? JSON.parse(text) : undefined;
      if (path === '/v1/auth/session' && data?.csrf_token) csrf = data.csrf_token;
      return { status: res.status, data };
    };
    return { send };
  };
  return { w, stripe, browser };
}

describe('billing through the console', () => {
  test('a sign-up gets a trial; Checkout, Stripe’s events and the portal move it between plans', async () => {
    const { w, stripe, browser } = setUp();
    const owner = browser();
    await owner.send('/console/auth/setup', 'POST', { name: 'Ana Owner', email: 'ana@acme.dev', password: 'correct horse battery', workspace: 'Acme' });
    await owner.send('/v1/auth/session');
    expect((await owner.send('/console/config.json')).data.billing).toBe(true);
    // The server's own workspace isn't billed.
    expect((await owner.send('/v1/console/billing')).data).toMatchObject({ enabled: true, status: 'exempt' });

    const ed = browser();
    expect((await ed.send('/console/auth/signup', 'POST', { name: 'Ed Founder', email: 'ed@startup.dev', password: 'a long password', workspace: 'Startup' })).status).toBe(200);
    const me = (await ed.send('/v1/auth/session')).data;
    const tenant = me.tenant.id;
    const workspace = (await ed.send('/v1/console/account')).data.workspaces[0].id;
    w.usage = { [tenant]: 37 };
    const trial = (await ed.send('/v1/console/billing')).data;
    expect(trial).toMatchObject({ status: 'trial', days_left: TRIAL_DAYS, can_manage: true, plan: { name: 'Team', included: 50_000 }, limits: { checks_per_day: 2000 }, usage: { checks: 37, period: 'day' } });

    // Checkout: the monthly price and the metered one, for this workspace.
    const out = await ed.send('/v1/console/billing/checkout', 'POST', {});
    expect(out.data.url).toBe('https://checkout.stripe.com/c/cs_1');
    const f = stripe.calls.find((c) => c.path === '/checkout/sessions')!.form;
    expect(f.get('mode')).toBe('subscription');
    expect(f.get('line_items[0][price]')).toBe('price_team_monthly');
    expect(f.get('line_items[1][price]')).toBe('price_team_checks');
    expect(f.get('client_reference_id')).toBe(workspace);
    expect(f.get('subscription_data[metadata][workspace]')).toBe(workspace);
    expect(f.get('customer_email')).toBe('ed@startup.dev');
    expect((await ed.send('/v1/console/billing/portal', 'POST', {})).data.error.code).toBe('no_customer');

    // Stripe's events: unsigned ones are refused.
    const event = (type: string, object: Record<string, unknown>) => JSON.stringify({ id: `evt_${type}`, type, data: { object } });
    const hook = (body: string, sig: string) => browser().send('/console/billing/webhook', 'POST', undefined, { 'stripe-signature': sig }, body);
    const done = event('checkout.session.completed', { client_reference_id: workspace, customer: 'cus_1', subscription: 'sub_1', metadata: { workspace } });
    expect((await hook(done, 't=1,v1=forged')).status).toBe(400);
    expect((await hook(done, await signed(done, 'whsec_someone_else'))).status).toBe(400);
    expect((await hook(done, await signed(done))).status).toBe(200);
    expect((await ed.send('/v1/console/billing')).data).toMatchObject({ status: 'active', has_customer: true, usage: { period: 'month' } });
    expect(w.plans?.[tenant]).toMatchObject({ plan: 'paid', watch_only: false });
    expect((await ed.send('/v1/console/billing/portal', 'POST', {})).data.url).toBe('https://billing.stripe.com/p/bps_1');
    expect((await ed.send('/v1/console/billing/checkout', 'POST', {})).data.error.code).toBe('already_subscribed');

    // Set to cancel at the period's end: still paid, and the date is shown.
    const end = Math.floor(Date.parse('2026-11-19T00:00:00Z') / 1000);
    const ending = event('customer.subscription.updated', { id: 'sub_1', customer: 'cus_1', status: 'active', cancel_at_period_end: true, items: { data: [{ current_period_end: end }] }, metadata: { workspace } });
    expect((await hook(ending, await signed(ending))).status).toBe(200);
    expect((await ed.send('/v1/console/billing')).data).toMatchObject({ status: 'active', cancel_at: '2026-11-19T00:00:00.000Z' });
    const kept = event('customer.subscription.updated', { id: 'sub_1', customer: 'cus_1', status: 'active', cancel_at_period_end: false, metadata: { workspace } });
    await hook(kept, await signed(kept));
    expect((await ed.send('/v1/console/billing')).data.cancel_at).toBeNull();

    // Cancelled while the trial would still run: back to the trial.
    const gone = event('customer.subscription.deleted', { id: 'sub_1', customer: 'cus_1', status: 'canceled', metadata: { workspace } });
    expect((await hook(gone, await signed(gone))).status).toBe(200);
    expect((await ed.send('/v1/console/billing')).data.status).toBe('trial');
    expect(w.plans?.[tenant]).toMatchObject({ plan: 'trial' });
  });

  test('only owners and admins change the plan', async () => {
    const { browser } = setUp();
    const ed = browser();
    await ed.send('/console/auth/signup', 'POST', { name: 'Ed Founder', email: 'ed@startup.dev', password: 'a long password', workspace: 'Startup' });
    await ed.send('/v1/auth/session');
    const link = (await ed.send('/v1/console/members/invite', 'POST', { email: 'mo@startup.dev', role: 'member' })).data.link;
    const mo = browser();
    await mo.send('/console/auth/signup', 'POST', { name: 'Mo Member', email: 'mo@startup.dev', password: 'a long password', invite: new URL(link).searchParams.get('invite') });
    await mo.send('/v1/auth/session');
    expect((await mo.send('/v1/console/billing')).data).toMatchObject({ status: 'trial', can_manage: false });
    expect((await mo.send('/v1/console/billing/checkout', 'POST', {})).status).toBe(403);
  });
});

describe('the hourly sweep', () => {
  function parts() {
    const w = world();
    w.platformKey = 'platform_test_key';
    const stripe = fakeStripe();
    const go = fakeFetch(w);
    let refuse = false;
    const fetchFn = async (url: string, init?: RequestInit) => {
      if (refuse && url.includes('/plan')) return Response.json({ error: { code: 'DOWN' } }, { status: 503 });
      return stripe.fetchFn(url, init) ?? go(url, init as never);
    };
    let now = Date.parse('2026-10-19T12:00:00Z');
    const env = readEnv({ LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_PLATFORM_KEY: 'platform_test_key', ...STRIPE });
    const store = new Store(':memory:');
    const billing = new Billing(env, store, fetchFn as never, () => now);
    const ws = store.createWorkspace('Startup', 't_startup', 'sealed', now);
    billing.startTrial(ws);
    return { w, stripe, store, billing, ws, advance: (ms: number) => (now += ms), at: () => now, refuse: (v: boolean) => (refuse = v) };
  }

  test('a trial that ends becomes watch-only on the API; a refused plan is tried again', async () => {
    const { w, store, billing, ws, advance, refuse } = parts();
    await billing.sweep();
    expect(w.plans?.t_startup).toBeUndefined(); // the API made it with trial limits already
    advance(TRIAL_DAYS * DAY + 1);
    refuse(true);
    await billing.sweep();
    expect(store.billing(ws.id)).toMatchObject({ status: 'watch', applied: 'trial' });
    refuse(false);
    await billing.sweep();
    expect(store.billing(ws.id)?.applied).toBe('watch');
    expect(w.plans?.t_startup).toMatchObject({ plan: 'watch', watch_only: true, limits: { checks_per_day: 500 } });
  });

  test('a subscribed workspace’s checks go to Stripe’s meter, once per window', async () => {
    const { w, stripe, store, billing, ws, advance, at } = parts();
    store.setBilling(ws.id, { status: 'active', stripe_customer: 'cus_9', stripe_subscription: 'sub_9', applied: 'paid', reported_until: at() }, at());
    w.usage = { t_startup: 1234 };
    advance(3_600_000);
    await billing.sweep();
    const meter = stripe.calls.filter((c) => c.path === '/billing/meter_events');
    expect(meter).toHaveLength(1);
    expect(meter[0]!.form.get('event_name')).toBe('lumni_checks');
    expect(meter[0]!.form.get('payload[stripe_customer_id]')).toBe('cus_9');
    expect(meter[0]!.form.get('payload[value]')).toBe('1234');
    expect(meter[0]!.form.get('identifier')).toContain(ws.id);
    expect(store.billing(ws.id)?.reported_until).toBe(at() - 2 * 60_000);
    // Moments later there's no new window to report.
    advance(30_000);
    await billing.sweep();
    expect(stripe.calls.filter((c) => c.path === '/billing/meter_events')).toHaveLength(1);
    // Stripe down: the window stays unreported and goes next time.
    stripe.fail(true);
    advance(3_600_000);
    const before = store.billing(ws.id)?.reported_until;
    await billing.sweep();
    expect(store.billing(ws.id)?.reported_until).toBe(before!);
  });
});
