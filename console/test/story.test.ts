// The demo's story across screens (app/lib/story.js): every step lands on a
// real page, on data that's really there. DOM-free.

import { describe, expect, test } from 'bun:test';
import { PAGES } from '../server/app';

(globalThis as any).window ??= globalThis;
// @ts-expect-error: browser scripts with no types; they set globals.
await import('../app/lib/story.js');
// @ts-expect-error: as above.
await import('../app/lib/contracts.js');
// @ts-expect-error: as above.
await import('../app/lib/compiler.js');
// @ts-expect-error: as above.
await import('../app/lib/live.js');
const { Story, Contracts, Compiler, Live } = globalThis as any;

const url = (href: string) => new URL(href, 'http://x');

describe('the story', () => {
  test('five steps, each on a page the console serves, in demo data', () => {
    expect(Story.STEPS.map((s: any) => s.id)).toEqual(['intent', 'contract', 'execute', 'verify', 'optimize']);
    for (const s of Story.STEPS) {
      const u = url(s.href);
      expect(PAGES).toContain(u.pathname.slice(1));
      expect(u.searchParams.get('data')).toBe('mock');
      expect(u.searchParams.get('story')).toBe(s.id);
    }
  });

  test('the intent reads into the contract the order keeps, with nothing left to ask', () => {
    const rules = Contracts.classify(Story.INTENT);
    expect(rules.every((r: any) => r.state === 'sure')).toBe(true);
    expect(rules.map((r: any) => [r.type, r.slots.action])).toEqual([['once-per-key', 'charge'], ['once-per-key', 'refund'], ['end-state', 'cancellation']]);
    for (const r of rules.filter((x: any) => x.type === 'once-per-key')) expect(r.slots.key).toBe('order');
  });

  test('the order scene keeps that contract: one charge, one refund, cancelled', () => {
    const r = Live.result(Live.scenario({ scene: 'payment' }));
    expect(r).toMatchObject({ charges: 1, refunds: 1, order: 'cancelled', payment: 'refunded' });
  });

  test('step 5 opens a compiled path that exists, for the refund check the order runs', () => {
    const id = url(Story.step('optimize').href).searchParams.get('path');
    const p = Compiler.def(id);
    expect(p?.id).toBe(id);
    expect(p.app).toBe('stripe');
    expect(p.steps.some((s: any) => s.id === 'prior')).toBe(true);
  });

  test('the order’s result links back into the story at its contract and its compiled path', () => {
    const links = Live.scenario({ scene: 'payment' }).links.map((l: any) => url(l.href));
    expect(links.map((u: any) => u.searchParams.get('story'))).toEqual(['contract', 'optimize']);
    expect(links[1].searchParams.get('path')).toBe(url(Story.step('optimize').href).searchParams.get('path'));
  });

  test('the strip marks the steps before as done and the one you are on', () => {
    const h = Story.html('verify');
    expect(h.match(/<li class="done">/g)?.length).toBe(3);
    expect(h).toContain('<li class="on">');
    expect(h).toContain('Next: Optimize future runs');
    expect(Story.html('nope')).toBe('');
  });
});
