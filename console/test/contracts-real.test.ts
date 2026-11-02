// Outcome contracts on real data (app/lib/contracts-real.js) against the fake
// Go API's contract routes (test/real/fake-contracts.ts). DOM-free.

import { describe, expect, test } from 'bun:test';
import { classify, fakeContracts, KINDS, validateRules } from './real/fake-contracts';

(globalThis as any).window ??= globalThis;
// @ts-expect-error: browser scripts with no types; they set globalThis.Contracts and ContractsReal.
await import('../app/lib/contracts.js');
// @ts-expect-error: as above.
await import('../app/lib/contracts-real.js');
const C = (globalThis as any).Contracts;
const R = (globalThis as any).ContractsReal;

const DAY = 864e5;
const verifiers = [{ id: 'v_stripe', name: 'Stripe events', enabled: true, config: { app: 'stripe' } }, { id: 'v_gmail', name: 'Gmail', enabled: false, config: { app: 'gmail' } }];
const ctx = { verifiers, bindings: [{ id: 'stripe.balance', app: 'stripe', read_only: true, enabled: true }, { id: 'stripe.refund', app: 'stripe', read_only: false, enabled: true }] };

/** Shell.api, answered by the fake: JSON both ways, and errors with a status. */
function harness(actions: any[] = []) {
  const fake = fakeContracts({ principal: 'dana@example.com', verifiers: () => verifiers, actions: () => actions });
  const seen: { method: string; path: string; body?: any }[] = [];
  const call = async (method: string, path: string, body?: unknown) => {
    const sent = body === undefined ? undefined : JSON.parse(JSON.stringify(body));
    seen.push({ method, path, body: sent });
    if (path.startsWith('/v1/verifiers')) return { items: verifiers };
    const res = fake(method, path, sent ?? {});
    if (!res) throw new Error(`no route for ${method} ${path}`);
    const text = await res.text();
    const data = text ? JSON.parse(text) : undefined;
    if (!res.ok) throw Object.assign(new Error(data.error.message), { status: res.status, code: data.error.code });
    return data;
  };
  const api = { get: (p: string) => call('GET', p), post: (p: string, b = {}) => call('POST', p, b), put: (p: string, b = {}) => call('PUT', p, b), patch: (p: string, b = {}) => call('PATCH', p, b), del: (p: string) => call('DELETE', p) };
  return { api: R.client(api), raw: api, seen };
}

/** A console rule of one kind, read from its example the way the page reads it. */
const ruleOf = (type: string) => {
  const t = C.typeOf(type);
  const [r] = R.fromReadings(classify(t.example));
  return { ...C.choose(r, type), id: 'r1', binding: {} };
};

