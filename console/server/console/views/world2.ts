// The real world, from the Console API's exact aggregates (October 2026).
//
// One call per question, each answered by the backend at any volume:
//   GET /v1/analytics/day         totals, kinds, hours, by actor, app, reason, resource
//   GET /v1/approvals?status=PENDING&include=action,rule   decisions waiting
//   GET /v1/finops/graph          spend, waste by stable key, by actor
//   GET /v1/billing/usage         what Lumni cost, for the return
//   GET /v1/config/spend-limit    the daily limit, and when it slows
//   GET /v1/incidents?kind=BURST  today's bursts
//   GET /v1/analytics/anomalies   loops, runaways, drift
//   GET /v1/analytics/freshness   how old each AI's reads are
//   GET /v1/resources/contended   who holds the busiest things, and the queue
//   GET /v1/resources/{id}/versions   what a shared fact was, and is
//   GET /v1/config/autopilot      which AIs have earned Autopilot
//   GET /v1/analytics/daily       the last week, for comparisons
//
// It returns the same shape as views/world.ts (the snapshot reader, kept as the
// fallback for older APIs) and as the demo's app/lib/world-mock.js.

import type { AppId } from '../../../src/domain/types';
import { HOUR, MIN, wall, type Zone } from '../../../src/domain/format';
import { HttpError } from '../../http';
import type { Ctx } from '../load';
import { appName, initials, opWords, whoOf } from '../model';
import { lowerFirst, reasonWords } from '../words';
import { checkers, rules } from './shared';

const LEVELS = ['Watch', 'Warn', 'Enforce', 'Autopilot'] as const;
const LEVEL_OF: Record<string, number> = { SHADOW: 0, WARN: 1, ENFORCE: 2 };
const STEP_KINDS = ['stop', 'wait', 'recheck', 'ask', 'bad'] as const;
const BRAND: Record<string, string> = {
  stripe: '#635bff', salesforce: '#00a1e0', zendesk: '#03363d', gmail: '#ea4335', quickbooks: '#2ca01c', vercel: '#0d0e14',
  slack: '#4a154b', github: '#0d0e14', hubspot: '#ff7a59', shopify: '#5e8e3e', intercom: '#1f8ded', jira: '#0052cc', linear: '#5e6ad2',
};
const HOURS_PER = { stop: 0.12, bad: 0.5, ask: 0.05, wait: 0.02 };
/** Waste keys from the API, to the five the screens draw. */
const WASTE: Record<string, { key: string; t: string }> = {
  repeat_stopped: { key: 'repeats', t: 'Repeats Lumni stopped before they ran' },
  stale_redo: { key: 'stale', t: 'Work redone after reading old info' },
  retry_loop: { key: 'loops', t: 'Retry loops Lumni cut short' },
  superseded: { key: 'superseded', t: 'Work thrown away because a newer task replaced it' },
  never_happened: { key: 'failed', t: 'Actions that never happened' },
};

type Num = Record<string, number>;
interface Breakdown {
  actor_id?: string; app?: string; operation?: string; kind?: string; reason_code?: string;
  total?: number; count?: number; kinds?: Num; verified?: number; cost_micros_usd?: number; kept_micros_usd?: number;
  hours?: number[]; last_at?: string; streak?: number; checked?: boolean; top_resource?: string; sample_action_id?: string;
}
interface DayAnalytics {
  total: number; kinds: Num; would: Num; outcomes: Num; cost_micros_usd?: number; kept_micros_usd?: number; truncated: boolean;
  actor_count?: number; app_count?: number; resource_count?: number;
  hours?: { h?: number; total?: number; kinds?: Num }[];
  by_actor?: Breakdown[]; by_app?: Breakdown[]; by_reason?: Breakdown[]; by_operation?: Breakdown[];
  by_resource?: { resource_id?: string; name?: string; touches?: number; actors?: number; stepins?: Num; hours?: Record<string, number[]> }[];
}
interface Approval {
  id: string; action_id: string; status: string; reason_code?: string; requested_at?: string; expires_at?: string | null;
  action?: { id: string; operation: string; actor_id: string; metadata?: Record<string, unknown> };
  rule?: { id?: string; name?: string; numeric_threshold?: number; operation?: string };
}
interface Lease {
  resource_id?: string;
  holder?: { action_id?: string; actor_id?: string; since?: string; expires_at?: string; fencing_token?: number };
  queue?: { action_id?: string; actor_id?: string; waiting_since?: string; position?: number }[];
  limit?: { max_in_flight?: number; in_flight?: number };
  handoffs_last_hour?: number; refused_late_writes_today?: number;
}
interface Burst {
  id: string; title: string; created_at: string;
  evidence?: { app?: string; started_at?: string; ended_at?: string; pages?: number; totals?: Num; per_minute?: { at: string; stepins: number }[]; events?: { at: string; kind: string; action_id: string; text: string }[] };
}

