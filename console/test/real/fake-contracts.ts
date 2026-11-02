// Outcome contracts in the fake Go API: the /v1/contracts routes in
// internal/api/automation.go and contract_reads.go, with the rules of
// internal/contracts/service.go (ValidateRules, readiness, revisions, mode
// transitions) and the deterministic reader of classify.go. Bodies are
// decoded strictly, as the API does, so a field the API doesn't know is a 400.
// The week, runs and preview are seeded or counted from the fake's actions;
// savings are never measured, so they stay null.

type Json = Record<string, any>;
interface Binding { operation: string; [k: string]: unknown }
interface Rule { id: string; type: string; slots: Json; binding: Binding }
interface Contract {
  id: string; title: string; text: string; rules: Rule[]; revision: number; mode: string; was: string;
  created_at: string; updated_at: string; deleted: boolean;
  week?: { seen: number; verified: number; would_stop: number; stopped: number; days: number[] };
  runs?: Json[];
}
interface ToolBinding { id: string; version: number; app: string; operation: string; method: string; read_only: boolean; enabled: boolean; objects: Record<string, string[]>; granted: boolean }
type Verifier = { id: string; enabled: boolean };
type Action = { id: string; at: number; actor: string; op: string; status: string; meta?: Json; outcome?: { status: string } };

const iso = (ms: number) => new Date(ms).toISOString();
const DAY = 864e5;

export const KINDS = ['once-per-key', 'approval-over', 'end-state', 'amount-cap', 'order-before', 'rate-limit', 'human-wins'];
const BINDING_FIELDS = ['operation', 'actor_id', 'key_pointer', 'amount_pointer', 'currency_pointer', 'currency', 'read_binding_id', 'read_arguments', 'value_pointer', 'last_editor_pointer', 'last_edited_at_pointer', 'verifier_id', 'expected', 'within_seconds', 'max_age_seconds', 'predecessor_operation'];

class Refusal extends Error {
  constructor(public status: number, public code: string, message: string) { super(message); }
}
const invalid = (m: string) => new Refusal(400, 'INVALID_REQUEST', m);
const conflict = (m: string) => new Refusal(409, 'CONFLICT', m);
const missing = (m: string) => new Refusal(404, 'NOT_FOUND', m);

/** json.Decoder.DisallowUnknownFields, for the shapes the contract routes take. */
function strict(body: Json, allowed: string[], where = 'body') {
  for (const k of Object.keys(body ?? {})) if (!allowed.includes(k)) throw new Refusal(400, 'MALFORMED_JSON', `json: unknown field "${k}" in ${where}`);
}
function strictRules(rules: unknown) {
  if (rules == null) return;
  if (!Array.isArray(rules)) throw new Refusal(400, 'MALFORMED_JSON', 'rules must be an array');
  for (const r of rules) {
    strict(r, ['id', 'type', 'slots', 'binding'], 'rule');
    strict(r.binding ?? {}, BINDING_FIELDS, 'binding');
  }
}

const int = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) && v === Math.trunc(v) && Math.abs(v) <= 9007199254740991 ? v : null);

