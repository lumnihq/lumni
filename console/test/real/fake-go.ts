// A stand-in for the Go API, answering the calls the console’s server makes
// with the shapes the real one sends: durable lists as raw rows, core lists
// that can say null, graph snapshots that stop at a node limit, and a session
// exchange that sets a cookie. Tests pass its fetch to createBff.
//
// It also answers the Console API (api/openapi.yaml, tag "Console API"): day
// analytics, paged lists with cursors, journeys, controls that change what
// later reads say, proofs with public links, notifications, audit history,
// leases, version history, app health, bursts, billing and anomalies. Every
// number is worked out from the world's records, the way the Go handlers in
// internal/api/console_*.go work them out from Postgres.

import { makeZone, type Zone } from '../../src/domain/format';

export interface FakeEval {
  stage: string;
  outcome: string;
  reason_code?: string;
  details?: Record<string, unknown>;
}

export interface FakeAction {
  id: string;
  at: number;
  actor: string;
  work: string;
  op: string;
  status: string;
  meta?: Record<string, unknown>;
  /** `existing` is the action a repeat was stopped against. */
  decision?: { d: string; r: string; at?: number; evals?: FakeEval[]; existing?: string };
  outcome?: { status: string; verifier?: string | null; attempts?: number; updated?: number };
  resources?: { id: string; mode: string; type?: string; name?: string; version?: string }[];
  cost?: number;
}

export interface FakeActor {
  id: string;
  type: string;
  display_name?: string;
  runtime?: string;
  /** Seeded actors have none: they read as made long ago. */
  created_at?: string;
  metadata?: Record<string, unknown>;
  paused?: boolean;
  paused_at?: string | null;
  paused_by?: string | null;
  until?: string | null;
  pause_reason?: string;
}

export interface FakeApproval {
  id: string;
  action_id: string;
  status: string;
  reason_code: string;
  requested_at: string;
  expires_at: string | null;
  resolved_at: string | null;
  resolved_by?: string | null;
  metadata?: Record<string, unknown>;
  /** The approval rule that asked, when known. */
  policy_id?: string;
}

export interface FakeRule {
  id: string;
  name: string;
  operation: string;
  resource_type: string;
  minimum_risk: string | null;
  argument_pointer: string | null;
  numeric_threshold: number | null;
  outcome: string;
  reason_code: string;
  enabled: boolean;
  version: number;
  created_at: string;
  deleted_at?: string | null;
}

export interface FakeIncident {
  id: string;
  kind: string;
  action_id: string | null;
  work_id: string | null;
  status: string;
  title: string;
  evidence: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface FakeScope {
  operation?: string | null;
  actor_id?: string | null;
  resource?: string | null;
}

export interface FakeLimit {
  id: string;
  scope: FakeScope;
  max_in_flight: number | null;
  per_minute: number | null;
  hold: boolean;
  mode: string;
  created_at: string;
}

export interface FakeGrant {
  actor_id: string;
  operation: string;
  active: boolean;
  since: string;
  reverted_at: string | null;
  skip_rules: string[];
  below: { pointer: string; value: number };
  revert_after: { slips: number; per: number };
}

export interface FakeProof {
  id: string;
  token: string;
  action_id: string;
  show: { what?: boolean; checks?: boolean; stopped_repeat?: boolean; actor?: boolean };
  expires_at: string;
  revoked_at: string | null;
  created_at: string;
}

export interface AuditEvent {
  id: string;
  at: string;
  by: string;
  type: string;
  target: Record<string, unknown>;
  before: Record<string, unknown> | null;
  after: Record<string, unknown> | null;
}

export interface VersionRow {
  version: string;
  at: string;
  actor_id: string;
  action_id: string;
  summary: string;
  value_hash: string;
}

export interface FakeLease {
  resource_id: string;
  action_id: string;
  actor_id: string;
  since: string;
  expires_at: string;
  fencing_token: number;
}

export interface TelemetryPoint {
  at: number;
  p50_ms: number;
  p95_ms: number;
  errors: number;
  timeouts: number;
}

/** A finished day before today, for /v1/analytics/daily. `hours` holds totals per local hour. */
export interface DayHistory {
  days_ago: number;
  hours: number[];
  kinds: Record<string, number>;
  kept_micros_usd: number;
  cost_micros_usd: number;
  verified: number;
}

export interface FakeWorld {
  tenant: string;
  key: string;
  /** The workspace name public proofs show. */
  workspace: string;
  /** Who the key's writes are recorded as, in audit history and approvals. */
  principal: string;
  actors: FakeActor[];
  agents: { id: string; actor_id: string; name: string }[];
  actions: FakeAction[];
  approvals: FakeApproval[];
  incidents: FakeIncident[];
  verifiers: { id: string; name: string; kind: string; version: string; config: Record<string, unknown>; enabled: boolean; created_at: string }[];
  rules: FakeRule[];
  enforcement: { operation: string; rule: string; mode: string }[];
  mode: string;
  /** The whole workspace's admissions are paused. */
  paused: boolean;
  limits: FakeLimit[];
  /** Daily budgets; scope_key '' is the workspace, otherwise an actor id. */
  spendLimits: { scope_key: string; daily_micros_usd: number; when_reached: string; tz?: string }[];
  autopilot: FakeGrant[];
  proofs: FakeProof[];
  notifications: { channels: Record<string, unknown>[]; routes: Record<string, unknown>[] };
  notificationJobs: { id: string; channel_id: string; kind: string; ref_id: string; at: string; payload?: Record<string, unknown> }[];
  /** Composio connections the workspace owns (Slack channels need one). */
  connections: { id: string; toolkit: string }[];
  audit: AuditEvent[];
  versions: Record<string, VersionRow[]>;
  leases: FakeLease[];
  leaseStats: Record<string, { handoffs_last_hour: number; refused_late_writes_today: number }>;
  /** Per-minute call timings per app. */
  telemetry: Record<string, TelemetryPoint[]>;
  billing: { plan: string; price_per_action_micros_usd: number } | null;
  history: DayHistory[];
  /** Snapshots hold this many actions before they say truncated. */
  perSnapshot: number;
  /** Every request, for tests that check what was sent. */
  seen: { method: string; path: string; headers: Record<string, string>; body?: string }[];
  /** Sessions the fake has handed out. */
  sessions: Set<string>;
  csrf: Map<string, string>;
  /** Pretend to be an API from before onboarding codes (D1). */
  noOnboarding?: boolean;
  /** The key that may create tenants (POST /v1/tenants), for open sign-up. */
  platformKey?: string;
  /** Plans set with PUT /v1/tenants/{id}/plan, and checks GET .../usage answers with. */
  plans?: Record<string, { plan: string; limits: Record<string, number>; watch_only: boolean }>;
  usage?: Record<string, number>;
  /** The change stream's log, as Go's graph_changes table. */
  changes?: { seq: number; type: string; entity: string; at: number }[];
}

/** Record a change for the stream, as Go's triggers do. */
function changed(w: FakeWorld, type: string, entity: string) {
  w.changes ??= [];
  w.changes.push({ seq: (w.changes.at(-1)?.seq ?? 100) + 1, type, entity, at: Date.now() });
}

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const iso = (t: number) => new Date(t).toISOString();

export function world(over: Partial<FakeWorld> = {}): FakeWorld {
  return {
    tenant: 't_123', key: 'test_admin_key', workspace: 'Acme', principal: 'Console admin key',
    actors: [], agents: [], actions: [], approvals: [], incidents: [], verifiers: [], rules: [], enforcement: [],
    mode: 'SHADOW', paused: false, limits: [], spendLimits: [], autopilot: [], proofs: [], notifications: { channels: [], routes: [] }, notificationJobs: [], connections: [],
    audit: [], versions: {}, leases: [], leaseStats: {}, telemetry: {}, billing: null, history: [],
    perSnapshot: 40, seen: [], sessions: new Set(), csrf: new Map(), ...over,
  };
}

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } });
const fail = (status: number, code: string, message = code) => json({ error: { code, message, request_id: 'req_fake' } }, status);
const none = () => new Response(null, { status: 204 });

const OPEN = new Set(['PROPOSED', 'AWAITING_APPROVAL', 'RESERVED', 'ACTIVE', 'UNKNOWN']);

function snapshot(w: FakeWorld, from: number, to: number) {
  const acts = w.actions.filter((a) => (a.at >= from && a.at <= to) || OPEN.has(a.status)).sort((a, b) => b.at - a.at);
  const nodes: Record<string, unknown>[] = [];
  const edges: Record<string, unknown>[] = [];
  const resources = new Map<string, Record<string, unknown>>();
  for (const a of acts.slice(0, w.perSnapshot)) {
    nodes.push({ id: `action:${a.id}`, entity_id: a.id, type: 'action', label: a.op, subtitle: a.id, status: a.status, risk: 'MEDIUM', cost_micros_usd: a.cost, occurred_at: iso(a.at), metadata: a.meta ?? {} });
    if (a.decision) {
      const rid = `rcpt_${a.id}`;
      nodes.push({ id: `decision:${rid}`, entity_id: rid, type: 'decision', label: a.decision.d, subtitle: a.decision.r, status: a.decision.d, occurred_at: iso(a.decision.at ?? a.at) });
      edges.push({ id: `action-decision:${a.id}:${rid}`, source: `action:${a.id}`, target: `decision:${rid}`, type: 'EVALUATED_AS' });
      let prev = `decision:${rid}`;
      (a.decision.evals ?? []).forEach((e, i) => {
        const nid = `evaluation:${rid}:0${i}`;
        // The real graph puts an evaluation's details in its node's metadata.
        nodes.push({ id: nid, entity_id: `${rid}:${i}`, type: 'evaluation', label: e.stage, subtitle: e.reason_code ?? '', status: e.outcome, ...(e.details ? { metadata: e.details } : {}) });
        edges.push({ id: `evaluation-edge:${rid}:0${i}`, source: prev, target: nid, type: 'NEXT_STAGE' });
        prev = nid;
      });
    }
    if (a.outcome) {
      nodes.push({ id: `outcome:out_${a.id}`, entity_id: `out_${a.id}`, type: 'outcome', label: `out_${a.id}`, status: a.outcome.status, occurred_at: iso(a.at) });
      edges.push({ id: `EXPECTED_OUTCOME:${a.id}`, source: `action:${a.id}`, target: `outcome:out_${a.id}`, type: 'EXPECTED_OUTCOME' });
    }
    for (const r of a.resources ?? []) {
      resources.set(r.id, { id: `resource:${r.id}`, entity_id: r.id, type: 'resource', label: r.type ?? 'thing', subtitle: `${r.id.split(':')[0]}:${r.id.split(':')[2]}${r.version ? ` @ ${r.version}` : ''}`, metadata: r.name ? { name: r.name } : {} });
      edges.push({ id: `action-resource:${a.id}:${r.id}:${r.mode}`, source: `action:${a.id}`, target: `resource:${r.id}`, type: r.mode });
    }
  }
  nodes.push(...resources.values());
  return { schema_version: '1', lens: 'transaction', nodes, edges, attention: [], truncated: acts.length > w.perSnapshot, cursor: 1, limit: 500 };
}

// ─── facts: one per action, the way console_action_facts holds them ─────────

const KIND: Record<string, string> = { DEDUPLICATE: 'stop', REJECT: 'stop', WAIT: 'wait', REPLAN: 'recheck', REQUIRE_APPROVAL: 'ask' };
const BAD = new Set(['MISMATCH', 'PARTIAL', 'FAILED']);
const MONEY = /(refund|charge|payment|payout|invoice|transfer|credit)/i;
const STEPS = ['stop', 'wait', 'recheck', 'ask'] as const;

export interface Fact {
  a: FakeAction;
  at: number;
  actor: string;
  op: string;
  app: string;
  /** stop, wait, recheck, ask or allow, from the decision alone. */
  kind: string;
  would: string;
  reason: string;
  outcome: string;
  cost: number;
  kept: number;
  resources: string[];
  clean: boolean;
}

export function factOf(a: FakeAction): Fact {
  const d = a.decision?.d ?? '';
  const kind = KIND[d] ?? 'allow';
  const would = d.startsWith('WOULD_') ? (KIND[d.slice(6)] ?? '') : '';
  const outcome = a.outcome?.status ?? 'UNKNOWN';
  const amount = a.meta?.amount ?? a.meta?.amount_usd ?? a.meta?.value ?? a.meta?.total;
  const kept = d === 'DEDUPLICATE' && MONEY.test(a.op) && /^[0-9]+(\.[0-9]+)?$/.test(String(amount ?? '')) ? Math.round(Number(amount) * 1e6) : 0;
  const app = String(a.meta?.app || a.op.split('.')[0]).trim().toLowerCase();
  return {
    a, at: a.at, actor: a.actor, op: a.op, app, kind, would, reason: a.decision?.r ?? 'NO_DECISION', outcome, cost: a.cost ?? 0, kept,
    resources: [...new Set((a.resources ?? []).map((r) => r.id))].sort(), clean: kind === 'allow' && !BAD.has(outcome),
  };
}