const usd = (micros?: number | null) => (micros ?? 0) / 1e6;
const by = <T>(xs: T[], key: (x: T) => string) => {
  const m = new Map<string, T[]>();
  for (const x of xs) {
    const k = key(x);
    const l = m.get(k);
    if (l) l.push(x);
    else m.set(k, [x]);
  }
  return m;
};
const median = (xs: number[]) => {
  if (!xs.length) return 0;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)]!;
};
const compact = (n: number) => (n < 10_000 ? n.toLocaleString('en-US') : n < 1e6 ? `${(n / 1000).toFixed(n < 1e5 ? 1 : 0).replace(/\.0$/, '')}k` : `${(n / 1e6).toFixed(1).replace(/\.0$/, '')}M`);
const mark = (name: string) => {
  const w = name.split(/\s+/).filter(Boolean);
  return w.length > 1 ? (w[0]![0]! + w[1]![0]!).toUpperCase() : name.slice(0, 2).replace(/^./, (c) => c.toUpperCase());
};
const amountOf = (meta: Record<string, unknown> | undefined): number | null => {
  for (const k of ['amount', 'amount_usd', 'value', 'total']) {
    const v = meta?.[k];
    const n = typeof v === 'number' ? v : typeof v === 'string' ? Number(v.replace(/[$,]/g, '')) : NaN;
    if (Number.isFinite(n) && n > 0) return n;
  }
  return null;
};
const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : null);

function labels(at: number, z: Zone) {
  const w = wall(at, z);
  const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const h = ((w.h + 11) % 12) + 1;
  return {
    day: `${days[w.dow]}, ${months[w.mo]} ${w.d}`,
    time: `${h}:${String(w.m).padStart(2, '0')} ${w.h < 12 ? 'AM' : 'PM'}`,
    clock: `${h}:${String(w.m).padStart(2, '0')}:${String(w.s).padStart(2, '0')}`,
  };
}

/** A read that may not exist on this API or for this key: null instead of failing the page. */
async function maybe<T>(p: Promise<T>): Promise<T | null> {
  try {
    return await p;
  } catch {
    return null;
  }
}

