// /v1/console/live with real data: who holds each shared thing right now,
// who waits behind them, and the last hour on each. “Things” are the
// resources actions touch: a charge, an account, a ticket.

import type { HandOff, LaneMark, LaneV, LiveView, Stretch, Who, Worker } from '../../../src/domain/types';
import { HOUR, MIN, plural, time } from '../../../src/domain/format';
import { splitResourceId, type Res } from '../day';
import type { Ctx } from '../load';
import { appName, didOf, stepDecision, stepKind, verdict, type Act } from '../model';
import { cap, reasonWords } from '../words';
import { people } from './shared';

/** How far back each lane looks. */
const BACK = 70 * MIN;
/** Work this close together reads as one block. */
const JOIN = 30_000;
/** How long a finished action holds its thing, when nothing says. */
const HELD = 20_000;
const MOST_LANES = 40;

const HOLDING = new Set(['RESERVED', 'ACTIVE']);

/** Waiting its turn: held, sent back, or asking you. */
function waiting(a: Act): boolean {
  if (a.status === 'AWAITING_APPROVAL') return !a.approval || a.approval.status === 'PENDING';
  if (a.status !== 'PROPOSED') return false;
  const d = a.decision;
  return !!d && (d.d === 'WAIT' || d.d === 'REPLAN');
}

export function thingName(id: string, res: Res | undefined): { thing: string; sub: string } {
  const split = res ?? { ...splitResourceId(id), name: null as string | null };
  const sub = `${split.ns ? `${appName(split.ns)} ` : ''}${(split.type || 'thing').replace(/[._]+/g, ' ')}`;
  return { thing: res?.name ?? split.ext ?? id, sub: cap(sub.trim()) };
}

export async function liveView(ctx: Ctx): Promise<LiveView> {
  const [edges, day, who] = await Promise.all([ctx.live(), ctx.today(), people(ctx)]);
  const t = ctx.now;
  const byId = new Map(day.acts.map((a) => [a.id, a]));
  // Actions still under way from before today.
  for (const id of [...new Set(edges.map((e) => e.action_id))].filter((x) => !byId.has(x)).slice(0, 30)) {
    const a = await ctx.one(id).catch(() => null);
    if (a) byId.set(id, a);
  }

  const onThing = new Map<string, Act[]>();
  const add = (rid: string, a: Act) => {
    const l = onThing.get(rid);
    if (!l) onThing.set(rid, [a]);
    else if (!l.includes(a)) l.push(a);
  };
  for (const e of edges) {
    const a = byId.get(e.action_id);
    if (a) add(e.resource_id, a);
  }
  for (const a of day.acts) {
    if (a.at < t - BACK) continue;
    for (const r of a.resources) add(r.id, a);
  }

  const lanes: LaneV[] = [];
  for (const [rid, list] of onThing) {
    list.sort((x, y) => x.at - y.at);
    lanes.push(lane(rid, list, day.resources.get(rid), who, t));
  }
  // What’s busy now first, then what was busiest lately.
  lanes.sort((a, b) => Number(!!b.now) - Number(!!a.now) || b.next.length - a.next.length || b.past.length - a.past.length);
  const shown = lanes.slice(0, MOST_LANES);
  if (lanes.length > MOST_LANES) ctx.note(`Showing the ${MOST_LANES} busiest things of ${lanes.length}.`);

  const ais = new Set<string>();
  let working = 0;
  let waitingN = 0;
  for (const l of shown) {
    if (l.now) {
      ais.add(l.now.who.id);
      working += 1;
    }
    for (const x of l.next) ais.add(x.who.id);
    waitingN += l.next.length;
  }
  const held = day.acts.filter((a) => stepKind(a) === 'wait').length;
  const things = shown.filter((l) => l.now || l.next.length).length;

  return {
    head: head(ais.size, things, shown),
    lanes: shown,
    ais: ais.size,
    people: [],
    things,
    waiting: waitingN,
    working,
    held,
    paused: false,
    pausedAt: null,
    log: handoffs(onThing, day.resources, who, t),
  };
}

function worker(a: Act, who: (id: string | null) => Who): Worker {
  return { who: who(a.actor), doing: didOf(a) };
}

