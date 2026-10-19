// /v1/console/map with real data: one action, the things it touched, and
// whether the app agrees it happened. Other actions on the same things today
// sit beside it, so a repeat Lumni stopped shows up dashed next to the first.

import type { MapEdge, MapFix, MapNode, MapPickItem, MapSpan, MapView, ProofV } from '../../../src/domain/types';
import { time } from '../../../src/domain/format';
import { HttpError } from '../../http';
import { items, ms, obj } from '../../upstream';
import type { GoAttempt } from '../go';
import type { Ctx } from '../load';
import { appIdOf, appName, customerOf, didOf, isBad, stepDecision, stepKind, verdict, type Act } from '../model';
import { cap, lowerFirst, reasonWords, stepText } from '../words';
import { checkers, people, proofIn, type Checkers } from './shared';
import { thingName } from './live';

const SEC = 1000;
const OTHERS = 4;
const MODE: Record<string, string> = { WRITE: 'changed', READ: 'read', DELETE: 'deleted', OBSERVE: 'looked at', CREATE: 'created' };

export async function mapView(ctx: Ctx, id: string | null): Promise<MapView> {
  const [today, who, chk] = await Promise.all([ctx.today(), people(ctx), checkers(ctx)]);
  const latest = [...today.acts].reverse().find((a) => a.resources.length) ?? today.acts.at(-1);
  const target = id ?? latest?.id;
  if (!target) throw new HttpError(404, 'not_found', 'There’s nothing to map yet today.');
  const a = await ctx.one(target);
  if (!a) throw new HttpError(404, 'not_found', 'There’s no map for that action.');

  const z = ctx.zone;
  const from = z.dayStart(a.at);
  const day = from === z.dayStart(ctx.now) ? today : await ctx.day(from, Math.min(ctx.now, from + 86_400_000));
  const proofOf = proofIn(chk);
  const by = who(a.actor);
  const did = didOf(a);
  const p = proofOf(a);
  const d = stepDecision(a);
  const attempts = a.outcome ? await attemptsOf(ctx, a.outcome.id) : [];

  const things = a.resources.length ? a.resources : [{ id: '', mode: 'WRITE' }];
  const mid = (things.length - 1) / 2;
  const nodes: MapNode[] = [];
  const edges: MapEdge[] = [];

  nodes.push({
    id: 'act', layer: 0, row: mid, actor: by, label: by.name, title: did,
    verdict: verdict(a), proof: stepKind(a) === 'stop' ? p : { kind: 'ok', text: 'Checked before it ran' },
    receipt: a.id, story: stepKind(a) ? a.id : undefined,
    timeline: [
      { at: a.at, text: `${by.name} asked to ${lowerFirst(did)}` },
      d ? { at: d.at || a.at, text: `${stepText(a)}, since ${reasonWords(d.r)}` } : { at: a.at, text: 'Lumni checked it against your rules and let it go ahead' },
    ],
  });

  const others = new Map<string, Act>();
  things.forEach((r, i) => {
    const node = thingNode(a, r, i, day.resources.get(r.id), p, attempts, chk, ctx.now);
    nodes.push(node);
    edges.push({ from: 'act', to: node.id, label: MODE[r.mode] ?? r.mode.toLowerCase(), pulse: p.kind === 'busy' && p.text !== 'In progress' });
    if (!r.id) return;
    for (const o of day.acts) {
      if (o.id === a.id || others.size >= OTHERS || !o.resources.some((x) => x.id === r.id)) continue;
      others.set(o.id, o);
    }
  });

  [...others.values()].forEach((o, i) => {
    const ow = who(o.actor);
    const stopped = stepKind(o) === 'stop';
    const nid = `o:${o.id}`;
    nodes.push({
      id: nid, layer: 0, row: things.length + i, actor: ow, label: ow.name, title: didOf(o), would: stopped || undefined,
      verdict: verdict(o), proof: proofOf(o), receipt: o.id, story: stepKind(o) ? o.id : undefined,
      timeline: [{ at: o.at, text: `${ow.name} asked to ${lowerFirst(didOf(o))}` }, ...(stopped ? [{ at: o.at, text: `${stepText(o)}` }] : [])],
    });
    for (const r of o.resources) {
      if (!things.some((x) => x.id === r.id)) continue;
      edges.push({ from: nid, to: `r:${r.id}`, label: stopped ? 'tried too' : o.at < a.at ? 'came before' : 'came after', tone: stopped ? 'would' : undefined });
    }
  });

  const appNodes = nodes.filter((n) => n.app);
  const tallies = { checked: 0, bad: 0, waiting: 0, none: 0, busy: 0 };
  for (const n of appNodes) {
    const k = n.proof.kind;
    if (k === 'ok') tallies.checked += 1;
    else if (k === 'bad') tallies.bad += 1;
    else if (k === 'wait') tallies.waiting += 1;
    else if (k === 'none') tallies.none += 1;
    else if (k === 'busy') tallies.busy += 1;
  }
  const app = appName(appIdOf(a));
  const span = windowOf(nodes, a.at, ctx.now);

  return {
    id: a.id,
    scope: 'action',
    customer: customerOf(a) ?? undefined,
    one: 'This action',
    title: titleOf(p, did, app, things.length),
    sub: `It reached ${things.length} ${things.length === 1 ? 'thing' : 'things'} in ${app}. Lumni checks what the app shows after an AI acts.`,
    at: a.at,
    from: span.from,
    to: span.to,
    nodes,
    edges,
    tallies,
    picks: picks(today.acts, a, who),
  };
}

