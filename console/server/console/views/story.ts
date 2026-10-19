// /v1/console/story/:id with real data: one step-in, told in order. What the
// AI tried, the four things Lumni checked, and what the app shows now.

import type { Bead, StepKind, StoryCheck, StoryEvent, StoryLine, StoryView } from '../../../src/domain/types';
import { DAY, date, money, time, timePrecise } from '../../../src/domain/format';
import { verdictOf } from '../../../src/domain/vocab';
import { HttpError } from '../../http';
import type { Ctx } from '../load';
import { appOf, didOf, evaluations, isBad, stepDecision, stepKind, verdict, type Act } from '../model';
import { keptMoney, keptText, lowerFirst, reasonWords, stepText } from '../words';
import { checkers, hill, people, proofIn, quartersSoFar, rules } from './shared';

const QUESTIONS: Record<StoryCheck['stage'], { q: string; pass: string }> = {
  FRESHNESS: { q: 'Is its info up to date?', pass: 'Yes' },
  IDEMPOTENCY: { q: 'Was this already done?', pass: 'No' },
  LEASE: { q: 'Is anyone else working on it?', pass: 'No' },
  POLICY: { q: 'Do the rules allow it?', pass: 'Yes' },
};

const notFound = () => new HttpError(404, 'not_found', 'No story with that id.');

export async function storyView(ctx: Ctx, id: string): Promise<StoryView> {
  const a = await ctx.one(id);
  if (!a) throw notFound();
  const step = stepKind(a);
  const d = stepDecision(a);
  if (!step || !d) throw notFound();

  const z = ctx.zone;
  const from = z.dayStart(a.at);
  const isToday = from === z.dayStart(ctx.now);
  const [day, who, chk, rs] = await Promise.all([ctx.day(from, Math.min(ctx.now, from + DAY)), people(ctx), checkers(ctx), rules(ctx)]);
  const steps = day.acts.filter((x) => stepKind(x) !== null);
  if (!steps.some((x) => x.id === a.id)) steps.push(a);
  steps.sort((x, y) => x.at - y.at || (x.id < y.id ? -1 : 1));
  const i = steps.findIndex((x) => x.id === a.id);

  const by = who(a.actor);
  const app = appOf(a).name;
  const did = didOf(a);
  const v = verdict(a);
  const rule = rs.match(a.op, d.r, d.policy);
  const evals = evaluations(d);

  const checks: StoryCheck[] = evals.map((e) => ({
    stage: e.stage, question: QUESTIONS[e.stage].q, hit: e.outcome === 'HIT', answer: e.outcome === 'HIT' ? answer(e.stage, v.kind) : QUESTIONS[e.stage].pass,
  }));
  const tallies: Record<StepKind, number> = { stop: 0, wait: 0, recheck: 0, ask: 0 };
  for (const x of steps) tallies[stepKind(x)!] += 1;
  const beads: Bead[] = steps.map((x) => ({ id: x.id, h: z.hourOf(x.at), step: stepKind(x)!, text: stepText(x), verdict: verdict(x) }));
  const nowH = isToday ? z.hourOf(ctx.now) : 24;

  const events: StoryEvent[] = [
    {
      id: 'step', at: d.at || a.at, precise: timePrecise(d.at || a.at), who: by,
      title: `${by.name} tried to ${lowerFirst(did)}`,
      verdict: { v }, checks, after: after(step, v.kind, app), node: 'lumni',
    },
    result(a, step, app, by.name, ctx.now, proofIn(chk)),
  ];

  return {
    id: a.id,
    step,
    head: {
      before: `${by.name} tried to ${lowerFirst(did)}.`,
      after: `Lumni ${lowerFirst(stepText(a))}.`,
      sub: `At ${time(a.at)}${isToday ? '' : ` on ${date(a.at)}`}, Lumni checked 4 things before this could reach ${app}, because ${reasonWords(d.r)}. Here’s each step, and the receipt it kept.`,
    },
    index: i + 1,
    total: steps.length,
    prev: steps[i - 1]?.id ?? null,
    next: steps[i + 1]?.id ?? null,
    rule: rule ? { id: rule.rule.id, text: rule.text } : null,
    ribbon: {
      now: nowH, quarters: hill(day.acts, from, isToday ? quartersSoFar(nowH) : 96, z), beads,
      current: { id: a.id, h: z.hourOf(a.at), label: time(a.at) },
    },
    tallies,
    evaluations: evals,
    events,
    settled: !['AWAITING_APPROVAL', 'PROPOSED', 'RESERVED', 'ACTIVE'].includes(a.status) && a.outcome?.status !== 'PENDING',
    receipt: receipt(a, step, app, by.name),
  };
}

