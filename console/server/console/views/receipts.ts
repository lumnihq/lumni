// /v1/console/receipts with real data: every action of the day, what Lumni
// decided before it, and whether the app shows it happened. Search, filters,
// the stretch of the day and paging all happen here, since the API can’t.

import type {
  ProofV, ReceiptAction, ReceiptDetail, ReceiptFilter, ReceiptRow, ReceiptsView, Who,
} from '../../../src/domain/types';
import { DAY, date, fmt, plural, time, words } from '../../../src/domain/format';
import { HttpError } from '../../http';
import type { Ctx } from '../load';
import { appOf, customerOf, didOf, isBad, stepKind, verdict, type Act } from '../model';
import { and, cap, reasonWords, stepText } from '../words';
import { checkers, people, proofIn, type Checkers } from './shared';

export const PAGE = 60;

type Kind = Exclude<ReceiptFilter, 'stepins'>;

function kindOf(p: ProofV): Kind {
  switch (p.kind) {
    case 'ok':
      return 'checked';
    case 'none':
      return 'none';
    case 'stopped':
      return 'stopped';
    case 'bad':
      return 'bad';
    default:
      return 'waiting';
  }
}

export interface Query {
  day: 'today' | 'yesterday';
  filter: ReceiptFilter | null;
  q: string;
  ai: string | null;
  app: string | null;
  from: number | null;
  to: number | null;
  limit: number;
  focus: string | null;
}

const zero = (): Record<ReceiptFilter, number> => ({ checked: 0, none: 0, stopped: 0, waiting: 0, bad: 0, stepins: 0 });

interface Entry {
  a: Act;
  h: number;
  who: Who;
  proof: ProofV;
  kind: Kind;
  stepin: boolean;
  hay: string;
}

export async function receiptsView(ctx: Ctx, query: Query): Promise<ReceiptsView> {
  const isToday = query.day === 'today';
  const [read, who, chk] = await Promise.all([isToday ? ctx.today() : ctx.yesterday(), people(ctx), checkers(ctx)]);
  const z = ctx.zone;
  const start = isToday ? z.dayStart(ctx.now) : z.dayStart(z.dayStart(ctx.now) - DAY / 2);
  const nowH = isToday ? z.hourOf(ctx.now) : 24;
  const proofOf = proofIn(chk);

  const all: Entry[] = read.acts.map((a) => {
    const w = who(a.actor);
    const p = proofOf(a);
    const app = appOf(a);
    return {
      a, h: z.hourOf(a.at), who: w, proof: p, kind: kindOf(p), stepin: stepKind(a) !== null,
      hay: `${didOf(a)} ${w.name} ${app.name} ${customerOf(a) ?? ''} ${a.op} ${a.id}`.toLowerCase(),
    };
  });

  // The stretch of the day, snapped to quarter hours.
  const from = Math.max(0, Math.min(nowH, Math.floor((query.from ?? 0) * 4) / 4));
  let to = query.to === null ? null : Math.ceil(query.to * 4) / 4;
  if (to !== null && (to >= nowH || to <= from)) to = null;
  const end = to ?? 24;
  const needles = query.q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (e: Entry) =>
    (!query.ai || e.a.actor === query.ai) && (!query.app || appOf(e.a).id === query.app) && needles.every((n) => e.hay.includes(n));

  const tallies = zero();
  const dayTally = zero();
  const count = isToday ? Math.min(96, Math.floor(nowH * 4) + 1) : 96;
  const buckets = new Array<number>(count).fill(0);
  const hatch = new Array<number>(count).fill(0);
  const stops: { id: string; h: number }[] = [];
  const bad: { id: string; h: number }[] = [];
  const shown: Entry[] = [];
  for (const e of all) {
    const qi = Math.min(count - 1, Math.floor(e.h * 4));
    buckets[qi]! += 1;
    if (e.kind === 'none') hatch[qi]! += 1;
    if (e.kind === 'stopped') stops.push({ id: e.a.id, h: e.h });
    if (isBad(e.a)) bad.push({ id: e.a.id, h: e.h });
    dayTally[e.kind] += 1;
    if (e.stepin) dayTally.stepins += 1;
    if (e.h < from || e.h >= end || !hit(e)) continue;
    shown.push(e);
    tallies[e.kind] += 1;
    if (e.stepin) tallies.stepins += 1;
  }
  const peak = Math.max(1, ...buckets);
  const hatchPeak = Math.max(1, ...hatch);

  const matching = shown.filter((e) => !query.filter || (query.filter === 'stepins' ? e.stepin : e.kind === query.filter)).reverse();
  let limit = Math.max(PAGE, query.limit);
  let focusHidden = false;
  if (query.focus) {
    const i = matching.findIndex((e) => e.a.id === query.focus);
    if (i < 0) focusHidden = all.some((e) => e.a.id === query.focus);
    else if (i >= limit) limit = Math.ceil((i + 1) / PAGE) * PAGE;
  }

  const ais = new Map<string, Who>();
  const apps = new Map<string, { id: string; name: string }>();
  for (const e of all) {
    if (e.a.actor) ais.set(e.a.actor, e.who);
    const app = appOf(e.a);
    apps.set(app.id, app);
  }

  return {
    day: query.day,
    date: date(start),
    head: head(isToday, all, dayTally, chk),
    ais: [...ais.values()].sort((a, b) => a.name.localeCompare(b.name)),
    apps: [...apps.values()].sort((a, b) => a.name.localeCompare(b.name)),
    partial: read.partial ? 'Some busy minutes had more actions than Lumni could read at once, so this list may be missing a few.' : null,
    total: all.length,
    ribbon: { now: nowH, quarters: buckets.map((c) => c / peak), hatch: hatch.map((c) => c / hatchPeak), stops, bad },
    window: { from, to },
    shown: shown.length,
    tallies,
    tip: isToday ? tipOf(all) : null,
    items: matching.slice(0, limit).map(row),
    matched: matching.length,
    focusHidden,
  };
}

