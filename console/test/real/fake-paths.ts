// Compiled paths in the fake Go API: the routes in internal/api/automation.go
// with the shapes and rules of internal/compiled/service.go (status, the `can`
// verbs, revisions, promotion waiting on labelled disagreements). Jobs finish
// at once, so the console sees a compile land on its next read.

type Json = Record<string, unknown>;
interface Version { v: number; status: string; program: Json; program_hash: string; note: string; promoted_by: string | null; created_at: string; replay_complete: boolean }
interface Case { id: string; trace_id: string; kind: string; result: 'agree' | 'disagree' | 'out'; right: string | null; reviewed_by: string | null }
interface Drift { id: string; kind: string; binding_id: string; detail: string; open: boolean; fixed_by: number | null; created_at: string }
interface Path {
  id: string; name: string; actor_id: string; operation: string; signature: string; revision: number;
  paused: boolean; paused_by: string | null; pause_reason: string | null; created_at: string;
  versions: Version[]; drift: Drift[]; cases: Record<number, Case[]>; traces: number;
  stats: Json; week: number[]; fallbacks: { reason: string; count: number }[];
}

const iso = (ms: number) => new Date(ms).toISOString();
const DAY = 864e5;

const program = (op: string): Json => ({
  interpreter: 'path-v1', input_schema: { type: 'object' }, output_schema: { type: 'object' },
  nodes: [
    { id: 'order', kind: 'read', binding_id: 'shop.orders', binding_version: 2, value: { object: { id: { ref: '/input/order_id' } } }, next: 'paid' },
    { id: 'paid', kind: 'guard', value: { op: 'eq', args: [{ ref: '/vars/order/status' }, { literal: 'paid' }] }, otherwise: 'off' },
    { id: 'days', kind: 'let', value: { op: 'days_since', args: [{ ref: '/vars/order/paid_at' }] } },
    { id: 'recent', kind: 'guard', value: { op: 'lte', args: [{ ref: '/vars/days' }, { literal: 14 }] }, otherwise: 'off' },
    { id: 'done', kind: 'call', binding_id: op, binding_version: 1, value: { object: { order_id: { ref: '/input/order_id' }, amount: { ref: '/vars/order/total' } } } },
    { id: 'out', kind: 'return', value: { object: { refunded: { literal: true }, id: { ref: '/vars/done/id' } } } },
    { id: 'off', kind: 'fallback', reason: 'OUTSIDE_POLICY', value: {} },
  ],
});

const cases = (v: number, agree: number, dis: number, out: number, labelled = 0): Case[] => [
  ...Array.from({ length: agree }, (_, i) => ({ id: `c_${v}_a${i}`, trace_id: `tr_${v}_a${i}`, kind: 'replay', result: 'agree' as const, right: null, reviewed_by: null })),
  ...Array.from({ length: dis }, (_, i) => ({ id: `c_${v}_d${i}`, trace_id: `tr_${v}_d${i}`, kind: i % 2 ? 'shadow' : 'replay', result: 'disagree' as const, right: i < labelled ? 'code' : null, reviewed_by: i < labelled ? 'dana@example.com' : null })),
  ...Array.from({ length: out }, (_, i) => ({ id: `c_${v}_o${i}`, trace_id: `tr_${v}_o${i}`, kind: 'replay', result: 'out' as const, right: null, reviewed_by: null })),
];

function seed(actor: string, now: number): Path[] {
  const ver = (v: number, status: string, days: number, note: string, by: string | null = null): Version => ({ v, status, program: program('shop.refund'), program_hash: `sha256:${v}f00d${v}`.padEnd(40, 'a'), note, promoted_by: by, created_at: iso(now - days * DAY), replay_complete: true });
  const base = { actor_id: actor, revision: 3, paused: false, paused_by: null, pause_reason: null, created_at: iso(now - 20 * DAY), drift: [] as Drift[], traces: 0 };
  return [
    { ...base, id: 'path_refund', name: 'Refund a recent order', operation: 'shop.refund', signature: 'sig_refund',
      versions: [ver(1, 'retired', 14, 'First compile, from 412 traces', 'dana@example.com'), ver(2, 'live', 5, 'Hands disputed orders to the LLM', 'dana@example.com')],
      cases: { 1: cases(1, 38, 0, 4), 2: cases(2, 41, 0, 3) }, traces: 520,
      stats: { runs: 1840, served: 1702, llm: 0, fallback: 138, reported_runs: 0, hit: 0.925, duration_ms: 640, cost_micros_usd: 510000, ms_before: 6400, cost_before_micros_usd: 52440000, estimated_savings_micros_usd: 51930000, calls_saved: 8510, time_saved_ms: 9805000, comparison_is_estimated: true },
      week: [1210, 1390, 1502, 1477, 1610, 1702, 1702], fallbacks: [{ reason: 'OUTSIDE_POLICY', count: 101 }, { reason: 'OUTPUT_SCHEMA', count: 37 }] },
    { ...base, id: 'path_address', name: 'Change a shipping address', operation: 'shop.update_address', signature: 'sig_address', revision: 2, created_at: iso(now - 4 * DAY),
      versions: [{ ...ver(1, 'shadow', 2, 'First compile, from 96 traces'), program: program('shop.update_address') }],
      cases: { 1: cases(1, 30, 3, 5, 1) }, traces: 140,
      stats: { runs: 380, served: 0, llm: 380, fallback: 0, reported_runs: 380, hit: 0, duration_ms: null, cost_micros_usd: null, ms_before: 5100, cost_before_micros_usd: null, estimated_savings_micros_usd: null, calls_saved: null, time_saved_ms: null, comparison_is_estimated: true },
      week: [0, 0, 0, 0, 0, 0, 0], fallbacks: [] },
    { ...base, id: 'path_invoice', name: 'Resend an invoice', operation: 'billing.resend_invoice', signature: 'sig_invoice', revision: 1, created_at: iso(now - DAY),
      versions: [], cases: {}, traces: 64, stats: {}, week: [], fallbacks: [] },
  ];
}

