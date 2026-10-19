// /v1/console/spend with real data: what the AIs have spent today, hour by
// hour from the API’s cost records, and where today’s pace leads by midnight.
// There’s no daily limit to set yet, so the day has only its pace.

import type { SpendByAi, SpendCurve, SpendView, WastedItem, Who } from '../../../src/domain/types';
import { HOUR, fmt, money, plural } from '../../../src/domain/format';
import { verdictOf } from '../../../src/domain/vocab';
import { ms } from '../../upstream';
import type { Ctx } from '../load';
import { stepDecision, stepKind } from '../model';
import { stepText } from '../words';
import { people } from './shared';

const OTHER: Who = { id: 'other', name: 'Not tied to an action', initials: '+', human: false };

export async function spendView(ctx: Ctx): Promise<SpendView> {
  const z = ctx.zone;
  const from = z.dayStart(ctx.now);
  const nowH = (ctx.now - from) / HOUR;
  const [f, day, who] = await Promise.all([ctx.finops(from, ctx.now), ctx.today(), people(ctx)]);

  // The API buckets cost by the hour; each bucket’s total lands at its end.
  const series = (f.series ?? [])
    .map((p) => ({ t: ms(p.bucket) ?? 0, cost: (p.cost_micros_usd ?? 0) / 1e6, actions: p.actions ?? 0 }))
    .filter((p) => p.t + HOUR > from)
    .sort((a, b) => a.t - b.t);
  const curve: SpendCurve = [[0, 0]];
  let cum = 0;
  for (const p of series) {
    cum += p.cost;
    curve.push([Math.min(nowH, Math.max(0, (p.t + HOUR - from) / HOUR)), cum]);
  }
  const spent = (f.total_cost_micros_usd ?? 0) / 1e6 || cum;
  if (curve.at(-1)![0] < nowH) curve.push([nowH, spent]);
  const actions = series.reduce((a, p) => a + p.actions, 0) || day.acts.length;

  // Today’s pace: the last two hours, or the day so far when it’s younger than that.
  const recentFrom = Math.max(0, nowH - 2);
  const before = valueAt(curve, recentFrom);
  const rate = nowH > 0 ? (spent - before) / Math.max(1 / 60, nowH - recentFrom) : 0;
  const forecast = Math.round((spent + rate * Math.max(0, 24 - nowH)) * 100) / 100;
  const pace: SpendCurve = [];
  for (let h = nowH; h < 24; h += 1 / 6) pace.push([h, spent + rate * (h - nowH)]);
  pace.push([24, forecast]);

  const items: WastedItem[] = day.acts
    .filter((a) => stepKind(a) === 'stop' && a.cost > 0)
    .map((a) => ({
      id: a.id, at: a.at, h: z.hourOf(a.at), who: who(a.actor), text: stepText(a),
      verdict: verdictOf(stepDecision(a)!.d, stepDecision(a)!.r), cost: a.cost / 1e6, to: `/story/${a.id}`,
    }))
    .sort((a, b) => b.cost - a.cost || a.at - b.at);
  const wasteTotal = Math.max((f.waste_micros_usd ?? 0) / 1e6, items.reduce((a, x) => a + x.cost, 0));

  const $ = (v: number) => money(v, 'never');
  const perAction = actions ? spent / actions : 0;
  return {
    head: spent
      ? { title: `${$(spent)} so far, on pace for ${$(forecast)}`, sub: `Your AIs spend about ${money(perAction, 'always')} an action, across ${fmt(actions)} ${plural(actions, 'action')} today. There’s no daily limit, so nothing slows them down.` }
      : { title: 'Nothing spent yet today', sub: 'Lumni counts what your AIs spend as their costs come in, hour by hour.' },
    now: nowH,
    at: nowH,
    spent,
    forecast,
    perAction,
    actions,
    paused: false,
    limit: null,
    held: null,
    curve,
    pace,
    heldCurve: null,
    options: [{ value: null, label: 'None', effect: `${$(forecast)} by midnight`, said: '' }],
    aside: 'No daily limit. Setting one isn’t available yet.',
    wasted: { total: Math.round(wasteTotal * 100) / 100, count: items.length, items },
    byAi: byAi(day.acts, spent, who),
  };
}

function valueAt(c: SpendCurve, h: number): number {
  let v = 0;
  for (const [x, y] of c) {
    if (x > h) break;
    v = y;
  }
  return v;
}

function byAi(acts: { actor: string | null; cost: number }[], spent: number, who: (id: string | null) => Who): SpendByAi[] {
  const by = new Map<string, { n: number; cost: number }>();
  for (const a of acts) {
    const k = a.actor ?? 'unknown';
    const cur = by.get(k) ?? { n: 0, cost: 0 };
    cur.n += 1;
    cur.cost += a.cost / 1e6;
    by.set(k, cur);
  }
  const rows: SpendByAi[] = [...by].map(([id, x]) => ({ who: who(id === 'unknown' ? null : id), spent: x.cost, actions: x.n, perAction: x.n ? x.cost / x.n : 0 }));
  const tied = rows.reduce((a, r) => a + r.spent, 0);
  // Model calls and tools billed without an action still count toward the day.
  if (spent - tied >= 0.01) rows.push({ who: OTHER, spent: spent - tied, actions: 0, perAction: 0 });
  return rows.filter((r) => r.spent > 0 || r.actions > 0).sort((a, b) => b.spent - a.spent);
}