function row(e: Entry): ReceiptRow {
  return { id: e.a.id, at: e.a.at, who: e.who, did: didOf(e.a), app: appOf(e.a), verdict: verdict(e.a), proof: e.proof, flagged: isBad(e.a) };
}

function head(isToday: boolean, all: Entry[], tally: Record<ReceiptFilter, number>, chk: Checkers): ReceiptsView['head'] {
  const when = isToday ? 'today' : 'yesterday';
  if (!all.length) {
    return { title: `No actions ${when}`, sub: 'Every action your AIs take shows up here, with what Lumni decided and whether the app shows it happened.' };
  }
  const flagged = all.filter((e) => isBad(e.a));
  if (isToday && flagged.length) {
    const last = flagged.at(-1)!;
    const more = flagged.length > 1 ? ` ${cap(words(flagged.length - 1))} more ${flagged.length > 2 ? 'are' : 'is'} flagged below.` : '';
    return {
      title: `${words(flagged.length, true)} ${plural(flagged.length, 'action')} didn’t happen`,
      sub: `${last.who.name} said “${didOf(last.a)}” at ${time(last.a.at)}, but ${appOf(last.a).name} doesn’t show it.${more}`,
    };
  }
  const parts = [`${fmt(tally.checked)} checked in their apps`, `${fmt(tally.stopped)} stopped before they went out`];
  if (tally.none) {
    const blind = [...new Set(all.filter((e) => e.kind === 'none').map((e) => appOf(e.a)))];
    const off = blind.filter((a) => chk.off.has(a.id)).map((a) => a.name);
    parts.push(off.length ? `${fmt(tally.none)} Lumni can’t check while ${and(off)} ${off.length > 1 ? 'aren’t' : 'isn’t'} checked` : `${fmt(tally.none)} Lumni can’t check`);
  }
  if (tally.waiting) parts.push(`${fmt(tally.waiting)} still in progress`);
  return { title: `Your AIs took ${fmt(all.length)} ${plural(all.length, 'action')} ${when}`, sub: `${cap(and(parts))}.` };
}

/** The newest action that didn’t happen, pinned to the ribbon with a retry. */
function tipOf(all: Entry[]): ReceiptsView['tip'] {
  for (let i = all.length - 1; i >= 0; i--) {
    const e = all[i]!;
    const o = e.a.outcome;
    if (!o) continue;
    const retrying = o.status === 'PENDING' && isRetry(e.a);
    if (!isBad(e.a) && !retrying) continue;
    const app = appOf(e.a).name;
    return {
      id: e.a.id,
      h: e.h,
      retrying,
      title: retrying ? `Retrying “${didOf(e.a)}”` : `“${didOf(e.a)}” never arrived`,
      note: retrying ? `Lumni is checking ${app} again.` : `${e.who.name} did it at ${time(e.a.at)}. ${app} has no record of it.`,
      post: `/v1/outcomes/${encodeURIComponent(o.id)}/retry`,
    };
  }
  return null;
}

/** An outcome checked again after it failed: pending with its attempts reset. */
function isRetry(a: Act): boolean {
  const o = a.outcome;
  return !!o && o.status === 'PENDING' && o.attempts === 0 && o.updatedAt !== null && o.updatedAt > a.at + 30_000;
}