/** ValidateRules, as internal/contracts/service.go has it. */
export function validateRules(rules: Rule[], active: boolean) {
  if (!Array.isArray(rules) || rules.length < 1 || rules.length > 50) throw invalid('1-50 rules required');
  const seen = new Set<string>();
  rules.forEach((r, i) => {
    if (!r.id) r.id = `r${i + 1}`;
    if (r.id.length > 200 || seen.has(r.id)) throw invalid('unique bounded rule ids required');
    seen.add(r.id);
    if (!KINDS.includes(r.type)) throw invalid('unknown contract type');
    r.slots ??= {};
    r.binding ??= { operation: '' };
    if (!active) return;
    const b = r.binding as Json;
    const s = (k: string) => (typeof b[k] === 'string' ? (b[k] as string) : '');
    if (!s('operation') || s('operation') === '*') throw invalid('explicit operation required');
    if ((b.max_age_seconds ?? 0) < 0 || (b.max_age_seconds ?? 0) > 3600 || (b.within_seconds ?? 0) < 0 || (b.within_seconds ?? 0) > 86400) throw invalid('invalid freshness/deadline');
    for (const p of ['key_pointer', 'amount_pointer', 'currency_pointer', 'value_pointer', 'last_editor_pointer', 'last_edited_at_pointer']) if (s(p) && !s(p).startsWith('/')) throw invalid('bindings must use JSON pointers');
    if (['once-per-key', 'amount-cap', 'order-before', 'rate-limit', 'human-wins'].includes(r.type) && !s('key_pointer')) throw invalid('key_pointer required');
    if (['approval-over', 'amount-cap'].includes(r.type) && (!s('amount_pointer') || !s('currency'))) throw invalid('amount_pointer and currency required');
    if (r.type === 'approval-over') { const n = int(r.slots.amount_minor); if (n == null || n < 0) throw invalid('nonnegative amount_minor required'); }
    if (r.type === 'amount-cap' && (!s('read_binding_id') || !s('value_pointer'))) throw invalid('amount cap requires authoritative read and value pointer');
    if (r.type === 'rate-limit') { const n = int(r.slots.n); if (n == null || n < 1 || !['minute', 'hour', 'day', 'week'].includes(String(r.slots.window))) throw invalid('positive n and minute/hour/day/week required'); }
    if (r.type === 'order-before' && !s('predecessor_operation')) throw invalid('predecessor_operation required');
    if (r.type === 'human-wins') { const n = int(r.slots.minutes); if (n == null || n < 1 || !s('read_binding_id') || !s('last_editor_pointer') || !s('last_edited_at_pointer')) throw invalid('human-wins needs minutes and authoritative editor/timestamp pointers'); }
    if (r.type === 'end-state' && !s('verifier_id')) throw invalid('end-state requires a verifier');
    if (s('verifier_id')) { const e = b.expected; if (!e || typeof e !== 'object' || Array.isArray(e) || !Object.keys(e).length) throw invalid('nonempty expected verification template required'); }
  });
}

// ─── the reader in classify.go ──────────────────────

const CUES: Record<string, RegExp[]> = {
  'once-per-key': [/\b(no more than|at most|only) (one|1)\b/i, /\b(twice|duplicate|second|once)\b/i],
  'approval-over': [/\b(ask|approv\w*|person|human|manager)\b/i, /(over|above|greater than)\s*\$?\d/i],
  'end-state': [/\b(must leave|must be|verify|check|inactive|canceled|closed|paid)\b/i],
  'amount-cap': [/(eligible balance|amount paid|order total|daily budget|remaining balance)/i, /(not exceed|never exceed|within|≤|<=)/i],
  'order-before': [/\b(before|only after|until)\b/i, /\b(lands?|landed|settled|finished)\b/i],
  'rate-limit': [/\b(per|each|a|every) (minute|hour|day|week)\b/i, /(at most|no more than) [2-9]/i],
  'human-wins': [/\b(overwrite|edited|edit|person edited|human edited)\b/i, /\blast \d+ minutes?\b/i],
};

