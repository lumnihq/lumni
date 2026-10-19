// /v1/console/pulse: what just happened, for Today's live mode.
//
//   ?after=<ms>   only actions after this moment (default: the last 10 minutes)
//
// The totals are exact at any volume: the day's analytics. The events are the
// newest few actions after `after`, each with its decision, read from its
// journey; anything beyond those is counted in `more`, never invented. A busy
// workspace (millions a day) still costs one list, a few journeys and one
// aggregate per call, and the journeys are remembered.

import { MIN } from '../../../src/domain/format';
import { isWould, stepOf, verdictOf } from '../../../src/domain/vocab';
import type { Ctx } from '../load';
import { appOf, didOf, isBad, stepDecision, whoOf, type Act } from '../model';
import { amountOf, reasonWords } from '../words';
import { actFrom, type ApiJourney } from './journey';
import { items } from '../../upstream';

/** Events told in full per call; the rest are counted. */
const DETAIL = 8;
/** Actions listed per call. */
const LIST = 60;

export interface PulseEvent {
  id: string;
  at: number;
  who: { name: string; initials: string; human: boolean };
  did: string;
  app: string;
  /** stop, wait, recheck, ask or bad when Lumni stepped in (or would have); allow otherwise. */
  kind: 'allow' | 'stop' | 'wait' | 'recheck' | 'ask' | 'bad';
  would: boolean;
  why: string | null;
  amount: number | null;
}

export interface Pulse {
  now: number;
  /** Pass back as `after` next time. */
  cursor: number;
  events: PulseEvent[];
  /** Actions after `after` that aren't told in `events`. */
  more: number;
  /** True when there were more than one list holds. */
  moreIsAtLeast: boolean;
  /** The newest action at all, so a quiet page can say how long it's been. */
  lastAt: number | null;
  totals: { total: number; steps: number; kinds: Record<string, number>; would: number; spend: number; kept: number } | null;
}

/** Journeys don't change once the decision is in; keep the last few hundred. */
const told = new Map<string, PulseEvent>();
function remember(e: PulseEvent) {
  told.set(e.id, e);
  if (told.size > 600) told.delete(told.keys().next().value!);
}

function eventOf(a: Act, actors: Parameters<typeof whoOf>[0]): PulseEvent {
  const d = stepDecision(a);
  const v = d ? verdictOf(d.d, d.r) : null;
  const step = isBad(a) ? 'bad' : v ? stepOf(v.kind) : null;
  const w = whoOf(actors, a.actor);
  return {
    id: a.id, at: a.at,
    who: { name: w.name, initials: w.initials, human: w.human },
    did: didOf(a), app: appOf(a).name,
    kind: step ?? 'allow',
    would: !!(d && isWould(d.d)),
    why: d && step ? reasonWords(d.r) : null,
    amount: amountOf(a),
  };
}

export async function pulseView(ctx: Ctx, afterMs: number | null): Promise<Pulse> {
  const after = afterMs && afterMs > ctx.now - 24 * 60 * MIN ? afterMs : ctx.now - 10 * MIN;
  const tz = encodeURIComponent(ctx.zone.name);
  const from = new Date(after + 1).toISOString();
  const [list, day, actors] = await Promise.all([
    ctx.get<{ items?: { id: string; requested_at: string }[] | null; next_cursor?: string | null }>(`/v1/actions?from=${encodeURIComponent(from)}&limit=${LIST}`),
    ctx.get<{ total?: number; kinds?: Record<string, number>; would?: Record<string, number>; cost_micros_usd?: number; kept_micros_usd?: number }>(`/v1/analytics/day?tz=${tz}&group_by=kind`).catch(() => null),
    ctx.actors(),
  ]);
  const rows = items<{ id: string; requested_at: string }>(list).filter((r) => Date.parse(r.requested_at) > after);
  rows.sort((a, b) => Date.parse(b.requested_at) - Date.parse(a.requested_at));

  const newest = rows.slice(0, DETAIL);
  const events = (await Promise.all(newest.map(async (r) => {
    const known = told.get(r.id);
    if (known) return known;
    try {
      const j = await ctx.get<ApiJourney>(`/v1/actions/${encodeURIComponent(r.id)}/journey`);
      const e = eventOf(actFrom(j), actors);
      // A decision still being made (no decision yet) is read again next time.
      if (j.decision) remember(e);
      return e;
    } catch {
      return null;
    }
  }))).filter((e): e is PulseEvent => !!e);

  // Nothing new: when was the last action, however long ago?
  let lastAt = rows.length ? Date.parse(rows[0]!.requested_at) : null;
  if (lastAt === null) {
    const last = await ctx.get<{ items?: { requested_at: string }[] | null }>('/v1/actions?limit=1').catch(() => null);
    const at = items<{ requested_at: string }>(last)[0]?.requested_at;
    lastAt = at ? Date.parse(at) : null;
  }

  const k = (n: string) => day?.kinds?.[n] ?? 0;
  const kinds = { stop: k('stop'), wait: k('wait'), recheck: k('recheck'), ask: k('ask'), bad: k('bad') };
  return {
    now: ctx.now,
    cursor: rows.length ? Date.parse(rows[0]!.requested_at) : after,
    events,
    more: Math.max(0, rows.length - events.length),
    moreIsAtLeast: !!list?.next_cursor,
    lastAt,
    totals: day && typeof day.total === 'number' ? {
      total: day.total,
      steps: kinds.stop + kinds.wait + kinds.recheck + kinds.ask + kinds.bad,
      kinds,
      would: Object.values(day.would ?? {}).reduce((s, n) => s + (n ?? 0), 0),
      spend: (day.cost_micros_usd ?? 0) / 1e6,
      kept: (day.kept_micros_usd ?? 0) / 1e6,
    } : null,
  };
}