function lane(rid: string, list: Act[], res: Res | undefined, who: (id: string | null) => Who, t: number): LaneV {
  const { thing, sub } = thingName(rid, res);
  const holder = [...list].reverse().find((a) => HOLDING.has(a.status));
  const queue = list.filter((a) => a !== holder && waiting(a));
  const first = queue[0];
  const done = list.filter((a) => !HOLDING.has(a.status) && !waiting(a) && a.at >= t - BACK);

  const out: LaneV = {
    id: rid, thing, sub,
    now: holder ? worker(holder, who) : null,
    next: queue.map((a) => worker(a, who)),
    verdict: null,
    say: 'Nobody is on it.',
    small: 'It’s free for whoever needs it next.',
    past: blocks(done, who, t),
    since: holder ? holder.at : null,
    marks: list
      .filter((a) => a.at >= t - BACK - 10 * MIN && stepKind(a))
      .map((a): LaneMark => ({ at: stepDecision(a)?.at || a.at, kind: stepKind(a)!, rec: a.id })),
    soon: [],
  };
  if (first) {
    const w = who(first.actor);
    out.verdict = verdict(first);
    const d = stepDecision(first);
    out.say = first.status === 'AWAITING_APPROVAL' ? `${w.name} waits for you before it goes ahead.` : holder ? `${w.name} goes next, once ${who(holder.actor).name} is done.` : `${w.name} goes next.`;
    out.small = d ? `${cap(reasonWords(d.r))}.` : 'Lumni lines it up behind the one before.';
    if (first.status === 'AWAITING_APPROVAL') {
      out.link = { label: 'Open Needs you', to: '/needs-you' };
      if (first.approval) out.ask = { from: first.approval.at, until: first.approval.expiresAt ?? first.approval.at + 24 * HOUR, who: w };
    }
  } else if (holder) {
    const w = who(holder.actor);
    out.say = `${w.name} is working on “${didOf(holder)}”.`;
    out.small = `Since ${time(holder.at)}. Anyone else who needs it waits their turn.`;
  }
  return out;
}

/** Finished work on a thing, joined up where it ran back to back. */
function blocks(done: Act[], who: (id: string | null) => Who, t: number): Stretch[] {
  const out: (Stretch & { ids: Set<string>; n: number })[] = [];
  for (let i = 0; i < done.length; i++) {
    const a = done[i]!;
    const next = done[i + 1];
    const t1 = Math.min(t, a.at + HELD, next ? next.at : Infinity);
    const w = who(a.actor);
    const last = out.at(-1);
    if (last && a.at - last.t1 < JOIN) {
      last.t1 = Math.max(last.t1, t1);
      last.ids.add(w.id);
      last.n += 1;
      continue;
    }
    out.push({ who: w, t0: a.at, t1: Math.max(a.at + 1000, t1), label: w.name, doing: didOf(a), ids: new Set([w.id]), n: 1 });
  }
  return out.map(({ ids, n, ...b }) => {
    if (n === 1) return b;
    if (ids.size === 1) return { ...b, doing: `${n} pieces of work` };
    return { ...b, who: null, label: `${ids.size} AIs`, doing: `${n} pieces of work by ${ids.size} AIs` };
  });
}

function head(ais: number, things: number, lanes: LaneV[]): LiveView['head'] {
  if (!things) {
    return {
      title: 'Nothing is clashing',
      sub: lanes.length ? 'Nobody is on anything shared right now. The last hour of each thing is below.' : 'Nobody is on anything shared right now. When two AIs need the same thing, Lumni lines them up here.',
    };
  }
  const waitingLane = lanes.find((l) => l.now && l.next.length);
  const crew = `${ais} ${plural(ais, 'AI')} ${ais === 1 ? 'is' : 'are'} on ${things} ${plural(things, 'thing')}.`;
  return {
    title: 'Nothing is clashing',
    sub: `${crew} ${waitingLane ? `${waitingLane.next[0]!.who.name} goes next on ${waitingLane.thing}, when ${waitingLane.now!.who.name} is done.` : 'Each thing has one worker at a time. Lumni lines up everyone else.'}`,
  };
}

function handoffs(onThing: Map<string, Act[]>, resources: Map<string, Res>, who: (id: string | null) => Who, t: number): HandOff[] {
  const out: HandOff[] = [];
  for (const [rid, list] of onThing) {
    const recent = list.filter((a) => a.at > t - HOUR);
    for (let i = 1; i < recent.length; i++) {
      const a = recent[i - 1]!;
      const b = recent[i]!;
      if (!a.actor || !b.actor || a.actor === b.actor) continue;
      out.push({ at: b.at, thing: thingName(rid, resources.get(rid)).thing, text: `${who(b.actor).name} went after ${who(a.actor).name}` });
    }
  }
  return out.sort((a, b) => b.at - a.at).slice(0, 40);
}
