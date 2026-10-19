// /v1/console/today with real data: the day so far, what is passing the gate
// right now, and where Lumni stepped in. Yesterday is the same day read,
// finished.

import type { Bead, FlowChip, StepKind, StepRow, TodayView } from '../../../src/domain/types';
import { DAY, MIN, date, fmt, money, plural, time, words } from '../../../src/domain/format';
import { ms } from '../../upstream';
import type { GoApproval } from '../go';
import type { Ctx } from '../load';
import { didOf, isBad, stepDecision, stepKind, verdict, wouldKind, type Act } from '../model';
import { and, cap, keptMoney, keptText, lowerFirst, reasonWords, stepText } from '../words';
import { checkers, hill, people, proofIn, quartersSoFar } from './shared';
import { openMismatches } from './frame';

const zero = (): Record<StepKind, number> => ({ stop: 0, wait: 0, recheck: 0, ask: 0 });

/** How long after it happens an action reaches the gate on the strip, so a fresh one can be seen arriving. */
const LAG = 1600;
/** How far back the strip shows what already went through. */
const STRIP = 12_000;

export async function todayView(ctx: Ctx, day: 'today' | 'yesterday'): Promise<TodayView> {
  const isToday = day === 'today';
  const [read, who, chk, approvals, incidents] = await Promise.all([
    isToday ? ctx.today() : ctx.yesterday(),
    people(ctx),
    checkers(ctx),
    ctx.approvals(),
    ctx.incidents(),
  ]);
  const z = ctx.zone;
  const start = isToday ? z.dayStart(ctx.now) : z.dayStart(z.dayStart(ctx.now) - DAY / 2);
  const nowH = isToday ? z.hourOf(ctx.now) : 24;
  const acts = read.acts;
  const proofOf = proofIn(chk);

  const tallies = zero();
  const steps: StepRow[] = [];
  const bad: { id: string; h: number }[] = [];
  let kept = 0;
  let wouldHave = 0;
  for (const a of acts) {
    if (wouldKind(a)) wouldHave += 1;
    if (isBad(a)) bad.push({ id: a.id, h: z.hourOf(a.at) });
    const k = stepKind(a);
    if (!k) continue;
    tallies[k] += 1;
    kept += keptMoney(a);
    steps.push(stepRow(a, k, who(a.actor)));
  }
  steps.sort((x, y) => y.at - x.at);

  const beads: Bead[] = steps.map((s) => ({ id: s.id, h: z.hourOf(s.at), step: s.step, text: s.text, verdict: s.verdict }));
  const latest = isToday && steps[0] ? { ...steps[0], h: z.hourOf(steps[0].at) } : null;
  const pending = approvals.filter((a) => a.status === 'PENDING').sort((x, y) => (ms(x.requested_at) ?? 0) - (ms(y.requested_at) ?? 0));
  const mismatches = openMismatches(incidents);

  const recent = isToday ? acts.filter((a) => a.at > ctx.now - 2 * MIN).length : 0;
  const perMinute = Math.round(recent / 2);
  const firstExpiry = pending.map((p) => ms(p.expires_at)).find((t): t is number => t !== null && t < start + DAY) ?? null;
  const deadline = isToday && firstExpiry !== null
    ? { from: z.hourOf(ms(pending[0]!.requested_at) ?? ctx.now), to: Math.min(24, z.hourOf(firstExpiry)), at: firstExpiry, note: 'The ask runs out, unless you choose first' }
    : null;

  return {
    day,
    date: date(start),
    head: head(isToday, { bad: bad.length, caught: steps.length, done: acts.length, kept, pending: pending.length, mismatches: mismatches.length, partial: read.partial }),
    ribbon: { now: nowH, quarters: hill(acts, start, isToday ? quartersSoFar(nowH) : 96, z), beads, latest, deadline, bad },
    bad: bad.length,
    perMinute,
    refundsKept: kept,
    done: acts.length,
    caught: steps.length,
    wouldHave,
    tallies,
    steps,
    stepsTotal: steps.length,
    flow: isToday ? flow(acts, ctx.now, who, proofOf) : [],
    flowLabel: !isToday ? 'Yesterday is over' : perMinute ? `About ${perMinute} ${plural(perMinute, 'action')} a minute` : 'Nothing is going out right now',
    ais: aisOf(acts, who),
    needs: isToday ? await needsCard(ctx, pending, mismatches.length, who) : null,
    week: await week(ctx),
    paused: false,
    pausedAt: null,
  };
}

export function stepRow(a: Act, step: StepKind, by: ReturnType<Awaited<ReturnType<typeof people>>>): StepRow {
  const d = stepDecision(a)!;
  return {
    id: a.id,
    at: a.at,
    who: by,
    verdict: verdict(a),
    step,
    text: stepText(a),
    note: `${by.name}, ${reasonWords(d.r)}`,
    kept: keptText(a, step),
  };
}

function aisOf(acts: Act[], who: (id: string | null) => ReturnType<Awaited<ReturnType<typeof people>>>) {
  const ids = [...new Set(acts.map((a) => a.actor).filter((x): x is string => !!x))];
  return ids.map(who).filter((w) => !w.human).sort((a, b) => a.name.localeCompare(b.name));
}

