// /v1/console/world with real data: the one object every console screen
// reads, built from today's actions in your workspace. The demo builds the same
// shape in the page (app/lib/world-mock.js), so a screen never knows which
// world it has. Nothing here is invented: where the API has no source for a
// field, it's null or empty, and the screen says so.

import type { AppId, StepKind } from '../../../src/domain/types';
import { HOUR, MIN, wall, type Zone } from '../../../src/domain/format';
import type { Ctx } from '../load';
import { appOf, customerOf, didOf, isBad, opWords, stepDecision, stepKind, whoOf, wouldKind, type Act } from '../model';
import { amountOf, keptMoney, lowerFirst, reasonWords, stepText } from '../words';
import { checkers, rules } from './shared';
import { worldFromAnalytics } from './world2';
import { HttpError } from '../../http';

const LEVELS = ['Watch', 'Warn', 'Enforce', 'Autopilot'] as const;
const LEVEL_OF = { SHADOW: 0, WARN: 1, ENFORCE: 2 } as const;
const KINDS: StepKind[] = ['stop', 'wait', 'recheck', 'ask'];
const BRAND: Record<string, string> = {
  stripe: '#635bff', salesforce: '#00a1e0', zendesk: '#03363d', gmail: '#ea4335', quickbooks: '#2ca01c', vercel: '#0d0e14',
  slack: '#4a154b', github: '#0d0e14', hubspot: '#ff7a59', shopify: '#5e8e3e', intercom: '#1f8ded', jira: '#0052cc', linear: '#5e6ad2',
};
/** Team hours a step-in saves, the same weights the demo uses. */
const HOURS_PER = { stop: 0.12, bad: 0.5, ask: 0.05, wait: 0.02 };
/** Five of the same action on the same thing within ten minutes is a loop. */
const LOOP = { n: 5, within: 10 * MIN };

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
/** 1,283, 48.2k, 2.1M: the demo's volume labels. */
const compact = (n: number) => (n < 10_000 ? n.toLocaleString('en-US') : n < 1e6 ? `${(n / 1000).toFixed(n < 1e5 ? 1 : 0).replace(/\.0$/, '')}k` : `${(n / 1e6).toFixed(1).replace(/\.0$/, '')}M`);

const mark = (name: string) => {
  const w = name.split(/\s+/).filter(Boolean);
  return w.length > 1 ? (w[0]![0]! + w[1]![0]!).toUpperCase() : name.slice(0, 2).replace(/^./, (c) => c.toUpperCase());
};

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

/**
 * The world for every screen: from the Console API's exact aggregates when this
 * API has them (views/world2.ts), else rebuilt here from graph snapshots.
 */
export async function worldView(ctx: Ctx) {
  try {
    return await worldFromAnalytics(ctx);
  } catch (e) {
    // Only an API without analytics falls back; anything else is a real failure.
    if (!(e instanceof HttpError) || ![404, 405, 501].includes(e.status)) throw e;
  }
  return { ...(await legacyWorld(ctx)), source: 'snapshots', paused: false, vsLastWeek: null, week: null };
}