export function classify(text: string) {
  const out: Json[] = [];
  for (let sentence of text.split(/[.!?]\s+|[;\n]+/)) {
    sentence = sentence.replace(/[.!?]+$/, '').trim();
    if (!sentence) continue;
    const r: Json = { text: sentence, ranked: [], state: 'pick', type: null, slots: {}, guessed: [], cues: [], source: 'deterministic' };
    const weights: Record<string, number> = {};
    let sum = 1;
    for (const kind of KINDS) {
      let score = 0;
      for (const cue of CUES[kind]!) {
        const m = cue.exec(sentence);
        if (m) { score += 3; r.cues.push({ start: m.index, end: m.index + m[0].length, type: kind }); }
      }
      weights[kind] = Math.exp(score);
      sum += weights[kind]!;
    }
    r.ranked = KINDS.map((k) => ({ type: k, p: weights[k]! / sum })).sort((a, b) => b.p - a.p);
    const [a, b] = r.ranked;
    if (a.p > 0.6 && a.p - b.p > 0.3) { r.state = 'sure'; r.type = a.type; } else if (a.p > 0.2) r.state = 'ask';
    const low = sentence.toLowerCase();
    let [action, app, key] = ['refund', 'stripe', 'order'];
    for (const x of [['cancel', 'cancellation', 'stripe', 'subscription'], ['email', 'email', 'gmail', 'customer'], ['ticket', 'ticket close', 'zendesk', 'ticket'], ['deploy', 'deploy', 'vercel', 'project'], ['record', 'record update', 'salesforce', 'record']]) {
      if (low.includes(x[0]!)) { [action, app, key] = [x[1]!, x[2]!, x[3]!]; break; }
    }
    if (low.includes('refund')) [action, app, key] = ['refund', 'stripe', 'order'];
    Object.assign(r.slots, { action, app, key });
    let explicit = false;
    for (const name of ['stripe', 'shopify', 'salesforce', 'zendesk', 'gmail', 'quickbooks', 'vercel', 'hubspot']) if (low.includes(name)) { r.slots.app = name; explicit = true; }
    if (!explicit) r.guessed.push('app');
    const km = /(?:per|same|each) (order|customer|subscription|ticket|record|project|recipient|invoice)/i.exec(sentence);
    if (km) r.slots.key = km[1]!.toLowerCase(); else r.guessed.push('key');
    const money = /\$\s*([0-9,]+(?:\.[0-9]{1,2})?)/.exec(sentence);
    if (money) { const n = Number(money[1]!.replace(/,/g, '')); r.slots.amount = n; r.slots.amount_minor = Math.round(n * 100); } else r.guessed.push('amount_minor');
    for (const st of ['inactive', 'canceled', 'closed', 'paid', 'active', 'succeeded']) if (low.includes(st)) { r.slots.state = st; break; }
    for (const ob of ['subscription', 'refund', 'invoice', 'order', 'ticket', 'record']) if (low.includes(ob)) { r.slots.object = ob; break; }
    let m = /(?:no more than|at most) (\d+)/i.exec(sentence);
    if (m) r.slots.n = Number(m[1]);
    for (const w of ['minute', 'hour', 'day', 'week']) if (low.includes(` ${w}`)) { r.slots.window = w; break; }
    m = /(\d+) minutes?/i.exec(sentence);
    if (m) r.slots.minutes = Number(m[1]);
    for (const l of ['eligible balance', 'amount paid', 'order total', 'daily budget', 'remaining balance']) if (low.includes(l)) r.slots.limit = l;
    r.guessed.push('execution_binding');
    out.push(r);
  }
  return out;
}

// ─── the routes ─────────────────────────────────────

