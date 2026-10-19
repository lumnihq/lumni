// Onboarding codes: a code stands for an agent key, works once, expires, and
// never lets the key reach a browser.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../server/env';
import { createBff } from '../server/index';
import { HttpError } from '../server/http';
import { Onboarding, normalize } from '../server/onboarding';
import type { Upstream } from '../server/upstream';
import { fakeFetch } from './real/fake-go';
import { busyDay } from './real/day';

/** An upstream from before onboarding codes (D1): records calls and makes keys. */
function stubUp() {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  let n = 0;
  const up = {
    async send(method: string, path: string, body: unknown) {
      calls.push({ method, path, body });
      if (path === '/v1/onboarding/codes') throw new HttpError(404, 'BAD_UPSTREAM', 'not here');
      if (method === 'POST' && path === '/v1/api-keys') {
        n++;
        return { id: `key_${n}`, prefix: `lumni_k${n}`, secret: `lumni_k${n}_secret` };
      }
      return {};
    },
  } as unknown as Upstream;
  return { up, calls };
}

const who = { session: 's1', csrf: 'c1' };

describe('onboarding codes, made here when the API can’t', () => {
  test('a code stands for a fresh, scoped agent key, and the key is never in the answer', async () => {
    const { up, calls } = stubUp();
    const o = new Onboarding(up);
    const made = await o.create(who, 't1', { actor_hint: 'support-agent' });
    expect(made.code).toMatch(/^LUM-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$/);
    expect(JSON.stringify(made)).not.toContain('secret');
    expect(made.made_by).toBe('console');
    expect(calls[1]).toMatchObject({ method: 'POST', path: '/v1/api-keys', body: { scopes: ['ingest', 'query', 'runtime', 'admission'] } });
  });

  test('works once, says where it ran, then reads as used', async () => {
    const { up } = stubUp();
    const o = new Onboarding(up);
    const made = await o.create(who, 't1', {});
    const got = o.exchange('1.1.1.1', { code: made.code.toLowerCase().replace(/-/g, ' '), host: 'dev-laptop', project: 'billing-agent' });
    expect(got.api_key).toBe('lumni_k1_secret');
    expect(await o.status(made.id, 't1')).toMatchObject({ used: true, host: 'dev-laptop', project: 'billing-agent', expired: false });
    expect(() => o.exchange('1.1.1.1', { code: made.code })).toThrow('isn’t valid');
  });

  test('another workspace can’t read a code’s status', async () => {
    const { up } = stubUp();
    const o = new Onboarding(up);
    const made = await o.create(who, 't1', {});
    await expect(o.status(made.id, 't2')).rejects.toThrow('No onboarding code');
  });

  test('expires after 15 minutes, and its unused key is revoked', async () => {
    const { up, calls } = stubUp();
    let now = 1_000_000;
    const o = new Onboarding(up, () => now);
    const made = await o.create(who, 't1', {});
    now += 15 * 60_000 + 1;
    expect(() => o.exchange('1.1.1.1', { code: made.code })).toThrow('isn’t valid');
    expect(await o.sweep()).toBe(1);
    expect(calls.at(-1)).toMatchObject({ method: 'DELETE', path: '/v1/api-keys/key_1' });
    expect(await o.status(made.id, 't1')).toMatchObject({ used: false, expired: true, revoked: true });
  });

  test('a used code’s key is never revoked', async () => {
    const { up, calls } = stubUp();
    let now = 1_000_000;
    const o = new Onboarding(up, () => now);
    const made = await o.create(who, 't1', {});
    o.exchange('1.1.1.1', { code: made.code });
    now += 16 * 60_000;
    expect(await o.sweep()).toBe(0);
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  test('one machine gets 10 tries a minute', async () => {
    const { up } = stubUp();
    const o = new Onboarding(up);
    for (let i = 0; i < 10; i++) expect(() => o.exchange('9.9.9.9', { code: 'LUM-AAAA-AAAA' })).toThrow('isn’t valid');
    expect(() => o.exchange('9.9.9.9', { code: 'LUM-AAAA-AAAA' })).toThrow('Too many tries');
  });

  test('30 wrong codes from anywhere pause exchanges, so guessing is pointless', async () => {
    const { up } = stubUp();
    const o = new Onboarding(up);
    const made = await o.create(who, 't1', {});
    for (let i = 0; i < 30; i++) expect(() => o.exchange(`10.0.0.${i}`, { code: 'LUM-AAAA-AAAA' })).toThrow();
    expect(() => o.exchange('10.0.1.1', { code: made.code })).toThrow('Too many wrong codes');
  });

  test('codes read the same however they’re typed', () => {
    expect(normalize('LUM-7Q4K-9P2X')).toBe(normalize('lum 7q4k 9p2x'));
  });
});

describe('onboarding through the console’s server', () => {
  function setUp(over: { noOnboarding?: boolean } = {}) {
    const w = busyDay(5);
    w.noOnboarding = over.noOnboarding;
    const fake = fakeFetch(w);
    const bff = createBff(readEnv({ LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, LUMNI_CONSOLE_PUBLIC_API_URL: 'https://lumni.acme.dev', PORT: '5200' }), fake);
    const jar = new Map<string, string>();
    let csrf = '';
    const send = async (path: string, method = 'GET', body?: unknown, extra: Record<string, string> = {}) => {
      const headers: Record<string, string> = { host: 'localhost:5200', ...extra };
      if (jar.size && !extra.anonymous) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
      if (method !== 'GET') {
        headers['content-type'] = 'application/json';
        if (csrf && !extra.anonymous) headers['x-csrf-token'] = csrf;
      }
      delete headers.anonymous;
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
    /** The agent's side, straight to the API with no session, as lumni init does. */
    const toApi = async (path: string, body: unknown) => {
      const res = await fake(`http://go.test${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
      return { status: res.status, data: await res.json() };
    };
    return { send, toApi };
  }

  test('the API makes the code; the agent trades it with the API; the console sees where it ran', async () => {
    const { send, toApi } = setUp();
    await send('/v1/auth/session');
    const made = await send('/v1/console/onboarding/codes', 'POST', { actor_hint: 'shop' });
    expect(made.status).toBe(200);
    expect(made.data).toMatchObject({ made_by: 'api', key_prefix: null });
    expect(made.data.code).toMatch(/^LUM-[A-Z2-7]{13}-[A-Z2-7]{13}$/);
    expect((await send(`/v1/console/onboarding/codes/${made.data.id}`)).data).toMatchObject({ used: false, expired: false });

    const ex = await toApi('/v1/onboarding/exchange', { code: made.data.code, host: 'laptop', project: 'shop' });
    expect(ex.status).toBe(200);
    expect(ex.data).toMatchObject({ tenant_name: 'Acme', actor_hint: 'shop' });

    const st = await send(`/v1/console/onboarding/codes/${made.data.id}`);
    expect(st.data).toMatchObject({ used: true, host: 'laptop', project: 'shop', actor_id: 'shop' });
    expect(st.data.key_prefix).toBe(String(ex.data.api_key).slice(0, 10));
    expect((await toApi('/v1/onboarding/exchange', { code: made.data.code })).status).toBe(404);
  });

  test('an older API: the console makes the code and trades it, with no session', async () => {
    const { send } = setUp({ noOnboarding: true });
    await send('/v1/auth/session');
    const made = await send('/v1/console/onboarding/codes', 'POST', { actor_hint: 'shop' });
    expect(made.status).toBe(200);
    expect(made.data.code).toMatch(/^LUM-/);
    expect(JSON.stringify(made.data)).not.toMatch(/lumni_ab12_\w{6,}/);

    const ex = await send('/console/onboarding/exchange', 'POST', { code: made.data.code, host: 'laptop', project: 'shop' }, { anonymous: '1', 'x-forwarded-for': '203.0.113.9' });
    expect(ex.status).toBe(200);
    expect(ex.data.api_key).toMatch(/^lumni_ab12_/);
    expect(ex.data.api_url).toBe('https://lumni.acme.dev');

    const st = await send(`/v1/console/onboarding/codes/${made.data.id}`);
    expect(st.data).toMatchObject({ used: true, host: 'laptop', project: 'shop' });

    const again = await send('/console/onboarding/exchange', 'POST', { code: made.data.code }, { anonymous: '1' });
    expect(again.status).toBe(404);
  });

  test('a code’s key is listed on Settings and can be revoked there', async () => {
    const { send, toApi } = setUp();
    await send('/v1/auth/session');
    const made = await send('/v1/console/onboarding/codes', 'POST', { actor_hint: 'shop' });
    await toApi('/v1/onboarding/exchange', { code: made.data.code, host: 'laptop', project: 'shop' });
    const listed = await send('/v1/api-keys');
    const k = listed.data.items.find((x: { name: string }) => x.name === 'onboarding: shop @ laptop');
    expect(k).toBeDefined();
    expect(JSON.stringify(listed.data)).not.toContain('secret');
    const gone = await send(`/v1/api-keys/${k.id}`, 'DELETE');
    expect(gone.status).toBe(200);
    expect((await send('/v1/api-keys')).data.items.find((x: { id: string }) => x.id === k.id).revoked_at).toBeTruthy();
  });

  test('making a code needs a session', async () => {
    const { send } = setUp();
    const r = await send('/v1/console/onboarding/codes', 'POST', {});
    expect(r.status).toBe(401);
  });
});
