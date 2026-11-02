// Live execution: the order and quote scenes (app/lib/live.js). DOM-free.

import { describe, expect, test } from 'bun:test';

(globalThis as any).window ??= globalThis;
// @ts-expect-error: a browser script with no types; it sets globalThis.Live.
await import('../app/lib/live.js');
const L = (globalThis as any).Live;

// Only what the Lumni API really returns (pkg/protocol/types.go, durable.go,
// verification.go). The screen must never show a decision the API can't give.
const DECISIONS = ['ALLOW', 'WAIT', 'REPLAN', 'DEDUPLICATE', 'REQUIRE_APPROVAL', 'REJECT', 'VERIFIED'];
const REASONS = ['NO_CONFLICT', 'LEASE_BUSY', 'ACTION_RESULT_UNKNOWN', 'WORK_CANCELLED', 'STALE_OBSERVATION', 'WRITE_WRITE_CONFLICT', 'EXACT_EFFECT_ALREADY_COMMITTED'];
const ROUTES = [/^POST \/v1\/actions\/check$/, /^POST \/v1\/observations$/, /^POST \/v1\/resources\/[\w:-]+\/version$/, /^POST \/v1\/actions\/\{id\}\/(result|recheck)$/, /^POST \/v1\/work\/[\w-]+\/cancel$/, /^GET \/v1\/verification-attempts\/\{id\}$/];

const both = [L.scenario({ fault: true }), L.scenario({ fault: false })];
const quotes = [L.scenario({ scene: 'supplier', fault: true }), L.scenario({ scene: 'supplier', fault: false })];
const all = [...both, ...quotes];

describe('the scenario', () => {
  test('events run in time order and end before the clock does', () => {
    for (const s of all) {
      const ts = s.events.map((e: any) => e.t);
      expect([...ts].sort((a, b) => a - b)).toEqual(ts);
      expect(ts[ts.length - 1]).toBeLessThan(s.end);
    }
  });

  test('every decision, reason and route is the API’s own', () => {
    for (const s of all) {
      for (const e of s.events) {
        if (e.decision) expect(DECISIONS).toContain(e.decision.type);
        if (e.decision?.reason_code) expect(REASONS).toContain(e.decision.reason_code);
        if (e.api) expect(ROUTES.some((r) => r.test(`${e.api.method} ${e.api.path}`))).toBe(true);
        if (e.api?.response?.decision) expect(e.api.response.decision).toBe(e.decision.type);
      }
    }
  });

  test('the order ends cancelled, refunded and restocked, with one charge and one refund', () => {
    for (const s of both) {
      const r = L.result(s);
      expect(r).toMatchObject({ order: 'cancelled', payment: 'refunded', charges: 1, refunds: 1, reserved: 0, reconciled: true });
      expect(r.available).toBe(r.startAvailable);
    }
  });

  test('with the fault on, the retry is held and no second charge ever appears', () => {
    const s = both[0];
    for (let t = 0; t <= s.end; t += 0.1) expect(L.result(s, t).charges).toBeLessThanOrEqual(1);
    expect(L.result(s).prevented).toBe(1);
    const held = s.events.find((e: any) => e.prevented);
    expect(held.decision).toEqual({ type: 'WAIT', reason_code: 'ACTION_RESULT_UNKNOWN' });
  });

  test('nobody refunds or releases until Lumni has read the charge back', () => {
    const s = both[0];
    const asked = s.events.find((e: any) => e.kind === 'verify').t;
    const ran = s.events.filter((e: any) => e.decision?.type === 'ALLOW' && ['stripe.refund', 'inventory.release', 'order.cancel'].includes(e.api?.body?.operation));
    const later = s.events.filter((e: any) => e.decision?.type === 'ALLOW' && e.t > asked);
    expect(later.length).toBeGreaterThan(0);
    for (const e of ran) expect(e.t).toBeGreaterThan(asked);
  });

  test('at most one AI holds the order at a time, and the holder is never also waiting', () => {
    for (const s of both) {
      for (const e of s.events.filter((x: any) => x.lease)) {
        if (e.lease.holder) expect(e.lease.queue).not.toContain(e.lease.holder);
        expect(new Set(e.lease.queue).size).toBe(e.lease.queue.length);
      }
    }
  });

  test('reading a moment: nothing has happened at the start, everything by the end', () => {
    const s = both[0];
    const a = L.at(s, -1);
    expect(a.seen.length).toBe(0);
    expect(a.systems).toEqual(s.start);
    expect(Object.values(a.cards).every((c: any) => c.tone === 'idle')).toBe(true);
    const z = L.at(s, s.end);
    expect(z.resolved).toBe(true);
    expect(z.hold).toBe(false);
  });

  test('every agent has a lane, and lanes cover the run without gaps or overlaps', () => {
    for (const s of all) {
      const lanes = L.lanes(s);
      for (const a of s.agents) {
        const ls = lanes[a.id];
        expect(ls.length).toBeGreaterThan(0);
        ls.forEach((b: any, i: number) => {
          expect(b.to).toBeGreaterThan(b.from);
          if (i) expect(b.from).toBe(ls[i - 1].to);
        });
        expect(ls[ls.length - 1].to).toBe(s.end);
      }
    }
  });
});

