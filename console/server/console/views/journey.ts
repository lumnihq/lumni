// /v1/console/journey/:id with real data: one action's trip through Lumni, as
// the Journey screen replays it. The four checks come from the kernel's own
// evaluations, the proof from the outcome the app reported, and nothing is
// added: a check that didn't run reads as passed only when the kernel says so.

import { HttpError } from '../../http';
import type { Ctx } from '../load';
import { appOf, customerOf, didOf, evaluations, isBad, stepDecision, stepKind, whoOf, wouldKind, type Act } from '../model';
import { amountOf, keptMoney, reasonWords, stepText, wouldText } from '../words';
import { checkers } from './shared';
import { decisionFromGo } from '../load';
import { outcomeStatus } from '../day';

const STAGES = { FRESHNESS: 'fresh', IDEMPOTENCY: 'done', LEASE: 'other', POLICY: 'rule' } as const;

function clock(at: number, ctx: Ctx) {
  const d = new Date(at + ctx.zone.offset(at));
  const h = d.getUTCHours();
  return `${((h + 11) % 12) + 1}:${String(d.getUTCMinutes()).padStart(2, '0')}:${String(d.getUTCSeconds()).padStart(2, '0')} ${h < 12 ? 'AM' : 'PM'}`;
}

export interface ApiJourney {
  action: { id: string; operation: string; actor_id: string; work_id?: string; status: string; risk?: string; requested_at: string; metadata?: Record<string, unknown>; resources?: { resource_id?: string; mode?: string }[] };
  decision?: Parameters<typeof decisionFromGo>[0];
  earlier?: Parameters<typeof decisionFromGo>[0][];
  approval?: { id?: string; status?: string; requested_at?: string; resolved_at?: string | null; expires_at?: string | null; resolved_by?: string | null };
  outcome?: { id?: string; status?: string; verifier_id?: string | null; attempts?: number; reason?: string; created_at?: string };
  attempts?: Record<string, unknown>[];
  duplicate_of?: string | null;
}

const at = (v?: string | null) => (v ? Date.parse(v) : null);

/** The Console API's one-call journey, as the console's action row. */
export function actFrom(j: ApiJourney): Act {
  const x = j.action;
  const decision = j.decision ? decisionFromGo(j.decision) : null;
  const latestAllows = !decision || decision.d === 'ALLOW' || decision.d === 'WOULD_ALLOW';
  const firstEarlier = (j.earlier ?? []).find((e) => e.decision !== 'ALLOW' && e.decision !== 'WOULD_ALLOW');
  return {
    id: x.id, at: Date.parse(x.requested_at), op: x.operation, tool: null, status: x.status as Act['status'], risk: x.risk ?? null,
    meta: x.metadata ?? {}, actor: x.actor_id, work: x.work_id ?? null, cost: 0,
    decision, earlier: latestAllows && firstEarlier ? decisionFromGo(firstEarlier) : null,
    outcome: j.outcome?.id ? { id: j.outcome.id, status: outcomeStatus(j.outcome.status), verifier: j.outcome.verifier_id ?? null, attempts: j.outcome.attempts ?? 0, reason: j.outcome.reason ?? '', updatedAt: at(j.outcome.created_at) } : null,
    resources: (x.resources ?? []).filter((r) => r.resource_id).map((r) => ({ id: r.resource_id!, mode: r.mode ?? 'WRITE' })),
    approval: j.approval?.id ? { id: j.approval.id, status: j.approval.status ?? 'PENDING', at: at(j.approval.requested_at) ?? Date.parse(x.requested_at), resolvedAt: at(j.approval.resolved_at), expiresAt: at(j.approval.expires_at) } : null,
  };
}

/** How old the AI's read was, when the freshness check recorded it. */
function freshnessOf(d: ReturnType<typeof decisionFromGo> | null) {
  const e = d?.evals.find((x) => x.stage === 'resource_versions');
  const r = (e?.details as { resources?: { resource_id?: string; observed_version?: string; current_version?: string; age_ms?: number; changed_by_actor_id?: string }[] } | undefined)?.resources?.[0];
  return r ? { resource: r.resource_id ?? null, observed: r.observed_version ?? null, current: r.current_version ?? null, ageMs: r.age_ms ?? null, changedBy: r.changed_by_actor_id ?? null } : null;
}

export async function journeyView(ctx: Ctx, id: string) {
  // The Console API answers in one call; older APIs need the action and its outcome read apart.
  let a: Act | null = null;
  let api: ApiJourney | null = null;
  try {
    api = await ctx.get<ApiJourney>(`/v1/actions/${encodeURIComponent(id)}/journey`);
    a = actFrom(api);
  } catch (e) {
    if (e instanceof HttpError && e.status === 404 && !/journey/i.test(e.message)) {
      // Either no such action, or no journey route: try the old way to tell which.
    } else if (!(e instanceof HttpError) || ![404, 405, 501].includes(e.status)) throw e;
  }
  if (!a) a = await ctx.one(id);
  if (!a) throw new HttpError(404, 'not_found', 'No action with that id in this workspace.');
  const [actors, chk, out] = await Promise.all([ctx.actors(), checkers(ctx), api ? Promise.resolve(null) : ctx.outcome(a.id).catch(() => null)]);
  const w = whoOf(actors, a.actor);
  const app = appOf(a);
  const d = stepDecision(a);
  const step = stepKind(a);
  const would = wouldKind(a);
  const evals = evaluations(d);
  const amount = amountOf(a);
  const outcome = out ?? a.outcome;
  const bad = isBad(a);

  // Where it ended: stopped at a check, asked a person, or went on to the app.
  const end = step === 'stop' ? 'stop' : step === 'ask' ? 'ask' : bad ? 'bad' : outcome?.status === 'VERIFIED' ? 'proven' : 'blind';
  const stages = evals.map((e) => ({
    id: STAGES[e.stage],
    stage: e.stage,
    hit: e.outcome === 'HIT',
    kind: e.outcome !== 'HIT' ? 'pass' : step ?? would ?? 'stop',
    words: e.outcome === 'HIT' ? reasonWords(e.reason_code ?? d!.r) : null,
    would: e.outcome === 'HIT' && !!would && !step,
  }));

  return {
    id: a.id,
    receipt: d?.receipt ?? null,
    at: a.at, time: clock(a.at, ctx),
    who: { name: w.name, initials: w.initials, human: w.human },
    did: didOf(a), customer: customerOf(a), amount,
    app: { id: app.id, name: app.name, checked: !!chk.byApp.get(app.id)?.enabled },
    title: step ? stepText(a) : would ? wouldText(a) : didOf(a),
    decision: d ? { type: d.d, reason: d.r, words: reasonWords(d.r), enforced: d.enforced } : null,
    stages,
    end,
    kept: keptMoney(a),
    approval: a.approval,
    status: a.status,
    freshness: freshnessOf(d ?? a.decision),
    attempts: api?.attempts?.length ?? a.outcome?.attempts ?? 0,
    duplicateOf: api?.duplicate_of ?? null,
    outcome: outcome ? { id: outcome.id, status: outcome.status, reason: outcome.reason || null, attempts: outcome.attempts ?? 0 } : null,
  };
}
