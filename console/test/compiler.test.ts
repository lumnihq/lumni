// Compiled paths: the demo's numbers and the version lifecycle (app/lib/compiler.js). DOM-free.

import { describe, expect, test } from 'bun:test';

(globalThis as any).window ??= globalThis;
// @ts-expect-error: browser scripts with no types; they set globalThis.World and globalThis.Compiler.
await import('../app/lib/world-mock.js');
// @ts-expect-error: as above.
await import('../app/lib/compiler.js');
const C = (globalThis as any).Compiler;
const World = (globalThis as any).World;
const w = World('mid');

describe('numbers', () => {
  test('every run is answered by code or the LLM', () => {
    for (const p of C.paths(w, C.seed())) {
      expect(p.hit + p.fallback).toBeCloseTo(1, 9);
      expect(p.served + p.llm).toBe(p.runs);
    }
  });

  test('a path that answers is never slower, dearer or chattier than before', () => {
    for (const p of C.paths(w, C.seed()).filter((x: any) => x.status === 'live' || x.status === 'drifted')) {
      expect(p.msAfter).toBeLessThanOrEqual(p.ms);
      expect(p.costAfter).toBeLessThan(p.costBefore);
      expect(p.callsAfter).toBeLessThan(p.calls);
      expect(p.saved).toBeGreaterThan(0);
    }
  });

  test('paths that answer nothing save nothing', () => {
    for (const p of C.paths(w, C.seed()).filter((x: any) => !['live', 'drifted'].includes(x.status))) {
      expect(p.hit).toBe(0);
      expect(p.saved).toBe(0);
    }
  });

  test('totals grow with the size of the client', () => {
    const t = (k: string) => C.totals(C.paths(World(k), C.seed()));
    const [s, m, l] = [t('small'), t('mid'), t('large')];
    for (const f of ['runs', 'served', 'callsSaved', 'saved']) {
      expect(s[f]).toBeLessThan(m[f]);
      expect(m[f]).toBeLessThan(l[f]);
    }
  });

  test('drift sends the runs that touch it back to the LLM', () => {
    const st = C.seed();
    const before = C.path(w, st, 'ticket-triage');
    expect(before.status).toBe('drifted');
    const fixed = C.promote(C.recompile(st, 'ticket-triage'), 'ticket-triage');
    expect(C.path(w, fixed, 'ticket-triage').hit).toBeGreaterThan(before.hit);
  });

  test('a watched path projects a saving it doesn’t have yet', () => {
    const st = C.seed();
    const s = C.suggest(w, st);
    expect(s.map((x: any) => x.id)).toEqual(['invoice-resend', 'ledger-booking', 'quote-discount']);
    for (const x of s) expect(x.saved).toBeGreaterThan(0);
    // Compiling changes the state, not the run count.
    const after = C.path(w, C.compile(st, 'invoice-resend'), 'invoice-resend');
    expect(after.runs).toBe(s[0].runs);
  });
});

describe('checking on past runs', () => {
  test('the same version always gets the same result', () => {
    const p = C.def('cancellation');
    const v = p.versions.at(-1);
    expect(C.replay(p, v)).toEqual(C.replay(p, v));
  });

  test('agree, disagree and off the path add up to every case', () => {
    for (const p of C.PATHS.filter((x: any) => x.versions.length)) {
      for (const v of p.versions) {
        const r = C.replay(p, v);
        expect(r.agree + r.disagree + r.out).toBe(v.cases);
        expect(r.tiles.length).toBe(60);
        expect(r.rate).toBeCloseTo(v.agree, 2);
      }
    }
  });
});

describe('versions', () => {
  test('watching → candidate → shadow → live, a step at a time', () => {
    let st = C.seed();
    const id = 'invoice-resend';
    expect(C.status(st.paths[id])).toBe('watching');
    expect(C.promote(st, id)).toBeNull();
    expect(C.shadow(st, id)).toBeNull();
    st = C.compile(st, id);
    expect(C.status(st.paths[id])).toBe('candidate');
    expect(C.promote(st, id)).toBeNull();
    st = C.shadow(st, id);
    expect(C.status(st.paths[id])).toBe('shadow');
    st = C.promote(st, id);
    expect(C.status(st.paths[id])).toBe('live');
    expect(st.paths[id].versions[0].by).toBe('You');
  });

  test('changes return a new state and leave the old one alone', () => {
    const st = C.seed();
    const next = C.promote(st, 'lead-routing');
    expect(next).not.toBe(st);
    expect(C.status(st.paths['lead-routing'])).toBe('shadow');
    expect(C.status(next.paths['lead-routing'])).toBe('live');
  });

  test('promoting retires the live version', () => {
    const st = C.promote(C.seed(), 'refund-eligibility');
    const vs = st.paths['refund-eligibility'].versions;
    expect(vs.find((v: any) => v.status === 'live').v).toBe(3);
    expect(vs.find((v: any) => v.v === 2).status).toBe('retired');
    // v3 handled the policy change, so it's closed.
    expect(st.paths['refund-eligibility'].drift[0].open).toBe(false);
  });

  test('rolling back returns to the version before and reopens what the newer one fixed', () => {
    const fixed = C.promote(C.recompile(C.seed(), 'ticket-triage'), 'ticket-triage');
    const back = C.rollback(fixed, 'ticket-triage');
    const ps = back.paths['ticket-triage'];
    expect(ps.versions.find((v: any) => v.status === 'live').v).toBe(4);
    expect(ps.versions.find((v: any) => v.v === 5).status).toBe('rolled-back');
    expect(C.status(ps)).toBe('drifted');
    expect(C.can(back, 'ticket-triage')).toContain('recompile');
  });

  test('a recompile starts in shadow and can’t be repeated until it’s decided', () => {
    const st = C.recompile(C.seed(), 'ticket-triage');
    const ps = st.paths['ticket-triage'];
    expect(ps.versions.at(-1)).toMatchObject({ v: 5, status: 'shadow' });
    expect(C.status(ps)).toBe('live');
    expect(C.recompile(st, 'ticket-triage')).toBeNull();
  });

  test('a paused path answers nothing until it’s resumed', () => {
    const st = C.seed();
    expect(C.path(w, st, 'plan-upgrade').hit).toBe(0);
    expect(C.can(st, 'plan-upgrade')).toEqual(['resume']);
    const on = C.resume(st, 'plan-upgrade');
    expect(C.path(w, on, 'plan-upgrade').hit).toBeGreaterThan(0);
    expect(C.path(w, C.pause(on, 'plan-upgrade'), 'plan-upgrade').hit).toBe(0);
  });

  test('a path with nowhere to roll back to can’t roll back', () => {
    const st = C.promote(C.shadow(C.compile(C.seed(), 'ledger-booking'), 'ledger-booking'), 'ledger-booking');
    expect(C.can(st, 'ledger-booking')).not.toContain('rollback');
    expect(C.rollback(st, 'ledger-booking')).toBeNull();
  });
});
