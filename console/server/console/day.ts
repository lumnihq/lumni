// Reading a stretch of the day, action by action.
//
// The Go API has no paged, time-filtered list of actions (its lists stop at
// the newest 200), so this reads graph snapshots instead: the transaction lens
// gives every action in a window with its latest decision, its checks, its
// outcome and the things it touched. A snapshot holds at most 500 nodes, so a
// window that comes back cut off is read again in halves, down to a couple of
// minutes; whatever is still cut off then is marked partial. Finished windows
// are kept for a minute. Once the API can list actions by time, this file goes.

import type { ActionStatus, DecisionType, OutcomeStatus, ReasonCode } from '../../src/domain/types';
import { HOUR, MIN } from '../../src/domain/format';
import { ms, obj } from '../upstream';
import type { GoEdge, GoEvaluation, GoNode, GoSnapshot } from './go';
import type { Act, Dec } from './model';

/** Something an action touched: a charge, an account, a ticket. */
export interface Res {
  id: string;
  type: string;
  ns: string;
  ext: string;
  version: string | null;
  /** What it’s called, when whoever made it said. */
  name: string | null;
}

export interface WindowRead {
  acts: Act[];
  resources: Res[];
  /** Cut off even at the smallest window. */
  partial: boolean;
  /** Snapshot calls it took. */
  calls: number;
}

export interface DayRead {
  acts: Act[];
  resources: Map<string, Res>;
  partial: boolean;
}

/** Smallest window worth splitting again. */
const MIN_SPAN = 2 * MIN;
/** How long a finished window is trusted before it’s read again. */
const CLOSED_FOR = 60_000;
/** A window this close to now may still change. */
const SETTLING = 2 * MIN;
/** Snapshots in flight at once, per day read. */
const PARALLEL = 4;

export type Snap = (from: number, to: number) => Promise<GoSnapshot>;

export interface WindowCache {
  get(key: string): { at: number; value: Promise<WindowRead> } | undefined;
  set(key: string, v: { at: number; value: Promise<WindowRead> }): void;
  delete(key: string): void;
}

/** Hour windows, cut at the hour, from `from` to `to`. */
export function windows(from: number, to: number): [number, number][] {
  const out: [number, number][] = [];
  let a = from;
  while (a < to) {
    const b = Math.min(to, Math.floor(a / HOUR) * HOUR + HOUR);
    out.push([a, b]);
    a = b;
  }
  return out;
}

export async function readDay(snap: Snap, cache: WindowCache, key: string, from: number, to: number, now: number): Promise<DayRead> {
  const end = Math.min(to, now);
  const list = windows(from, end);
  const reads: WindowRead[] = new Array(list.length);
  let next = 0;
  const worker = async () => {
    while (next < list.length) {
      const i = next++;
      const [a, b] = list[i]!;
      reads[i] = await cached(snap, cache, `${key}|${a}|${b}`, a, b, now);
    }
  };
  await Promise.all(Array.from({ length: Math.min(PARALLEL, list.length) }, worker));
  const byId = new Map<string, Act>();
  const resources = new Map<string, Res>();
  let partial = false;
  for (const r of reads) {
    partial ||= r.partial;
    // Each read gets its own copies, since views fill them in.
    for (const a of r.acts) byId.set(a.id, { ...a, resources: [...a.resources], meta: a.meta });
    for (const x of r.resources) resources.set(x.id, x);
  }
  return { acts: [...byId.values()].sort((x, y) => x.at - y.at), resources, partial };
}

function cached(snap: Snap, cache: WindowCache, key: string, a: number, b: number, now: number): Promise<WindowRead> {
  const hit = cache.get(key);
  const closed = b <= now - SETTLING;
  if (hit && closed && now - hit.at < CLOSED_FOR) return hit.value;
  const value = readWindow(snap, a, b);
  // Only finished windows are worth keeping; the open one is read on every look.
  if (closed) {
    cache.set(key, { at: now, value });
    value.catch(() => cache.delete(key));
  }
  return value;
}

export async function readWindow(snap: Snap, a: number, b: number): Promise<WindowRead> {
  const s = await snap(a, b);
  if (s.truncated && b - a > MIN_SPAN) {
    const mid = a + Math.floor((b - a) / 2 / 1000) * 1000;
    const [l, r] = await Promise.all([readWindow(snap, a, mid), readWindow(snap, mid, b)]);
    return { acts: [...l.acts, ...r.acts], resources: [...l.resources, ...r.resources], partial: l.partial || r.partial, calls: l.calls + r.calls + 1 };
  }
  return { acts: parseSnapshot(s).filter((x) => x.at >= a && x.at < b), resources: parseResources(s), partial: s.truncated, calls: 1 };
}

// ─── one snapshot, as actions ──────────────────────────