// ─── one receipt ───────────────────────────────────────

export async function receiptDetail(ctx: Ctx, id: string): Promise<ReceiptDetail> {
  const a = await ctx.one(id);
  if (!a) throw new HttpError(404, 'not_found', 'No receipt with that id.');
  const [who, chk] = await Promise.all([people(ctx), checkers(ctx)]);
  const by = who(a.actor);
  const app = appOf(a);
  const did = didOf(a);
  const proof = proofIn(chk)(a);
  const step = stepKind(a);
  const actions: ReceiptAction[] = [];
  const today = ctx.zone.dayStart(ctx.now);
  const at = a.at >= today ? `At ${time(a.at)}` : `At ${time(a.at)} on ${date(a.at)}`;
  const onApp = (tone: ReceiptDetail['shows']['tone'], title: string, note: string): ReceiptDetail['shows'] => ({ by: 'app', app, head: 'shows', title, note, tone });

  const said: ReceiptDetail['said'] = step
    ? { who: by, head: `${by.name} asked`, text: `“${did}.”`, note: `${at}, before it reached ${app.name}` }
    : { who: by, head: `${by.name} said`, text: `“${did}.”`, note: at };

  let shows: ReceiptDetail['shows'];
  const d = a.decision;
  if (step === 'stop' && d && d.d !== 'ALLOW' && d.d !== 'WOULD_ALLOW') {
    shows = { by: 'lumni', app, head: 'Lumni stepped in', title: stepText(a), note: `${cap(reasonWords(d.r))}.`, tone: 'lumni' };
    actions.push({ kind: 'story', label: 'Open its story', to: `/story/${a.id}` });
  } else if (a.outcome?.status === 'PENDING' && isRetry(a)) {
    shows = onApp('busy', `Checking ${app.name} again`, 'Lumni was asked to check it again.');
    actions.push({ kind: 'retry', label: 'Retrying…', busy: true });
  } else if (proof.kind === 'bad') {
    const o = a.outcome;
    const tries = Math.max(1, o?.attempts ?? 1);
    shows = onApp('off', proof.text === 'Failed' ? 'It failed' : 'No sign of it', o?.reason ? `${cap(o.reason)}.` : `Lumni checked ${words(tries)} ${plural(tries, 'time')}.`);
    if (o) actions.push({ kind: 'retry', label: 'Check it again', post: `/v1/outcomes/${encodeURIComponent(o.id)}/retry`, done: `Checking ${app.name} again` });
    actions.push({ kind: 'handled', label: 'Mark as handled', unavailable: 'Lumni can’t mark something handled yet.' });
  } else if (proof.kind === 'none') {
    const v = chk.byApp.get(app.id);
    if (v && !v.enabled) {
      shows = onApp('none', `Lumni isn’t checking ${app.name}`, `Its checker is switched off. Switch it on and Lumni checks what your AIs do there.`);
      shows.head = 'isn’t checked';
      actions.push({ kind: 'connect', label: `Check ${app.name}`, post: `/v1/console/apps/${encodeURIComponent(app.id)}/checker`, body: { on: true }, done: `Lumni checks ${app.name} again` });
    } else {
      shows = onApp('none', proof.text, a.status === 'UNKNOWN' ? `${by.name} couldn’t say whether it went through.` : `Nothing tells Lumni what ${app.name} shows for this one.`);
      shows.head = 'isn’t checked';
    }
  } else if (proof.kind === 'wait') {
    shows = onApp('wait', proof.text, `Nothing has changed in ${app.name} yet.`);
    if (a.status === 'AWAITING_APPROVAL') actions.push({ kind: 'need', label: 'Open Needs you', to: '/needs-you' });
  } else if (proof.kind === 'busy') {
    shows = proof.text === 'In progress' ? onApp('busy', 'Still in progress', `${by.name} hasn’t finished yet.`) : onApp('busy', `Checking ${app.name}…`, 'Lumni checks every action once it’s done.');
  } else if (proof.kind === 'stopped') {
    shows = onApp('ok', `Nothing changed in ${app.name}`, 'Lumni stopped it before it went out.');
  } else {
    const t = a.outcome?.updatedAt;
    shows = onApp('ok', did, t ? `Lumni checked at ${time(t)}. It matches.` : 'It matches.');
  }

  if (step && !actions.some((x) => x.kind === 'story')) actions.push({ kind: 'story', label: 'Open its story', to: `/story/${a.id}` });
  if (a.resources.length) actions.push({ kind: 'map', label: 'See what it reached', to: `/map/${a.id}` });
  return { id: a.id, said, shows, actions };
}