/** The kind the console counts: an allowed action whose outcome went wrong is bad. */
const effective = (f: Fact) => (f.kind === 'allow' && BAD.has(f.outcome) ? 'bad' : f.kind);

interface Counts {
  total: number;
  kinds: Record<string, number>;
  would: Record<string, number>;
  outcomes: Record<string, number>;
  cost_micros_usd: number;
  kept_micros_usd: number;
  verified: number;
}

const newCounts = (): Counts => ({
  total: 0, kinds: { allow: 0, stop: 0, wait: 0, recheck: 0, ask: 0, bad: 0, blind: 0 }, would: { stop: 0, wait: 0, recheck: 0, ask: 0 },
  outcomes: { VERIFIED: 0, MISMATCH: 0, PARTIAL: 0, FAILED: 0, PENDING: 0, UNKNOWN: 0 }, cost_micros_usd: 0, kept_micros_usd: 0, verified: 0,
});

function addTo(c: Counts, f: Fact) {
  c.total += 1;
  c.cost_micros_usd += f.cost;
  c.kept_micros_usd += f.kept;
  const k = effective(f);
  c.kinds[k] = (c.kinds[k] ?? 0) + 1;
  if (f.outcome === 'UNKNOWN') c.kinds.blind! += 1;
  if (f.would) c.would[f.would] = (c.would[f.would] ?? 0) + 1;
  c.outcomes[f.outcome] = (c.outcomes[f.outcome] ?? 0) + 1;
  if (f.outcome === 'VERIFIED') c.verified += 1;
}

// ─── time windows and zones ─────────────────────────────

function zoneNamed(name: string | null): Zone | null {
  const z = name || 'UTC';
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: z });
  } catch {
    return null;
  }
  return makeZone(z);
}
const hourIn = (z: Zone, at: number) => Math.floor(z.hourOf(at));
const dateIn = (z: Zone, at: number) => new Date(at + z.offset(at)).toISOString().slice(0, 10);

interface Window {
  from: number;
  to: number;
  z: Zone;
  tz: string;
}

/** Like timeRange in console_analytics.go: local midnight to now unless given, positive, at most `max`. */
function timeRange(q: URLSearchParams, max: number, now: number): Window | Response {
  const tz = q.get('tz') || 'UTC';
  const z = zoneNamed(tz);
  if (!z) return fail(400, 'INVALID_TIME_RANGE', 'tz must be an IANA zone');
  let from = z.dayStart(now);
  let to = now;
  for (const k of ['from', 'to'] as const) {
    const v = q.get(k);
    if (!v) continue;
    const t = Date.parse(v);
    if (Number.isNaN(t)) return fail(400, 'INVALID_TIME_RANGE', `${k} must be RFC 3339`);
    if (k === 'from') from = t;
    else to = t;
  }
  if (!(from < to) || to - from > max) return fail(400, 'INVALID_TIME_RANGE', 'range must be positive and within the maximum');
  return { from, to, z, tz };
}

const factsIn = (w: FakeWorld, from: number, to: number) => w.actions.filter((a) => a.at >= from && a.at < to).map(factOf);

// ─── paging ─────────────────────────────────────────────

const b64 = (s: string) => btoa(unescape(encodeURIComponent(s))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
const unb64 = (s: string) => decodeURIComponent(escape(atob(s.replace(/-/g, '+').replace(/_/g, '/'))));

function fingerprint(path: string, q: URLSearchParams) {
  const keep = [...q].filter(([k]) => k !== 'cursor' && k !== 'limit').sort(([a, x], [b, y]) => (a + x < b + y ? -1 : 1));
  return `${path}?${keep.map(([k, v]) => `${k}=${v}`).join('&')}`;
}

/**
 * One page of rows that are already filtered and sorted newest first. The
 * cursor is opaque base64 of the offset and the filters, so a cursor from
 * another query is refused the way the real one refuses it.
 */
function page<T>(rows: T[], path: string, q: URLSearchParams, time: ((x: T) => number) | null, defaultLimit = 200): Response | { items: T[]; next_cursor: string | null } {
  let limit = defaultLimit;
  const lv = q.get('limit');
  if (lv) {
    limit = Number(lv);
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) return fail(400, 'INVALID_QUERY', 'limit must be between 1 and 1000');
  }
  const from = q.get('from') ? Date.parse(q.get('from')!) : null;
  const to = q.get('to') ? Date.parse(q.get('to')!) : null;
  if (Number.isNaN(from) || Number.isNaN(to)) return fail(400, 'INVALID_QUERY', 'from and to must be RFC 3339');
  if (from !== null && to !== null && !(from < to)) return fail(400, 'INVALID_QUERY', 'from must precede to');
  const filter = fingerprint(path, q);
  let offset = 0;
  const cv = q.get('cursor');
  if (cv) {
    try {
      const c = JSON.parse(unb64(cv)) as { o: number; f: string };
      if (c.f !== filter || !Number.isInteger(c.o) || c.o < 0) throw new Error('foreign');
      offset = c.o;
    } catch {
      return fail(400, 'INVALID_QUERY', 'cursor does not belong to this query');
    }
  }
  const inWindow = time ? rows.filter((r) => (from === null || time(r) >= from) && (to === null || time(r) < to)) : rows;
  const items = inWindow.slice(offset, offset + limit);
  const next = offset + limit < inWindow.length ? b64(JSON.stringify({ o: offset + limit, f: filter })) : null;
  return { items, next_cursor: next };
}

const csv = (v: string | null) => (v ?? '').split(',').map((s) => s.trim()).filter(Boolean);
const byNewest = <T>(time: (x: T) => number, key: (x: T) => string) => (a: T, b: T) => time(b) - time(a) || (key(a) < key(b) ? 1 : key(a) > key(b) ? -1 : 0);

// ─── rows the API sends ─────────────────────────────────

function actionRow(a: FakeAction) {
  return { id: a.id, work_id: a.work, actor_id: a.actor, operation: a.op, tool: '', source: 'test', status: a.status, risk: 'MEDIUM', requested_at: iso(a.at), started_at: null, finished_at: null, metadata: a.meta ?? {} };
}

function decisionRow(a: FakeAction) {
  const d = a.decision!;
  return {
    schema_version: '1', action_id: a.id, receipt_id: `rcpt_${a.id}`, decision: d.d, reason_code: d.r, enforced: !d.d.startsWith('WOULD_'), confidence: 1,
    kernel_version: 'k1', resolver_version: 'r1', policy_version: 'p1', created_at: iso(d.at ?? a.at), evaluations: d.evals ?? [], evidence: [],
    ...(duplicateOf(a) ? { existing_action_id: duplicateOf(a) } : {}),
  };
}

function outcomeRow(a: FakeAction) {
  return {
    id: `out_${a.id}`, action_id: a.id, work_id: a.work, verifier_id: a.outcome!.verifier ?? null, expected: {}, status: a.outcome!.status, observed: {},
    attempts: a.outcome!.attempts ?? 1, reason: '', created_at: iso(a.at), updated_at: iso(a.outcome!.updated ?? a.at + 2000),
  };
}

/** The action a stopped repeat was stopped against: the one it names, or the last allowed one with the same operation and resources. */
function duplicateOf(a: FakeAction, w?: FakeWorld): string | null {
  const d = a.decision;
  if (!d || (d.d !== 'DEDUPLICATE' && d.d !== 'WOULD_DEDUPLICATE')) return null;
  if (d.existing) return d.existing;
  if (!w) return null;
  const ids = (a.resources ?? []).map((r) => r.id).sort().join('|');
  let best: FakeAction | null = null;
  for (const b of w.actions) {
    if (b.id === a.id || b.at > a.at || b.op !== a.op || b.actor !== a.actor) continue;
    if ((b.resources ?? []).map((r) => r.id).sort().join('|') !== ids) continue;
    if (KIND[b.decision?.d ?? ''] !== undefined) continue;
    if (!best || b.at > best.at) best = b;
  }
  return best?.id ?? null;
}

function approvalStatus(ap: FakeApproval, now: number) {
  return ap.status === 'PENDING' && ap.expires_at && Date.parse(ap.expires_at) <= now ? 'EXPIRED' : ap.status;
}

function approvalRule(w: FakeWorld, ap: FakeApproval) {
  if (ap.policy_id) return w.rules.find((r) => r.id === ap.policy_id) ?? null;
  const a = w.actions.find((x) => x.id === ap.action_id);
  return w.rules.find((r) => !r.deleted_at && r.outcome === 'REQUIRE_APPROVAL' && (r.operation === a?.op || r.operation === '*')) ?? null;
}

function approvalRow(w: FakeWorld, ap: FakeApproval, now: number, include: string[]) {
  const who = ap.resolved_by ? (w.actors.find((x) => x.id === ap.resolved_by)?.display_name || ap.resolved_by) : null;
  const out: Record<string, unknown> = { ...ap, resolved_by: who, status: approvalStatus(ap, now), metadata: ap.metadata ?? {} };
  delete out.policy_id;
  if (include.includes('action')) {
    const a = w.actions.find((x) => x.id === ap.action_id);
    out.action = a ? actionRow(a) : null;
  }
  if (include.includes('rule')) out.rule = approvalRule(w, ap);
  return out;
}

function actorRow(a: FakeActor, now: number) {
  const paused = !!a.paused && (!a.until || Date.parse(a.until) > now);
  return { ...a, paused, paused_at: paused ? (a.paused_at ?? null) : null, paused_by: paused ? (a.paused_by ?? null) : null, until: paused ? (a.until ?? null) : null };
}

// ─── audit ──────────────────────────────────────────────

const strip = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== null));

/** Appends an audit event, the way the Postgres triggers write one in the same transaction as the change. */
export function audit(w: FakeWorld, type: string, target: Record<string, unknown>, before: unknown, after: unknown, by = w.principal, at = Date.now()) {
  const clean = (v: unknown) => (v === null || v === undefined ? null : (JSON.parse(JSON.stringify(v)) as Record<string, unknown>));
  const e: AuditEvent = { id: `aud_${String(w.audit.length + 1).padStart(6, '0')}`, at: iso(at), by, type, target: strip(target), before: clean(before), after: clean(after) };
  if (e.after) delete e.after.token;
  if (e.before) delete e.before.token;
  w.audit.push(e);
  return e;
}

// ─── analytics ──────────────────────────────────────────

interface Breakdown extends Counts {
  actor_id?: string;
  app?: string;
  operation?: string;
  kind?: string;
  reason_code?: string;
  count?: number;
  hours: number[];
  last_at: string;
  streak: number;
  checked: boolean;
  sample_action_id?: string;
  top_resource?: string;
  _last: number;
}

const newBreakdown = (): Breakdown => ({ ...newCounts(), hours: Array(24).fill(0), last_at: iso(-62135596800000), streak: 0, checked: false, _last: -Infinity });

function topBreakdowns(m: Map<string, Breakdown>, n: number): Breakdown[] {
  const key = (b: Breakdown) => (b.actor_id ?? '') + (b.app ?? '') + (b.operation ?? '') + (b.reason_code ?? '');
  const out = [...m.values()].sort((a, b) => b.total - a.total || (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0));
  if (out.length > n) {
    const other = newBreakdown();
    Object.assign(other, { actor_id: 'other', app: 'other', operation: 'other', reason_code: 'other' });
    for (const v of out.slice(n)) {
      other.total += v.total;
      if (v.count) other.count = (other.count ?? 0) + v.count;
      other.cost_micros_usd += v.cost_micros_usd;
      other.kept_micros_usd += v.kept_micros_usd;
      other.verified += v.verified;
      for (const [k, x] of Object.entries(v.kinds)) other.kinds[k] = (other.kinds[k] ?? 0) + x;
      for (const [k, x] of Object.entries(v.would)) other.would[k] = (other.would[k] ?? 0) + x;
      for (const [k, x] of Object.entries(v.outcomes)) other.outcomes[k] = (other.outcomes[k] ?? 0) + x;
      if (v._last > other._last) {
        other._last = v._last;
        other.last_at = v.last_at;
      }
      v.hours.forEach((x, h) => (other.hours[h]! += x));
    }
    out.splice(n, out.length - n, other);
  }
  return out;
}

const publicBreakdown = (b: Breakdown) => {
  const { _last, ...rest } = b;
  void _last;
  return rest;
};

/** Clean actions since the actor's last step-in or bad outcome, as streaks.go counts them. */
function streakOf(facts: Fact[], actor: string) {
  const mine = facts.filter((f) => f.actor === actor).sort((a, b) => a.at - b.at || (a.a.id < b.a.id ? -1 : 1));
  let n = 0;
  for (const f of mine) n = f.clean ? n + 1 : 0;
  return n;
}

