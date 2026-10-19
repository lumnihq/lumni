// The demo world (built in the page) and the real world (built by the server)
// must have the same shape, so a screen never reads a field one of them lacks.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../server/env';
import { createBff } from '../server/index';
import { fakeFetch } from './real/fake-go';
import { richDay } from './real/rich';

(globalThis as { window?: unknown }).window = globalThis;
// @ts-expect-error: plain JS that sets window.World.
await import('../app/lib/world-mock.js');
const World = (globalThis as unknown as { World: (k: string) => Record<string, unknown> }).World;

async function realWorld(): Promise<Record<string, unknown>> {
  const w = richDay(400);
  const bff = createBff(readEnv({ LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, PORT: '5200' }), fakeFetch(w));
  const jar = new Map<string, string>();
  const send = async (path: string) => {
    const headers: Record<string, string> = { host: 'localhost:5200', 'x-lumni-tz': 'UTC' };
    if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
    const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { headers })))!;
    for (const line of res.headers.getSetCookie()) {
      const [k, v] = line.split(';')[0]!.split('=');
      jar.set(k!, v!);
    }
    return res.json() as Promise<Record<string, unknown>>;
  };
  await send('/v1/auth/session');
  return send('/v1/console/world');
}

const keysOf = (o: unknown) => (o && typeof o === 'object' && !Array.isArray(o) ? Object.keys(o).filter((k) => typeof (o as Record<string, unknown>)[k] !== 'function').sort() : []);
const first = (x: unknown) => (Array.isArray(x) ? x[0] : x);

describe('demo and real worlds', () => {
  test('have the same top-level fields', async () => {
    const real = await realWorld();
    const demo = World('mid');
    const extraReal = ['notes'];
    expect(keysOf(real).filter((k) => !extraReal.includes(k))).toEqual(keysOf(demo).filter((k) => !['team', 'app'].includes(k)));
  });

  test('every nested record a screen reads has the demo’s fields', async () => {
    const real = await realWorld();
    const demo = World('mid');
    for (const path of ['scale', 'kinds', 'would', 'money', 'money.waste.repeats']) {
      const get = (o: Record<string, unknown>) => path.split('.').reduce<unknown>((x, k) => (x as Record<string, unknown>)?.[k], o);
      for (const k of keysOf(get(demo)).filter((k) => k !== 'seed')) expect(keysOf(get(real))).toContain(k);
    }
    for (const list of ['teams', 'apps', 'fleet', 'things', 'facts', 'groups']) {
      const d = first(demo[list]);
      const r = first(real[list]);
      if (!d || !r) continue;
      const optional = ['blind', 'promote', 'demote', 'human', 'rule', 'amount'];
      const missing = keysOf(d).filter((k) => !optional.includes(k) && !keysOf(r).includes(k));
      expect({ list, missing }).toEqual({ list, missing: [] });
    }
    for (const kind of ['stop', 'recheck', 'wait', 'bad']) {
      const d = first((demo.patterns as Record<string, unknown[]>)[kind]);
      const r = first((real.patterns as Record<string, unknown[]>)[kind]);
      if (!d || !r) continue;
      for (const k of ['id', 't', 'team', 'app', 'n', 'kept', 'story']) expect(keysOf(r)).toContain(k);
    }
  });

  test('the demo is the same moment on every load', () => {
    for (const v of ['small', 'mid', 'large']) {
      const w = World(v);
      expect(w.timeLabel).toBe('2:13 PM');
      expect(w.dayLabel).toBe('Thursday, Sep 25');
      expect((w.hourly as number[]).length).toBe(24);
    }
  });

  test('there is no fake real world in the page', () => {
    expect(() => World('real')).toThrow();
  });
});