describe('the quote scene', () => {
  const [changes, holds] = quotes;
  // The Go kernel's rule (internal/coordination/kernel.go, detectStale): an
  // action that declares an observed_version older than the resource's current
  // one gets REPLAN STALE_OBSERVATION. Replay the scene's own version changes
  // and check every send against that rule.
  function sends(s: any) {
    const current: Record<string, string> = {};
    const out: any[] = [];
    for (const e of s.events) {
      const m = e.api?.path.match(/^\/v1\/resources\/(.+)\/version$/);
      if (m) current[m[1]] = e.api.body.version;
      if (e.api?.body?.operation !== 'quote.send') continue;
      const stale = e.api.body.resources.some((r: any) => r.observed_version && current[r.id] && current[r.id] !== r.observed_version);
      out.push({ e, stale });
    }
    return out;
  }

  test('a send built on an old version is sent back, and only that one', () => {
    for (const s of quotes) {
      for (const { e, stale } of sends(s)) {
        expect(e.decision.type).toBe(stale ? 'REPLAN' : 'ALLOW');
        if (stale) expect(e.decision.reason_code).toBe('STALE_OBSERVATION');
      }
    }
    expect(sends(changes).map((x) => x.stale)).toEqual([true, false]);
    expect(sends(holds).map((x) => x.stale)).toEqual([false]);
  });

  test('the stale answer names what changed, as the kernel’s evidence does', () => {
    const back = sends(changes).find((x) => x.stale)!.e;
    expect(back.api.response.evidence[0]).toMatchObject({ type: 'RESOURCE_VERSION', data: { resource_id: 'supplier_cost:SKU-7731', observed_version: '18', current_version: '19' } });
  });

  test('with the price change, the corrected quote goes out once, on today’s cost', () => {
    const r = L.result(changes);
    expect(r).toMatchObject({ sent: 1, belowCost: 0, basis: 19, version: 19, cost: 110, unit: 148.5, total: 17820, prevented: 1, ok: true });
    expect(r.margin).toBeGreaterThanOrEqual(0.25);
    for (let t = 0; t <= changes.end; t += 0.1) {
      const x = L.result(changes, t);
      expect(x.sent).toBeLessThanOrEqual(1);
      expect(x.belowCost).toBe(0);
    }
  });

  test('with the price holding, the first quote goes out as drafted', () => {
    expect(L.result(holds)).toMatchObject({ sent: 1, belowCost: 0, basis: 18, version: 18, unit: 108, total: 12960, prevented: 0, ok: true });
  });

  test('the dependency strip shows the quote out of date only between the change and the reprice', () => {
    const stale = (t: number) => L.at(changes, t).deps.some((d: any) => d.current !== d.observed);
    const recorded = changes.events.find((e: any) => e.api?.path.endsWith('/version')).t;
    const reread = changes.events.find((e: any) => e.api?.path === '/v1/observations' && e.api.body.version === '19').t;
    expect(stale(recorded - 0.1)).toBe(false);
    expect(stale(recorded)).toBe(true);
    expect(stale(reread)).toBe(false);
  });

  test('the supplier row says the draft is behind until Lumni sends the quote back', () => {
    const back = sends(changes).find((x) => x.stale)!.e.t;
    expect(Object.keys(L.at(changes, back - 0.1).unseen)).toEqual(['supplier/SKU-7731']);
    expect(L.at(changes, back).unseen).toEqual({});
  });

  test('both scenes pick by name, and an unknown one falls back to the order', () => {
    expect(L.scenes().map((x: any) => x.id)).toEqual(['payment', 'supplier']);
    expect(L.scenario({ scene: 'nope' }).scene).toBe('payment');
    for (const s of all) expect(L.outcome(s).nums.length).toBe(4);
  });
});
