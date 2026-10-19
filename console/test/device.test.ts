// Browser login for the onboarding command: start, wait, connect in the
// browser, collect a one-time code once, trade it with the API.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../server/env';
import { createBff } from '../server/index';
import { DeviceLogin, normalizeUserCode } from '../server/device';
import type { Onboarding } from '../server/onboarding';
import { fakeFetch } from './real/fake-go';
import { busyDay } from './real/day';

function setUp(over: { noOnboarding?: boolean } = {}) {
  const w = busyDay(5);
  w.noOnboarding = over.noOnboarding;
  const fake = fakeFetch(w);
  const bff = createBff(readEnv({ LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, LUMNI_CONSOLE_PUBLIC_API_URL: 'https://lumni.acme.dev', PORT: '5200' }), fake);
  const jar = new Map<string, string>();
  let csrf = '';
  const send = async (path: string, method = 'GET', body?: unknown, anonymous = false) => {
    const headers: Record<string, string> = { host: 'localhost:5200' };
    if (jar.size && !anonymous) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
    if (method !== 'GET') {
      headers['content-type'] = 'application/json';
      if (csrf && !anonymous) headers['x-csrf-token'] = csrf;
    }
    const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })))!;
    for (const line of res.headers.getSetCookie()) {
      const [k, v] = line.split(';')[0]!.split('=');
      jar.set(k!, v!);
    }
    const text = await res.text();
    const data = text ? JSON.parse(text) : undefined;
    if (path === '/v1/auth/session' && data?.csrf_token) csrf = data.csrf_token;
    return { status: res.status, data };
  };
  const toApi = async (path: string, body: unknown) => {
    const res = await fake(`http://go.test${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    return { status: res.status, data: await res.json() };
  };
  return { send, toApi };
}

describe('browser login through the console’s server', () => {
  test('start, wait, connect in the browser, then collect a one-time code once and trade it with the API', async () => {
    const { send, toApi } = setUp();
    const start = await send('/console/device', 'POST', { host: 'dev-laptop', project: 'billing-agent', client: 'lumni init' }, true);
    expect(start.status).toBe(200);
    expect(start.data.user_code).toMatch(/^[B-DF-HJ-NP-TV-XZ]{4}-[B-DF-HJ-NP-TV-XZ]{4}$/);
    expect(start.data.verification_uri_complete).toBe(`http://localhost:5200/device?code=${start.data.user_code}`);
    expect(start.data.device_code).toHaveLength(64);

    const waiting = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
    expect(waiting.status).toBe(400);
    expect(waiting.data.error.code).toBe('authorization_pending');

    // In the browser, signed in.
    await send('/v1/auth/session');
    const seen = await send(`/v1/console/device/${start.data.user_code.toLowerCase().replace('-', ' ')}`);
    expect(seen.data).toMatchObject({ host: 'dev-laptop', project: 'billing-agent', status: 'pending' });
    const ok = await send(`/v1/console/device/${start.data.user_code}/approve`, 'POST', {});
    expect(ok.status).toBe(200);
    expect(ok.data.status).toBe('approved');

    const got = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
    expect(got.status).toBe(200);
    expect(got.data).toMatchObject({ made_by: 'api', actor_hint: 'billing-agent', api_url: 'https://lumni.acme.dev' });
    expect(JSON.stringify(got.data)).not.toMatch(/api_key|lm_/);

    const traded = await toApi('/v1/onboarding/exchange', { code: got.data.code, host: 'dev-laptop', project: 'billing-agent' });
    expect(traded.status).toBe(200);
    expect(traded.data.api_key).toBeTruthy();

    const again = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
    expect(again.data.error.code).toBe('expired_token');
    const recent = await send('/v1/console/device?since=0');
    expect(recent.data.items).toHaveLength(1);
    expect(recent.data.items[0]).toMatchObject({ host: 'dev-laptop', project: 'billing-agent' });
  });

  test('lumniverse try: its login is marked as the sample agents, and only it gets verification and admin', async () => {
    const { send, toApi } = setUp();
    await send('/v1/auth/session');
    const scopesFor = async (client: string, project: string) => {
      const start = await send('/console/device', 'POST', { host: 'dev-laptop', project, client }, true);
      const seen = await send(`/v1/console/device/${start.data.user_code}`);
      await send(`/v1/console/device/${start.data.user_code}/approve`, 'POST', { actor_hint: 'ignored-for-try' });
      const got = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
      const traded = await toApi('/v1/onboarding/exchange', { code: got.data.code, host: 'dev-laptop', project });
      const keys = (await send('/v1/api-keys')).data.items as { id: string; scopes: string[] }[];
      return { purpose: seen.data.purpose, hint: got.data.actor_hint, scopes: keys.find((k) => k.id === traded.data.key_id)!.scopes };
    };
    const sample = await scopesFor('lumni try', 'lumni-try');
    expect(sample.purpose).toBe('try');
    expect(sample.hint ?? '').toBe('');
    for (const scope of ['admission', 'verification', 'admin']) expect(sample.scopes).toContain(scope);
    const agent = await scopesFor('lumni init', 'billing-agent');
    expect(agent.purpose).toBe('agent');
    expect(agent.scopes).not.toContain('admin');
    expect(agent.scopes).not.toContain('verification');
  });

  test('cancelled in the browser: the command is told, and nothing is made', async () => {
    const { send } = setUp();
    const start = await send('/console/device', 'POST', { host: 'h' }, true);
    await send('/v1/auth/session');
    await send(`/v1/console/device/${start.data.user_code}/deny`, 'POST', {});
    const r = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
    expect(r.data.error.code).toBe('access_denied');
    expect((await send(`/v1/console/device/${start.data.user_code}/approve`, 'POST', {})).status).toBe(409);
  });

  test('seeing or connecting a login needs a session; a made-up secret gets nothing', async () => {
    const { send } = setUp();
    const start = await send('/console/device', 'POST', {}, true);
    expect((await send(`/v1/console/device/${start.data.user_code}`)).status).toBe(401);
    const r = await send('/console/device/token', 'POST', { device_code: 'f'.repeat(64) }, true);
    expect(r.data.error.code).toBe('expired_token');
  });

  test('an older API: the console makes the code, and says so', async () => {
    const { send } = setUp({ noOnboarding: true });
    const start = await send('/console/device', 'POST', { project: 'shop' }, true);
    await send('/v1/auth/session');
    await send(`/v1/console/device/${start.data.user_code}/approve`, 'POST', { actor_hint: 'shop-agent' });
    const got = await send('/console/device/token', 'POST', { device_code: start.data.device_code }, true);
    expect(got.data).toMatchObject({ made_by: 'console', actor_hint: 'shop-agent' });
    const traded = await send('/console/onboarding/exchange', 'POST', { code: got.data.code }, true);
    expect(traded.status).toBe(200);
  });
});

