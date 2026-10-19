// Everything a real view reads, for one request: the lists, each read once,
// and the actions of a day joined with who asked, the step-ins before the
// latest decision, and whether the app agrees. What never changes about an
// action is remembered per workspace, so it’s fetched once.

import type { DecisionType, ReasonCode } from '../../src/domain/types';
import { DAY, type Zone } from '../../src/domain/format';
import type { Env } from '../env';
import { HttpError } from '../http';
import { items, ms, obj, str, type Caller, type Upstream } from '../upstream';
import { actionStatus, outcomeStatus, readDay, type DayRead, type WindowCache, type WindowRead } from './day';
import type {
  GoAction, GoActionRow, GoActor, GoAgent, GoApproval, GoConfig, GoEnforcement, GoFinOps, GoIncident, GoLiveEdge, GoOutcome,
  GoPolicyRule, GoSnapshot, GoVerifier,
} from './go';
import type { Act, Dec, Out } from './model';

/** What never changes about an action once it exists. */
interface Facts {
  actor: string;
  work: string;
  tool: string | null;
  op: string;
}

/** What the console remembers about one workspace, while the server runs. */
export interface TenantState {
  facts: Map<string, Facts>;
  /** Outcomes that won’t change again. */
  outcomes: Map<string, Out>;
  windows: WindowCache & Map<string, { at: number; value: Promise<WindowRead> }>;
  /** A word you’re trying in a rule, before you save it. */
  drafts: Map<string, { token: string; value: string }>;
  /** A rule as it was before the last save, for undo. */
  undo: Map<string, Record<string, unknown>>;
  /** When a rule was switched to protecting from the console. */
  protectedAt: Map<string, number>;
  /** Screens opened, for their first-time moments. */
  seen: Set<string>;
}

export function newTenantState(): TenantState {
  return { facts: new Map(), outcomes: new Map(), windows: new Map(), drafts: new Map(), undo: new Map(), protectedAt: new Map(), seen: new Set() };
}

/** How many single-action reads one request may make, so a cold start can’t stall a page. */
const DETAIL_BUDGET = 400;
const PARALLEL = 8;

const once = <T>(fn: () => Promise<T>): (() => Promise<T>) => {
  let p: Promise<T> | null = null;
  return () => (p ??= fn());
};

const iso = (t: number) => new Date(t).toISOString().replace(/\.\d{3}Z$/, 'Z');