function answer(stage: StoryCheck['stage'], kind: string): string {
  switch (stage) {
    case 'FRESHNESS':
      return 'No, it was out of date';
    case 'IDEMPOTENCY':
      return kind === 'onit' ? 'It’s already under way' : 'Yes, already done';
    case 'LEASE':
      return 'Yes, so it waits';
    case 'POLICY':
      return kind === 'ask' ? 'Only if a person says yes' : 'No';
  }
}

function after(step: StepKind, kind: string, app: string): string {
  if (kind === 'done') return `So Lumni stopped it. It had already reached ${app} once.`;
  if (kind === 'onit') return 'So Lumni held it until the first one finished.';
  switch (step) {
    case 'stop':
      return `So Lumni stopped it. Nothing reached ${app}.`;
    case 'wait':
      return 'So Lumni held it until the way was clear.';
    case 'recheck':
      return 'So Lumni sent it back to look again first.';
    case 'ask':
      return 'So Lumni asked you before it could go ahead.';
  }
}

/** The last moment: what the app shows, or what it’s still waiting on. */
function result(a: Act, step: StepKind, app: string, ai: string, now: number, proofOf: ReturnType<typeof proofIn>): StoryEvent {
  const at = a.outcome?.updatedAt ?? a.at + 1500;
  const base = { at, precise: timePrecise(at, false) };
  const ap = a.approval;
  if (a.status === 'AWAITING_APPROVAL' || ap?.status === 'PENDING') {
    return {
      id: 'result-wait', at: now, precise: 'Now', title: 'Waiting for you', node: 'busy',
      note: `Nothing changes in ${app} until you choose.`, link: { label: 'Open Needs you', to: '/needs-you' },
    };
  }
  if (ap?.status === 'DENIED' || ap?.status === 'EXPIRED') {
    return {
      id: 'result', ...base, node: 'plain', title: ap.status === 'DENIED' ? 'You said no' : 'Nobody answered in time',
      note: `${ai} didn’t go ahead, so nothing changed in ${app}.`,
    };
  }
  if (a.status === 'PROPOSED') {
    return {
      id: 'result-wait', at: now, precise: 'Now', title: step === 'recheck' ? 'Looking again' : 'Waiting its turn', node: 'busy',
      note: step === 'recheck' ? `Once ${ai} has the latest, Lumni checks it again.` : `Lumni lets it through once it’s clear, then checks ${app}.`,
    };
  }
  const p = proofOf(a);
  if (p.kind === 'stopped') return { id: 'result', ...base, title: `Nothing changed in ${app}`, note: 'Lumni never let it through.', node: 'plain' };
  if (p.kind === 'busy') return { id: 'result-busy', at: now, precise: 'Now', title: p.text === 'In progress' ? `${ai} is on it` : `Checking ${app}…`, note: 'Lumni checks every action once it’s done.', node: 'busy' };
  if (isBad(a)) {
    return {
      id: 'result', ...base, title: `${app} doesn’t show it`, proof: p, node: 'bad',
      note: 'The AI said it was done, but the app’s records disagree.', link: { label: 'Open it on Receipts', to: `/receipts/${a.id}` },
    };
  }
  if (p.kind === 'ok') {
    return {
      id: 'result', ...base, node: 'plain', title: `${ai} went ahead, and ${app} shows it`,
      verdict: { v: verdictOf('ALLOW', 'NO_CONFLICT'), word: 'Confirmed' }, note: 'Lumni looks at the real records, not just what the AI said.',
    };
  }
  return {
    id: 'result', ...base, node: 'plain', title: `Lumni can’t check ${app}`, proof: p,
    note: `Nothing tells Lumni what ${app} shows for this one.`, link: { label: 'See your apps', to: '/apps' },
  };
}

function receipt(a: Act, step: StepKind, app: string, ai: string): StoryView['receipt'] {
  const stopped = step === 'stop' && a.decision === stepDecision(a);
  const o = a.outcome?.status;
  const sent = stopped || a.approval?.status === 'DENIED' || a.approval?.status === 'EXPIRED' ? 'Nothing' : a.status === 'SUCCEEDED' ? 'Once' : 'Not yet';
  const verified = stopped ? `${app}, nothing changed` : o === 'VERIFIED' ? `${app}, it matches` : o === 'PENDING' ? 'Not yet' : isBad(a) ? 'Didn’t match' : 'Can’t check';
  const lines: StoryLine[] = [
    { label: 'What', value: didOf(a) },
    { label: 'Asked by', value: ai },
    { label: 'Checked', value: '4 things' },
    { label: 'Decision', value: '', verdict: verdict(a) },
    { label: 'Sent', value: sent },
    { label: 'Verified', value: verified },
  ];
  const when = `${date(a.at)}, ${time(a.at)}`;
  const kept = keptMoney(a);
  if (kept) return { when, lines, totalLabel: 'Kept', total: money(kept, 'always'), money: true };
  if (a.cost) lines.push({ label: 'Cost', value: money(a.cost / 1e6, 'always') });
  return { when, lines, totalLabel: 'Result', total: keptText(a, step), money: false };
}