function analyticsDay(w: FakeWorld, q: URLSearchParams, now: number): Response {
  const win = timeRange(q, 7 * DAY, now);
  if (win instanceof Response) return win;
  let top = 10;
  if (q.get('top')) {
    top = Number(q.get('top'));
    if (!Number.isInteger(top) || top < 1 || top > 50) return fail(400, 'INVALID_QUERY', 'top must be 1–50');
  }
  let groups = csv(q.get('group_by'));
  if (!groups.length) groups = ['kind', 'hour', 'actor', 'app', 'reason'];
  for (const g of groups) if (!['kind', 'hour', 'actor', 'app', 'reason', 'resource', 'operation'].includes(g)) return fail(400, 'INVALID_GROUP_BY', 'Unsupported analytics dimension');
  const facts = factsIn(w, win.from, win.to);
  const total = newCounts();
  const hours = Array.from({ length: 24 }, newCounts);
  const actors = new Map<string, Breakdown>();
  const apps = new Map<string, Breakdown>();
  const reasons = new Map<string, Breakdown>();
  const ops = new Map<string, Breakdown>();
  const actorOps = new Map<string, Map<string, number>>();
  const actorApps = new Map<string, string>();
  const add = (m: Map<string, Breakdown>, key: string, f: Fact) => {
    let b = m.get(key);
    if (!b) m.set(key, (b = newBreakdown()));
    addTo(b, f);
    b.hours[hourIn(win.z, f.at)]! += 1;
    if (f.at > b._last) {
      b._last = f.at;
      b.last_at = iso(f.at);
    }
    return b;
  };
  for (const f of facts) {
    addTo(total, f);
    addTo(hours[hourIn(win.z, f.at)]!, f);
    const a = add(actors, f.actor, f);
    a.actor_id = f.actor;
    a.app = f.app;
    if (!actorOps.has(f.actor)) actorOps.set(f.actor, new Map());
    actorOps.get(f.actor)!.set(f.op, (actorOps.get(f.actor)!.get(f.op) ?? 0) + 1);
    actorApps.set(`${f.actor}|${f.op}`, f.app);
    add(ops, f.op, f).operation = f.op;
    add(apps, f.app, f).app = f.app;
    const k = effective(f);
    if (k !== 'allow') {
      const b = add(reasons, `${k}|${f.reason}|${f.op}|${f.actor}|${f.app}`, f);
      Object.assign(b, { kind: k, reason_code: f.reason, actor_id: f.actor, operation: f.op, app: f.app, count: b.total });
    }
  }
  for (const a of actors.values()) {
    let best = 0;
    for (const [op, n] of actorOps.get(a.actor_id!) ?? []) {
      if (n > best) {
        best = n;
        a.operation = op;
        a.app = actorApps.get(`${a.actor_id}|${op}`);
      }
    }
  }
  const out: Record<string, unknown> = {
    from: iso(win.from), to: iso(win.to), tz: win.tz, total: total.total, kinds: total.kinds, would: total.would, outcomes: total.outcomes,
    cost_micros_usd: total.cost_micros_usd, kept_micros_usd: total.kept_micros_usd, truncated: false, actor_count: actors.size, app_count: apps.size,
  };
  if (groups.includes('hour')) out.hours = hours.map((c, h) => ({ h, total: c.total, kinds: c.kinds }));
  if (groups.includes('actor')) {
    const xs = topBreakdowns(actors, top);
    for (const a of xs) if (a.actor_id !== 'other') a.streak = streakOf(facts, a.actor_id!);
    out.by_actor = xs.map(publicBreakdown);
  }
  if (groups.includes('app')) {
    const checked = new Set<string>();
    for (const v of w.verifiers) {
      if (!v.enabled) continue;
      checked.add(v.kind.toLowerCase());
      checked.add(String(v.config.app ?? ''));
    }
    const xs = topBreakdowns(apps, top);
    for (const x of xs) x.checked = checked.has(x.app!);
    out.by_app = xs.map(publicBreakdown);
  }
  if (groups.includes('reason')) {
    const xs = topBreakdowns(reasons, top);
    for (const b of xs) {
      if (b.reason_code === 'other') continue;
      const matching = facts.filter((f) => f.actor === b.actor_id && f.op === b.operation && f.reason === b.reason_code);
      const latest = [...matching].sort(byNewest((f) => f.at, (f) => f.a.id))[0];
      if (latest) b.sample_action_id = latest.a.id;
      const tally = new Map<string, number>();
      for (const f of matching) for (const r of f.resources) tally.set(r, (tally.get(r) ?? 0) + 1);
      const best = [...tally].sort((x, y) => y[1] - x[1] || (x[0] < y[0] ? -1 : 1))[0];
      b.top_resource = best?.[0] ?? '';
    }
    out.by_reason = xs.map(publicBreakdown);
  }
  if (groups.includes('operation')) out.by_operation = topBreakdowns(ops, top).map(publicBreakdown);
  if (groups.includes('resource')) {
    const { rows, count } = resourceAnalytics(w, facts, win.z, top);
    out.resource_count = count;
    out.by_resource = rows;
  }
  return json(out);
}

function resourceName(w: FakeWorld, id: string) {
  for (const a of w.actions) for (const r of a.resources ?? []) if (r.id === id && r.name) return r.name;
  return id.split(':')[2] ?? id;
}

function resourceAnalytics(w: FakeWorld, facts: Fact[], z: Zone, top: number) {
  const touches = new Map<string, Fact[]>();
  for (const f of facts) for (const r of f.resources) touches.set(r, [...(touches.get(r) ?? []), f]);
  const ranked = [...touches].sort((a, b) => b[1].length - a[1].length || (a[0] < b[0] ? -1 : 1));
  const groups = new Map<string, Fact[]>();
  ranked.forEach(([id, fs], i) => {
    const key = i < top ? id : 'other';
    groups.set(key, [...(groups.get(key) ?? []), ...fs]);
  });
  const rows = [...groups].map(([id, fs]) => {
    const byActor = new Map<string, number>();
    const stepins: Record<string, number> = { stop: 0, wait: 0, recheck: 0, ask: 0 };
    const hours: Record<string, number[]> = Object.fromEntries(STEPS.map((k) => [k, Array(24).fill(0)]));
    for (const f of fs) {
      byActor.set(f.actor, (byActor.get(f.actor) ?? 0) + 1);
      if (f.kind in stepins) {
        stepins[f.kind]! += 1;
        hours[f.kind]![hourIn(z, f.at)]! += 1;
      }
    }
    return {
      resource_id: id, name: id === 'other' ? 'other' : resourceName(w, id), touches: fs.length, actors: byActor.size, stepins, hours,
      by_actor: [...byActor].sort((a, b) => b[1] - a[1]).slice(0, 8).map(([actor_id, n]) => ({ actor_id, touches: n })),
    };
  });
  rows.sort((a, b) => Number(a.resource_id === 'other') - Number(b.resource_id === 'other') || b.touches - a.touches || (a.resource_id < b.resource_id ? -1 : 1));
  return { rows, count: touches.size };
}

function analyticsDaily(w: FakeWorld, q: URLSearchParams, now: number): Response {
  let days = 30;
  if (q.get('days')) {
    days = Number(q.get('days'));
    if (!Number.isInteger(days) || days < 1 || days > 90) return fail(400, 'INVALID_QUERY', 'days must be 1–90');
  }
  let hour = -1;
  if (q.get('at_hour')) {
    hour = Number(q.get('at_hour'));
    if (!Number.isInteger(hour) || hour < 0 || hour > 23) return fail(400, 'INVALID_QUERY', 'at_hour must be 0–23');
  }
  const win = timeRange(q, 90 * DAY, now);
  if (win instanceof Response) return win;
  const z = win.z;
  const today = z.dayStart(now);
  const out: Record<string, unknown>[] = [];
  for (let d = days - 1; d >= 0; d--) {
    const dayStart = z.dayStart(today - d * DAY + 12 * HOUR);
    const date = dateIn(z, dayStart + 12 * HOUR);
    if (d === 0) {
      const c = newCounts();
      let until = 0;
      for (const f of factsIn(w, today, now + 1)) {
        addTo(c, f);
        if (hour < 0 || hourIn(z, f.at) <= hour) until += 1;
      }
      out.push({ date, total: c.total, kinds: c.kinds, kept_micros_usd: c.kept_micros_usd, cost_micros_usd: c.cost_micros_usd, verified: c.verified, until_hour: { total: until } });
      continue;
    }
    const h = w.history.find((x) => x.days_ago === d);
    if (!h) {
      out.push({ date, total: 0, kinds: newCounts().kinds, kept_micros_usd: 0, cost_micros_usd: 0, verified: 0, until_hour: { total: 0 } });
      continue;
    }
    const total = h.hours.reduce((s, x) => s + x, 0);
    const until = hour < 0 ? total : h.hours.slice(0, hour + 1).reduce((s, x) => s + x, 0);
    out.push({ date, total, kinds: { ...newCounts().kinds, ...h.kinds }, kept_micros_usd: h.kept_micros_usd, cost_micros_usd: h.cost_micros_usd, verified: h.verified, until_hour: { total: until } });
  }
  return json({ days: out });
}

function percentile(xs: number[], p: number): number | null {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const i = p * (s.length - 1);
  const lo = Math.floor(i);
  const hi = Math.ceil(i);
  return s[lo]! + (s[hi]! - s[lo]!) * (i - lo);
}

function analyticsFreshness(w: FakeWorld, q: URLSearchParams, now: number): Response {
  const win = timeRange(q, 90 * DAY, now);
  if (win instanceof Response) return win;
  const by = new Map<string, { ages: number[]; stale: number }>();
  for (const a of w.actions) {
    const d = a.decision;
    const at = d?.at ?? a.at;
    if (!d || at < win.from || at >= win.to) continue;
    for (const e of d.evals ?? []) {
      if (e.stage !== 'resource_versions') continue;
      const rs = (e.details?.resources ?? []) as { observed_version?: string | null; current_version?: string | null; age_ms?: number }[];
      for (const r of rs) {
        if (r.observed_version == null) continue;
        const cur = by.get(a.actor) ?? { ages: [], stale: 0 };
        if (typeof r.age_ms === 'number') cur.ages.push(r.age_ms);
        else cur.ages.push(NaN);
        if (r.current_version != null && r.observed_version !== r.current_version) cur.stale += 1;
        by.set(a.actor, cur);
      }
    }
  }
  const items = [...by].map(([actor_id, v]) => {
    const ages = v.ages.filter((x) => !Number.isNaN(x));
    return { actor_id, reads: v.ages.length, median_age_ms: percentile(ages, 0.5), p90_age_ms: percentile(ages, 0.9), stale: v.stale };
  });
  items.sort((a, b) => b.reads - a.reads);
  return json({ by_actor: items });
}

/** Loops as the real query finds them: five or more of the same call on the same things within ten minutes. */
function loops(w: FakeWorld, from: number, to: number, now: number) {
  const parts = new Map<string, Fact[]>();
  for (const f of factsIn(w, from, to)) {
    const k = `${f.actor}|${f.op}|${f.resources.join(',')}|${JSON.stringify(f.a.meta ?? {})}`;
    parts.set(k, [...(parts.get(k) ?? []), f]);
  }
  const out: { anomaly: Record<string, unknown>; members: Fact[] }[] = [];
  for (const fs of parts.values()) {
    fs.sort((a, b) => a.at - b.at);
    let latest: { n: number; since: number; cost: number; at: number; members: Fact[] } | null = null;
    fs.forEach((f) => {
      const win = fs.filter((g) => g.at >= f.at - 10 * MIN && g.at <= f.at);
      if (win.length >= 5) latest = { n: win.length, since: win[0]!.at, cost: win.reduce((s, g) => s + g.cost, 0), at: f.at, members: win };
    });
    if (!latest) continue;
    const l = latest as { n: number; since: number; cost: number; at: number; members: Fact[] };
    const f0 = fs[0]!;
    out.push({
      anomaly: {
        kind: 'loop', actor_id: f0.actor, operation: f0.op, resource_id: f0.resources[0] ?? null, count: l.n, interval_ms: Math.floor((l.at - l.since) / Math.max(1, l.n - 1)),
        since: iso(l.since), cost_micros_usd: l.cost, open: l.at > now - 10 * MIN,
      },
      members: l.members,
    });
  }
  return out.sort((a, b) => (b.anomaly.count as number) - (a.anomaly.count as number)).slice(0, 100);
}