/** Run `fn` over `xs`, a few at a time. */
async function pool<T, R>(xs: T[], n: number, fn: (x: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(xs.length);
  let i = 0;
  await Promise.all(Array.from({ length: Math.min(n, xs.length) }, async () => {
    while (i < xs.length) {
      const k = i++;
      out[k] = await fn(xs[k]!);
    }
  }));
  return out;
}

export function decisionFromGo(d: { receipt_id: string; decision: string; reason_code: string; enforced?: boolean; created_at: string; evaluations?: unknown; evidence?: unknown }): Dec {
  const evidence = Array.isArray(d.evidence) ? (d.evidence as { type?: string; data?: Record<string, unknown> }[]) : [];
  const policy = evidence.find((e) => e.type === 'POLICY')?.data?.policy_id;
  return {
    receipt: d.receipt_id,
    d: d.decision as DecisionType,
    r: d.reason_code as ReasonCode,
    at: ms(d.created_at) ?? 0,
    enforced: d.enforced ?? null,
    evals: Array.isArray(d.evaluations) ? (d.evaluations as Dec['evals']) : [],
    policy: typeof policy === 'string' ? policy : null,
  };
}

export function outcomeFromGo(o: GoOutcome): Out {
  return {
    id: o.id,
    status: outcomeStatus(o.status),
    verifier: o.verifier_id,
    attempts: o.attempts ?? 0,
    reason: o.reason ?? '',
    updatedAt: ms(o.updated_at),
    expected: o.expected,
    observed: o.observed,
  };
}

const FINAL = new Set(['VERIFIED', 'MISMATCH', 'PARTIAL', 'FAILED']);

export class Ctx {
  readonly partial: string[] = [];
  private details = 0;

  constructor(
    readonly up: Upstream,
    readonly who: Caller,
    readonly tenant: string,
    readonly state: TenantState,
    readonly zone: Zone,
    readonly now: number,
    readonly env: Env,
  ) {}

  get<T>(path: string, fresh?: number): Promise<T> {
    return this.up.get<T>(path, this.who, { fresh });
  }

  send<T>(method: 'POST' | 'PUT' | 'DELETE', path: string, body: unknown): Promise<T> {
    return this.up.send<T>(method, path, body, this.who);
  }

  // ─── lists, each read at most once per request ──────

  readonly actors = once(async () => new Map(items<GoActor>(await this.get('/v1/actors')).map((a) => [a.id, a])));
  readonly agents = once(async () => items<GoAgent>(await this.get('/v1/agents')));
  readonly approvals = once(async () => items<GoApproval>(await this.get('/v1/approvals')));
  readonly incidents = once(async () => items<GoIncident>(await this.get('/v1/incidents')));
  readonly outcomeList = once(async () => items<GoOutcome>(await this.get('/v1/outcomes')));
  readonly recentActions = once(async () => items<GoActionRow>(await this.get('/v1/actions')));
  readonly verifiers = once(async () => items<GoVerifier>(await this.get('/v1/verifiers')));
  readonly policyRules = once(async () => items<GoPolicyRule>(await this.get('/v1/config/policy-rules')));
  readonly enforcement = once(async () => items<GoEnforcement>(await this.get('/v1/enforcement')));
  readonly config = once(() => this.get<GoConfig>('/v1/config'));
  readonly live = once(async () => {
    const v = await this.get<{ edges: GoLiveEdge[] | null }>('/v1/graph/live');
    return v.edges ?? [];
  });

  finops(from: number, to: number): Promise<GoFinOps> {
    return this.get<GoFinOps>(`/v1/finops/graph?from=${iso(from)}&to=${iso(Math.max(to, from + 1000))}`);
  }

  // ─── days ───────────────────────────────────────────

  private days = new Map<string, Promise<DayRead>>();

  /** Every action from `from` to `to`, joined, oldest first. */
  day(from: number, to: number): Promise<DayRead> {
    const key = `${from}|${to}`;
    let p = this.days.get(key);
    if (!p) {
      p = this.readJoined(from, to);
      this.days.set(key, p);
    }
    return p;
  }

  /** Today, from local midnight to now. */
  today() {
    const from = this.zone.dayStart(this.now);
    return this.day(from, this.now);
  }

  /** Yesterday, midnight to midnight. */
  yesterday() {
    const to = this.zone.dayStart(this.now);
    return this.day(this.zone.dayStart(to - DAY / 2), to);
  }

  private async readJoined(from: number, to: number) {
    const snap = (a: number, b: number) =>
      this.get<GoSnapshot>(`/v1/graph/snapshot?lens=transaction&from=${iso(a)}&to=${iso(b)}&limit=500`, b <= this.now - 120_000 ? 30_000 : undefined);
    const read = await readDay(snap, this.state.windows, `${this.tenant}|tx`, from, to, this.now);
    if (read.partial) this.note('Some busy minutes had more than Lumni could read at once, so a few actions may be missing.');
    await this.join(read.acts);
    return read;
  }

  /** Fill in what a snapshot leaves out: who asked, earlier step-ins, approvals, outcomes. */
  async join(acts: Act[]): Promise<void> {
    if (!acts.length) return;
    const [recent, approvals, incidents, outcomes] = await Promise.all([this.recentActions(), this.approvals(), this.incidents(), this.outcomeList()]);
    const facts = this.state.facts;
    for (const r of recent) {
      if (!facts.has(r.id)) facts.set(r.id, { actor: r.actor_id, work: r.work_id, tool: str(r.tool), op: r.operation });
    }

    // Who asked: one read per action the lists don’t cover, remembered for good.
    const missing = acts.filter((a) => !facts.has(a.id));
    const room = Math.max(0, DETAIL_BUDGET - this.details);
    const fetch = missing.slice(-room);
    this.details += fetch.length;
    await pool(fetch, PARALLEL, async (a) => {
      const d = await this.action(a.id);
      if (d) facts.set(a.id, { actor: d.actor_id, work: d.work_id, tool: str(d.tool), op: d.operation });
    });
    if (missing.length > fetch.length) this.note('Lumni is still reading who did some of the older actions. They’re grouped under “Not read yet” until it has.');

    const approvalByAction = new Map<string, GoApproval>();
    for (const ap of approvals) {
      const cur = approvalByAction.get(ap.action_id);
      // The newest approval for an action is the one that counts.
      if (!cur || (ms(ap.requested_at) ?? 0) > (ms(cur.requested_at) ?? 0)) approvalByAction.set(ap.action_id, ap);
    }
    const findings = new Map<string, GoIncident[]>();
    for (const i of incidents) {
      if (!i.action_id || !i.id.startsWith('finding_')) continue;
      const l = findings.get(i.action_id);
      if (l) l.push(i);
      else findings.set(i.action_id, [i]);
    }
    const outcomeByAction = new Map(outcomes.map((o) => [o.action_id, o]));

    const needOutcome: Act[] = [];
    for (const a of acts) {
      const f = facts.get(a.id);
      if (f) {
        a.actor = f.actor;
        a.work = f.work;
        a.tool = f.tool;
      }
      const ap = approvalByAction.get(a.id);
      if (ap) {
        a.approval = { id: ap.id, status: ap.status, at: ms(ap.requested_at) ?? a.at, resolvedAt: ms(ap.resolved_at), expiresAt: ms(ap.expires_at) };
      }
      // When the latest decision let it go, the step-in before it still counts.
      const latestAllows = !a.decision || a.decision.d === 'ALLOW' || a.decision.d === 'WOULD_ALLOW';
      if (latestAllows) {
        const first = (findings.get(a.id) ?? []).sort((x, y) => (ms(x.created_at) ?? 0) - (ms(y.created_at) ?? 0))[0];
        const ev = obj(first?.evidence);
        if (first && typeof ev.decision === 'string') {
          a.earlier = {
            receipt: String(ev.receipt_id ?? first.id.slice('finding_'.length)),
            d: ev.decision as DecisionType,
            r: String(ev.reason_code ?? first.kind) as ReasonCode,
            at: ms(first.created_at) ?? a.at,
            enforced: typeof ev.enforced === 'boolean' ? ev.enforced : null,
            evals: [],
            policy: null,
          };
        } else if (ap) {
          a.earlier = { receipt: ap.id, d: 'REQUIRE_APPROVAL', r: ap.reason_code as ReasonCode, at: ms(ap.requested_at) ?? a.at, enforced: true, evals: [], policy: null };
        }
      }
      const listed = outcomeByAction.get(a.id);
      const kept = this.state.outcomes.get(a.id);
      if (listed) a.outcome = { ...outcomeFromGo(listed), status: outcomeStatus(listed.status) };
      else if (kept) a.outcome = kept;
      else if (!a.outcome || !FINAL.has(a.outcome.status)) needOutcome.push(a);
      if (a.outcome && FINAL.has(a.outcome.status)) this.state.outcomes.set(a.id, a.outcome);
    }

    // Outcomes the snapshot and the list both missed, newest first, a few at a time.
    const room2 = Math.max(0, DETAIL_BUDGET - this.details);
    const look = needOutcome.filter((a) => !a.outcome).slice(-room2);
    this.details += look.length;
    await pool(look, PARALLEL, async (a) => {
      const o = await this.outcome(a.id);
      if (!o) return;
      a.outcome = o;
      if (FINAL.has(o.status)) this.state.outcomes.set(a.id, o);
    });
  }

  // ─── one record ─────────────────────────────────────

  async action(id: string): Promise<GoAction | null> {
    try {
      return await this.get<GoAction>(`/v1/actions/${encodeURIComponent(id)}`, 5000);
    } catch (e) {
      if (e instanceof HttpError && e.status === 404) return null;
      throw e;
    }
  }

  /** An action’s outcome by its id: out_<action id>. */
  async outcome(actionId: string): Promise<Out | null> {
    const kept = this.state.outcomes.get(actionId);
    if (kept) return kept;
    const v = await this.get<{ items: GoOutcome[] | null }>(`/v1/outcomes/${encodeURIComponent(`out_${actionId}`)}`);
    const o = items<GoOutcome>(v)[0];
    return o ? outcomeFromGo(o) : null;
  }

  /** What one action did, from what the console has read or the action itself. */
  async opOf(actionId: string): Promise<string | null> {
    const f = this.state.facts.get(actionId);
    if (f) return f.op;
    if (this.details >= DETAIL_BUDGET) return null;
    this.details += 1;
    const d = await this.action(actionId).catch(() => null);
    if (!d) return null;
    this.state.facts.set(actionId, { actor: d.actor_id, work: d.work_id, tool: str(d.tool), op: d.operation });
    return d.operation;
  }

  /** Who asked for one action, the same way. */
  actorOf(actionId: string): string | null {
    return this.state.facts.get(actionId)?.actor ?? null;
  }

  /** One action, joined, whatever day it’s from. */
  async one(id: string): Promise<Act | null> {
    const d = await this.action(id);
    if (!d) return null;
    const at = ms(d.requested_at) ?? this.now;
    // Its day holds what the action alone doesn’t: its resources, its cost.
    const day = await this.day(this.zone.dayStart(at), Math.min(this.now, this.zone.dayStart(at) + DAY));
    const found = day.acts.find((a) => a.id === id);
    const act: Act = found ?? {
      id, at, op: d.operation, tool: str(d.tool), status: actionStatus(d.status), risk: d.risk ?? null, meta: obj(d.metadata),
      actor: d.actor_id, work: d.work_id, cost: 0, decision: null, earlier: null, outcome: null, resources: [], approval: null,
    };
    if (d.latest_decision) act.decision = decisionFromGo(d.latest_decision);
    act.actor = d.actor_id;
    act.work = d.work_id;
    act.status = actionStatus(d.status);
    if (!found) await this.join([act]);
    const o = await this.outcome(id).catch(() => null);
    if (o) act.outcome = o;
    return act;
  }

  note(text: string): void {
    if (!this.partial.includes(text)) this.partial.push(text);
  }
}
