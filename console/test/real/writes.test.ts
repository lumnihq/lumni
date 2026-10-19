// The console's writes against a fake Lumni API: what they change, and what
// they must never change.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../../server/env';
import { createBff } from '../../server/index';
import { fakeFetch, type FakeWorld } from './fake-go';
import { richDay } from './rich';

function setUp(w: FakeWorld) {
  const bff = createBff(readEnv({ LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, PORT: '5200' }), fakeFetch(w));
  const jar = new Map<string, string>();
  let csrf = '';
  const send = async (path: string, method = 'GET', body?: unknown) => {
    const headers: Record<string, string> = { host: 'localhost:5200', 'x-lumni-tz': 'UTC' };
    if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
    if (method !== 'GET') {
      headers.origin = 'http://localhost:5200';
      headers['content-type'] = 'application/json';
      if (csrf) headers['x-csrf-token'] = csrf;
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
  return { send };
}

describe('writes', () => {
  test('adding a rule never weakens one that already protects the same operation and reason', async () => {
    const w = richDay(200);
    const { send } = setUp(w);
    await send('/v1/auth/session');
    const before = w.enforcement.map((e) => ({ ...e }));
    const r = await send('/v1/console/rules', 'POST', { template: 'refund-approval' });
    expect(r.status).toBe(200);
    expect(r.data.mode).toBe('ENFORCE');
    expect(r.data.sharesWith).toBe('Refunds over $500 need a person');
    // No enforcement was set to watching for stripe.refund.
    const refunds = w.enforcement.filter((e) => e.operation === 'stripe.refund');
    expect(refunds.every((e) => e.mode !== 'SHADOW')).toBe(true);
    expect(w.enforcement.length).toBe(before.length);
  });

  test('a rule for an unprotected operation still starts out watching', async () => {
    const w = richDay(200);
    const { send } = setUp(w);
    await send('/v1/auth/session');
    const r = await send('/v1/console/rules', 'POST', { template: 'mass-email' });
    expect(r.data.mode).toBe('SHADOW');
    expect(w.enforcement.find((e) => e.operation === 'gmail.send')?.mode).toBe('SHADOW');
  });

  test('an approved decision leaves the waiting list on the next read', async () => {
    const w = richDay(200);
    const { send } = setUp(w);
    await send('/v1/auth/session');
    const waiting = (await send('/v1/console/world')).data.groups.reduce((s: number, g: { n: number }) => s + g.n, 0);
    const ok = await send('/v1/approvals/ap_0/approve', 'POST', {});
    expect(ok.status).toBe(200);
    // Straight after the write: the read cache was dropped, so this is fresh.
    const after = (await send('/v1/console/world')).data.groups.reduce((s: number, g: { n: number }) => s + g.n, 0);
    expect(after).toBe(waiting - 1);
  });
});