function runaways(w: FakeWorld, from: number, to: number, now: number) {
  const cells = new Map<string, { actor: string; op: string; h: number; n: number; cost: number }>();
  for (const f of factsIn(w, from, to)) {
    const h = Math.floor(f.at / HOUR) * HOUR;
    const k = `${f.actor}|${f.op}|${h}`;
    const c = cells.get(k) ?? { actor: f.actor, op: f.op, h, n: 0, cost: 0 };
    c.n += 1;
    c.cost += f.cost;
    cells.set(k, c);
  }
  const toHour = Math.floor(to / HOUR) * HOUR;
  const base = new Map<string, { sum: number; n: number }>();
  for (const c of cells.values()) {
    if (c.h >= toHour) continue;
    const b = base.get(`${c.actor}|${c.op}`) ?? { sum: 0, n: 0 };
    b.sum += c.n;
    b.n += 1;
    base.set(`${c.actor}|${c.op}`, b);
  }
  const out: Record<string, unknown>[] = [];
  for (const c of cells.values()) {
    const b = base.get(`${c.actor}|${c.op}`);
    if (!b) continue;
    const baseline = b.sum / b.n;
    if (c.n >= 10 && c.n > 3 * baseline) {
      out.push({ kind: 'runaway', actor_id: c.actor, operation: c.op, baseline, factor: c.n / Math.max(1, baseline), since: iso(c.h), cost_micros_usd: c.cost, open: c.h > now - HOUR });
    }
  }
  return out.sort((a, b) => (b.factor as number) - (a.factor as number)).slice(0, 100);
}

function drifts(w: FakeWorld, from: number, to: number, now: number) {
  const groups = new Map<string, Fact[]>();
  for (const f of factsIn(w, from, to)) groups.set(`${f.actor}|${f.op}`, [...(groups.get(`${f.actor}|${f.op}`) ?? []), f]);
  const out: Record<string, unknown>[] = [];
  for (const fs of groups.values()) {
    const rechecks = fs.filter((f) => f.kind === 'recheck').length;
    if (fs.length < 20 || rechecks <= 0.05 * fs.length) continue;
    const first = Math.min(...fs.map((f) => f.at));
    const last = Math.max(...fs.map((f) => f.at));
    out.push({
      kind: 'drift', actor_id: fs[0]!.actor, operation: fs[0]!.op, count: rechecks, baseline: 0.01, factor: rechecks / Math.max(1, fs.length) / 0.01,
      since: iso(Math.floor(first / (15 * MIN)) * 15 * MIN), cost_micros_usd: fs.reduce((s, f) => s + f.cost, 0), open: last > now - 10 * MIN,
    });
  }
  return out.sort((a, b) => (b.count as number) - (a.count as number)).slice(0, 100);
}

function analyticsAnomalies(w: FakeWorld, q: URLSearchParams, now: number): Response {
  const win = timeRange(q, 7 * DAY, now);
  if (win instanceof Response) return win;
  let kinds = csv(q.get('kinds'));
  if (!kinds.length) kinds = ['loop', 'runaway', 'drift'];
  for (const k of kinds) if (!['loop', 'runaway', 'drift'].includes(k)) return fail(400, 'INVALID_QUERY', 'Unknown anomaly kind');
  const items: Record<string, unknown>[] = [];
  for (const k of kinds) {
    if (k === 'loop') items.push(...loops(w, win.from, win.to, now).map((l) => l.anomaly));
    if (k === 'runaway') items.push(...runaways(w, win.from, win.to, now));
    if (k === 'drift') items.push(...drifts(w, win.from, win.to, now));
  }
  return json({ items });
}

// ─── resources ──────────────────────────────────────────

function knownResources(w: FakeWorld) {
  const ids = new Set<string>();
  for (const a of w.actions) for (const r of a.resources ?? []) ids.add(r.id);
  return ids;
}

function scopeMatches(s: FakeScope, op: string, actor: string, resources: string[]) {
  if (s.operation && s.operation !== '*' && s.operation !== op) return false;
  if (s.actor_id && s.actor_id !== actor) return false;
  if (s.resource) {
    const r0 = s.resource;
    const hit = resources.some((r) => (r0.endsWith('*') ? r.startsWith(r0.slice(0, -1)) : r === r0));
    if (!hit) return false;
  }
  return true;
}

const waiting = (a: FakeAction) => a.status === 'PROPOSED' && a.decision?.d === 'WAIT';

function leaseData(w: FakeWorld, id: string, now: number) {
  const holder = w.leases.filter((l) => l.resource_id === id && Date.parse(l.expires_at) > now).sort((a, b) => a.since.localeCompare(b.since))[0];
  const queue = w.actions
    .filter((a) => waiting(a) && (a.resources ?? []).some((r) => r.id === id))
    .sort((a, b) => a.at - b.at || (a.id < b.id ? -1 : 1))
    .map((a, i) => ({ action_id: a.id, actor_id: a.actor, waiting_since: iso(a.at), position: i + 1 }));
  const caps = w.limits.filter((l) => l.max_in_flight != null && scopeMatches({ resource: l.scope.resource }, '', '', [id])).map((l) => l.max_in_flight!);
  const stats = w.leaseStats[id] ?? { handoffs_last_hour: 0, refused_late_writes_today: 0 };
  return {
    resource_id: id,
    holder: holder ? { action_id: holder.action_id, actor_id: holder.actor_id, since: holder.since, expires_at: holder.expires_at, fencing_token: holder.fencing_token } : null,
    queue,
    limit: { max_in_flight: caps.length ? Math.min(...caps) : null, in_flight: w.leases.filter((l) => l.resource_id === id && Date.parse(l.expires_at) > now).length },
    handoffs_last_hour: stats.handoffs_last_hour,
    refused_late_writes_today: stats.refused_late_writes_today,
  };
}

// ─── money ──────────────────────────────────────────────

const WASTE_LABEL: Record<string, string> = {
  repeat_stopped: 'Repeats stopped', stale_redo: 'Stale reads redone', retry_loop: 'Retry loops', superseded: 'Superseded work', never_happened: 'Actions that never happened', other: 'Other waste',
};

function finops(w: FakeWorld, q: URLSearchParams, now: number) {
  const from = Date.parse(q.get('from') ?? '') || 0;
  const to = Date.parse(q.get('to') ?? '') || now;
  const inWin = w.actions.filter((a) => a.at >= from && a.at <= to);
  const byHour = new Map<number, { actions: number; cost: number; waste: number; failures: number }>();
  const inLoop = new Set(loops(w, from, to + 1, now).flatMap((l) => l.members.map((m) => m.a.id)));
  const waste = new Map<string, { key: string; label: string; amount_micros_usd: number; waste_micros_usd: number; count: number; avoided: boolean }>();
  const actors = new Map<string, { actor_id: string; cost_micros_usd: number; actions: number }>();
  for (const a of inWin) {
    const f = factOf(a);
    let key: string | null = null;
    if (a.decision?.d === 'DEDUPLICATE') key = inLoop.has(a.id) ? 'retry_loop' : 'repeat_stopped';
    else if (a.decision?.d === 'REPLAN') key = 'stale_redo';
    else if (BAD.has(f.outcome)) key = 'never_happened';
    else if (a.decision?.d === 'REJECT') key = 'other';
    const h = Math.floor(a.at / HOUR) * HOUR;
    const cell = byHour.get(h) ?? { actions: 0, cost: 0, waste: 0, failures: 0 };
    cell.actions += 1;
    cell.cost += f.cost;
    if (key) cell.waste += f.cost;
    if (BAD.has(f.outcome)) cell.failures += 1;
    byHour.set(h, cell);
    if (key) {
      const v = waste.get(key) ?? { key, label: WASTE_LABEL[key]!, amount_micros_usd: 0, waste_micros_usd: 0, count: 0, avoided: key === 'repeat_stopped' || key === 'retry_loop' };
      v.amount_micros_usd += f.cost;
      v.waste_micros_usd += f.cost;
      v.count += 1;
      waste.set(key, v);
    }
    const ac = actors.get(a.actor) ?? { actor_id: a.actor, cost_micros_usd: 0, actions: 0 };
    ac.cost_micros_usd += f.cost;
    ac.actions += 1;
    actors.set(a.actor, ac);
  }
  const series = [...byHour].sort((a, b) => a[0] - b[0]).map(([h, v]) => ({ bucket: iso(h), actions: v.actions, decisions: v.actions, failures: v.failures, cost_micros_usd: v.cost, waste_micros_usd: v.waste }));
  const total = inWin.reduce((s, a) => s + (a.cost ?? 0), 0);
  const byWaste = [...waste.values()].sort((a, b) => (a.key < b.key ? -1 : 1));
  return {
    schema_version: '1', period_start: iso(from), period_end: iso(to), series, by_provider: [], by_model: [], by_waste: byWaste, nodes: [], edges: [],
    total_cost_micros_usd: total, waste_micros_usd: byWaste.reduce((s, x) => s + x.waste_micros_usd, 0),
    analytics: { source: 'postgres', as_of: iso(now), lag_seconds: 0 },
    by_actor: [...actors.values()].sort((a, b) => b.cost_micros_usd - a.cost_micros_usd || (a.actor_id < b.actor_id ? -1 : 1)),
  };
}

function spendResponse(w: FakeWorld, key: string, now: number) {
  const l = w.spendLimits.find((x) => x.scope_key === key);
  if (!l) return json(null);
  const start = Math.floor(now / DAY) * DAY;
  const spent = w.actions.filter((a) => a.at >= start && a.at <= now && (!key || a.actor === key)).reduce((s, a) => s + (a.cost ?? 0), 0);
  const pace = (spent * 86400) / Math.max(1, (now - start) / 1000);
  const slow = spent > 0 && pace > l.daily_micros_usd ? iso(start + (l.daily_micros_usd / pace) * DAY) : null;
  return json({ daily_micros_usd: l.daily_micros_usd, when_reached: l.when_reached, tz: l.tz ?? 'UTC', scope: { actor_id: key }, spent_today_micros_usd: spent, pace_micros_usd: Math.floor(pace), slow_at: slow });
}

// ─── autopilot ──────────────────────────────────────────

function slipsOf(w: FakeWorld, g: FakeGrant) {
  const since = Date.parse(g.since);
  const recent = w.actions
    .filter((a) => a.actor === g.actor_id && a.op === g.operation && a.at >= since)
    .map(factOf)
    .sort(byNewest((f) => f.at, (f) => f.a.id))
    .slice(0, g.revert_after.per);
  return recent.filter((f) => f.kind === 'recheck' || BAD.has(f.outcome)).length;
}

function grantRow(w: FakeWorld, g: FakeGrant, now: number) {
  const slips = slipsOf(w, g);
  // Reaching the slip count revokes the grant and records why.
  if (g.active && slips >= g.revert_after.slips) {
    const before = { ...g };
    g.active = false;
    g.reverted_at = iso(now);
    audit(w, 'autopilot.reverted', { actor_id: g.actor_id, operation: g.operation }, before, g, 'system', now);
  }
  return { ...g, slips };
}

// ─── public proofs ──────────────────────────────────────