export async function worldFromAnalytics(ctx: Ctx) {
  const z = ctx.zone;
  const from = z.dayStart(ctx.now);
  const nowH = Math.max(1 / 60, (ctx.now - from) / HOUR);
  const tz = encodeURIComponent(z.name);
  const iso = (t: number) => new Date(t).toISOString();

  // The one call that must work: without it, the caller falls back to snapshots.
  const day = await ctx.get<DayAnalytics>(`/v1/analytics/day?tz=${tz}&group_by=kind,hour,actor,app,reason,resource,operation&top=12`);
  if (!day || typeof day.total !== 'number') throw new HttpError(404, 'not_found', 'No analytics on this API.');

  const [config, actors, chk, rs, agents, approvals, fin, billing, spendLimit, bursts, anomalies, fresh, contended, autopilot, daily] = await Promise.all([
    maybe(ctx.config()),
    ctx.actors(),
    checkers(ctx),
    rules(ctx),
    ctx.agents().catch(() => []),
    maybe(ctx.get<{ items: Approval[] }>('/v1/approvals?status=PENDING&include=action,rule&limit=1000')),
    maybe(ctx.finops(from, ctx.now)),
    maybe(ctx.get<{ checked_actions?: number; price_per_action_micros_usd?: number; cost_micros_usd?: number; plan?: string }>(`/v1/billing/usage?tz=${tz}`)),
    maybe(ctx.get<{ daily_micros_usd?: number; when_reached?: string; spent_today_micros_usd?: number; pace_micros_usd?: number; slow_at?: string | null } | null>('/v1/config/spend-limit')),
    maybe(ctx.get<{ items: Burst[] }>(`/v1/incidents?kind=BURST&from=${iso(from)}&to=${iso(ctx.now + 1000)}&limit=50`)),
    maybe(ctx.get<{ items: { kind?: string; actor_id?: string; operation?: string; resource_id?: string; count?: number; interval_ms?: number; factor?: number; cost_micros_usd?: number; open?: boolean }[] }>(`/v1/analytics/anomalies?tz=${tz}&kinds=loop,runaway,drift`)),
    maybe(ctx.get<{ by_actor?: { actor_id?: string; reads?: number; median_age_ms?: number | null; p90_age_ms?: number | null; stale?: number }[] }>(`/v1/analytics/freshness?tz=${tz}`)),
    maybe(ctx.get<{ items: Lease[] }>(`/v1/resources/contended?tz=${tz}&top=10`)),
    maybe(ctx.get<{ items: { actor_id?: string; operation?: string; active?: boolean; since?: string; slips?: number; skip_rules?: string[]; below?: Record<string, unknown>; revert_after?: Record<string, unknown> }[] }>('/v1/config/autopilot')),
    maybe(ctx.get<{ days?: { date?: string; total?: number; kinds?: Num; kept_micros_usd?: number; cost_micros_usd?: number; verified?: number; until_hour?: { total?: number; kinds?: Num; kept_micros_usd?: number; cost_micros_usd?: number; verified?: number } }[] }>(`/v1/analytics/daily?days=8&tz=${tz}&at_hour=${Math.floor(nowH)}`)),
  ]);

  const who = (id: string | null | undefined) => whoOf(actors, id ?? null);
  const total = day.total;
  const k = (name: string) => day.kinds?.[name] ?? 0;
  const kinds = { stop: k('stop'), wait: k('wait'), recheck: k('recheck'), ask: k('ask'), bad: k('bad'), blind: k('blind') };
  const would = { stop: day.would?.stop ?? 0, wait: day.would?.wait ?? 0, recheck: day.would?.recheck ?? 0, ask: day.would?.ask ?? 0 };
  const steps = kinds.stop + kinds.wait + kinds.recheck + kinds.ask + kinds.bad;
  const verified = day.outcomes?.VERIFIED ?? 0;
  const kept = usd(day.kept_micros_usd);

  const hourly = Array<number>(24).fill(0);
  const stepHourly = Array<number>(24).fill(0);
  for (const h of day.hours ?? []) {
    const i = h.h ?? -1;
    if (i < 0 || i > 23) continue;
    hourly[i] = h.total ?? 0;
    stepHourly[i] = STEP_KINDS.reduce((s, kk) => s + (h.kinds?.[kk] ?? 0), 0);
  }

  // ─── who: teams are what actors say, or each AI on its own ─
  const byActor = (day.by_actor ?? []).filter((a) => a.actor_id);
  const isAi = (id?: string) => !who(id).human;
  const teamKey = (id?: string) => {
    const t = id ? actors.get(id)?.metadata?.team : null;
    return typeof t === 'string' && t.trim() ? `team:${t.trim().toLowerCase()}` : `ai:${id ?? 'unknown'}`;
  };
  const teamRows = [...by(byActor.filter((a) => isAi(a.actor_id)), (a) => teamKey(a.actor_id))]
    .map(([key, rows]) => ({ key, rows, n: rows.reduce((s, r) => s + (r.total ?? 0), 0) }))
    .sort((a, b) => b.n - a.n);
  const top = teamRows.slice(0, 6);
  const teams = top.map(({ key, rows, n }) => {
    const first = rows[0]!.actor_id!;
    const name = key.startsWith('team:') ? String(actors.get(first)?.metadata?.team) : who(first).name;
    return { id: key, name, ai: key.startsWith('team:') ? initials(name, false) : who(first).initials, n, w: n / Math.max(1, total) };
  });
  const rest = Math.max(0, total - teams.reduce((s, t) => s + t.n, 0));
  if (rest) teams.push({ id: 'other', name: 'Everyone else', ai: '+', n: rest, w: rest / Math.max(1, total) });
  const teamOfActor = (id?: string) => {
    const kk = teamKey(id);
    return teams.some((t) => t.id === kk) ? kk : 'other';
  };

  // ─── where ──────────────────────────────────────────
  const apps = (day.by_app ?? []).filter((a) => a.app).slice(0, 7).map((a) => {
    const id = a.app as AppId;
    const name = appName(id);
    const checked = a.checked ?? !!chk.byApp.get(id)?.enabled;
    return { id, name, mark: mark(name), color: BRAND[id] ?? '#0d0e14', n: a.total ?? 0, w: (a.total ?? 0) / Math.max(1, total), blind: !checked, verified: a.verified ?? 0 };
  });

  // ─── resources: names, contention, leases ───────────
  const resources = new Map((day.by_resource ?? []).filter((r) => r.resource_id).map((r) => [r.resource_id!, r]));
  const resName = (id?: string | null) => (id ? resources.get(id)?.name ?? id.split(':').slice(2).join(':') ?? id : null);

  // ─── why: step-ins by reason ────────────────────────
  const patterns: Record<string, unknown[]> = { stop: [], recheck: [], wait: [], ask: [], bad: [] };
  for (const kind of ['stop', 'recheck', 'wait', 'bad'] as const) {
    patterns[kind] = (day.by_reason ?? [])
      .filter((r) => r.kind === kind)
      .sort((a, b) => (b.count ?? 0) - (a.count ?? 0))
      .slice(0, 6)
      .map((r, i) => {
        const n = r.count ?? 0;
        const hours = (r.hours ?? []).slice(0, 24);
        const peak = hours.length ? hours.indexOf(Math.max(...hours)) : -1;
        const mean = n / Math.max(1, Math.ceil(nowH));
        const name = who(r.actor_id).name;
        const what = lowerFirst(opWords(r.operation ?? 'an action'));
        const keptR = usd(r.kept_micros_usd);
        return {
          id: `${kind}-${i}`, kind, t: kind === 'bad' ? `${name}’s ${what}: the app shows nothing` : `${name}’s ${what}: ${reasonWords((r.reason_code ?? 'NO_CONFLICT') as never)}`,
          team: teamOfActor(r.actor_id), app: r.app ?? null, n, kept: keptR, money: n ? keptR / n : 0,
          spike: peak >= 0 && hours[peak]! >= 5 && hours[peak]! >= 3 * mean ? peak + 0.5 : null,
          story: r.sample_action_id ?? null, hours, operation: r.operation ?? null, actor: r.actor_id ?? null,
          thing: r.top_resource ? { id: r.top_resource, name: resName(r.top_resource), n: null } : null,
        };
      });
  }

  // ─── decisions waiting, by the rule that asked ──────
  const pending = (approvals?.items ?? []).filter((a) => a.status === 'PENDING');
  const groups = [...by(pending, (a) => a.rule?.id ?? `${a.reason_code ?? 'ASK'}|${a.action?.operation ?? ''}`)].map(([key, xs]) => {
    const a = xs[0]!;
    const op = a.action?.operation ?? '';
    const ruleText = a.rule?.name ?? `${opWords(op || 'an action')}: ${reasonWords((a.reason_code ?? 'APPROVAL_REQUIRED') as never)}`;
    const amounts = xs.map((x) => amountOf(x.action?.metadata)).filter((n): n is number => n !== null);
    return {
      id: key, t: ruleText, rule: ruleText, ruleId: a.rule?.id ?? null, team: teamOfActor(a.action?.actor_id), app: op.split('.')[0] || null,
      kind: 'ask', n: xs.length, w: xs.length / Math.max(1, pending.length), kept: 0,
      amount: amounts.length ? [Math.min(...amounts), Math.max(...amounts)] : null,
      members: xs.map((x) => {
        const meta = x.action?.metadata ?? {};
        return {
          id: x.id, action: x.action_id, who: str(meta.customer) ?? str(meta.summary) ?? opWords(x.action?.operation ?? 'an action'),
          did: str(meta.summary) ?? opWords(x.action?.operation ?? 'an action'), amount: amountOf(meta), actor: x.action?.actor_id ?? null,
          mins: Math.max(0, Math.round((ctx.now - (Date.parse(x.requested_at ?? '') || ctx.now)) / MIN)),
          expires: x.expires_at ? Date.parse(x.expires_at) : null,
        };
      }),
    };
  });
  patterns.ask = groups.map((g) => ({ id: g.id, kind: 'ask', t: g.t, team: g.team, app: g.app, n: g.n, kept: 0, money: 0, spike: null, story: g.members[0]?.action ?? null, thing: null }));

  // ─── the fleet ──────────────────────────────────────
  const anomaly = new Map<string, { loop?: (typeof anomalies & object)['items'][number]; runaway?: (typeof anomalies & object)['items'][number]; drift?: (typeof anomalies & object)['items'][number] }>();
  for (const a of anomalies?.items ?? []) {
    if (!a.actor_id || a.open === false || !a.kind) continue;
    const cur = anomaly.get(a.actor_id) ?? {};
    (cur as Record<string, unknown>)[a.kind] = a;
    anomaly.set(a.actor_id, cur);
  }
  const lag = new Map((fresh?.by_actor ?? []).filter((f) => f.actor_id).map((f) => [f.actor_id!, f]));
  const grants = new Map((autopilot?.items ?? []).filter((g) => g.actor_id && g.active !== false).map((g) => [g.actor_id!, g]));
  const finByActor = new Map(((fin as { by_actor?: { actor_id?: string; cost_micros_usd?: number; actions?: number }[] } | null)?.by_actor ?? []).filter((r) => r.actor_id).map((r) => [r.actor_id!, r]));
  const mainOpOf = (id: string) => {
    // Newer APIs put each AI's main operation on its own row.
    const own = byActor.find((a) => a.actor_id === id)?.operation;
    if (own) return own;
    const rows = [...(day.by_operation ?? []), ...(day.by_reason ?? [])].filter((r) => r.actor_id === id && r.operation);
    return rows.sort((a, b) => (b.total ?? b.count ?? 0) - (a.total ?? a.count ?? 0))[0]?.operation ?? null;
  };
  const topAis = byActor.filter((a) => isAi(a.actor_id)).sort((a, b) => (b.total ?? 0) - (a.total ?? 0)).slice(0, 12);
  // The day's breakdowns only name an operation's busiest AI. For the rest,
  // use the operation it loops on, else the one it acted on last.
  const opFallback = new Map(await Promise.all(topAis.filter((a) => !mainOpOf(a.actor_id!)).map(async (a) => {
    const id = a.actor_id!;
    const looped = (anomaly.get(id) as { loop?: { operation?: string } } | undefined)?.loop?.operation;
    if (looped) return [id, looped] as const;
    const last = await maybe(ctx.get<{ items?: { operation?: string }[] }>(`/v1/actions?actor_id=${encodeURIComponent(id)}&limit=1`));
    return [id, last?.items?.[0]?.operation ?? null] as const;
  })));
  const fleetRaw = topAis.map((a) => {
    const id = a.actor_id!;
    const w = who(id);
    const n = a.total ?? 0;
    const op = mainOpOf(id) ?? opFallback.get(id) ?? null;
    const grant = grants.get(id);
    const level = grant ? 3 : LEVEL_OF[rs.modeFor(op ?? '*', '*')] ?? 0;
    const spend = usd(finByActor.get(id)?.cost_micros_usd ?? a.cost_micros_usd);
    const proven = a.verified ?? 0;
    const an = anomaly.get(id) ?? {};
    const f = lag.get(id);
    const actor = actors.get(id) as { paused?: boolean } | undefined;
    return {
      id, name: w.name, initials: w.initials, team: teamOfActor(id), app: op?.split('.')[0] ?? null, op,
      copies: Math.max(1, agents.filter((g) => g.actor_id === id).length),
      n, w: n / Math.max(1, total), spend, cost: spend / Math.max(1, n), proven, perProven: proven ? spend / proven : null,
      repeats: a.kinds?.stop ?? 0, stale: a.kinds?.recheck ?? 0, bad: a.kinds?.bad ?? 0,
      streak: a.streak ?? 0, level, levelName: LEVELS[level],
      loop: an.loop ? 1 : 0, loopOp: an.loop?.operation ?? null, loopCount: an.loop?.count ?? null, loopEveryMs: an.loop?.interval_ms ?? null, loopResource: an.loop?.resource_id ?? null,
      runaway: an.runaway ? { factor: an.runaway.factor ?? null, cost: usd(an.runaway.cost_micros_usd), operation: an.runaway.operation ?? null } : null,
      drift: an.drift ? true : false,
      lag: f?.median_age_ms != null ? Math.round(f.median_age_ms / 1000) : null,
      lagP90: f?.p90_age_ms != null ? Math.round(f.p90_age_ms / 1000) : null,
      paused: !!actor?.paused,
      autopilot: grant ? { since: grant.since ?? null, slips: grant.slips ?? 0, operation: grant.operation ?? null, skipRules: grant.skip_rules ?? [], below: grant.below ?? null, revertAfter: grant.revert_after ?? null } : null,
      recent: a.last_at ? ctx.now - Date.parse(a.last_at) < 15 * MIN : false,
    };
  });
  const slipRate = (f: (typeof fleetRaw)[number]) => (f.stale + f.bad) / Math.max(1, f.n);
  const med = median(fleetRaw.map(slipRate));
  const fleet = fleetRaw.map((f) => ({ ...f, promote: f.level === 2 && f.streak >= 500, demote: f.n >= 20 && slipRate(f) > Math.max(0.01, 3 * med) }));

  // ─── money ──────────────────────────────────────────
  const spend = fin?.total_cost_micros_usd ? usd(fin.total_cost_micros_usd) : usd(day.cost_micros_usd);
  const unit = spend / Math.max(1, total);
  const waste: Record<string, { t: string; n: number; usd: number; avoided: boolean }> = {
    repeats: { t: WASTE.repeat_stopped!.t, n: kinds.stop, usd: 0, avoided: true },
    stale: { t: WASTE.stale_redo!.t, n: kinds.recheck, usd: 0, avoided: false },
    loops: { t: WASTE.retry_loop!.t, n: 0, usd: 0, avoided: true },
    superseded: { t: WASTE.superseded!.t, n: 0, usd: 0, avoided: false },
    failed: { t: WASTE.never_happened!.t, n: kinds.bad, usd: 0, avoided: false },
  };
  const extra: { key: string; t: string; n: number; usd: number; avoided: boolean }[] = [];
  for (const b of (fin?.by_waste ?? []) as { key: string; label?: string; amount_micros_usd?: number; count?: number; avoided?: boolean }[]) {
    const m = WASTE[b.key];
    if (m) waste[m.key] = { t: m.t, n: b.count ?? 0, usd: usd(b.amount_micros_usd), avoided: b.avoided ?? waste[m.key]!.avoided };
    else extra.push({ key: b.key, t: b.label ?? b.key, n: b.count ?? 0, usd: usd(b.amount_micros_usd), avoided: !!b.avoided });
  }
  const hoursSaved = Math.round(kinds.stop * HOURS_PER.stop + kinds.bad * HOURS_PER.bad + kinds.ask * HOURS_PER.ask + kinds.wait * HOURS_PER.wait);
  const avoidedSpend = Object.values(waste).filter((x) => x.avoided).reduce((s, x) => s + x.usd, 0);
  const price = Number(ctx.env.pricePerAction ?? NaN);
  const lumniCost = billing?.cost_micros_usd != null ? usd(billing.cost_micros_usd) : Number.isFinite(price) && price > 0 ? total * price : null;
  const money = {
    unit, waste, extra, avoidedSpend, hoursSaved, lumniCost,
    wasted: Object.values(waste).filter((x) => !x.avoided).reduce((s, x) => s + x.usd, 0),
    roi: lumniCost ? (kept + avoidedSpend + hoursSaved * 85) / lumniCost : null,
    limit: spendLimit?.daily_micros_usd ? usd(spendLimit.daily_micros_usd) : null,
    limitMode: spendLimit?.when_reached ?? null,
    slowAt: spendLimit?.slow_at ? Date.parse(spendLimit.slow_at) : null,
    perProven: verified ? spend / verified : null,
    plan: billing?.plan ?? null,
    pricePerAction: billing?.price_per_action_micros_usd != null ? usd(billing.price_per_action_micros_usd) : Number.isFinite(price) ? price : null,
  };
  const pace = spendLimit?.pace_micros_usd ? usd(spendLimit.pace_micros_usd) : spend / Math.min(1, nowH / 24);

  // ─── the day's burst, if there was one ──────────────
  // The worker records bursts in fixed ten-minute windows, so one burst that
  // crosses a boundary arrives as two records: join touching ones per app.
  const merged: Burst[] = [];
  for (const b of [...(bursts?.items ?? [])].filter((x) => x.evidence?.started_at).sort((x, y) => Date.parse(x.evidence!.started_at!) - Date.parse(y.evidence!.started_at!))) {
    const prev = merged.at(-1);
    const e = b.evidence!;
    const touches = prev && prev.evidence!.app === e.app && Date.parse(e.started_at!) - Date.parse(prev.evidence!.ended_at ?? prev.evidence!.started_at!) <= 10 * MIN;
    if (!touches) {
      merged.push({ ...b, evidence: { ...e, totals: { ...(e.totals ?? {}) }, per_minute: [...(e.per_minute ?? [])], events: [...(e.events ?? [])] } });
      continue;
    }
    const pe = prev!.evidence!;
    pe.ended_at = e.ended_at ?? e.started_at;
    for (const [kk, v] of Object.entries(e.totals ?? {})) {
      // Distinct counts can't be summed exactly; the larger window's is the honest floor.
      pe.totals![kk] = kk === 'customers' || kk === 'actors' ? Math.max(pe.totals![kk] ?? 0, v) : (pe.totals![kk] ?? 0) + v;
    }
    pe.per_minute!.push(...(e.per_minute ?? []));
    pe.events = [...pe.events!, ...(e.events ?? [])].slice(0, 16);
  }
  const burst = merged.sort((a, b) => (b.evidence?.totals?.stepins ?? 0) - (a.evidence?.totals?.stepins ?? 0))[0];
  let incident = null;
  if (burst?.evidence?.started_at) {
    const e = burst.evidence;
    const startAt = Date.parse(e.started_at!);
    const endAt = Date.parse(e.ended_at ?? e.started_at!);
    const t = e.totals ?? {};
    const appLabel = e.app ? appName(e.app) : 'an app';
    incident = {
      id: burst.id, title: burst.title || `${appLabel} activity burst`, app: appLabel, appId: e.app ?? null,
      start: labels(startAt, z).time, end: labels(endAt, z).time, startAt, endAt,
      total: t.stepins ?? 0, burst: t.stop ?? 0, kept: usd(t.kept_micros_usd), customers: t.customers ?? 0, ais: t.actors ?? 0,
      byKind: { stop: t.stop ?? 0, wait: t.wait ?? 0, recheck: t.recheck ?? 0, ask: t.ask ?? 0 },
      perMinute: (e.per_minute ?? []).map((m) => ({ at: Date.parse(m.at), n: m.stepins })),
      pages: e.pages ?? 0,
      events: (e.events ?? []).map((ev) => ({
        t: labels(Date.parse(ev.at), z).clock, s: Math.round((Date.parse(ev.at) - startAt) / 1000), kind: ev.kind,
        text: ev.text.replace(/^(\S+)\s+([A-Z_]+)$/, (_, op: string, r: string) => `${opWords(op)}: ${reasonWords(r as never)}`), story: ev.action_id,
      })),
    };
  }

  // ─── things many AIs touch, and who holds them now ──
  const leases = new Map((contended?.items ?? []).filter((l) => l.resource_id).map((l) => [l.resource_id!, l]));
  // The analytics roll the long tail into an 'other' row; it isn't a thing anyone can hold.
  const ids = [...new Set([...leases.keys(), ...resources.keys()])].filter((id) => id !== 'other');
  const things = ids.map((id) => {
    const r = resources.get(id);
    const l = leases.get(id);
    const step = r?.stepins ?? {};
    const people = [l?.holder?.actor_id, ...(l?.queue ?? []).map((q) => q.actor_id)].filter((x): x is string => !!x);
    return {
      id, name: resName(id) ?? id, app: id.split(':')[0] || null, touches: r?.touches ?? 0,
      waits: step.wait ?? 0, holders: r?.actors ?? new Set(people).size,
      stepins: STEP_KINDS.reduce((s, kk) => s + (step[kk] ?? 0), 0),
      hours: r?.hours ?? null,
      ais: [...new Set(people)].map((a) => ({ id: a, name: who(a).name, initials: who(a).initials, human: who(a).human })),
      lease: l ? {
        holder: l.holder?.actor_id ? { actor: who(l.holder.actor_id).name, initials: who(l.holder.actor_id).initials, action: l.holder.action_id ?? null, since: l.holder.since ? Date.parse(l.holder.since) : null, expires: l.holder.expires_at ? Date.parse(l.holder.expires_at) : null, token: l.holder.fencing_token ?? null } : null,
        // Newer APIs always send the queue (maybe empty); older ones left it out.
        queued: Array.isArray(l.queue),
        queue: (l.queue ?? []).map((q) => ({ actorId: q.actor_id, actor: who(q.actor_id).name, initials: who(q.actor_id).initials, action: q.action_id ?? null, since: q.waiting_since ? Date.parse(q.waiting_since) : null, position: q.position ?? null })),
        limit: l.limit ?? null, handoffs: l.handoffs_last_hour ?? 0, refused: l.refused_late_writes_today ?? 0,
      } : null,
    };
  }).sort((a, b) => b.stepins - a.stepins || b.waits + b.holders - (a.waits + a.holders) || b.touches - a.touches).slice(0, 10);

  // ─── shared facts: what the busiest records were, and are ─
  const factIds = [...resources.values()].filter((r) => (r.stepins?.recheck ?? 0) > 0 || (r.actors ?? 0) > 1)
    .sort((a, b) => (b.stepins?.recheck ?? 0) - (a.stepins?.recheck ?? 0) || (b.actors ?? 0) - (a.actors ?? 0)).slice(0, 6);
  const histories = await Promise.all(factIds.map((r) => maybe(ctx.get<{ items: { version?: string; at?: string; actor_id?: string; action_id?: string; summary?: string }[] }>(`/v1/resources/${encodeURIComponent(r.resource_id!)}/versions?limit=2`))));
  const facts = factIds.map((r, i) => {
    const [now, was] = histories[i]?.items ?? [];
    const v = Number(now?.version);
    const recheck = r.stepins?.recheck ?? 0;
    return {
      id: r.resource_id!, thing: r.name ?? resName(r.resource_id) ?? r.resource_id!, field: r.resource_id!.split(':')[1] ?? 'record',
      v: Number.isFinite(v) ? v : null, value: now?.summary ?? (now?.version ? `version ${now.version}` : 'current version'),
      was: was?.summary ?? (was?.version ? `version ${was.version}` : null),
      changedBy: now?.actor_id ? who(now.actor_id).name : null, human: now?.actor_id ? who(now.actor_id).human : false,
      ago: now?.at ? Math.max(0, Math.round((ctx.now - Date.parse(now.at)) / 1000)) : null,
      readers: r.actors ?? 0, stale: Math.min(r.actors ?? 0, recheck), reads: r.touches ?? 0, caught: recheck,
    };
  });

  // ─── the last week, for comparisons ─────────────────
  const days = daily?.days ?? [];
  const today = labels(ctx.now, z);
  const weekAgo = days.length >= 8 ? days[0] : null;
  // Same hour, a week ago. A figure the API didn't send is unknown (null), never 0.
  const u = weekAgo?.until_hour;
  const vsLastWeek = u
    ? {
        total: u.total ?? null,
        steps: u.kinds ? STEP_KINDS.reduce((s, kk) => s + (u.kinds?.[kk] ?? 0), 0) : null,
        kept: u.kept_micros_usd != null ? usd(u.kept_micros_usd) : null,
        spend: u.cost_micros_usd != null ? usd(u.cost_micros_usd) : null,
        proven: u.total && u.verified != null ? u.verified / u.total : null,
      }
    : null;
  const week = days.length
    ? {
        days: days.map((d) => ({ date: d.date ?? '', total: d.total ?? 0, steps: STEP_KINDS.reduce((s, kk) => s + (d.kinds?.[kk] ?? 0), 0), kept: usd(d.kept_micros_usd), spend: usd(d.cost_micros_usd), verified: d.verified ?? 0 })),
      }
    : null;

  const perDay = Math.round((total / nowH) * 24);
  const ais = byActor.filter((a) => isAi(a.actor_id)).length || day.actor_count || 0;
  return {
    key: 'real', source: 'analytics',
    now: ctx.now, nowH, dayStart: from, zone: z.name, dayLabel: today.day, timeLabel: today.time,
    mode: rs.tenantMode, paused: !!config?.paused, partial: !!day.truncated, notes: [...ctx.partial],
    scale: {
      org: ctx.env.workspace ?? 'Your workspace', long: ctx.env.workspace ?? 'Your workspace',
      actions: perDay, label: compact(perDay), ais, apps: day.app_count ?? apps.length, things: day.resource_count ?? resources.size,
      people: [...actors.values()].filter((a) => a.type === 'HUMAN').length,
    },
    total, kinds, would, steps, kept, hourly, stepHourly,
    proven: total ? verified / total : 0,
    spend, pace, working: fleet.filter((f) => f.recent).length,
    teams, apps, patterns, groups, fleet, money, incident, things, facts, LEVELS,
    vsLastWeek, week,
  };
}