const STATUSES: ActionStatus[] = ['PROPOSED', 'AWAITING_APPROVAL', 'RESERVED', 'ACTIVE', 'SUCCEEDED', 'FAILED', 'UNKNOWN', 'EXPIRED', 'CANCELLED'];
const OUTCOMES: OutcomeStatus[] = ['VERIFIED', 'MISMATCH', 'PARTIAL', 'FAILED', 'PENDING', 'UNKNOWN'];

export function actionStatus(s: unknown): ActionStatus {
  return STATUSES.includes(s as ActionStatus) ? (s as ActionStatus) : 'PROPOSED';
}

export function outcomeStatus(s: unknown): OutcomeStatus {
  return OUTCOMES.includes(s as OutcomeStatus) ? (s as OutcomeStatus) : 'UNKNOWN';
}

/** Every action in a transaction-lens snapshot, with what hangs off it. */
export function parseSnapshot(s: GoSnapshot): Act[] {
  const nodes = new Map<string, GoNode>();
  for (const n of s.nodes ?? []) nodes.set(n.id, n);
  const from = new Map<string, GoEdge[]>();
  for (const e of s.edges ?? []) {
    const l = from.get(e.source);
    if (l) l.push(e);
    else from.set(e.source, [e]);
  }

  const out: Act[] = [];
  for (const n of nodes.values()) {
    if (n.type !== 'action') continue;
    const at = ms(n.occurred_at);
    if (at === null) continue;
    const act: Act = {
      id: n.entity_id,
      at,
      op: n.label,
      tool: null,
      status: actionStatus(n.status),
      risk: n.risk ?? null,
      meta: obj(n.metadata),
      actor: null,
      work: null,
      cost: n.cost_micros_usd ?? 0,
      decision: null,
      earlier: null,
      outcome: null,
      resources: [],
      approval: null,
    };
    for (const e of from.get(n.id) ?? []) {
      const t = nodes.get(e.target);
      if (!t) continue;
      if (e.type === 'EVALUATED_AS' && t.type === 'decision') act.decision = decisionOf(t, nodes, from);
      else if (e.type === 'EXPECTED_OUTCOME' && t.type === 'outcome') {
        act.outcome = { id: t.entity_id, status: outcomeStatus(t.status), verifier: null, attempts: 0, reason: '', updatedAt: ms(t.occurred_at) };
      } else if (t.type === 'resource') act.resources.push({ id: t.entity_id, mode: e.type });
      else if (e.type === 'REQUIRES' && t.type === 'approval') {
        act.approval = { id: t.entity_id, status: t.status ?? 'PENDING', at: ms(t.occurred_at) ?? at, resolvedAt: null, expiresAt: null };
      }
    }
    out.push(act);
  }
  return out;
}

/** Resource ids read `namespace:type:external id`, each part escaped. */
export function splitResourceId(id: string): { ns: string; type: string; ext: string } {
  const parts = id.split(':').map((p) => {
    try {
      return decodeURIComponent(p);
    } catch {
      return p;
    }
  });
  if (parts.length >= 3) return { ns: parts[0]!, type: parts[1]!, ext: parts.slice(2).join(':') };
  return { ns: '', type: parts[0] ?? '', ext: parts.slice(1).join(':') || id };
}

export function parseResources(s: GoSnapshot): Res[] {
  const out: Res[] = [];
  for (const n of s.nodes ?? []) {
    if (n.type !== 'resource') continue;
    const split = splitResourceId(n.entity_id);
    // The subtitle reads `namespace:external @ version`.
    const at = (n.subtitle ?? '').lastIndexOf(' @ ');
    const meta = obj(n.metadata);
    const name = [meta.name, meta.display_name, meta.title].find((v): v is string => typeof v === 'string' && !!v.trim()) ?? null;
    out.push({ id: n.entity_id, type: n.label || split.type, ns: split.ns, ext: split.ext, version: at >= 0 ? n.subtitle!.slice(at + 3) : null, name });
  }
  return out;
}

function decisionOf(n: GoNode, nodes: Map<string, GoNode>, from: Map<string, GoEdge[]>): Dec {
  const evals: GoEvaluation[] = [];
  // The checks hang off the decision as a chain: decision, first stage, second stage, …
  let cur = n.id;
  const seen = new Set<string>();
  for (;;) {
    const next = (from.get(cur) ?? []).find((e) => e.type === 'NEXT_STAGE');
    if (!next || seen.has(next.target)) break;
    seen.add(next.target);
    const st = nodes.get(next.target);
    if (!st) break;
    evals.push({ stage: st.label, outcome: st.status ?? '', reason_code: st.subtitle || undefined, details: obj(st.metadata) });
    cur = st.id;
  }
  return {
    receipt: n.entity_id,
    d: (n.label || 'ALLOW') as DecisionType,
    r: (n.subtitle || 'NO_CONFLICT') as ReasonCode,
    at: ms(n.occurred_at) ?? 0,
    enforced: null,
    evals,
    policy: null,
  };
}