async function attemptsOf(ctx: Ctx, outcomeId: string): Promise<GoAttempt[]> {
  try {
    return items<GoAttempt>(await ctx.get(`/v1/verification-attempts/${encodeURIComponent(outcomeId)}`));
  } catch {
    return [];
  }
}

function thingNode(a: Act, r: { id: string; mode: string }, row: number, res: Parameters<typeof thingName>[1], p: ProofV, attempts: GoAttempt[], chk: Checkers, now: number): MapNode {
  const appId = res?.ns || appIdOf(a);
  const app = { id: appId, name: appName(appId) };
  const named = r.id ? thingName(r.id, res) : { thing: didOf(a), sub: app.name };
  const o = a.outcome;
  const okAt = o?.status === 'VERIFIED' ? o.updatedAt ?? undefined : undefined;
  const checkedAt = o?.updatedAt ?? null;
  const spans: MapSpan[] = [];
  if (p.kind === 'stopped') spans.push({ kind: 'none', t0: a.at, t1: a.at + SEC, label: 'Never sent' });
  else if (p.kind === 'wait') spans.push({ kind: 'wait', t0: a.at, t1: null, label: p.text });
  else if (p.kind === 'busy') spans.push({ kind: 'busy', t0: a.at, t1: null });
  else if (p.kind === 'bad') spans.push({ kind: 'busy', t0: a.at, t1: checkedAt ?? a.at + SEC }, { kind: 'bad', t0: checkedAt ?? a.at + SEC, t1: null });
  else if (p.kind === 'none') spans.push({ kind: 'busy', t0: a.at, t1: a.at + 2 * SEC }, { kind: 'none', t0: a.at + 2 * SEC, t1: null });
  else spans.push({ kind: 'busy', t0: a.at, t1: okAt ?? checkedAt ?? a.at + SEC });

  const timeline = attempts.map((x) => {
    const ev = obj(x.evidence);
    const st = String(ev.status ?? '');
    return { at: ms(x.created_at) ?? now, text: st === 'VERIFIED' ? `${app.name} shows it` : st ? `Lumni checked ${app.name}: ${lowerFirst(cap(st.toLowerCase().replace(/_/g, ' ')))}` : `Lumni checked ${app.name}` };
  });
  let fix: MapFix | undefined;
  if (p.kind === 'bad' && o) fix = { kind: 'retry', post: `/v1/outcomes/${encodeURIComponent(o.id)}/retry`, label: `Check ${app.name} again`, done: `Checking ${app.name} again` };
  else if (p.kind === 'none' && chk.off.has(appIdOf(a))) fix = { kind: 'connect', app: appIdOf(a), label: `Check ${app.name}`, done: `Lumni checks ${app.name} again` };
  const stepAt = stepKind(a) ? stepDecision(a)?.at || a.at : null;
  return {
    id: `r:${r.id}`,
    layer: 1,
    row,
    app,
    label: named.sub.replace(new RegExp(`^${app.name}\\s*`, 'i'), '') || 'Record',
    title: named.thing,
    verdict: isBad(a) ? { kind: 'bad', would: false } : undefined,
    proof: p,
    receipt: a.id,
    timeline,
    fix,
    track: { spans, okAt, beads: stepAt !== null && stepKind(a) ? [{ at: stepAt, kind: stepKind(a)!, label: stepText(a), rec: a.id }] : [] },
  };
}

function titleOf(p: ProofV, did: string, app: string, n: number): string {
  if (p.kind === 'bad') return `${app} doesn’t show “${did}”`;
  if (p.kind === 'stopped') return `Lumni stopped “${did}” before it reached ${app}`;
  if (p.kind === 'busy') return `Lumni is checking “${did}” in ${app}`;
  if (p.kind === 'wait') return `“${did}” is waiting`;
  if (p.kind === 'ok') return `“${did}” checks out in ${app}`;
  return `Lumni can’t check “${did}” in ${app}${n > 1 ? ' yet' : ''}`;
}

function windowOf(nodes: MapNode[], at: number, now: number): { from: number; to: number } {
  let first = at;
  let last = at;
  let open = false;
  for (const n of nodes) {
    for (const s of n.track?.spans ?? []) {
      first = Math.min(first, s.t0);
      if (s.t1 === null) open ||= s.kind !== 'none';
      else last = Math.max(last, s.t1);
    }
    if (n.track?.okAt !== undefined) last = Math.max(last, n.track.okAt);
    for (const t of n.timeline) last = Math.max(last, t.at);
  }
  const end = open ? now : Math.min(now, last);
  const pad = Math.max(20 * SEC, (end - first) * 0.08);
  return { from: first - pad, to: end + pad };
}

function picks(acts: Act[], cur: Act, who: (id: string | null) => { name: string }): MapPickItem[] {
  const out: Act[] = [];
  for (let i = acts.length - 1; i >= 0 && out.length < 8; i--) {
    const a = acts[i]!;
    if (a.resources.length && a.id !== cur.id) out.push(a);
  }
  out.unshift(cur);
  return out.map((a) => ({ id: a.id, title: didOf(a), sub: `${who(a.actor).name}, ${time(a.at)}` }));
}