describe('bindings', () => {
  test('an unbound rule says what it needs, and the API agrees it isn’t ready', () => {
    for (const type of KINDS) {
      const r = ruleOf(type);
      expect(R.missing(r).length).toBeGreaterThan(0);
      expect(R.missing(r)[0].field).toBe('operation');
      expect(() => validateRules([R.apiRule(r)], true)).toThrow();
      // A draft takes it as it is.
      expect(() => validateRules([R.apiRule(r)], false)).not.toThrow();
    }
  });

  test('Lumni’s guesses bind every kind well enough for the API to run it', () => {
    for (const type of KINDS) {
      const r = ruleOf(type);
      const bound = { ...r, binding: R.fillGuesses(r, ctx) };
      expect(R.missing(bound)).toEqual([]);
      expect(() => validateRules([R.apiRule(bound)], true)).not.toThrow();
    }
  });

  test('missing() and the API refuse the same things', () => {
    const r = { ...ruleOf('once-per-key'), binding: { operation: 'stripe.refund', key_pointer: 'order_id' } };
    expect(R.missing(r).map((m: any) => m.field)).toEqual(['key_pointer']);
    expect(() => validateRules([R.apiRule(r)], true)).toThrow('bindings must use JSON pointers');
    const e = { ...ruleOf('end-state'), binding: { operation: 'stripe.cancel_subscription', verifier_id: 'v_stripe' } };
    expect(R.missing(e).map((m: any) => m.field)).toEqual(['expected']);
    expect(() => validateRules([R.apiRule(e)], true)).toThrow('nonempty expected');
    const rate = { ...ruleOf('rate-limit'), binding: { operation: 'gmail.send', key_pointer: '/customer_id' } };
    rate.slots = { ...rate.slots, window: 'fortnight' };
    expect(R.missing(rate).map((m: any) => m.field)).toEqual(['slot:window']);
    expect(() => validateRules([R.apiRule(rate)], true)).toThrow('minute/hour/day/week');
  });

  test('fields follow the kind, in the editor’s order', () => {
    expect(R.fields('once-per-key')).toEqual(['operation', 'key_pointer']);
    expect(R.fields('approval-over')).toEqual(['operation', 'amount_pointer', 'currency']);
    expect(R.fields('amount-cap')).toEqual(['operation', 'key_pointer', 'amount_pointer', 'currency', 'read_binding_id', 'value_pointer']);
    expect(R.fields('human-wins')).toEqual(['operation', 'key_pointer', 'read_binding_id', 'last_editor_pointer', 'last_edited_at_pointer']);
    expect(R.fields('end-state')).toEqual(['operation', 'verifier_id', 'expected']);
    for (const t of KINDS) for (const f of R.fields(t)) expect(R.LABELS[f]).toBeTruthy();
  });

  test('guesses fill only what’s empty, and never a write binding or an off verifier', () => {
    const r = { ...ruleOf('amount-cap'), binding: { operation: 'stripe.partial_refund' } };
    const b = R.fillGuesses(r, ctx);
    expect(b.operation).toBe('stripe.partial_refund');
    expect(b.read_binding_id).toBe('stripe.balance');
    expect(b.value_pointer).toBe('/eligible_balance');
    const e = R.fillGuesses(ruleOf('end-state'), { ...ctx, verifiers: [verifiers[1]] });
    expect(e.verifier_id).toBeUndefined();
    expect(e.expected).toEqual({ status: 'inactive' });
  });

  test('a rule goes out with only the fields the API takes', () => {
    const r = { ...ruleOf('approval-over'), ranked: [{ type: 'x', p: 1 }], binding: { operation: ' Stripe.Refund ', amount_pointer: '/amount', currency: 'usd', note: 'dropped', expected: {}, within_seconds: '' } };
    const out = R.apiRule(r);
    expect(Object.keys(out).sort()).toEqual(['binding', 'id', 'slots', 'type']);
    expect(out.binding).toEqual({ operation: 'Stripe.Refund', amount_pointer: '/amount', currency: 'usd' });
    expect(out.slots.amount_minor).toBe(50_000);
    expect(out.slots.sentence).toBe(r.text);
  });

  test('a verifier’s deadline follows the words', () => {
    const r = { ...ruleOf('end-state'), binding: { operation: 'stripe.cancel_subscription', verifier_id: 'v_stripe', expected: { status: 'inactive' }, within_seconds: 300 } };
    r.slots = { ...r.slots, within: 15 };
    expect(R.apiRule(r).binding.within_seconds).toBe(900);
    expect(R.apiRule({ ...r, binding: { operation: 'x' } }).binding.within_seconds).toBeUndefined();
  });
});

describe('reading through the API', () => {
  test('the API’s readings become the page’s rules', async () => {
    const { api } = harness();
    const rs = R.fromReadings(await api.classify('No more than one refund is permitted per order. Every successful cancellation must leave the subscription inactive in Stripe.'));
    expect(rs.map((r: any) => r.type)).toEqual(['once-per-key', 'end-state']);
    expect(rs[0].slots).toMatchObject({ action: 'refund', key: 'order', app: 'stripe' });
    expect(rs[1].slots).toMatchObject({ action: 'cancellation', app: 'stripe', state: 'inactive', object: 'subscription' });
    expect(rs[0].guessed).not.toContain('execution_binding');
    expect(rs[0].source).toBe('deterministic');
    // What the API doesn't read, the page still has, so its sentences read whole.
    expect(rs[1].slots.within).toBe(5);
    expect(C.sentence(rs[1])).toContain('subscription inactive');
  });

  test('an unsure reading stays a question', () => {
    const [r] = R.fromReadings(classify('Refunds should be fine'));
    expect(r.type).toBeNull();
    expect(['ask', 'pick']).toContain(r.state);
    expect(r.ranked.length).toBe(7);
  });
});