async function legacyWorld(ctx: Ctx) {
  const z = ctx.zone;
  const from = z.dayStart(ctx.now);
  const nowH = Math.max(1 / 60, (ctx.now - from) / HOUR);
  const [day, actors, chk, rs, agents, fin] = await Promise.all([
    ctx.today(),
    ctx.actors(),
    checkers(ctx),
    rules(ctx),
    ctx.agents().catch(() => []),
    ctx.finops(from, ctx.now).catch(() => null),
  ]);
  const acts = [...day.acts].sort((a, b) => a.at - b.at);
  const total = acts.length;
  const who = (id: string | null) => whoOf(actors, id);
  const hourOf = (a: Act) => Math.min(23, Math.max(0, Math.floor((a.at - from) / HOUR)));

  // ─── what Lumni did, per action ─────────────────────
  const kindOf = new Map<Act, StepKind | 'bad' | null>();
  const would = { stop: 0, wait: 0, recheck: 0, ask: 0 };
  for (const a of acts) {
    const k = stepKind(a);
    kindOf.set(a, k ?? (isBad(a) ? 'bad' : null));
    const wk = wouldKind(a);
    if (wk) would[wk]++;
  }
  const count = (k: StepKind | 'bad') => acts.filter((a) => kindOf.get(a) === k).length;
  const appIds = new Set<AppId>();
  const blindApp = (id: AppId) => !chk.byApp.get(id)?.enabled;
  const isBlind = (a: Act) => {
    const k = kindOf.get(a);
    if (k === 'stop' || k === 'ask' || k === 'bad') return false;
    const s = a.outcome?.status;
    return !s || s === 'UNKNOWN';
  };
  const kinds = {
    stop: count('stop'), wait: count('wait'), recheck: count('recheck'), ask: count('ask'), bad: count('bad'),
    blind: acts.filter(isBlind).length,
  };
  const steps = kinds.stop + kinds.wait + kinds.recheck + kinds.ask + kinds.bad;
  const verified = acts.filter((a) => a.outcome?.status === 'VERIFIED').length;

  // ─── the day, hour by hour ──────────────────────────
  const hourly = Array<number>(24).fill(0);
  const stepHourly = Array<number>(24).fill(0);
  for (const a of acts) {
    hourly[hourOf(a)]!++;
    if (kindOf.get(a)) stepHourly[hourOf(a)]!++;
  }

  // ─── who: teams are what actors say, or each AI on its own ─
  const isAi = (id: string | null) => !who(id).human;
  const teamKey = (id: string | null) => {
    const t = id ? actors.get(id)?.metadata?.team : null;
    return typeof t === 'string' && t.trim() ? `team:${t.trim().toLowerCase()}` : `ai:${id ?? 'unknown'}`;
  };
  const byTeam = [...by(acts.filter((a) => isAi(a.actor)), (a) => teamKey(a.actor))].sort((a, b) => b[1].length - a[1].length);
  const teamName = (k: string, xs: Act[]) => (k === 'ai:unknown' ? 'Not read yet' : k.startsWith('team:') ? String(actors.get(xs[0]!.actor ?? '')?.metadata?.team) : who(xs[0]!.actor).name);
  const top = byTeam.slice(0, 6);
  const rest = byTeam.slice(6).flatMap(([, xs]) => xs);
  const teams = top.map(([k, xs]) => {
    const name = teamName(k, xs);
    return { id: k, name, ai: k === 'ai:unknown' ? '?' : who(xs[0]!.actor).initials, n: xs.length, w: xs.length / Math.max(1, total) };
  });
  if (rest.length) teams.push({ id: 'other', name: 'Everyone else', ai: '+', n: rest.length, w: rest.length / Math.max(1, total) });
  const teamOfActor = (id: string | null) => {
    const k = teamKey(id);
    return teams.some((t) => t.id === k) ? k : 'other';
  };

  // ─── where: apps, and whether Lumni can check them ──
  const byApp = [...by(acts, (a) => appOf(a).id)].sort((a, b) => b[1].length - a[1].length);
  const apps = byApp.slice(0, 7).map(([id, xs]) => {
    appIds.add(id);
    const name = appOf(xs[0]!).name;
    return { id, name, mark: mark(name), color: BRAND[id] ?? '#0d0e14', n: xs.length, w: xs.length / Math.max(1, total), blind: blindApp(id) };
  });

  // ─── why: step-ins, grouped by reason, app and AI ───
  const patterns: Record<string, unknown[]> = { stop: [], recheck: [], wait: [], ask: [], bad: [] };
  for (const kind of [...KINDS, 'bad'] as const) {
    if (kind === 'ask') continue;
    const xs = acts.filter((a) => kindOf.get(a) === kind);
    const groups = by(xs, (a) => `${stepDecision(a)?.r ?? 'BAD'}|${appOf(a).id}|${a.actor ?? ''}`);
    patterns[kind] = [...groups.values()]
      .sort((a, b) => b.length - a.length)
      .slice(0, 6)
      .map((g, i) => {
        const a = g.at(-1)!;
        const d = stepDecision(a);
        const by = who(a.actor).name;
        const what = lowerFirst(opWords(a.op));
        const t = kind === 'bad' ? `${by}’s ${what}: the app shows nothing` : `${by}’s ${what}: ${reasonWords(d!.r)}`;
        const kept = g.reduce((s, x) => s + keptMoney(x), 0);
        const hours = Array<number>(24).fill(0);
        for (const x of g) hours[hourOf(x)]!++;
        const peak = hours.indexOf(Math.max(...hours));
        const mean = g.length / Math.max(1, Math.ceil(nowH));
        return {
          id: `${kind}-${i}`, kind, t, team: teamOfActor(a.actor), app: appOf(a).id, n: g.length, kept,
          money: kept ? kept / g.length : 0,
          // A burst worth naming: one hour with three times its share.
          spike: hours[peak]! >= 5 && hours[peak]! >= 3 * mean ? peak + 0.5 : null,
          story: a.id, sample: stepText(a), hours,
          thing: (() => {
            const c = new Map<string, number>();
            for (const x of g) for (const r of x.resources) c.set(r.id, (c.get(r.id) ?? 0) + 1);
            const top = [...c].sort((p, q) => q[1] - p[1])[0];
            return top ? { id: top[0], name: day.resources.get(top[0])?.name ?? day.resources.get(top[0])?.ext ?? top[0], n: top[1] } : null;
          })(),
        };
      });
  }
  const kept = acts.reduce((s, a) => s + keptMoney(a), 0);

  // ─── decisions waiting on you, by the rule that asked ─
  // The approval's own status wins; an action's status can lag behind it.
  const waiting = acts.filter((a) => (a.approval ? a.approval.status === 'PENDING' : a.status === 'AWAITING_APPROVAL'));
  const groups = [...by(waiting, (a) => {
    const d = stepDecision(a);
    return rs.match(a.op, d?.r ?? '', d?.policy ?? null)?.rule.id ?? `${d?.r ?? 'ASK'}|${a.op}`;
  })].map(([key, xs]) => {
    const a = xs[0]!;
    const d = stepDecision(a);
    const rule = rs.match(a.op, d?.r ?? '', d?.policy ?? null);
    const amounts = xs.map(amountOf).filter((n): n is number => n !== null);
    return {
      id: key, t: rule?.text ?? `${opWords(a.op)}: ${reasonWords(d?.r ?? 'APPROVAL_REQUIRED')}`, rule: rule?.text ?? reasonWords(d?.r ?? 'APPROVAL_REQUIRED'),
      team: teamOfActor(a.actor), app: appOf(a).id, kind: 'ask', n: xs.length, w: xs.length / Math.max(1, waiting.length), kept: 0,
      amount: amounts.length ? [Math.min(...amounts), Math.max(...amounts)] : null,
      members: xs.map((x) => ({
        id: x.approval?.id ?? x.id, action: x.id, who: customerOf(x) ?? didOf(x), did: didOf(x), amount: amountOf(x),
        mins: Math.max(0, Math.round((ctx.now - (x.approval?.at ?? x.at)) / MIN)), expires: x.approval?.expiresAt ?? null,
      })),
    };
  });

  // Waiting decisions read as the demo's ask patterns too.
  patterns.ask = groups.map((g) => ({ id: g.id, kind: 'ask', t: g.t, team: g.team, app: g.app, n: g.n, kept: 0, money: 0, spike: null, story: g.members[0]?.action ?? null, thing: null }));

  // ─── the fleet: every AI, and its record today ──────
  const loops = new Set<string>();
  /** The operation each looping AI repeats. */
  const loopOp = new Map<string, string>();
  const loopCount = { n: 0 };
  for (const g of by(acts, (a) => `${a.actor}|${a.op}|${a.resources[0]?.id ?? ''}`).values()) {
    for (let i = LOOP.n - 1; i < g.length; i++) {
      if (g[i]!.at - g[i - LOOP.n + 1]!.at <= LOOP.within) {
        loopCount.n++;
        if (ctx.now - g[i]!.at < LOOP.within) {
          loops.add(g[i]!.actor ?? '');
          loopOp.set(g[i]!.actor ?? '', g[i]!.op);
        }
      }
    }
  }
  const spendOf = (xs: Act[]) => xs.reduce((s, a) => s + a.cost, 0) / 1e6;
  // Only AIs we know: actions still being read don't make a fleet member.
  const fleetRaw = [...by(acts.filter((a) => a.actor && isAi(a.actor)), (a) => a.actor!)]
    .sort((a, b) => b[1].length - a[1].length)
    .slice(0, 12)
    .map(([id, xs]) => {
      const w = who(id);
      const n = xs.length;
      const mainOp = [...by(xs, (a) => a.op)].sort((a, b) => b[1].length - a[1].length)[0]![0];
      const level = LEVEL_OF[rs.modeFor(mainOp, '*')] ?? 0;
      let streak = 0;
      for (let i = xs.length - 1; i >= 0 && !kindOf.get(xs[i]!); i--) streak++;
      const proven = xs.filter((a) => a.outcome?.status === 'VERIFIED').length;
      const spend = spendOf(xs);
      return {
        id, name: w.name, initials: w.initials, team: teamOfActor(id), app: appOf(xs.at(-1)!).id, op: mainOp,
        copies: Math.max(1, agents.filter((g) => g.actor_id === id).length),
        n, w: n / Math.max(1, total), spend, cost: spend / Math.max(1, n), proven, perProven: proven ? spend / proven : null,
        repeats: xs.filter((a) => stepDecision(a)?.d === 'DEDUPLICATE').length,
        stale: xs.filter((a) => kindOf.get(a) === 'recheck').length,
        bad: xs.filter((a) => kindOf.get(a) === 'bad').length,
        streak, level, levelName: LEVELS[level], loop: loops.has(id) ? 1 : 0, loopOp: loopOp.get(id) ?? null,
        // The API doesn't say how old an AI's reads were.
        lag: null as number | null,
        recent: ctx.now - xs.at(-1)!.at < 15 * MIN,
      };
    });
  const slipRate = (f: (typeof fleetRaw)[number]) => (f.stale + f.bad) / Math.max(1, f.n);
  const med = median(fleetRaw.map(slipRate));
  const fleet = fleetRaw.map((f) => ({
    ...f,
    promote: f.level === 2 && f.streak >= 500,
    demote: f.n >= 20 && slipRate(f) > Math.max(0.01, 3 * med),
  }));

  // ─── money ──────────────────────────────────────────
  const spend = fin?.total_cost_micros_usd ? fin.total_cost_micros_usd / 1e6 : spendOf(acts);
  const unit = spend / Math.max(1, total);
  const byWaste = (fin?.by_waste ?? []).map((b) => ({ key: b.key, t: b.label, n: b.count, usd: b.amount_micros_usd / 1e6 }));
  const superseded = byWaste.find((b) => /supersed|replaced/i.test(`${b.key} ${b.t}`));
  const waste = {
    repeats: { t: 'Repeats Lumni stopped before they ran', n: kinds.stop, usd: kinds.stop * unit, avoided: true },
    stale: { t: 'Work redone after reading old info', n: kinds.recheck, usd: kinds.recheck * unit, avoided: false },
    loops: { t: 'Retry loops Lumni cut short', n: loopCount.n, usd: loopCount.n * unit, avoided: true },
    superseded: { t: 'Work thrown away because a newer task replaced it', n: superseded?.n ?? 0, usd: superseded?.usd ?? 0, avoided: false },
    failed: { t: 'Actions that never happened', n: kinds.bad, usd: kinds.bad * unit, avoided: false },
  };
  const hoursSaved = Math.round(kinds.stop * HOURS_PER.stop + kinds.bad * HOURS_PER.bad + kinds.ask * HOURS_PER.ask + kinds.wait * HOURS_PER.wait);
  const avoidedSpend = waste.repeats.usd + waste.loops.usd;
  const price = Number(ctx.env.pricePerAction ?? NaN);
  const lumniCost = Number.isFinite(price) && price > 0 ? total * price : null;
  const pace = spend / Math.min(1, nowH / 24);
  const money = {
    unit, waste, extra: byWaste, avoidedSpend, hoursSaved, lumniCost,
    wasted: Object.values(waste).filter((x) => !x.avoided).reduce((s, x) => s + x.usd, 0),
    roi: lumniCost ? (kept + avoidedSpend + hoursSaved * 85) / lumniCost : null,
    limit: null as number | null,
    perProven: verified ? spend / verified : null,
  };

  // ─── an incident: the busiest ten minutes, if it stands out ─
  let incident = null;
  const steppy = acts.filter((a) => kindOf.get(a));
  if (steppy.length >= 10) {
    let best = { i: 0, j: 0 };
    for (let i = 0, j = 0; i < steppy.length; i++) {
      while (j < steppy.length && steppy[j]!.at - steppy[i]!.at <= 10 * MIN) j++;
      if (j - i > best.j - best.i) best = { i, j };
    }
    const win = steppy.slice(best.i, best.j);
    const per10 = steppy.length / Math.max(1, (nowH * 60) / 10);
    if (win.length >= 10 && win.length >= 5 * per10) {
      const start = win[0]!.at;
      const end = win.at(-1)!.at;
      const topApp = [...by(win, (a) => appOf(a).name)].sort((a, b) => b[1].length - a[1].length)[0]![0];
      const notable = [win[0]!, ...win.filter((_, k) => k > 0 && k < win.length - 1 && k % Math.ceil(win.length / 6) === 0), win.at(-1)!];
      incident = {
        title: `${win.length} step-ins in ${Math.max(1, Math.round((end - start) / MIN))} minutes on ${topApp}`,
        total: win.length, app: topApp,
        byKind: Object.fromEntries((['stop', 'wait', 'recheck', 'ask', 'bad'] as const).map((k) => [k, win.filter((x) => kindOf.get(x) === k).length])),
        // Step-ins in each minute from the first to the last.
        perMinute: (() => {
          const m = Array<number>(Math.max(1, Math.ceil((end - start) / MIN) + 1)).fill(0);
          for (const x of win) m[Math.floor((x.at - start) / MIN)]!++;
          return m;
        })(),
        start: labels(start, z).time, end: labels(end, z).time, startAt: start, endAt: end,
        burst: win.filter((a) => kindOf.get(a) === 'stop').length,
        kept: win.reduce((s, a) => s + keptMoney(a), 0),
        customers: new Set(win.map((a) => customerOf(a)).filter(Boolean)).size,
        ais: new Set(win.map((a) => a.actor)).size,
        events: notable.map((a) => ({
          t: labels(a.at, z).clock, s: Math.round((a.at - start) / 1000),
          kind: kindOf.get(a) === 'bad' ? 'bad' : kindOf.get(a) ?? 'app', text: stepText(a), story: a.id,
        })),
      };
    }
  }

  // ─── things many AIs touch, and facts they read ─────
  const touches = new Map<string, Act[]>();
  for (const a of acts) for (const r of a.resources) {
    const l = touches.get(r.id);
    if (l) l.push(a);
    else touches.set(r.id, [a]);
  }
  const nameOf = (id: string) => day.resources.get(id)?.name ?? day.resources.get(id)?.ext ?? id;
  const contended = [...touches].map(([id, xs]) => ({
    id, name: nameOf(id), app: appOf(xs[0]!).id, touches: xs.length,
    // Step-ins on this thing, hour by hour, by kind, so a screen can place them in the day.
    hours: Object.fromEntries((['stop', 'wait', 'recheck', 'ask', 'bad'] as const).map((k) => {
      const h = Array<number>(24).fill(0);
      for (const a of xs) if (kindOf.get(a) === k) h[hourOf(a)]!++;
      return [k, h];
    })),
    stepins: xs.filter((a) => kindOf.get(a)).length,
    // Who touched it today, busiest first, so screens can name them.
    ais: [...by(xs.filter((a) => a.actor), (a) => a.actor!)].sort((p, q) => q[1].length - p[1].length).slice(0, 8)
      .map(([id, l]) => ({ id, name: who(id).name, initials: who(id).initials, human: who(id).human, n: l.length })),
    waits: xs.filter((a) => kindOf.get(a) === 'wait').length,
    rechecks: xs.filter((a) => kindOf.get(a) === 'recheck').length,
    holders: new Set(xs.map((a) => a.actor)).size,
    last: xs.at(-1)!,
  }));
  const things = [...contended].sort((a, b) => b.stepins - a.stepins || b.waits + b.holders - (a.waits + a.holders) || b.touches - a.touches).slice(0, 10)
    .map(({ last: _l, rechecks: _r, ...t }) => t);
  const facts = contended.filter((t) => t.rechecks > 0 || t.holders > 1).sort((a, b) => b.rechecks - a.rechecks || b.holders - a.holders).slice(0, 6)
    .map((t) => {
      const res = day.resources.get(t.id);
      const v = Number(res?.version);
      const readers = touches.get(t.id)!;
      return {
        id: t.id, thing: t.name, field: res?.type ?? 'record', v: Number.isFinite(v) ? v : null,
        value: res?.version ? `version ${res.version}` : 'current version', was: null as string | null,
        changedBy: who(t.last.actor).name, human: who(t.last.actor).human, ago: Math.round((ctx.now - t.last.at) / 1000),
        readers: t.holders, stale: new Set(readers.filter((a) => kindOf.get(a) === 'recheck').map((a) => a.actor)).size,
        reads: t.touches, caught: t.rechecks,
      };
    });

  const now = labels(ctx.now, z);
  const perDay = Math.round((total / nowH) * 24);
  const ais = new Set(acts.filter((a) => isAi(a.actor)).map((a) => a.actor)).size;
  return {
    key: 'real',
    now: ctx.now, nowH, dayStart: from, zone: z.name, dayLabel: now.day, timeLabel: now.time,
    mode: rs.tenantMode, partial: day.partial, notes: [...ctx.partial],
    scale: {
      org: ctx.env.workspace ?? 'Your workspace', long: ctx.env.workspace ?? 'Your workspace',
      actions: perDay, label: compact(perDay), ais, apps: appIds.size, things: touches.size,
      people: new Set([...actors.values()].filter((a) => a.type === 'HUMAN').map((a) => a.id)).size,
    },
    total, kinds, would, steps, kept, hourly, stepHourly,
    proven: total ? verified / total : 0,
    spend, pace, working: fleet.filter((f) => f.recent).length,
    teams, apps, patterns, groups, fleet, money, incident, things, facts, LEVELS,
  };
}