function state(p: Path): { status: string; can: string[] } {
  const live = p.versions.some((v) => v.status === 'live');
  const pending = p.versions.find((v) => v.status === 'candidate' || v.status === 'shadow')?.status ?? '';
  const retired = p.versions.some((v) => v.status === 'retired');
  const drift = p.drift.some((d) => d.open && d.fixed_by == null);
  let status = pending || 'watching';
  if (live) status = drift ? 'drifted' : 'live';
  if (p.paused) status = 'paused';
  const can: string[] = [];
  if (!live && !pending) can.push('compile');
  if (pending === 'candidate') can.push('shadow');
  if (live) {
    if (p.paused) can.push('resume');
    else { can.push('pause'); if (retired) can.push('rollback'); }
  }
  if (pending === 'shadow' && !p.paused) can.push('promote');
  if (drift && !pending) can.push('recompile');
  return { status, can };
}

export function fakePaths(actor: string, principal: string) {
  const now = Date.now();
  const paths = seed(actor, now);
  const jobs = new Map<string, Json>();
  const config = { enabled: true, cloud_processing_consent: true, server_configured: true };
  const json = (b: unknown, status = 200) => Response.json(b, { status });
  const fail = (status: number, code: string, message: string) => Response.json({ error: { code, message } }, { status });
  const compiled = (p: Path) => p.versions.length > 0;
  const view = (p: Path, full: boolean) => {
    const { cases: _c, stats, week, fallbacks: _f, traces, versions, drift, ...rest } = p;
    const s = state(p);
    const out: Json = { ...rest, status: s.status, stats, week: week.map((n, i) => ({ date: iso(now - (6 - i) * DAY).slice(0, 10), served: n })) };
    if (full) Object.assign(out, { versions, drift, compiling: false, can: config.enabled ? s.can : s.can.filter((v) => v !== 'compile' && v !== 'recompile') });
    else if (!compiled(p)) Object.assign(out, { status: 'watching', projected: true, estimated_savings_micros_usd: null, compile_agreement: null, traces });
    return out;
  };
  const job = (kind: string, subject: string, done: () => void) => {
    const id = `job_${jobs.size + 1}`;
    done();
    const j = { id, kind, subject_id: subject, status: 'COMPLETED', error: null, by: principal, attempts: 1, created_at: iso(Date.now()), updated_at: iso(Date.now()) };
    jobs.set(id, j);
    return json({ id, status: 'PENDING', kind, subject_id: subject }, 202);
  };

  return (method: string, p: string, body: Json): Response | null => {
    if (p === '/v1/config/compiler') {
      if (method === 'GET') return json(config);
      if (method === 'PUT') {
        if (body.enabled && (!body.cloud_processing_consent || !config.server_configured)) return fail(400, 'INVALID_REQUEST', 'server generation configuration and cloud processing consent required');
        config.enabled = !!body.enabled;
        config.cloud_processing_consent = !!body.cloud_processing_consent;
        return json({ enabled: config.enabled, cloud_processing_consent: config.cloud_processing_consent });
      }
    }
    const jm = /^\/v1\/jobs\/([^/]+)$/.exec(p);
    if (jm && method === 'GET') return jobs.has(jm[1]!) ? json(jobs.get(jm[1]!)) : fail(404, 'NOT_FOUND', 'Entity was not found');
    if (!p.startsWith('/v1/compiled-paths')) return null;
    if (method === 'GET' && p === '/v1/compiled-paths') return json({ items: paths.filter(compiled).map((x) => view(x, false)), next_cursor: null });
    if (method === 'GET' && p === '/v1/compiled-paths/suggestions') return json({ items: paths.filter((x) => !x.versions.some((v) => ['candidate', 'shadow', 'live'].includes(v.status))).map((x) => view(x, false)), next_cursor: null });
    if (method === 'GET' && p === '/v1/compiled-paths/totals') {
      const sum = (k: string) => paths.reduce((t, x) => t + (Number(x.stats[k]) || 0), 0);
      return json({ runs: sum('runs'), served: sum('served'), llm: sum('llm'), fallback: sum('fallback'), duration_ms: 640, ms_before: 6100, estimated_savings_micros_usd: sum('estimated_savings_micros_usd'), calls_saved: sum('calls_saved'), time_saved_ms: sum('time_saved_ms'), comparison_is_estimated: true });
    }
    if (method === 'POST' && p === '/v1/compiled-paths/discover') {
      if (!config.enabled) return fail(503, 'COMPILER_DISABLED', 'configure a generation model and enable tenant compiler consent');
      return job('discover', '', () => {});
    }
    const m = /^\/v1\/compiled-paths\/([^/]+)(?:\/(.+))?$/.exec(p);
    const x = m && paths.find((y) => y.id === decodeURIComponent(m[1]!));
    if (!m || !x) return fail(404, 'NOT_FOUND', 'compiled path not found');
    const rest = m[2] ?? '';
    if (method === 'GET' && rest === '') return json(view(x, true));
    if (method === 'GET' && rest === 'fallbacks') return json({ items: x.fallbacks, from: iso(now - DAY), to: iso(now) });
    const rm = /^versions\/(\d+)\/replay$/.exec(rest);
    if (method === 'GET' && rm) {
      const v = x.versions.find((y) => y.v === Number(rm[1]));
      if (!v) return fail(404, 'NOT_FOUND', 'Entity was not found');
      const cs = x.cases[v.v] ?? [];
      const n = (r: string) => cs.filter((c) => c.result === r).length;
      const answerable = n('agree') + n('disagree');
      return json({ version: v.v, complete: v.replay_complete, n: cs.length, agree: n('agree'), disagree: n('disagree'), out: n('out'), rate: answerable ? n('agree') / answerable : null, samples: cs });
    }
    const lm = /^disagreements\/([^/]+)\/label$/.exec(rest);
    if (method === 'PUT' && lm) {
      if (body.right !== 'llm' && body.right !== 'code') return fail(400, 'INVALID_REQUEST', 'right must be llm or code');
      const c = Object.values(x.cases).flat().find((y) => y.id === decodeURIComponent(lm[1]!) && y.result === 'disagree');
      if (!c) return fail(404, 'NOT_FOUND', 'Entity was not found');
      Object.assign(c, { right: body.right, reviewed_by: principal });
      return json({ reviewed: true });
    }
    if (method !== 'POST' || !['compile', 'recompile', 'shadow', 'promote', 'rollback', 'pause', 'resume'].includes(rest)) return fail(404, 'NOT_FOUND', 'Entity was not found');
    if (body.revision !== x.revision) return fail(409, 'CONFLICT', 'path revision mismatch');
    const { can } = state(x);
    if (!can.includes(rest) && !(rest === 'compile' && can.includes('compile'))) return fail(409, 'CONFLICT', 'invalid lifecycle transition');
    const next = Math.max(0, ...x.versions.map((v) => v.v)) + 1;
    const add = (status: string) => {
      x.versions.push({ v: next, status, program: program(x.operation), program_hash: `sha256:${next}c0de`.padEnd(40, 'b'), note: status === 'shadow' ? 'Recompiled after a change' : `First compile, from ${x.traces} traces`, promoted_by: null, created_at: iso(Date.now()), replay_complete: true });
      x.cases[next] = cases(next, 24, 2, 3);
    };
    if (rest === 'compile' || rest === 'recompile') {
      if (!config.enabled) return fail(503, 'COMPILER_DISABLED', 'configure a generation model and enable tenant compiler consent');
      x.revision++;
      return job(rest, x.id, () => add(rest === 'compile' ? 'candidate' : 'shadow'));
    }
    const at = (s: string) => x.versions.find((v) => v.status === s);
    if (rest === 'shadow') at('candidate')!.status = 'shadow';
    if (rest === 'promote') {
      const s = at('shadow')!;
      if ((x.cases[s.v] ?? []).some((c) => c.result === 'disagree' && !c.right)) return fail(409, 'CONFLICT', 'shadow evidence and reviewed disagreements required');
      const live = at('live');
      if (live) live.status = 'retired';
      Object.assign(s, { status: 'live', promoted_by: principal });
      for (const d of x.drift) if (d.open) Object.assign(d, { open: false, fixed_by: s.v });
    }
    if (rest === 'rollback') {
      at('live')!.status = 'rolled-back';
      x.versions.filter((v) => v.status === 'retired').pop()!.status = 'live';
    }
    if (rest === 'pause') Object.assign(x, { paused: true, paused_by: principal, pause_reason: String(body.why ?? '') });
    if (rest === 'resume') Object.assign(x, { paused: false, paused_by: null, pause_reason: null });
    x.revision++;
    return json(view(x, true));
  };
}