function head(
  isToday: boolean,
  s: { bad: number; caught: number; done: number; kept: number; pending: number; mismatches: number; partial: boolean },
): TodayView['head'] {
  const when = isToday ? 'today' : 'yesterday';
  if (!s.done) {
    return {
      title: isToday ? 'Your AIs haven’t done anything yet today' : 'Your AIs did nothing yesterday',
      sub: isToday ? 'Everything they do shows up here as it happens, with where Lumni stepped in.' : 'There’s nothing to show for that day.',
    };
  }
  const title = s.bad
    ? `${words(s.bad, true)} ${plural(s.bad, 'action')} didn’t happen ${when}`
    : s.caught
      ? `Lumni stepped in ${fmt(s.caught)} ${plural(s.caught, 'time')} ${when}`
      : `Your AIs took ${fmt(s.done)} ${plural(s.done, 'action')} ${when}`;
  const kept = s.kept ? `, and kept ${money(s.kept)} from going out twice` : '';
  let sub = `Lumni stepped in on ${fmt(s.caught)} of ${fmt(s.done)} actions${kept}.`;
  const needs = s.pending + s.mismatches;
  if (isToday && needs) {
    const parts = [s.pending ? `${words(s.pending)} ${plural(s.pending, 'decision')}` : '', s.mismatches ? `${words(s.mismatches)} ${plural(s.mismatches, 'action')} that didn’t happen` : ''].filter(Boolean);
    sub += ` ${cap(and(parts))} ${needs === 1 ? 'needs' : 'need'} you.`;
  }
  if (s.partial) sub += ' Some busy minutes couldn’t be read in full.';
  return { title, sub };
}

/** The strip at the gate: what went through in the last few seconds. */
function flow(acts: Act[], now: number, who: (id: string | null) => ReturnType<Awaited<ReturnType<typeof people>>>, proofOf: ReturnType<typeof proofIn>): FlowChip[] {
  const out: FlowChip[] = [];
  for (let i = acts.length - 1; i >= 0; i--) {
    const a = acts[i]!;
    if (a.at < now - STRIP) break;
    const v = verdict(a);
    out.push({
      id: a.id, at: a.at + LAG, who: who(a.actor), text: didOf(a), caught: stepKind(a) !== null,
      verdict: v.kind === 'allow' ? null : v, receipt: a.id, proof: proofOf(a).kind,
    });
  }
  return out.reverse();
}

async function needsCard(
  ctx: Ctx, pending: GoApproval[], mismatches: number, who: (id: string | null) => ReturnType<Awaited<ReturnType<typeof people>>>,
): Promise<TodayView['needs']> {
  const count = pending.length + mismatches;
  const first = pending[0];
  if (first) {
    const a = await ctx.one(first.action_id).catch(() => null);
    const by = who(a?.actor ?? null);
    const did = a ? lowerFirst(didOf(a)) : 'do something';
    return {
      id: first.id, kind: 'approval', who: by, title: `${by.name} wants to ${did}.`,
      body: `It’s waiting for you, since ${reasonWords(first.reason_code as never)}.`,
      foot: ms(first.expires_at) !== null ? 'If nobody answers in {left}, the ask runs out and it doesn’t go ahead.' : undefined,
      cta: 'Decide', expiresAt: ms(first.expires_at) ?? undefined, count,
    };
  }
  if (!mismatches) return null;
  const incidents = openMismatches(await ctx.incidents());
  const i = incidents[0]!;
  const a = i.action_id ? await ctx.one(i.action_id).catch(() => null) : null;
  const by = who(a?.actor ?? null);
  return {
    id: i.id, kind: 'mismatch', who: by,
    title: a ? `“${didOf(a)}” didn’t happen.` : 'An action didn’t happen.',
    body: `${by.name} said it was done${a ? ` at ${time(a.at)}` : ''}, but the app doesn’t show it.`,
    cta: 'See what happened', count,
  };
}

/** The last seven days, from the step-ins the API lists. */
async function week(ctx: Ctx): Promise<TodayView['week']> {
  const from = ctx.zone.dayStart(ctx.now) - 6 * DAY;
  const findings = (await ctx.incidents()).filter((i) => i.id.startsWith('finding_') && (ms(i.created_at) ?? 0) >= from);
  const count = (d: string) => findings.filter((f) => String((f.evidence as Record<string, unknown> | null)?.decision ?? '') === d).length;
  const repeats = count('DEDUPLICATE');
  const rechecked = count('REPLAN');
  const asked = count('REQUIRE_APPROVAL');
  return [
    { n: fmt(repeats), text: `${plural(repeats, 'repeat')} stopped before ${repeats === 1 ? 'it' : 'they'} went out` },
    { n: fmt(rechecked), text: `${plural(rechecked, 'change')} rechecked before ${rechecked === 1 ? 'it' : 'they'} went out` },
    { n: fmt(asked), text: `${plural(asked, 'time')} Lumni asked you first` },
  ];
}
