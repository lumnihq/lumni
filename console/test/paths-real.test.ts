// Compiled paths on real data (app/lib/paths-real.js) against the fake Go API's
// lifecycle (test/real/fake-paths.ts). DOM-free.

import { describe, expect, test } from 'bun:test';
import { fakePaths } from './real/fake-paths';

(globalThis as any).window ??= globalThis;
// @ts-expect-error: a browser script with no types; it sets globalThis.PathsReal.
await import('../app/lib/paths-real.js');
const P = (globalThis as any).PathsReal;

describe('program as code', () => {
  test('expressions read like code', () => {
    expect(P.expr({ ref: '/input/order_id' })).toBe('input.order_id');
    expect(P.expr({ ref: '/vars/order/status' })).toBe('order.status');
    expect(P.expr({ op: 'eq', args: [{ ref: '/vars/x' }, { literal: 'paid' }] })).toBe('(x === "paid")');
    expect(P.expr({ op: 'not', args: [{ literal: true }] })).toBe('!true');
    expect(P.expr({ op: 'days_since', args: [{ ref: '/vars/t' }] })).toBe('days_since(t)');
    expect(P.expr({ object: { a: { literal: 1 } } })).toBe('{ a: 1 }');
  });

  test('every node becomes a line, inside one function', () => {
    const prog = { nodes: [
      { id: 'o', kind: 'read', binding_id: 'shop.orders', binding_version: 2, value: { object: { id: { ref: '/input/id' } } } },
      { id: 'g', kind: 'guard', value: { literal: true }, otherwise: 'off' },
      { id: 'c', kind: 'call', binding_id: 'shop.refund', binding_version: 1, value: { object: {} } },
      { id: 'r', kind: 'return', value: { ref: '/vars/c' } },
      { id: 'off', kind: 'fallback', reason: 'OUTSIDE_POLICY', value: {} },
    ] };
    const ls = P.lines(prog).map((l: any) => l.text);
    expect(ls[0]).toBe('function path(input) {');
    expect(ls.at(-1)).toBe('}');
    expect(ls).toContain('  const o = read("shop.orders@v2", { id: input.id })');
    expect(ls).toContain('  if (!true) goto off');
    expect(ls).toContain('  off: return fallback("OUTSIDE_POLICY")');
    expect(ls.length).toBe(prog.nodes.length + 2);
  });
});

describe('model', () => {
  test('missing numbers stay null, never borrowed', () => {
    const m = P.model({ id: 'p', name: 'P', actor_id: 'a', operation: 'op', status: 'shadow', versions: [{ v: 1, status: 'shadow' }], stats: { runs: 0 } });
    expect(m.hit).toBeNull();
    expect(m.saved).toBeNull();
    expect(m.msAfter).toBeNull();
    expect(m.next.v).toBe(1);
    expect(m.live).toBeNull();
  });

  test('savings come in micro-USD and leave in dollars', () => {
    const m = P.model({ stats: { runs: 10, served: 8, estimated_savings_micros_usd: 2_500_000 } });
    expect(m.saved).toBe(2.5);
    expect(m.hit).toBe(0.8);
  });
});

describe('the fake lifecycle', () => {
  const call = (f: ReturnType<typeof fakePaths>, method: string, p: string, body = {}) => f(method, p, body)!.json() as Promise<any>;

  test('compile, shadow, label, promote', async () => {
    const f = fakePaths('agent', 'me');
    const sug = await call(f, 'GET', '/v1/compiled-paths/suggestions');
    const id = sug.items[0].id;
    let p = await call(f, 'GET', `/v1/compiled-paths/${id}`);
    expect(p.can).toEqual(['compile']);
    const job = await call(f, 'POST', `/v1/compiled-paths/${id}/compile`, { revision: p.revision });
    expect((await call(f, 'GET', `/v1/jobs/${job.id}`)).status).toBe('COMPLETED');
    p = await call(f, 'GET', `/v1/compiled-paths/${id}`);
    expect(p.status).toBe('candidate');
    p = await call(f, 'POST', `/v1/compiled-paths/${id}/shadow`, { revision: p.revision });
    expect(p.can).toContain('promote');
    expect(f('POST', `/v1/compiled-paths/${id}/promote`, { revision: p.revision })!.status).toBe(409);
    const r = await call(f, 'GET', `/v1/compiled-paths/${id}/versions/1/replay`);
    for (const c of r.samples.filter((c: any) => c.result === 'disagree')) await call(f, 'PUT', `/v1/compiled-paths/${id}/disagreements/${c.id}/label`, { right: 'code' });
    p = await call(f, 'POST', `/v1/compiled-paths/${id}/promote`, { revision: p.revision });
    expect(p.status).toBe('live');
    expect(P.model(p).live.promoted_by).toBe('me');
  });

  test('a stale revision is refused', async () => {
    const f = fakePaths('agent', 'me');
    expect(f('POST', '/v1/compiled-paths/path_refund/pause', { revision: 1 })!.status).toBe(409);
  });

  test('compiling off: no compile verb, and compile answers 503', async () => {
    const f = fakePaths('agent', 'me');
    await call(f, 'PUT', '/v1/config/compiler', { enabled: false, cloud_processing_consent: false });
    const p = await call(f, 'GET', '/v1/compiled-paths/path_invoice');
    expect(p.can).toEqual([]);
    expect(f('POST', '/v1/compiled-paths/path_invoice/compile', { revision: p.revision })!.status).toBe(503);
  });
});
