// Outcome contracts: how the demo reads a rule (app/lib/contracts.js). DOM-free.

import { describe, expect, test } from 'bun:test';

type Rule = { state: string; type: string | null; ranked: { type: string; p: number }[]; slots: Record<string, any> };
// @ts-expect-error: a browser script with no types; it sets globalThis.Contracts.
await import('../app/lib/contracts.js');
const C = (globalThis as any).Contracts;

describe('reading a rule', () => {
  test('two sentences become two rules, each read surely', () => {
    const [a, b] = C.classify('No more than one refund is permitted per order. Every successful cancellation must leave the subscription inactive in Stripe.') as Rule[];
    expect(a.state).toBe('sure');
    expect(a.type).toBe('once-per-key');
    expect(a.slots).toMatchObject({ action: 'refund', key: 'order', app: 'stripe' });
    expect(b.type).toBe('end-state');
    expect(b.slots).toMatchObject({ action: 'cancellation', object: 'subscription', state: 'inactive', app: 'stripe' });
  });

  test('each kind from its example', () => {
    for (const t of C.TYPES) {
      const [r] = C.classify(t.example) as Rule[];
      expect(r.type).toBe(t.id);
    }
    expect((C.classify('Refund amount ≤ eligible balance') as Rule[])[0].type).toBe('amount-cap');
  });

  test('probabilities are a ranking that sums to at most one', () => {
    const [r] = C.classify('Ask a person before any refund over $500') as Rule[];
    const sum = r.ranked.reduce((s, x) => s + x.p, 0);
    expect(sum).toBeLessThanOrEqual(1);
    expect(r.ranked[0].p).toBeGreaterThanOrEqual(r.ranked[1].p);
    expect(r.slots.amount).toBe(500);
  });

  test('two close readings ask; none at all asks you to pick', () => {
    expect((C.classify('don’t refund twice and check stripe') as Rule[])[0].state).toBe('ask');
    expect((C.classify('be careful with money') as Rule[])[0].state).toBe('pick');
    expect((C.classify('hello') as Rule[])[0].state).toBe('pick');
  });

  test('picking a kind by hand fills its parts', () => {
    const [r] = C.classify('be careful with money') as Rule[];
    const p = C.choose(r, 'approval-over');
    expect(p.type).toBe('approval-over');
    expect(p.slots.amount).toBeGreaterThan(0);
  });
});

describe('after a contract exists', () => {
  const rules = () => C.classify('Ask a person before any refund over $100');
  test('follow-ups change a part, the mode, or ask for a test', () => {
    expect(C.followUp('make it $500', rules())).toEqual({ kind: 'slot', slot: 'amount', value: 500 });
    expect(C.followUp('protect it', rules())).toEqual({ kind: 'mode', value: 'protect' });
    expect(C.followUp('write the regression test in pytest', rules())).toEqual({ kind: 'test', value: 'py' });
    expect(C.followUp('No more than 3 emails per customer a day.', rules()).kind).toBe('new');
  });

  test('needs: the app it reads, and the key agents send', () => {
    const rs = C.classify('No more than one refund per order');
    const ns = C.needs(rs, new Set(['stripe'])).map((n: any) => [n.kind, n.name, n.ok]);
    expect(ns).toEqual([['app', 'Stripe', true], ['field', 'order_id', true]]);
    expect(C.needs(C.classify('No more than one refund per order in Shopify'), new Set(['stripe']))[0].ok).toBe(false);
  });

  test('a test file names the contract and the miss', () => {
    const rs = C.classify('No more than one refund per order');
    const c = { text: 'No more than one refund per order', title: C.title(rs), rules: rs };
    const ex = { action: 'act_1', agent: 'Refund agent', key: 'ord_8812', day: 'Thu', at: '11:38 AM' };
    const ts = C.testFor(c, 'ts', ex);
    expect(ts).toContain("describe('Refund outcome contract'");
    expect(ts).toContain("order_id: 'ord_8812'");
    expect(C.testFor(c, 'py', ex)).toContain('def test_refuses_a_second_refund_for_order_ord_8812');
  });
});