export function fakeContracts(opts: { principal: string; verifiers: () => Verifier[]; actions: () => Action[] }) {
  const now = Date.now();
  const bindings: ToolBinding[] = [
    { id: 'stripe.balance', version: 2, app: 'stripe', operation: 'stripe.read_balance', method: 'GET', read_only: true, enabled: true, objects: { charges: ['amount', 'amount_refunded', 'eligible_balance'] }, granted: true },
    { id: 'salesforce.record', version: 1, app: 'salesforce', operation: 'salesforce.read_record', method: 'GET', read_only: true, enabled: true, objects: { records: ['last_editor', 'last_edited_at'] }, granted: false },
    { id: 'stripe.refund', version: 3, app: 'stripe', operation: 'stripe.refund', method: 'POST', read_only: false, enabled: true, objects: {}, granted: false },
  ];
  const days = (xs: number[]) => xs;
  const contracts: Contract[] = [
    {
      id: 'ctr_refund_once', title: 'Refund outcome contract', text: 'No more than one refund is permitted per order.', revision: 4, mode: 'watch', was: 'watch',
      created_at: iso(now - 9 * DAY), updated_at: iso(now - 2 * DAY), deleted: false,
      rules: [{ id: 'r1', type: 'once-per-key', slots: { sentence: 'No more than one refund is permitted per order', action: 'refund', key: 'order', app: 'stripe' }, binding: { operation: 'stripe.refund', key_pointer: '/order_id' } }],
      week: { seen: 1284, verified: 1279, would_stop: 31, stopped: 0, days: days([170, 192, 181, 205, 176, 168, 192]) },
      runs: [
        { action_id: 'act_9k2f1', agent: 'Refund agent', operation: 'stripe.refund', at: iso(now - 3_600_000), decision: 'DENY', outcome: null, receipt_id: 'dec_7a1', rules: [{ rule_id: 'r1', revision: 4, decision: 'DENY', reason: 'CONTRACT_KEY_ALREADY_USED', mode: 'watch', verification_status: null }] },
        { action_id: 'act_9k2f0', agent: 'Refund agent', operation: 'stripe.refund', at: iso(now - 5_400_000), decision: 'ALLOW', outcome: 'VERIFIED', receipt_id: 'dec_7a0', rules: [{ rule_id: 'r1', revision: 4, decision: 'ALLOW', reason: '', mode: 'watch', verification_status: 'VERIFIED' }] },
      ],
    },
    {
      id: 'ctr_big_refunds', title: 'Refund outcome contract', text: 'Ask a person before any refund over $500.', revision: 1, mode: 'draft', was: '',
      created_at: iso(now - DAY), updated_at: iso(now - DAY), deleted: false,
      rules: [{ id: 'r1', type: 'approval-over', slots: { sentence: 'Ask a person before any refund over $500', action: 'refund', app: 'stripe', amount: 500, amount_minor: 50000 }, binding: { operation: '' } }],
    },
  ];
  let made = 0;

  const json = (b: unknown, status = 200) => Response.json(b, { status });
  const fail = (e: unknown) => (e instanceof Refusal ? Response.json({ error: { code: e.code, message: e.message, request_id: 'req_fake' } }, { status: e.status }) : (() => { throw e; })());
  const effective = (c: Contract) => (c.mode === 'protect' ? 'ENFORCE' : 'SHADOW');
  const view = (c: Contract) => ({ id: c.id, title: c.title, text: c.text, rules: c.rules, revision: c.revision, mode: c.mode, was: c.was, effective_mode: effective(c), created_at: c.created_at, updated_at: c.updated_at });
  const find = (id: string) => { const c = contracts.find((x) => x.id === id && !x.deleted); if (!c) throw missing('contract not found'); return c; };

  /** Service.ready: valid bindings, read bindings granted, verifiers enabled. */
  function ready(rules: Rule[]) {
    const out: string[] = [];
    const apps: Json[] = [];
    try { validateRules(structuredClone(rules), true); } catch (e) { out.push((e as Error).message); }
    for (const r of rules) {
      const b = r.binding as Json;
      if (b.read_binding_id) {
        const t = bindings.find((x) => x.id === b.read_binding_id);
        const ok = !!t && t.enabled && t.read_only && t.granted;
        apps.push({ app: t?.app ?? '', binding_id: b.read_binding_id, objects: t?.objects ?? null, configured: !!t, granted: ok, provenance: 'operator_configured' });
        if (!ok) out.push(`read binding/grant: ${b.read_binding_id}`);
      }
      if (b.verifier_id && !opts.verifiers().some((v) => v.id === b.verifier_id && v.enabled)) out.push(`verifier: ${b.verifier_id}`);
    }
    return { ready: out.length === 0, missing: out, apps };
  }

  function save(id: string, body: Json, create: boolean) {
    strict(body, ['title', 'text', 'rules', 'revision']);
    strictRules(body.rules);
    const old = create ? null : find(id);
    const title = String(body.title || old?.title || '');
    const text = String(body.text || old?.text || '');
    const rules = structuredClone((body.rules ?? old?.rules) as Rule[]);
    if (!create && !(Number(body.revision) >= 1)) throw invalid('revision required for edit');
    for (const r of rules ?? []) {
      const b = (r.binding ??= { operation: '' }) as Json;
      b.operation = String(b.operation ?? '').trim().toLowerCase();
      if (b.predecessor_operation != null) b.predecessor_operation = String(b.predecessor_operation).trim().toLowerCase();
      if (b.currency != null) b.currency = String(b.currency).trim().toUpperCase();
    }
    if (!title.trim() || title.length > 300 || !text.trim() || text.length > 32000) throw invalid('bounded title and text required');
    validateRules(rules, false);
    if (old) {
      if (old.revision !== body.revision) throw conflict('contract revision mismatch');
      if (['watch', 'protect'].includes(old.mode) && !ready(rules).ready) throw conflict('edited active contract is not ready');
      Object.assign(old, { title, text, rules, revision: old.revision + 1, updated_at: iso(Date.now()) });
      return old;
    }
    const c: Contract = { id: `ctr_new${++made}`, title, text, rules, revision: 1, mode: 'draft', was: '', created_at: iso(Date.now()), updated_at: iso(Date.now()), deleted: false };
    contracts.push(c);
    return c;
  }

  function mode(c: Contract, body: Json) {
    strict(body, ['mode', 'revision']);
    const m = String(body.mode ?? '');
    if (!['draft', 'watch', 'protect', 'paused'].includes(m)) throw invalid('invalid mode');
    if (c.revision !== body.revision) throw conflict('contract revision mismatch');
    if (m === c.mode || (m === 'paused' && c.mode === 'draft')) throw conflict('invalid mode transition');
    if (m === 'watch' || m === 'protect') {
      if (!ready(c.rules).ready) throw conflict('contract needs explicit bindings and granted reads');
      c.was = m;
    } else if (['watch', 'protect'].includes(c.mode)) c.was = c.mode;
    c.mode = m;
    c.revision++;
    c.updated_at = iso(Date.now());
    return c;
  }

  /** The preview: the fake's recorded actions on the rules' operations, never today's state for old facts. */
  function preview(body: Json) {
    strict(body, ['rules', 'from', 'to', 'tz', 'action_id', 'fixture']);
    strictRules(body.rules);
    validateRules(body.rules as Rule[], true);
    const ops = new Set((body.rules as Rule[]).map((r) => r.binding.operation));
    const to = body.to ? Date.parse(body.to) : Date.now();
    const from = body.from ? Date.parse(body.from) : to - 7 * DAY;
    const xs = opts.actions().filter((a) => ops.has(a.op) && a.at >= from && a.at < to);
    const once = (body.rules as Rule[]).some((r) => r.type === 'once-per-key');
    const keys = new Map<string, number>();
    let would = 0;
    const examples: Json[] = [];
    const daysOut: Record<string, number> = {};
    for (const a of xs) {
      const key = String(a.meta?.customer ?? a.id);
      const n = (keys.get(key) ?? 0) + 1;
      keys.set(key, n);
      if (once && n > 1) {
        would++;
        const d = iso(a.at).slice(0, 10);
        daysOut[d] = (daysOut[d] ?? 0) + 1;
        if (examples.length < 10) examples.push({ action_id: a.id, at: iso(a.at), operation: a.op });
      }
    }
    const verified = xs.filter((a) => a.outcome?.status === 'VERIFIED').length;
    const reads = (body.rules as Rule[]).some((r) => (r.binding as Json).read_binding_id || r.type === 'order-before');
    const unavailable = reads ? xs.length : 0;
    return { from: iso(from), to: iso(to), tz: body.tz ?? 'UTC', seen: xs.length, would_stop: would, verified, gaps: xs.length - verified, saved_micros_usd: null, days: daysOut, examples, results: [], unavailable_evidence: unavailable, truncated: unavailable > 0 };
  }

  return (method: string, p: string, body: Json): Response | null => {
    const path = p.split('?')[0]!;
    const q = new URLSearchParams(p.split('?')[1] ?? '');
    try {
      if (path === '/v1/tool-bindings' && method === 'GET') return json({ items: bindings.map(({ granted: _g, ...b }) => b), next_cursor: null });
      if (!path.startsWith('/v1/contracts')) return null;
      if (path === '/v1/contracts' && method === 'GET') {
        const want = q.get('mode');
        const s = (q.get('q') ?? '').toLowerCase();
        return json({ items: contracts.filter((c) => !c.deleted && (!want || c.mode === want) && (!s || `${c.title} ${c.text}`.toLowerCase().includes(s))).map(view), next_cursor: null });
      }
      if (path === '/v1/contracts' && method === 'POST') return json(view(save('', body, true)), 201);
      if (path === '/v1/contracts/classify' && method === 'POST') {
        strict(body, ['text']);
        const t = String(body.text ?? '');
        if (!t.trim() || t.length > 32000) throw invalid('text must contain 1-32000 bytes');
        return json(classify(t));
      }
      if (path === '/v1/contracts/preview' && method === 'POST') return json(preview(body));
      const m = /^\/v1\/contracts\/([^/]+)(?:\/(.+))?$/.exec(path);
      if (!m) throw missing('Entity was not found');
      const id = decodeURIComponent(m[1]!);
      const rest = m[2] ?? '';
      if (rest === '' && method === 'GET') return json(view(find(id)));
      if (rest === '' && method === 'PATCH') return json(view(save(id, body, false)));
      if (rest === '' && method === 'DELETE') { const c = find(id); c.deleted = true; c.mode = 'draft'; return new Response(null, { status: 204 }); }
      if (rest === 'mode' && method === 'PUT') return json(view(mode(find(id), body)));
      const c = find(id);
      if (rest === 'needs' && method === 'GET') return json(ready(c.rules));
      if (rest === 'hook-status' && method === 'GET') {
        const since = q.get('since') ? Date.parse(q.get('since')!) : Date.parse(c.created_at);
        return json({ since: iso(since), items: c.rules.map((r) => {
          const xs = opts.actions().filter((a) => a.op === r.binding.operation && a.at >= since);
          const results = xs.filter((a) => a.outcome);
          return { operation: r.binding.operation, actor_id: (r.binding as Json).actor_id ?? null, checks_observed: xs.length, results_observed: results.length, last_check_at: xs.length ? iso(Math.max(...xs.map((a) => a.at))) : null, last_result_at: results.length ? iso(Math.max(...results.map((a) => a.at))) : null, status: xs.length ? 'observed' : 'not_observed' };
        }) });
      }
      if (rest === 'week' && method === 'GET') {
        const w = c.week ?? { seen: 0, verified: 0, would_stop: 0, stopped: 0, days: [0, 0, 0, 0, 0, 0, 0] };
        const start = Date.now() - 6 * DAY;
        return json({ from: iso(start), to: iso(Date.now() + DAY), tz: q.get('tz') ?? 'UTC', seen: w.seen, verified: w.verified, gaps: w.seen - w.verified, would_stop: w.would_stop, stopped: w.stopped, saved_micros_usd: null, days: w.days.map((n, i) => ({ date: iso(start + i * DAY).slice(0, 10), seen: n })) });
      }
      if (rest === 'runs' && method === 'GET') return json({ items: (c.runs ?? []).slice(0, Number(q.get('limit') ?? 200)), next_cursor: null });
      if (rest === 'tests' && method === 'GET') {
        const lang = q.get('lang');
        if (lang !== 'ts' && lang !== 'py') throw invalid('lang must be ts or py');
        const r = (c.runs ?? []).find((x) => x.action_id === q.get('action'));
        if (!r) throw missing('Entity was not found');
        const fixture = { rules: [c.rules[0]], fixture: { arguments: { order_id: 'ord_1' }, evaluated_at: r.at } };
        const code = lang === 'ts'
          ? `import test from 'node:test';\nimport assert from 'node:assert/strict';\nconst fixture = ${JSON.stringify(fixture)};\ntest('contract regression', async () => {\n const response = await fetch(process.env.LUMNI_URL + '/v1/contracts/preview', {method:'POST', headers:{Authorization:'Bearer '+process.env.LUMNI_API_KEY,'Content-Type':'application/json'},body:JSON.stringify(fixture)});\n assert.equal(response.status,200);\n assert.equal((await response.json()).results[0].decision,${JSON.stringify(r.decision)});\n});\n`
          : `import json, os, unittest, urllib.request\nfixture = ${JSON.stringify(fixture)}\nclass ContractRegression(unittest.TestCase):\n def test_recorded_case(self):\n  pass\n`;
        return json({ action_id: r.action_id, language: lang, filename: `contract-regression.${lang}`, fixture, code, expected_decision: r.decision });
      }
      throw missing('Entity was not found');
    } catch (e) {
      return fail(e);
    }
  };
}
