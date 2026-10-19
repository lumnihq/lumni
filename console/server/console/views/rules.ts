// /v1/console/rules and /v1/console/watch-report with real data. Your policy
// rules read as sentences, each in the mode its operation runs in. Counts for
// last week come from the step-ins the API lists, matched to a rule by its
// reason and operation. The API can’t replay last week under another word, so
// trying a word shows the change and waits for you to save it.

import type { RuleBead, RuleTemplate, RuleToken, RuleV, RulesView, WatchArea, WatchView } from '../../../src/domain/types';
import { DAY, fmt, plural, time, wall, words } from '../../../src/domain/format';
import { isWould } from '../../../src/domain/vocab';
import { ms, obj } from '../../upstream';
import type { GoIncident, GoPolicyRule } from '../go';
import type { Ctx } from '../load';
import { stepDecision, stepKind, wouldKind, type Act } from '../model';
import { cap, stepText, wouldText } from '../words';
import { people, ruleParts, rules, thresholdLabel, type RuleInfo, type Rules } from './shared';

/** Rules the console can add, each a real policy rule. */
export const TEMPLATES: (RuleTemplate & { body: Omit<GoPolicyRule, 'id' | 'enabled' | 'version' | 'created_at'> })[] = [
  {
    id: 'refund-approval', text: 'Ask a person before a Stripe refund over $100', area: 'Stripe refunds', added: false, verdict: { kind: 'ask', would: true },
    body: { name: 'Refunds over $100 need a person', operation: 'stripe.refund', resource_type: '*', minimum_risk: null, argument_pointer: '/amount', numeric_threshold: 100, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' },
  },
  {
    id: 'charge-approval', text: 'Ask a person before a Stripe charge over $1,000', area: 'Stripe charges', added: false, verdict: { kind: 'ask', would: true },
    body: { name: 'Charges over $1,000 need a person', operation: 'stripe.charge', resource_type: '*', minimum_risk: null, argument_pointer: '/amount', numeric_threshold: 1000, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' },
  },
  {
    id: 'deploy-risk', text: 'Stop a Vercel deploy when its risk is high or higher', area: 'Vercel deploys', added: false, verdict: { kind: 'stop', would: true },
    body: { name: 'No high-risk deploys', operation: 'vercel.deploy', resource_type: '*', minimum_risk: 'HIGH', argument_pointer: null, numeric_threshold: null, outcome: 'REJECT', reason_code: 'POLICY_REJECTED' },
  },
  {
    id: 'bulk-delete', text: 'Stop a Salesforce delete with count over 50', area: 'Salesforce deletes', added: false, verdict: { kind: 'stop', would: true },
    body: { name: 'No bulk deletes in Salesforce', operation: 'salesforce.delete', resource_type: '*', minimum_risk: null, argument_pointer: '/count', numeric_threshold: 50, outcome: 'REJECT', reason_code: 'POLICY_REJECTED' },
  },
  {
    id: 'mass-email', text: 'Ask a person before a Gmail send with recipients over 100', area: 'Gmail sends', added: false, verdict: { kind: 'ask', would: true },
    body: { name: 'Big email sends need a person', operation: 'gmail.send', resource_type: '*', minimum_risk: null, argument_pointer: '/recipients', numeric_threshold: 100, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' },
  },
];

const VERB: Record<GoPolicyRule['outcome'], RuleV['verb']> = { REQUIRE_APPROVAL: 'asked', REJECT: 'stopped', WARN: 'waited' };
const KIND: Record<GoPolicyRule['outcome'], 'ask' | 'stop' | 'allow'> = { REQUIRE_APPROVAL: 'ask', REJECT: 'stop', WARN: 'allow' };

/** Thresholds around the current one, as round numbers. */
export function thresholdOptions(v: number): number[] {
  const nice = (x: number) => {
    if (x <= 0) return 0;
    const p = 10 ** Math.floor(Math.log10(x));
    const m = x / p;
    return (m < 1.5 ? 1 : m < 3.5 ? 2.5 : m < 7.5 ? 5 : 10) * p;
  };
  return [...new Set([nice(v / 4), nice(v / 2), v, nice(v * 2), nice(v * 5)])].filter((x) => x > 0).sort((a, b) => a - b);
}

/** A step-in the API lists: when, what Lumni decided, and the action it was about. */
interface Finding {
  at: number;
  decision: string;
  reason: string;
  action: string;
}

function findingsSince(incidents: GoIncident[], from: number): Finding[] {
  const out: Finding[] = [];
  for (const i of incidents) {
    if (!i.id.startsWith('finding_') || !i.action_id) continue;
    const at = ms(i.created_at);
    if (at === null || at < from) continue;
    const ev = obj(i.evidence);
    out.push({ at, decision: String(ev.decision ?? ''), reason: String(ev.reason_code ?? i.kind), action: i.action_id });
  }
  return out;
}

interface Counted {
  info: RuleInfo;
  /** Last week, one count per day, oldest first. */
  days: number[];
  would: number[];
  ais: Set<string>;
  today: Act[];
}

/** Match last week’s step-ins and today’s actions to your rules. */
async function count(ctx: Ctx, rs: Rules, weekFrom: number, days: number): Promise<{ counted: Map<string, Counted>; lumni: number; capped: boolean }> {
  const [incidents, today] = await Promise.all([ctx.incidents(), ctx.today()]);
  const counted = new Map<string, Counted>(rs.list.map((r) => [r.rule.id, { info: r, days: new Array(days).fill(0), would: new Array(days).fill(0), ais: new Set(), today: [] }]));
  const todayStart = ctx.zone.dayStart(ctx.now);
  const found = findingsSince(incidents, weekFrom).filter((f) => f.at < todayStart);
  for (const f of found) {
    const op = await ctx.opOf(f.action);
    const hit = rs.match(op, f.reason);
    if (!hit) continue;
    const c = counted.get(hit.rule.id)!;
    const d = Math.floor((ctx.zone.dayStart(f.at) - weekFrom) / DAY + 0.5);
    if (d < 0 || d >= days) continue;
    if (isWould(f.decision as never)) c.would[d]! += 1;
    else c.days[d]! += 1;
    const actor = ctx.actorOf(f.action);
    if (actor) c.ais.add(actor);
  }
  let lumni = 0;
  for (const a of today.acts) {
    const d = stepDecision(a);
    if (!d) continue;
    const hit = rs.match(a.op, d.r, d.policy);
    if (hit) {
      counted.get(hit.rule.id)!.today.push(a);
      if (a.actor) counted.get(hit.rule.id)!.ais.add(a.actor);
    } else if (stepKind(a)) lumni += 1;
  }
  // The API lists the newest 200 step-ins, so an older week may be cut short.
  const capped = incidents.length >= 200 && found.length > 0 && Math.min(...found.map((f) => f.at)) > weekFrom + DAY;
  return { counted, lumni, capped };
}

function bead(a: Act, z: Ctx['zone']): RuleBead | null {
  const would = wouldKind(a);
  const k = would ?? stepKind(a);
  if (!k) return null;
  const at = time(a.at);
  return would
    ? { id: a.id, h: z.hourOf(a.at), k, would: true, label: `${wouldText(a)}, ${at}`, to: `/receipts/${a.id}` }
    : { id: a.id, h: z.hourOf(a.at), k, would: false, label: `${stepText(a)}, ${at}`, to: `/story/${a.id}` };
}

function ruleV(ctx: Ctx, c: Counted): RuleV {
  const r = c.info.rule;
  const watching = c.info.mode !== 'ENFORCE';
  const days = c.days.map((n, i) => n + c.would[i]!);
  const lastWeek = days.reduce((a, b) => a + b, 0);
  const tokens: RuleToken[] = [];
  const draft = ctx.state.drafts.get(r.id);
  if (r.argument_pointer && r.numeric_threshold !== null) {
    const cur = r.numeric_threshold;
    const opts = thresholdOptions(cur);
    if (draft && !opts.includes(Number(draft.value))) opts.push(Number(draft.value));
    tokens.push({
      id: 'threshold',
      value: String(cur),
      options: opts.sort((a, b) => a - b).map((v) => ({ value: String(v), label: thresholdLabel(r, v), lastWeek: v === cur ? lastWeek : null })),
    });
  }
  const tok = tokens[0];
  return {
    id: r.id,
    area: c.info.area,
    parts: ruleParts(r),
    tokens,
    text: c.info.text,
    verdict: { kind: KIND[r.outcome] ?? 'stop', would: watching },
    mode: watching ? 'SHADOW' : 'ENFORCE',
    verb: VERB[r.outcome] ?? 'stopped',
    lastWeek,
    days,
    draft: draft && tok ? { token: draft.token, value: draft.value, label: thresholdLabel(r, Number(draft.value)), lastWeek: null, delta: null } : undefined,
    ais: [...c.ais],
    today: { did: c.today.filter((a) => stepKind(a)).length, would: c.today.filter((a) => wouldKind(a)).length },
    isNew: (ms(r.created_at) ?? 0) >= ctx.zone.dayStart(ctx.now) || undefined,
  };
}

export async function rulesView(ctx: Ctx): Promise<RulesView> {
  const [rs, who] = await Promise.all([rules(ctx), people(ctx)]);
  const weekFrom = ctx.zone.dayStart(ctx.now) - 7 * DAY;
  const { counted, lumni, capped } = await count(ctx, rs, weekFrom, 7);
  const list = rs.list.map((r) => ruleV(ctx, counted.get(r.rule.id)!));
  const protecting = list.filter((r) => r.mode !== 'SHADOW');
  const watching = list.filter((r) => r.mode === 'SHADOW');
  const stoppedLastWeek = protecting.filter((r) => r.verb !== 'asked').reduce((a, r) => a + r.lastWeek, 0);
  const names = new Set(rs.list.map((r) => r.rule.name));
  if (capped) ctx.note('Last week’s counts include only the newest 200 step-ins.');

  return {
    head: head(list, protecting.length, watching.length, stoppedLastWeek),
    rules: list,
    protecting: protecting.length,
    watching: watching.length,
    stoppedLastWeek,
    templates: TEMPLATES.map(({ body, ...t }) => ({ ...t, added: names.has(body.name) })),
    ais: [...new Set(list.flatMap((r) => r.ais))].map(who).sort((a, b) => a.name.localeCompare(b.name)),
    ribbon: {
      now: ctx.zone.hourOf(ctx.now),
      lanes: list.map((r) => ({ id: r.id, beads: counted.get(r.id)!.today.flatMap((a) => bead(a, ctx.zone) ?? []) })),
    },
    today: { did: list.reduce((a, r) => a + r.today.did, 0), would: list.reduce((a, r) => a + r.today.would, 0), lumni },
  };
}

function head(rules: RuleV[], protecting: number, watching: number, stopped: number): RulesView['head'] {
  const trying = rules.find((r) => r.draft);
  if (trying?.draft) {
    return {
      title: `Trying ${trying.draft.label} in “${trying.area}”`,
      sub: 'Lumni can’t replay last week under a new word yet, so there’s no count to compare. Nothing changes until you save.',
    };
  }
  if (!rules.length) {
    return { title: 'You have no rules yet', sub: 'Lumni still runs its own checks on every action. Add a rule to stop or ask about the things you care about.' };
  }
  const how = 'Change a word, and nothing changes until you save it.';
  if (!protecting) return { title: 'Your rules are only watching', sub: `Lumni notes what they would have done, and lets it go through. ${how}` };
  const also = watching ? `${cap(words(watching))} more ${plural(watching, 'rule is', 'rules are')} only watching. ` : '';
  return {
    title: stopped ? `Your rules stopped ${fmt(stopped)} ${plural(stopped, 'mistake')} last week` : `Your ${plural(protecting, 'rule is', 'rules are')} protecting`,
    sub: `${also}${how}`,
  };
}

// ─── the watch report ──────────────────────────────────

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const WOULD: Record<RuleV['verb'], string> = { stopped: 'would have stopped it', asked: 'would have asked you first', waited: 'would have made it wait' };

export async function watchView(ctx: Ctx): Promise<WatchView> {
  const rs = await rules(ctx);
  const z = ctx.zone;
  const today = z.dayStart(ctx.now);
  const from = today - 7 * DAY;
  const { counted, capped } = await count(ctx, rs, from, 7);
  const inReport = rs.list.filter((r) => r.mode !== 'ENFORCE' || ctx.state.protectedAt.has(r.rule.id));
  const areas: WatchArea[] = inReport.map((r) => {
    const c = counted.get(r.rule.id)!;
    const days = c.would.map((n, i) => n + (ctx.state.protectedAt.has(r.rule.id) ? 0 : c.days[i]!));
    const n = days.reduce((a, b) => a + b, 0);
    const verb = VERB[r.rule.outcome] ?? 'stopped';
    return {
      ruleId: r.rule.id,
      area: r.area,
      title: n ? `${fmt(n)} ${plural(n, 'time')} Lumni ${WOULD[verb]}` : 'Nothing to step in on',
      sub: `${r.text}.`,
      count: n,
      days,
      verb,
      mode: r.mode === 'ENFORCE' ? 'ENFORCE' : 'SHADOW',
      protectedAt: ctx.state.protectedAt.get(r.rule.id) ?? null,
      text: r.text,
      today: { did: c.today.filter((a) => stepKind(a)).length, would: c.today.filter((a) => wouldKind(a)).length },
    };
  });

  let total = 0;
  try {
    const f = await ctx.finops(from, today);
    total = (f.series ?? []).reduce((a, p) => a + (p.actions ?? 0), 0);
  } catch {
    total = 0;
  }
  const stepped = areas.reduce((a, x) => a + x.count, 0);
  if (capped) ctx.note('The report counts only the newest 200 step-ins the API lists.');
  const verbs: RuleV['verb'][] = ['stopped', 'waited', 'asked'];
  return {
    head: watchHead(areas),
    week: { from, to: today - DAY, days: Array.from({ length: 7 }, (_, i) => {
      const d = wall(z.dayStart(from + i * DAY + DAY / 2), z);
      return `${DAYS[d.dow]} ${d.d}`;
    }) },
    areas,
    fine: Math.max(0, total - stepped),
    would: verbs.map((verb) => ({ verb, n: areas.filter((a) => a.verb === verb).reduce((s, a) => s + a.count, 0) })).filter((x) => x.n),
    watching: areas.filter((a) => a.mode === 'SHADOW').length,
    overheadMs: null,
    ribbon: {
      now: z.hourOf(ctx.now),
      lanes: areas.map((a) => ({ id: a.ruleId, beads: counted.get(a.ruleId)!.today.flatMap((x) => bead(x, z) ?? []) })),
    },
  };
}

function watchHead(areas: WatchArea[]): WatchView['head'] {
  if (!areas.length) {
    return { title: 'Every rule is protecting', sub: 'Nothing is only watching, so there’s nothing to report. New rules start by watching, and show up here.' };
  }
  const n = areas.reduce((a, x) => a + x.count, 0);
  const done = areas.filter((a) => a.protectedAt !== null);
  if (!n && !done.length) {
    const today = areas.reduce((a, x) => a + x.today.would, 0);
    return {
      title: 'Your watching rules had nothing to step in on this week',
      sub: today
        ? `Today so far they would have stepped in ${fmt(today)} ${plural(today, 'time')}. Protect one, and Lumni steps in for real.`
        : 'They note what they would have done as your AIs work, and it shows up here.',
    };
  }
  const verbs = new Set(areas.map((a) => a.verb));
  const only = verbs.size === 1 ? [...verbs][0] : null;
  const title = only === 'stopped'
    ? `In the last seven days, Lumni would have stopped ${fmt(n)} ${plural(n, 'action')}`
    : only === 'asked'
      ? `In the last seven days, Lumni would have asked you first ${fmt(n)} ${plural(n, 'time')}`
      : `In the last seven days, Lumni would have stepped in ${fmt(n)} ${plural(n, 'time')}`;
  const watching = areas.length - done.length;
  const sub = done.length
    ? watching
      ? `Lumni now protects ${done.map((a) => a.area.toLowerCase()).join(', ')}. ${watching} ${plural(watching, 'area')} to go.`
      : 'Everything here is protected now, so from here on Lumni steps in for real.'
    : 'These rules were only watching, so those actions went through. Protect one area at a time, or everything at once.';
  return { title, sub };
}