function randomToken() {
  const raw = new Uint8Array(32);
  crypto.getRandomValues(raw);
  return btoa(String.fromCharCode(...raw)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function proofData(w: FakeWorld, token: string, now: number): Response | Record<string, unknown> {
  if (token.length < 22 || token.length > 128) return fail(404, 'NOT_FOUND', 'Proof was not found');
  const p = w.proofs.find((x) => x.token === token);
  if (!p || Date.parse(p.expires_at) <= now) return fail(404, 'NOT_FOUND', 'Proof was not found');
  if (p.revoked_at) return fail(410, 'REVOKED', 'Proof was revoked');
  const a = w.actions.find((x) => x.id === p.action_id);
  if (!a) return fail(404, 'NOT_FOUND', 'Proof was not found');
  const outcome = a.outcome?.status;
  const status = outcome === 'VERIFIED' ? 'proven' : outcome && BAD.has(outcome) ? 'unproven' : 'pending';
  const out: Record<string, unknown> = { workspace: w.workspace, status, receipt: a.decision ? `rcpt_${a.id}` : null };
  if (p.show.what) out.what = { summary: a.op.replaceAll('.', ' '), at: iso(a.at), app: a.op.split('.')[0] };
  if (p.show.checks) out.checks = [{ t: 'App outcome verified', ok: status === 'proven' }];
  if (p.show.actor) {
    const actor = w.actors.find((x) => x.id === a.actor);
    out.actor = actor?.display_name || a.actor;
  }
  if (p.show.stopped_repeat) {
    const stops = w.actions.filter((b) => b.decision?.d === 'DEDUPLICATE' && duplicateOf(b, w) === a.id).map((b) => b.decision!.at ?? b.at);
    if (stops.length) out.stopped_repeat = { at: iso(Math.min(...stops)) };
  }
  return out;
}

const escapeHtml = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// ─── policy preview ─────────────────────────────────────

function pointer(v: unknown, p: string): unknown {
  let cur = v;
  for (const raw of p.split('/').slice(1)) {
    const k = raw.replace(/~1/g, '/').replace(/~0/g, '~');
    if (cur && typeof cur === 'object' && k in (cur as Record<string, unknown>)) cur = (cur as Record<string, unknown>)[k];
    else return undefined;
  }
  return cur;
}

const RISK = ['LOW', 'MEDIUM', 'HIGH', 'CRITICAL'];

function previewRule(w: FakeWorld, b: Record<string, unknown>): Response {
  const rule = (b.rule ?? {}) as Record<string, unknown>;
  const op = String(rule.operation || '*');
  const rtype = String(rule.resource_type || '*');
  const ptr = rule.argument_pointer as string | undefined;
  const threshold = rule.numeric_threshold as number | undefined;
  const outcome = String(rule.outcome ?? '');
  if (!rule.reason_code || !['REQUIRE_APPROVAL', 'REJECT', 'WARN'].includes(outcome) || (ptr != null && !String(ptr).startsWith('/')) || (ptr == null) !== (threshold == null)) {
    return fail(400, 'INVALID_RULE', 'Rule outcome, reason, and paired JSON pointer/threshold are required');
  }
  if (rule.minimum_risk != null && !RISK.includes(String(rule.minimum_risk))) return fail(400, 'INVALID_RULE', 'minimum_risk must be LOW, MEDIUM, HIGH, or CRITICAL');
  const z = zoneNamed(String(b.tz || 'UTC'));
  const from = Date.parse(String(b.from ?? ''));
  const to = Date.parse(String(b.to ?? ''));
  if (!z || Number.isNaN(from) || Number.isNaN(to) || !(from < to) || to - from > 30 * DAY) return fail(400, 'INVALID_TIME_RANGE', 'Valid timezone and a positive range of at most 30 days are required');
  const limit = b.examples == null ? 3 : Number(b.examples);
  if (!Number.isInteger(limit) || limit < 0 || limit > 50) return fail(400, 'INVALID_QUERY', 'examples must be 0–50');
  let matched = 0;
  let handled = 0;
  let falsePos = 0;
  let unavailable = 0;
  const days = new Map<string, number>();
  const hours = Array(24).fill(0) as number[];
  const examples: Record<string, unknown>[] = [];
  const acts = w.actions
    .filter((a) => a.at >= from && a.at < to && (op === '*' || a.op === op) && (rtype === '*' || (a.resources ?? []).some((r) => r.type === rtype)))
    .sort((a, b) => a.at - b.at || (a.id < b.id ? -1 : 1));
  for (const a of acts) {
    if (rule.minimum_risk != null && RISK.indexOf('MEDIUM') < RISK.indexOf(String(rule.minimum_risk))) continue;
    if (ptr != null) {
      // Retained arguments: the fake keeps an action's arguments in its metadata.
      if (!a.meta) {
        unavailable++;
        continue;
      }
      const v = pointer(a.meta, ptr);
      if (typeof v !== 'number' || v <= threshold!) continue;
    }
    matched++;
    const date = dateIn(z, a.at);
    days.set(date, (days.get(date) ?? 0) + 1);
    hours[hourIn(z, a.at)]! += 1;
    const then = a.decision?.d ?? 'ALLOW';
    if (then === outcome && (a.decision?.evals ?? []).some((e) => e.stage === 'policy' && e.outcome === outcome)) handled++;
    if (w.approvals.some((p) => p.action_id === a.id && p.status === 'APPROVED')) falsePos++;
    if (examples.length < limit) examples.push({ action_id: a.id, summary: a.meta?.summary, at: iso(a.at), decision_then: then, decision_with_rule: outcome });
  }
  const byDay: { date: string; matched: number }[] = [];
  for (let d = z.dayStart(from); d < to; d = z.dayStart(d + DAY + 2 * HOUR)) {
    const date = dateIn(z, d + 12 * HOUR);
    byDay.push({ date, matched: days.get(date) ?? 0 });
  }
  return json({ matched, would_change: matched - handled, already_handled: handled, by_day: byDay, by_hour: hours, examples, false_positive_estimate: falsePos, unavailable_arguments: unavailable, truncated: unavailable > 0 });
}

// ─── notifications ──────────────────────────────────────

const EMAIL = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
const SCHEDULE = /^(MON|TUE|WED|THU|FRI|SAT|SUN) ([01]\d|2[0-3]):[0-5]\d (\S+)$/;

function validateNotifications(w: FakeWorld, c: { channels?: Record<string, unknown>[]; routes?: Record<string, unknown>[] }): string | null {
  const channels = c.channels ?? [];
  const routes = c.routes ?? [];
  if (channels.length > 100 || routes.length > 100) return 'at most 100 channels and routes';
  const ids = new Set<string>();
  for (const ch of channels) {
    const id = String(ch.id ?? '');
    if (!id || ids.has(id)) return 'unique nonempty channel ids are required';
    ids.add(id);
    if (ch.kind === 'email') {
      if (!EMAIL.test(String(ch.target ?? ''))) return 'invalid email target';
    } else if (ch.kind === 'slack') {
      if (!ch.target || !ch.connection_id) return 'Slack target and connection_id required';
      if (!w.connections.some((x) => x.id === ch.connection_id && x.toolkit === 'slack')) return 'Slack connection is not owned by this workspace';
    } else return 'channel kind must be slack or email';
  }
  for (const r of routes) {
    if (!['approval.requested', 'incident.opened', 'digest.weekly'].includes(String(r.event))) return 'unsupported notification event';
    const chs = (r.channels ?? []) as string[];
    if (!chs.length) return 'route needs channels';
    for (const id of chs) if (!ids.has(id)) return 'route refers to unknown channel';
    if (r.event === 'digest.weekly') {
      const m = SCHEDULE.exec(String(r.schedule ?? ''));
      if (!m || !zoneNamed(m[3]!)) return 'schedule must read like MON 09:00 Asia/Kolkata';
    } else if (r.schedule) return 'only weekly digests accept schedule';
  }
  return null;
}

// ─── the fetch ──────────────────────────────────────────

export function fakeFetch(w: FakeWorld) {
  // The console's own admin key, so a key list is never empty.
  const codes = new Map<string, { scopes: string[]; actor_hint: string; expires: number; used_at: string | null; actor_id: string | null; host?: string | null; project?: string | null; key_id?: string | null }>();
  let exchanges: number[] = [];
  const keys: { id: string; name: string; prefix: string; scopes: string[]; created_at: string; expires_at: string | null; revoked_at: string | null; secret?: string }[] = [
    { id: 'key_console', name: 'Console admin key', prefix: 'lumni_adm1', scopes: ['admin'], created_at: '2026-09-01T09:00:00Z', expires_at: null, revoked_at: null },
  ];
  return async (input: string, init: RequestInit = {}): Promise<Response> => {
    const url = new URL(input);
    const method = (init.method ?? 'GET').toUpperCase();
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((v, k) => {
      headers[k] = v;
    });
    const body = typeof init.body === 'string' ? init.body : init.body instanceof ArrayBuffer ? new TextDecoder().decode(init.body) : undefined;
    w.seen.push({ method, path: url.pathname + url.search, headers, body });
    const p = url.pathname;
    const q = url.searchParams;
    const now = Date.now();

    // Health is open, as it is on the real API.
    if (p === '/v1/health/ready') return json({ status: 'ready' });
    // So are public proofs: the token is the credential.
    const pub = /^\/(?:v1\/public\/proofs|p)\/([^/]+)$/.exec(p);
    if (pub && method === 'GET') {
      const out = proofData(w, decodeURIComponent(pub[1]!), now);
      if (out instanceof Response) return out;
      const safety = { 'cache-control': 'no-store', 'referrer-policy': 'no-referrer', 'x-content-type-options': 'nosniff' };
      if (p.startsWith('/v1/')) return json(out, 200, safety);
      const what = out.what as { summary: string; at: string } | undefined;
      const html = `<!doctype html><html><meta charset="utf-8"><title>Lumni proof</title><body><main><h1>${escapeHtml(String(out.workspace))}</h1><p>Proof status: ${out.status}</p>${what ? `<h2>${escapeHtml(what.summary)}</h2><p>${what.at}</p>` : ''}${out.actor ? `<p>Actor: ${escapeHtml(String(out.actor))}</p>` : ''}<p>Receipt: ${out.receipt ?? ''}</p></main></body></html>`;
      return new Response(html, { headers: { 'content-type': 'text/html; charset=utf-8', 'content-security-policy': "default-src 'none'; frame-ancestors 'none'", ...safety } });
    }
    // Onboarding exchange is open too: the code is the credential. 10 a minute.
    if (p === '/v1/onboarding/exchange' && method === 'POST' && !w.noOnboarding) {
      exchanges = exchanges.filter((t) => now - t < MIN);
      exchanges.push(now);
      if (exchanges.length > 10) return fail(429, 'RATE_LIMITED', 'Too many onboarding attempts');
      const inp = JSON.parse(body ?? '{}') as { code?: string; host?: string; project?: string };
      const c = codes.get(String(inp.code ?? '').trim().toUpperCase());
      if (!c || c.used_at || c.expires <= now) return fail(404, 'NOT_FOUND', 'Onboarding code was not found');
      const secret = `lm_${Math.random().toString(36).slice(2, 14)}${Math.random().toString(36).slice(2, 14)}`;
      const id = `key_onb_${keys.length}`;
      keys.push({ id, name: `onboarding: ${`${inp.project ?? ''} @ ${inp.host ?? ''}`.trim()}`, prefix: secret.slice(0, 10), scopes: c.scopes, created_at: iso(now), expires_at: null, revoked_at: null, secret });
      c.used_at = iso(now);
      c.actor_id = c.actor_hint || null;
      Object.assign(c, { host: inp.host ?? null, project: inp.project ?? null, key_id: id });
      return json({ api_key: secret, key_id: id, tenant_name: w.workspace, api_url: 'http://lumni.example:8080', actor_hint: c.actor_hint }, 200, { 'cache-control': 'no-store' });
    }
    // Any key the API knows (the admin key, or one it made) can open a session.
    const keyFor = (auth?: string) => (auth === `Bearer ${w.key}` ? { scopes: ['admin'] } : keys.find((k) => k.secret && !k.revoked_at && auth === `Bearer ${k.secret}`) ?? null);
    if (p === '/v1/tenants' && method === 'POST') {
      if (!w.platformKey || headers.authorization !== `Bearer ${w.platformKey}`) return fail(401, 'UNAUTHENTICATED');
      // The fake has one world: a "new" tenant is its own id with a fresh admin key.
      const secret = `lm_admin_${Math.random().toString(36).slice(2, 14)}`;
      keys.push({ id: `key_tenant_${keys.length}`, name: 'Workspace admin', prefix: secret.slice(0, 10), scopes: ['admin'], created_at: iso(Date.now()), expires_at: null, revoked_at: null, secret });
      return json({ tenant_id: `t_${keys.length}`, admin_key: secret, name: JSON.parse(body ?? '{}').name ?? null }, 201);
    }
    const platformRoute = /^\/v1\/tenants\/([^/]+)\/(plan|usage)$/.exec(p);
    if (platformRoute) {
      if (!w.platformKey || headers.authorization !== `Bearer ${w.platformKey}`) return fail(401, 'UNAUTHENTICATED');
      const tenant = decodeURIComponent(platformRoute[1]!);
      if (platformRoute[2] === 'plan' && method === 'PUT') {
        (w.plans ??= {})[tenant] = JSON.parse(body ?? '{}');
        return json({ tenant_id: tenant, ...w.plans[tenant] });
      }
      if (platformRoute[2] === 'usage' && method === 'GET') return json({ tenant_id: tenant, checks: w.usage?.[tenant] ?? 0 });
    }
    if (p === '/v1/auth/session' && method === 'POST') {
      const k = keyFor(headers.authorization);
      if (!k) return fail(401, 'UNAUTHENTICATED');
      const token = `sess_${w.sessions.size + 1}`;
      const csrf = `csrf_${w.sessions.size + 1}`;
      w.sessions.add(token);
      w.csrf.set(token, csrf);
      return json({ tenant_id: w.tenant, scopes: k.scopes, csrf_token: csrf, expires_at: iso(Date.now() + 8 * 3600_000) }, 201, {
        'set-cookie': `lumni_session=${token}; Path=/; Max-Age=28800; HttpOnly; SameSite=Strict`,
      });
    }

    // Everything else needs a session (or the key), the way the API’s auth middleware does.
    const token = /lumni_session=([^;]+)/.exec(headers.cookie ?? '')?.[1];
    const bearer = headers.authorization === `Bearer ${w.key}` || keys.some((k) => k.secret && !k.revoked_at && headers.authorization === `Bearer ${k.secret}`);
    if (!bearer && (!token || !w.sessions.has(token))) return fail(401, 'UNAUTHENTICATED');
    if (!bearer && method !== 'GET' && headers['x-csrf-token'] !== w.csrf.get(token!)) return fail(403, 'CSRF_TOKEN_REQUIRED');

    let parsed: Record<string, unknown> = {};
    if (body) {
      try {
        parsed = JSON.parse(body) as Record<string, unknown>;
      } catch {
        return fail(400, 'INVALID_JSON', 'Request body must be JSON');
      }
    }
    const listed = (r: ReturnType<typeof page>) => (r instanceof Response ? r : json(r));

    if (p === '/v1/auth/session' && method === 'GET') return json({ tenant_id: w.tenant, scopes: ['admin'], session: true });
    if (p === '/v1/auth/session' && method === 'DELETE') {
      w.sessions.delete(token!);
      return json({ revoked: true });
    }
    if (p === '/v1/actors' && method === 'GET') {
      const since = q.get('since') ?? q.get('from');
      const from = since ? Date.parse(since) : null;
      const type = q.get('type');
      const rows = w.actors.filter((a) => (from === null || (a.created_at ? Date.parse(a.created_at) >= from : false)) && (!type || a.type === type));
      return json({ items: rows.length || from !== null ? rows.map((a) => actorRow(a, now)) : null, next_cursor: null });
    }
    const actor = /^\/v1\/actors\/([^/]+)$/.exec(p);
    if (actor && method === 'GET') {
      const a = w.actors.find((x) => x.id === decodeURIComponent(actor[1]!));
      return a ? json(actorRow(a, now)) : fail(404, 'NOT_FOUND');
    }
    const pause = /^\/v1\/actors\/([^/]+)\/(pause|resume)$/.exec(p);
    if (pause && method === 'POST') {
      const a = w.actors.find((x) => x.id === decodeURIComponent(pause[1]!));
      if (!a) return fail(404, 'NOT_FOUND', 'Actor was not found');
      const want = pause[2] === 'pause';
      const until = parsed.until ? Date.parse(String(parsed.until)) : null;
      if (until !== null && (Number.isNaN(until) || until <= now)) return fail(400, 'INVALID_TIME_RANGE', 'until must be in the future');
      if (actorRow(a, now).paused === want) return fail(409, want ? 'ALREADY_PAUSED' : 'NOT_PAUSED', 'Actor already has this pause state');
      const before = actorRow(a, now);
      Object.assign(a, want ? { paused: true, paused_at: iso(now), paused_by: w.principal, until: until === null ? null : iso(until), pause_reason: String(parsed.reason ?? '') } : { paused: false, paused_at: null, paused_by: null, until: null, pause_reason: String(parsed.reason ?? '') });
      const after = actorRow(a, now);
      audit(w, want ? 'actor.paused' : 'actor.resumed', { id: a.id }, before, after);
      return json({ actor_id: a.id, paused: after.paused, paused_at: after.paused_at, paused_by: after.paused_by, until: after.until });
    }
    if (p === '/v1/config/pause' && method === 'POST') {
      if (typeof parsed.paused !== 'boolean') return fail(400, 'INVALID_QUERY', 'paused is required');
      audit(w, 'workspace.paused', {}, { paused: w.paused }, { paused: parsed.paused });
      w.paused = parsed.paused;
      return json({ paused: w.paused });
    }
    if (p === '/v1/agents') return json({ items: w.agents.length ? w.agents : null });

    // Approvals, with old pending ones, embeddings and paging.
    if (p === '/v1/approvals' && method === 'GET') {
      const include = csv(q.get('include'));
      if (include.some((v) => v !== 'action' && v !== 'rule')) return fail(400, 'INVALID_QUERY', 'include supports action,rule');
      const statuses = csv(q.get('status'));
      const ids = csv(q.get('action_id'));
      if (ids.length > 100 || statuses.length > 100) return fail(400, 'INVALID_QUERY', 'At most 100 filter values');
      const rows = w.approvals
        .filter((ap) => (!statuses.length || statuses.includes(approvalStatus(ap, now))) && (!ids.length || ids.includes(ap.action_id)))
        .sort(byNewest((ap) => Date.parse(ap.requested_at), (ap) => ap.id));
      const r = page(rows, p, q, (ap) => Date.parse(ap.requested_at));
      return r instanceof Response ? r : json({ items: r.items.map((ap) => approvalRow(w, ap, now, include)), next_cursor: r.next_cursor });
    }
    const oneApproval = /^\/v1\/approvals\/([^/]+)$/.exec(p);
    if (oneApproval && method === 'GET' && oneApproval[1] !== 'decide') {
      const ap = w.approvals.find((x) => x.id === decodeURIComponent(oneApproval[1]!));
      return ap ? json(approvalRow(w, ap, now, csv(q.get('include')))) : fail(404, 'NOT_FOUND');
    }
    const decide = (ap: FakeApproval, state: string, note?: string) => {
      const before = { ...ap };
      ap.status = state;
      ap.resolved_at = iso(now);
      ap.resolved_by = w.principal;
      if (note) ap.metadata = { ...(ap.metadata ?? {}), note };
      audit(w, 'approval.decided', { id: ap.id, action_id: ap.action_id, ...approvalContext(w, ap.action_id) }, before, ap, w.principal, now);
    };
    if (p === '/v1/approvals/decide' && method === 'POST') {
      const ids = parsed.ids as unknown;
      const decision = parsed.decision;
      const note = parsed.note as string | undefined;
      if (!Array.isArray(ids) || ids.length < 1 || ids.length > 200 || (decision !== 'approve' && decision !== 'deny') || (note && note.length > 4000)) {
        return fail(400, 'INVALID_QUERY', '1–200 ids and approve or deny are required');
      }
      const state = decision === 'approve' ? 'APPROVED' : 'DENIED';
      const results = ids.map((id) => {
        const ap = w.approvals.find((x) => x.id === id);
        if (!ap) return { id, error: { code: 'NOT_FOUND' } };
        if (approvalStatus(ap, now) !== 'PENDING') return { id, error: { code: 'REQUEST_FAILED' } };
        decide(ap, state, note);
        return { id, status: state };
      });
      return json({ results });
    }
    const approve = /^\/v1\/approvals\/([^/]+)\/(approve|deny)$/.exec(p);
    if (approve) {
      const ap = w.approvals.find((a) => a.id === approve[1]);
      if (!ap || ap.status !== 'PENDING') return fail(404, 'NOT_FOUND');
      decide(ap, approve[2] === 'approve' ? 'APPROVED' : 'DENIED');
      return json({ status: ap.status });
    }

    if (p === '/v1/incidents' || p === '/v1/findings') {
      const kinds = csv(q.get('kind'));
      const statuses = csv(q.get('status'));
      const ids = csv(q.get('action_id'));
      const work = q.get('work_id');
      const rows = w.incidents
        .filter((i) => (!kinds.length || kinds.includes(i.kind)) && (!statuses.length || statuses.includes(i.status)) && (!ids.length || (i.action_id !== null && ids.includes(i.action_id))) && (!work || i.work_id === work))
        .sort(byNewest((i) => Date.parse(i.created_at), (i) => i.id));
      const r = page(rows, p, q, (i) => Date.parse(i.created_at));
      // A burst's evidence is lifted into its row.
      return r instanceof Response ? r : json({ items: r.items.map((i) => (i.kind === 'BURST' ? { ...i, ...i.evidence } : i)), next_cursor: r.next_cursor });
    }
    if (p === '/v1/verifiers' && method === 'GET') return json({ items: w.verifiers });
    if (p === '/v1/verifiers' && method === 'POST') {
      const v = parsed as (typeof w.verifiers)[number];
      const i = w.verifiers.findIndex((x) => x.id === v.id);
      const before = i >= 0 ? { ...w.verifiers[i]! } : null;
      if (i >= 0) w.verifiers[i] = { ...w.verifiers[i]!, ...v };
      audit(w, 'verifier.changed', { id: v.id }, before, i >= 0 ? w.verifiers[i] : v);
      return json(v, 201);
    }
    if (p === '/v1/config/policy-rules' && method === 'GET') return json({ items: w.rules.filter((r) => !r.deleted_at) });
    if (p === '/v1/config/policy-rules' && method === 'POST') {
      const r = parsed as Partial<FakeRule>;
      const cur = w.rules.find((x) => x.name === r.name && !x.deleted_at);
      const before = cur ? { ...cur } : null;
      if (cur) Object.assign(cur, r, { version: cur.version + 1 });
      else w.rules.push({ id: `pr_${w.rules.length + 1}`, resource_type: '*', minimum_risk: null, argument_pointer: null, numeric_threshold: null, enabled: true, version: 1, created_at: iso(Date.now()), ...r } as FakeRule);
      const saved = cur ?? w.rules.at(-1)!;
      audit(w, cur ? 'policy_rule.updated' : 'policy_rule.created', { id: saved.id, operation: saved.operation }, before, saved);
      return json({ id: saved.id }, 201);
    }
    if (p === '/v1/config/policy-rules/preview' && method === 'POST') return previewRule(w, parsed);
    const ruleId = /^\/v1\/config\/policy-rules\/([^/]+)$/.exec(p);
    if (ruleId && (method === 'PATCH' || method === 'DELETE')) {
      const rule = w.rules.find((x) => x.id === decodeURIComponent(ruleId[1]!) && !x.deleted_at);
      if (method === 'DELETE') {
        if (!rule) return fail(404, 'NOT_FOUND', 'Rule was not found');
        const before = { ...rule };
        Object.assign(rule, { enabled: false, deleted_at: iso(now), version: rule.version + 1 });
        audit(w, 'policy_rule.deleted', { id: rule.id, operation: rule.operation }, before, rule);
        return none();
      }
      const b = parsed as { enabled?: boolean; numeric_threshold?: number; name?: string; version?: number };
      if (b.enabled === undefined && b.numeric_threshold === undefined && b.name === undefined) return fail(400, 'INVALID_RULE', 'At least one editable field is required');
      if (b.name !== undefined && !String(b.name).trim()) return fail(400, 'INVALID_RULE', 'Name cannot be empty');
      let version = b.version;
      const match = headers['if-match'];
      if (match) {
        const v = Number(match.replace(/"/g, ''));
        if (!Number.isInteger(v) || (version !== undefined && version !== v)) return fail(400, 'INVALID_RULE', 'Invalid or conflicting If-Match version');
        version = v;
      }
      if (!rule) return fail(404, 'NOT_FOUND', 'Rule was not found');
      if (version !== undefined && version !== rule.version) return fail(409, 'VERSION_CONFLICT', 'Rule changed since it was read');
      const before = { ...rule };
      if (b.enabled !== undefined) rule.enabled = b.enabled;
      if (b.numeric_threshold !== undefined) rule.numeric_threshold = b.numeric_threshold;
      if (b.name !== undefined) rule.name = b.name;
      rule.version += 1;
      audit(w, 'policy_rule.updated', { id: rule.id, operation: rule.operation }, before, rule);
      return json(rule);
    }
    if (p === '/v1/enforcement') return json({ items: w.enforcement });
    if (p === '/v1/config/enforcement' && method === 'PUT') {
      const e = parsed as { operation: string; rule?: string; mode: string };
      const rule = e.rule || '*';
      const cur = w.enforcement.find((x) => x.operation === e.operation && x.rule === rule);
      const before = cur ? { ...cur } : null;
      if (cur) cur.mode = e.mode;
      else w.enforcement.push({ operation: e.operation, rule, mode: e.mode });
      audit(w, 'enforcement.set', { operation: e.operation, rule }, before, { operation: e.operation, rule, mode: e.mode });
      return json({ ...e, rule: e.rule ?? rule, effective_mode: e.mode });
    }
    if (p === '/v1/config/enforcement' && method === 'DELETE') {
      const operation = String(parsed.operation ?? '');
      if (!operation) return fail(400, 'INVALID_RULE', 'operation is required');
      const rule = String(parsed.rule || '*');
      const i = w.enforcement.findIndex((x) => x.operation === operation && x.rule === rule);
      if (i < 0) return fail(404, 'NOT_FOUND', 'No such override');
      const [gone] = w.enforcement.splice(i, 1);
      audit(w, 'enforcement.cleared', { operation, rule }, gone, null);
      return none();
    }
    // The real API reports the workspace pause here too (tenants.paused).
    if (p === '/v1/config') return json({ enforcement_mode: w.mode, semantic_enabled: false, paused: !!w.paused });

    // Limits: caps, rates and holds.
    if (p === '/v1/config/limits' && method === 'GET') {
      const resource = q.get('resource');
      const rows = w.limits
        .filter((l) => !resource || scopeMatches({ resource: l.scope.resource }, '', '', [resource]))
        .sort(byNewest((l) => Date.parse(l.created_at), (l) => l.id))
        .map((l) => limitRow(w, l));
      return listed(page(rows, p, q, (l) => Date.parse(l.created_at)));
    }
    const limitId = /^\/v1\/config\/limits\/([^/]+)$/.exec(p);
    if (limitId && method === 'PUT') {
      const id = decodeURIComponent(limitId[1]!);
      const b = parsed as Partial<FakeLimit>;
      const mode = b.mode || 'ENFORCE';
      const max = b.max_in_flight ?? null;
      const per = b.per_minute ?? null;
      if ((max !== null && max <= 0) || (per !== null && per <= 0) || (!b.hold && max === null && per === null) || !['ENFORCE', 'SHADOW', 'WARN'].includes(mode)) {
        return fail(400, 'INVALID_LIMIT', 'Positive concurrency or rate, or hold, and a valid mode are required');
      }
      const scope = (b.scope ?? {}) as FakeScope;
      if (scope.resource && scope.resource.replace(/\*$/, '').includes('*')) return fail(400, 'INVALID_LIMIT', 'Resource wildcard is allowed only as a suffix');
      const cur = w.limits.find((l) => l.id === id);
      const before = cur ? { ...cur } : null;
      const next: FakeLimit = { id, scope: strip(scope as Record<string, unknown>) as FakeScope, max_in_flight: max, per_minute: per, hold: !!b.hold, mode, created_at: cur?.created_at ?? iso(now) };
      if (cur) Object.assign(cur, next);
      else w.limits.push(next);
      audit(w, 'limit.set', { id }, before, next);
      return json(limitRow(w, next));
    }
    if (limitId && method === 'DELETE') {
      const i = w.limits.findIndex((l) => l.id === decodeURIComponent(limitId[1]!));
      if (i < 0) return fail(404, 'NOT_FOUND');
      const [gone] = w.limits.splice(i, 1);
      audit(w, 'limit.set', { id: gone!.id }, gone, null);
      return none();
    }

    if (p === '/v1/config/spend-limit') {
      if (method === 'GET') return spendResponse(w, q.get('actor_id') ?? '', now);
      if (method === 'DELETE') {
        const key = q.get('actor_id') ?? '';
        const i = w.spendLimits.findIndex((l) => l.scope_key === key);
        if (i < 0) return fail(404, 'NOT_FOUND');
        const [gone] = w.spendLimits.splice(i, 1);
        audit(w, 'spend_limit.set', { id: key || 'workspace', actor_id: key || undefined }, gone, null);
        return none();
      }
      if (method === 'PUT') {
        const b = parsed as { daily_micros_usd?: number; when_reached?: string; scope?: FakeScope; tz?: string };
        if (b.tz) {
          try {
            new Intl.DateTimeFormat('en', { timeZone: b.tz });
          } catch {
            return fail(400, 'INVALID_TIME_ZONE', 'tz must be an IANA zone');
          }
        }
        if (!b.daily_micros_usd || b.daily_micros_usd <= 0 || !Number.isInteger(b.daily_micros_usd) || (b.when_reached !== 'SLOW' && b.when_reached !== 'HOLD') || b.scope?.operation || b.scope?.resource) {
          return fail(400, 'INVALID_LIMIT', 'A positive daily budget and SLOW or HOLD are required; scope supports actor_id');
        }
        const key = b.scope?.actor_id ?? '';
        if (key && !w.actors.some((a) => a.id === key)) return fail(404, 'NOT_FOUND', 'Actor was not found');
        const cur = w.spendLimits.find((l) => l.scope_key === key);
        const before = cur ? { ...cur } : null;
        const next = { scope_key: key, daily_micros_usd: b.daily_micros_usd, when_reached: b.when_reached, tz: b.tz || 'UTC' };
        if (cur) Object.assign(cur, next);
        else w.spendLimits.push(next);
        audit(w, 'spend_limit.set', { id: key || 'workspace', actor_id: key || undefined }, before, next);
        return spendResponse(w, key, now);
      }
    }

    if (p === '/v1/config/autopilot' && method === 'GET') {
      const rows = [...w.autopilot].sort(byNewest((g) => Date.parse(g.since), (g) => g.actor_id)).map((g) => grantRow(w, g, now));
      return listed(page(rows, p, q, (g) => Date.parse(g.since)));
    }
    const pilot = /^\/v1\/config\/autopilot\/([^/]+)$/.exec(p);
    if (pilot && method === 'PUT') {
      const id = decodeURIComponent(pilot[1]!);
      const b = parsed as { operation?: string; skip_rules?: string[]; below?: { pointer?: string; value?: number }; revert_after?: { slips?: number; per?: number } };
      const skip = b.skip_rules ?? [];
      const slips = b.revert_after?.slips ?? 0;
      const per = b.revert_after?.per ?? 0;
      if (!b.operation || !skip.length || skip.length > 100 || !String(b.below?.pointer ?? '').startsWith('/') || !((b.below?.value ?? 0) > 0) || slips < 1 || per < slips || per > 100000) {
        return fail(400, 'INVALID_LIMIT', 'Operation, rules, positive threshold, and valid slip window are required');
      }
      if (!w.actors.some((a) => a.id === id)) return fail(404, 'NOT_FOUND', 'Actor was not found');
      const ok = skip.every((rid) => w.rules.some((r) => r.id === rid && r.enabled && !r.deleted_at && r.outcome === 'REQUIRE_APPROVAL' && (r.operation === '*' || r.operation === b.operation)));
      if (!ok) return fail(400, 'INVALID_RULE', 'skip_rules must refer to enabled approval rules for this operation');
      const cur = w.autopilot.find((g) => g.actor_id === id);
      const before = cur ? { ...cur } : null;
      const below = { pointer: b.below!.pointer!, value: b.below!.value! };
      const revert = { slips, per };
      const same = cur && cur.active && cur.operation === b.operation && JSON.stringify(cur.skip_rules) === JSON.stringify(skip) && JSON.stringify(cur.below) === JSON.stringify(below) && JSON.stringify(cur.revert_after) === JSON.stringify(revert);
      const next: FakeGrant = { actor_id: id, operation: b.operation, active: true, since: same ? cur!.since : iso(now), reverted_at: null, skip_rules: skip, below, revert_after: revert };
      if (cur) Object.assign(cur, next);
      else w.autopilot.push(next);
      audit(w, 'autopilot.granted', { actor_id: id, operation: b.operation }, before, next);
      return json(grantRow(w, cur ?? next, now));
    }
    if (pilot && method === 'DELETE') {
      const i = w.autopilot.findIndex((g) => g.actor_id === decodeURIComponent(pilot[1]!));
      if (i < 0) return fail(404, 'NOT_FOUND');
      const [gone] = w.autopilot.splice(i, 1);
      audit(w, 'autopilot.reverted', { actor_id: gone!.actor_id, operation: gone!.operation }, gone, null);
      return none();
    }

    if (p === '/v1/proofs' && method === 'POST') {
      const b = parsed as { action_id?: string; show?: FakeProof['show']; expires_in_hours?: number; recipients?: string[] };
      const hours = b.expires_in_hours ?? 168;
      const recipients = b.recipients ?? [];
      if (!b.action_id || !Number.isInteger(hours) || hours < 1 || hours > 8760 || recipients.length > 100) return fail(400, 'INVALID_QUERY', 'Action and expiry between 1 and 8760 hours are required');
      if (recipients.some((r) => !EMAIL.test(r))) return fail(400, 'INVALID_QUERY', 'Invalid recipient email');
      if (!w.actions.some((a) => a.id === b.action_id)) return fail(404, 'NOT_FOUND', 'Action was not found');
      const proof: FakeProof = { id: `prf_${w.proofs.length + 1}`, token: randomToken(), action_id: b.action_id, show: b.show ?? {}, expires_at: iso(now + hours * HOUR), revoked_at: null, created_at: iso(now) };
      w.proofs.push(proof);
      const email = w.notifications.channels.find((c) => c.kind === 'email');
      const sent: string[] = [];
      if (email) {
        for (const r of recipients) {
          w.notificationJobs.push({ id: `ntf_${w.notificationJobs.length + 1}`, channel_id: String(email.id), kind: 'proof', ref_id: proof.id, at: iso(now), payload: { recipient: r } });
          sent.push(r);
        }
      }
      audit(w, 'proof.created', { id: proof.id, action_id: proof.action_id }, null, proof);
      const base = `http://${headers.host ?? 'localhost:8080'}`;
      return json({ id: proof.id, url: `${base}/p/${proof.token}`, token: proof.token, expires_at: proof.expires_at, sent_to: sent }, 201);
    }
    const proofId = /^\/v1\/proofs\/([^/]+)$/.exec(p);
    if (proofId && method === 'DELETE') {
      const proof = w.proofs.find((x) => x.id === decodeURIComponent(proofId[1]!));
      if (!proof) return fail(404, 'NOT_FOUND');
      const before = { ...proof };
      proof.revoked_at ??= iso(now);
      if (before.revoked_at !== proof.revoked_at) audit(w, 'proof.created', { id: proof.id, action_id: proof.action_id }, before, proof);
      return none();
    }

    if (p === '/v1/config/notifications' && method === 'GET') return json(w.notifications);
    if (p === '/v1/config/notifications' && method === 'PUT') {
      const c = parsed as { channels?: Record<string, unknown>[]; routes?: Record<string, unknown>[] };
      const problem = validateNotifications(w, c);
      if (problem) return fail(400, 'INVALID_NOTIFICATIONS', problem);
      const before = w.notifications;
      w.notifications = { channels: c.channels ?? [], routes: c.routes ?? [] };
      audit(w, 'notifications.updated', {}, before, w.notifications);
      return json(w.notifications);
    }
    if (p === '/v1/notifications/send' && method === 'POST') {
      const b = parsed as { channel_id?: string; kind?: string; ref_id?: string };
      if (!b.channel_id || !b.ref_id || !['incident', 'digest', 'proof'].includes(String(b.kind))) return fail(400, 'INVALID_NOTIFICATION', 'channel_id, ref_id, and incident, digest, or proof are required');
      if (!w.notifications.channels.some((c) => c.id === b.channel_id)) return fail(404, 'NOT_FOUND', 'Channel was not found');
      const exists =
        b.kind === 'incident' ? w.incidents.some((i) => i.id === b.ref_id)
        : b.kind === 'proof' ? w.proofs.some((x) => x.id === b.ref_id && !x.revoked_at && Date.parse(x.expires_at) > now)
        : true;
      if (!exists) return fail(404, 'NOT_FOUND', 'Reference was not found');
      const job = { id: `ntf_${w.notificationJobs.length + 1}`, channel_id: b.channel_id, kind: b.kind!, ref_id: b.ref_id, at: iso(now) };
      w.notificationJobs.push(job);
      return json({ queued: true, id: job.id }, 202);
    }

    if (p === '/v1/audit' && method === 'GET') {
      const types = csv(q.get('type'));
      const by = q.get('actor');
      if (types.length > 100) return fail(400, 'INVALID_QUERY', 'At most 100 filter values');
      const rows = w.audit.filter((e) => (!types.length || types.includes(e.type)) && (!by || e.by === by)).sort(byNewest((e) => Date.parse(e.at), (e) => e.id));
      return listed(page(rows, p, q, (e) => Date.parse(e.at)));
    }

    if (p === '/v1/analytics/day' && method === 'GET') return analyticsDay(w, q, now);
    if (p === '/v1/analytics/daily' && method === 'GET') return analyticsDaily(w, q, now);
    if (p === '/v1/analytics/freshness' && method === 'GET') return analyticsFreshness(w, q, now);
    if (p === '/v1/analytics/anomalies' && method === 'GET') return analyticsAnomalies(w, q, now);

    if (p === '/v1/resources/contended' && method === 'GET') {
      const win = timeRange(q, 90 * DAY, now);
      if (win instanceof Response) return win;
      let top = 10;
      if (q.get('top')) {
        top = Number(q.get('top'));
        if (!Number.isInteger(top) || top < 1 || top > 50) return fail(400, 'INVALID_QUERY', 'top must be 1–50');
      }
      const tally = new Map<string, number>();
      for (const f of factsIn(w, win.from, win.to)) if (f.kind === 'stop' || f.kind === 'wait' || f.kind === 'recheck') for (const r of f.resources) tally.set(r, (tally.get(r) ?? 0) + 1);
      const ids = [...tally].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, top).map(([id]) => id);
      return json({
        // Go now includes each queue here, as /lease does.
        items: ids.map((id) => leaseData(w, id, now)),
      });
    }
    const res = /^\/v1\/resources\/([^/]+)\/(lease|versions)$/.exec(p);
    if (res && method === 'GET') {
      const id = decodeURIComponent(res[1]!);
      if (!knownResources(w).has(id) && !w.versions[id]) return fail(404, 'NOT_FOUND', 'Resource was not found');
      if (res[2] === 'lease') return json(leaseData(w, id, now));
      const rows = [...(w.versions[id] ?? [])].sort(byNewest((v) => Date.parse(v.at), (v) => v.version.padStart(12, '0')));
      return listed(page(rows, p, q, (v) => Date.parse(v.at), 20));
    }

    const health = /^\/v1\/apps\/([^/]+)\/health$/.exec(p);
    if (health && method === 'GET') {
      const win = timeRange(q, 7 * DAY, now);
      if (win instanceof Response) return win;
      const step = q.get('step') || '1m';
      if (step !== '1m' && step !== '5m') return fail(400, 'INVALID_QUERY', 'step must be 1m or 5m');
      const app = decodeURIComponent(health[1]!);
      const points = (w.telemetry[app] ?? []).filter((x) => x.at >= win.from && x.at < win.to);
      return json({ app, series: step === '1m' ? points.map((x) => ({ ...x, at: iso(x.at) })) : fiveMinutes(points) });
    }

    if (p === '/v1/billing/usage' && method === 'GET') {
      const win = timeRange(q, 366 * DAY, now);
      if (win instanceof Response) return win;
      const n = factsIn(w, win.from, win.to).length;
      const price = w.billing?.price_per_action_micros_usd ?? 20000;
      return json({ checked_actions: n, price_per_action_micros_usd: price, cost_micros_usd: n * price, plan: w.billing?.plan ?? 'team' });
    }

    // What an agent sends when it connects: lumni init's hello.
    if (p === '/v1/actors' && method === 'POST') {
      const b = parsed as unknown as FakeActor;
      // An upsert keeps the first created_at, as the Go API does.
      const created = w.actors.find((x) => x.id === b.id)?.created_at ?? iso(Date.now());
      if (!w.actors.some((x) => x.id === b.id)) changed(w, 'actor.created', b.id);
      w.actors = w.actors.filter((x) => x.id !== b.id).concat({ id: b.id, type: b.type, display_name: b.display_name, runtime: b.runtime, metadata: b.metadata, created_at: created });
      return json({ ...b, created_at: created }, 201);
    }
    if (p === '/v1/work' && method === 'POST') return json({ ...parsed, status: 'ACTIVE' }, 201);
    if (p === '/v1/actions/check' && method === 'POST') {
      const b = parsed as { action_id?: string; actor_id: string; work_id: string; operation: string; idempotency_key?: string; metadata?: Record<string, unknown>; resources?: { type: string; external_id: string; mode: string }[] };
      const id = b.action_id ?? `act_${w.actions.length}`;
      // One rule from the kernel: a repeated idempotency key is already done.
      const repeat = !!b.idempotency_key && w.actions.some((a) => a.meta?.idempotency_key === b.idempotency_key);
      const d = repeat ? { d: 'DEDUPLICATE', r: 'EXACT_EFFECT_ALREADY_COMMITTED' } : { d: 'ALLOW', r: 'NO_CONFLICT' };
      w.actions.push({
        id, at: Date.now(), actor: b.actor_id, work: b.work_id, op: b.operation, status: repeat ? 'REJECTED' : 'RESERVED',
        meta: { summary: 'Said hello to Lumni', ...(b.metadata ?? {}), ...(b.idempotency_key ? { idempotency_key: b.idempotency_key } : {}) },
        decision: { ...d, evals: [] }, resources: (b.resources ?? []).map((r) => ({ id: `${r.type}:${r.type}:${r.external_id}`, mode: r.mode, type: r.type })), cost: 0,
      } as FakeAction);
      changed(w, 'action.checked', id);
      return json({ action_id: id, decision: d.d, reason_code: d.r, enforced: true, ...(repeat ? {} : { lease_token: `lt_${id}` }) });
    }
    const life = /^\/v1\/actions\/([^/]+)\/(start|result)$/.exec(p);
    if (life && method === 'POST') {
      const a = w.actions.find((x) => x.id === decodeURIComponent(life[1]!));
      if (!a) return fail(404, 'NOT_FOUND');
      a.status = life[2] === 'start' ? 'ACTIVE' : (parsed.status as string | undefined) ?? 'SUCCEEDED';
      return json({ action_id: a.id, status: a.status });
    }
    if (p === '/v1/config/mode' && method === 'PUT') {
      w.mode = parsed.mode as string;
      return json({ enforcement_mode: w.mode });
    }
    if (p === '/v1/api-keys' && method === 'POST') {
      const k = { id: `key_${w.seen.length}`, name: parsed.name as string, prefix: 'lumni_ab12', scopes: parsed.scopes as string[], created_at: iso(Date.now()), expires_at: null, revoked_at: null as string | null };
      const secret = `lumni_ab12_${Math.random().toString(36).slice(2, 14)}`;
      keys.push({ ...k, secret });
      return json({ ...k, secret }, 201);
    }
    if (p === '/v1/api-keys' && method === 'GET') return json({ items: keys.map(({ secret: _s, ...k }) => k), next_cursor: null });
    if (p === '/v1/onboarding/codes' && method === 'POST' && !w.noOnboarding) {
      const minutes = (parsed.expires_in_minutes as number | undefined) ?? 15;
      if (minutes < 1 || minutes > 15) return fail(400, 'INVALID_ONBOARDING_CODE', 'Expiry must be 1–15 minutes');
      const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
      const body26 = Array.from({ length: 26 }, () => A[Math.floor(Math.random() * 32)]).join('');
      const code = `LUM-${body26.slice(0, 13)}-${body26.slice(13)}`;
      const expires = now + minutes * MIN;
      codes.set(code, { scopes: (parsed.scopes as string[] | undefined) ?? ['ingest', 'query', 'runtime', 'admission'], actor_hint: String(parsed.actor_hint ?? ''), expires, used_at: null, actor_id: null });
      return json({ code, expires_at: iso(expires) }, 201, { 'cache-control': 'no-store' });
    }
    const codeStatus = /^\/v1\/onboarding\/codes\/([^/]+)\/status$/.exec(p);
    if (codeStatus && method === 'GET' && !w.noOnboarding) {
      const c = codes.get(decodeURIComponent(codeStatus[1]!).trim().toUpperCase());
      if (!c) return fail(404, 'NOT_FOUND', 'Onboarding code was not found');
      return json({ used: !!c.used_at, used_at: c.used_at, actor_id: c.actor_id, host: c.host ?? null, project: c.project ?? null, key_id: c.key_id ?? null });
    }
    const keyId = /^\/v1\/api-keys\/([^/]+)$/.exec(p);
    if (keyId && method === 'DELETE') {
      const k = keys.find((x) => x.id === decodeURIComponent(keyId[1]!));
      if (!k) return fail(404, 'NOT_FOUND', 'API key was not found');
      k.revoked_at ??= iso(Date.now());
      return json(k);
    }
    if (p === '/v1/actions' && method === 'GET') {
      const statuses = csv(q.get('status'));
      const actorId = q.get('actor_id');
      const op = q.get('operation');
      const work = q.get('work_id');
      const rows = w.actions
        .filter((a) => (!statuses.length || statuses.includes(a.status)) && (!actorId || a.actor === actorId) && (!op || a.op === op) && (!work || a.work === work))
        .sort(byNewest((a) => a.at, (a) => a.id));
      const r = page(rows, p, q, (a) => a.at);
      return r instanceof Response ? r : json({ items: r.items.map(actionRow), next_cursor: r.next_cursor });
    }
    const journey = /^\/v1\/actions\/([^/]+)\/journey$/.exec(p);
    if (journey && method === 'GET') {
      const a = w.actions.find((x) => x.id === decodeURIComponent(journey[1]!));
      if (!a) return fail(404, 'NOT_FOUND', 'Action was not found');
      const ap = w.approvals.filter((x) => x.action_id === a.id).sort(byNewest((x) => Date.parse(x.requested_at), (x) => x.id))[0];
      const attempts = a.outcome?.verifier
        ? Array.from({ length: a.outcome.attempts ?? 1 }, (_, i) => ({
            id: `va_${a.id}_${i + 1}`, outcome_id: `out_${a.id}`, attempt: i + 1, verifier_id: a.outcome!.verifier,
            status: i + 1 < (a.outcome!.attempts ?? 1) ? 'RETRY' : a.outcome!.status, created_at: iso(a.at + 2000 * (i + 1)),
          }))
        : [];
      return json({
        action: { ...actionRow(a), resources: (a.resources ?? []).map((r) => ({ action_id: a.id, resource_id: r.id, mode: r.mode })) },
        decision: a.decision ? decisionRow({ ...a, decision: { ...a.decision, existing: duplicateOf(a, w) ?? undefined } }) : null,
        earlier: [],
        approval: ap ? { ...ap, status: approvalStatus(ap, now), policy_id: undefined } : null,
        outcome: a.outcome ? outcomeRow(a) : null,
        attempts,
        duplicate_of: duplicateOf(a, w),
      });
    }
    const one = /^\/v1\/actions\/([^/]+)$/.exec(p);
    if (one && method === 'GET') {
      const a = w.actions.find((x) => x.id === decodeURIComponent(one[1]!));
      if (!a) return fail(404, 'NOT_FOUND');
      return json({
        ...actionRow(a),
        ...(a.decision ? { latest_decision: { receipt_id: `rcpt_${a.id}`, decision: a.decision.d, reason_code: a.decision.r, enforced: !a.decision.d.startsWith('WOULD_'), evidence: [], evaluations: a.decision.evals ?? [], kernel_version: 'k1', created_at: iso(a.decision.at ?? a.at) } } : {}),
      });
    }
    if (p === '/v1/outcomes') {
      return json({ items: w.actions.filter((a) => a.outcome).slice(0, 200).map((a) => outcomeRow(a)) });
    }
    const out = /^\/v1\/outcomes\/([^/]+)$/.exec(p);
    if (out && method === 'GET') {
      const a = w.actions.find((x) => `out_${x.id}` === decodeURIComponent(out[1]!));
      return json({ items: a?.outcome ? [outcomeRow(a)] : [] });
    }
    if (/^\/v1\/outcomes\/[^/]+\/retry$/.test(p)) return json({ queued: true }, 202);
    if (/^\/v1\/verification-attempts\//.test(p)) return json({ items: [] });
    if (p === '/v1/graph/snapshot') {
      const from = Date.parse(q.get('from') ?? '') || Date.now() - 86_400_000;
      const to = Math.min(Date.now(), Date.parse(q.get('to') ?? '') || Date.now());
      return json(snapshot(w, from, to));
    }
    if (p === '/v1/graph/live') {
      const edges = w.actions.filter((a) => ['PROPOSED', 'AWAITING_APPROVAL', 'RESERVED', 'ACTIVE'].includes(a.status)).flatMap((a) => (a.resources ?? []).map((r) => ({ action_id: a.id, operation: a.op, status: a.status, resource_id: r.id, mode: r.mode })));
      return json({ edges: edges.length ? edges : null });
    }
    if (p === '/v1/finops/graph') return json(finops(w, q, now));
    if (p === '/v1/graph/stream') {
      // Like Go: start at the head, or after Last-Event-ID / cursor; send what's new and close (the browser reconnects).
      const log = w.changes ?? [];
      const head = log.at(-1)?.seq ?? 100;
      const after = Number(headers['last-event-id'] ?? q.get('cursor') ?? head);
      const types = csv(q.get('types'));
      const out = log.filter((c) => c.seq > after && (!types.length || types.includes(c.type)));
      const events = out.map((c) => `event: invalidate\nid: ${c.seq}\ndata: ${JSON.stringify({ sequence: c.seq, event_type: c.type, entity_type: c.type.split('.')[0], entity_id: c.entity, occurred_at: iso(c.at) })}\n\n`).join('');
      return new Response(`retry: 1000\n\nevent: ready\nid: ${after}\ndata: {"cursor":${after}}\n\n${events}`, { headers: { 'content-type': 'text/event-stream' } });
    }
    return fail(404, 'NOT_FOUND', `fake has no ${method} ${p}`);
  };
}

/** What Go snapshots into an approval.decided entry: who asked, and what for. */
export function approvalContext(w: FakeWorld, actionId: string) {
  const a = w.actions.find((x) => x.id === actionId);
  if (!a) return {};
  const summary = typeof a.meta?.summary === 'string' && a.meta.summary ? a.meta.summary : a.op;
  return { actor_id: a.actor, operation: a.op, summary };
}

function limitRow(w: FakeWorld, l: FakeLimit) {
  const matches = (a: FakeAction) => scopeMatches(l.scope, a.op, a.actor, (a.resources ?? []).map((r) => r.id));
  return {
    ...l,
    in_flight_now: w.actions.filter((a) => ['RESERVED', 'ACTIVE', 'UNKNOWN'].includes(a.status) && matches(a)).length,
    waiting_now: w.actions.filter((a) => waiting(a) && matches(a)).length,
  };
}

/** Five-minute cells from minute cells: the middle of the medians, the worst p95, every error and timeout. */
function fiveMinutes(points: TelemetryPoint[]) {
  const cells = new Map<number, TelemetryPoint[]>();
  for (const x of points) {
    const k = Math.floor(x.at / (5 * MIN)) * 5 * MIN;
    cells.set(k, [...(cells.get(k) ?? []), x]);
  }
  return [...cells].sort((a, b) => a[0] - b[0]).map(([at, xs]) => ({
    at: iso(at), p50_ms: percentile(xs.map((x) => x.p50_ms), 0.5)!, p95_ms: Math.max(...xs.map((x) => x.p95_ms)), errors: xs.reduce((s, x) => s + x.errors, 0), timeouts: xs.reduce((s, x) => s + x.timeouts, 0),
  }));
}