describe('a contract through the API', () => {
  test('draft, bind, preview, watch, pause, resume, delete', async () => {
    const now = Date.now();
    const actions = [
      { id: 'a1', at: now - 2 * DAY, actor: 'ai', op: 'stripe.refund', status: 'SUCCEEDED', meta: { customer: 'Ana' }, outcome: { status: 'VERIFIED' } },
      { id: 'a2', at: now - DAY, actor: 'ai', op: 'stripe.refund', status: 'SUCCEEDED', meta: { customer: 'Ana' }, outcome: { status: 'UNKNOWN' } },
      { id: 'a3', at: now - 9 * DAY, actor: 'ai', op: 'stripe.refund', status: 'SUCCEEDED', meta: { customer: 'Bo' } },
    ];
    const { api, seen } = harness(actions);
    const rules = R.fromReadings(classify('Never refund the same order twice'));
    rules[0].type ??= 'once-per-key';
    let c = await api.create({ title: C.title(rules), text: 'Never refund the same order twice', rules: rules.map((r: any) => ({ ...r, binding: {} })) });
    expect(c.mode).toBe('draft');
    expect(c.revision).toBe(1);
    expect(c.rules[0].text).toBe('Never refund the same order twice');
    expect(seen.at(-1)!.body.rules[0].slots.sentence).toBe('Never refund the same order twice');

    // Not bound: the API won't run it, and says why.
    let n = await api.needs(c);
    expect(n.ready).toBe(false);
    expect(n.general).toEqual(['explicit operation required']);
    await expect(api.mode(c, 'watch')).rejects.toMatchObject({ status: 409 });

    // Bind it and save at its revision; a stale revision is refused.
    c.rules[0].binding = R.fillGuesses(c.rules[0], ctx);
    await expect(api.save({ ...c, revision: 9 })).rejects.toMatchObject({ status: 409 });
    c = await api.save(c);
    expect(c.revision).toBe(2);
    expect(c.rules[0].binding).toEqual({ operation: 'stripe.refund', key_pointer: '/order_id' });
    n = await api.needs(c);
    expect(n.ready).toBe(true);

    // Preview: the last 7 days of recorded actions on its operation.
    const p = await api.preview(c);
    expect(p.seen).toBe(2);
    expect(p.wouldStop).toBe(1);
    expect(p.verified).toBe(1);
    expect(p.gaps).toBe(1);
    expect(p.saved).toBeNull();
    expect(p.examples[0].action_id).toBe('a2');

    // A fresh contract has observed nothing: zeros, and no savings.
    const hooks = await api.hooks(c);
    expect(hooks[0]).toMatchObject({ operation: 'stripe.refund', status: 'not_observed' });

    c = await api.mode(c, 'watch');
    expect(c.mode).toBe('watch');
    const wk = await api.week(c);
    expect(wk.seen).toBe(0);
    expect(wk.saved).toBeNull();
    expect(wk.days.length).toBe(7);

    c = await api.mode(c, 'paused');
    expect(c.mode).toBe('paused');
    expect(c.was).toBe('watch');
    c = await api.mode(c, c.was);
    expect(c.mode).toBe('watch');
    c = await api.mode(c, 'protect');
    expect(c.effective).toBe('ENFORCE');

    // Running, so an edit has to stay bound.
    await expect(api.save({ ...c, rules: [{ ...c.rules[0], binding: {} }] })).rejects.toMatchObject({ status: 409 });

    await api.remove(c);
    expect((await api.list()).some((x: any) => x.id === c.id)).toBe(false);
  });

  test('a draft can’t be paused, and unknown fields are refused', async () => {
    const { api, raw } = harness();
    const [draft] = (await api.list()).filter((x: any) => x.mode === 'draft');
    await expect(api.mode(draft, 'paused')).rejects.toMatchObject({ status: 409 });
    await expect(raw.post('/v1/contracts', { title: 'x', text: 'x', rules: [{ id: 'r1', type: 'once-per-key', slots: {}, binding: { operation: '' }, ranked: [] }] })).rejects.toMatchObject({ status: 400, code: 'MALFORMED_JSON' });
    await expect(raw.post('/v1/contracts', { title: 'x', text: 'x', rules: [{ id: 'r1', type: 'once-per-key', slots: {}, binding: { operation: '', note: 1 } }] })).rejects.toMatchObject({ status: 400 });
  });

  test('a seeded running contract reads back whole', async () => {
    const { api } = harness();
    const list = await api.list();
    const c = list.find((x: any) => x.id === 'ctr_refund_once');
    expect(c).toMatchObject({ mode: 'watch', revision: 4, title: 'Refund outcome contract' });
    expect(c.rules[0]).toMatchObject({ type: 'once-per-key', text: 'No more than one refund is permitted per order' });
    expect(c.forced[c.rules[0].text]).toBe('once-per-key');
    expect(c.over[c.rules[0].text]).toMatchObject({ action: 'refund', key: 'order' });
    const wk = await api.week(c);
    expect(wk).toMatchObject({ seen: 1284, verified: 1279, gaps: 5, wouldStop: 31, stopped: 0, saved: null });
    const runs = await api.runs(c);
    expect(runs[0]).toMatchObject({ id: 'act_9k2f1', broke: true, reason: 'CONTRACT_KEY_ALREADY_USED', mode: 'watch' });
    expect(runs[1]).toMatchObject({ broke: false, verified: true });
    const t = await api.tests(c, 'ts', runs[0].id);
    expect(t.code).toContain('/v1/contracts/preview');
    expect(t.expected_decision).toBe('DENY');
  });

  test('a read the operator hasn’t granted shows up against its binding', async () => {
    const { api } = harness();
    const r = { ...ruleOf('human-wins'), binding: { operation: 'salesforce.update_account', key_pointer: '/record_id', read_binding_id: 'salesforce.record', last_editor_pointer: '/last_editor', last_edited_at_pointer: '/last_edited_at' } };
    const c = await api.create({ title: 'People win', text: r.text, rules: [r] });
    const n = await api.needs(c);
    expect(n.ready).toBe(false);
    expect(n.general).toEqual([]);
    expect(n.reads.has('salesforce.record')).toBe(true);
    expect(n.apps[0]).toMatchObject({ binding_id: 'salesforce.record', configured: true, granted: false, provenance: 'operator_configured' });
    expect(R.says(n.reads.get('salesforce.record'))).toContain('needs a read grant');
  });
});

describe('numbers', () => {
  test('what the API doesn’t know stays a dash', () => {
    expect(R.week({ seen: 3, verified: null, saved_micros_usd: null, days: [] })).toMatchObject({ seen: 3, verified: null, saved: null, wouldStop: null });
    expect(R.week({ saved_micros_usd: 2_500_000 }).saved).toBe(2.5);
    expect(R.dash(null)).toBe('—');
    expect(R.dash(undefined)).toBe('—');
    expect(R.dash(1284)).toBe('1,284');
    expect(R.dash(0)).toBe('0');
    expect(R.preview({ seen: 0, days: { '2026-11-02': 2, '2026-11-01': 1 } })).toMatchObject({ seen: 0, wouldStop: null, saved: null, days: [1, 2], unavailable: 0 });
    expect(R.run({ action_id: 'a', rules: [] })).toMatchObject({ broke: false, verified: null, reason: null });
  });
});