describe('the login itself', () => {
  const fakeOnboarding = { create: async () => ({ id: 'x', code: 'LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ', expires_at: '', key_prefix: null, made_by: 'api' as const }) } as unknown as Onboarding;

  test('polling faster than every 3 s is told to slow down', () => {
    let now = 1_000_000;
    const d = new DeviceLogin(fakeOnboarding, () => now);
    const s = d.start('1.1.1.1', {}, 'http://c');
    expect(() => d.token({ device_code: s.device_code })).toThrow('Waiting');
    now += 1000;
    expect(() => d.token({ device_code: s.device_code })).toThrow('too fast');
  });

  test('a login lasts 10 minutes', async () => {
    let now = 1_000_000;
    const d = new DeviceLogin(fakeOnboarding, () => now);
    const s = d.start('1.1.1.1', {}, 'http://c');
    now += 10 * 60_000 + 1;
    expect(d.lookup(s.user_code).status).toBe('expired');
    await expect(d.approve(s.user_code, { session: 's', csrf: 'c' }, 't', {})).rejects.toThrow('expired');
    expect(() => d.token({ device_code: s.device_code })).toThrow('expired');
  });

  test('one machine can start 10 logins a minute', () => {
    const d = new DeviceLogin(fakeOnboarding);
    for (let i = 0; i < 10; i++) d.start('9.9.9.9', {}, 'http://c');
    expect(() => d.start('9.9.9.9', {}, 'http://c')).toThrow('Too many');
  });

  test('codes read the same however they’re typed', () => {
    expect(normalizeUserCode('wdjb mjht')).toBe(normalizeUserCode('WDJB-MJHT'));
  });
});
