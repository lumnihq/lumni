// /v1/console/needs-you with real data: approvals waiting on a person, actions
// the app doesn’t show, and what was decided today.

import type { DecidedRow, NeedItem, NeedPin, NeedsView, Who } from '../../../src/domain/types';
import { MIN, ago, fmt, money, time, words } from '../../../src/domain/format';
import { ms, obj } from '../../upstream';
import type { GoApproval, GoIncident } from '../go';
import type { Ctx } from '../load';
import { appOf, customerOf, didOf, type Act } from '../model';
import { amountOf, cap, lowerFirst, reasonWords } from '../words';
import { openMismatches } from './frame';
import { people, rules, type Rules } from './shared';

/** How long a settled item stays on Waiting, so a choice can be seen landing. */
const SETTLED_FOR = 15 * MIN;
/** Most approvals a look reads in full. */
const MOST = 40;

type WhoFn = (id: string | null) => Who;

export async function needsView(ctx: Ctx): Promise<NeedsView> {
  const [approvals, incidents, who, rs] = await Promise.all([ctx.approvals(), ctx.incidents(), people(ctx), rules(ctx)]);
  const z = ctx.zone;
  const today = z.dayStart(ctx.now);
  const t = (s: string | null) => ms(s);

  const pending = approvals.filter((a) => a.status === 'PENDING').sort((a, b) => (t(a.requested_at) ?? 0) - (t(b.requested_at) ?? 0));
  const resolved = approvals.filter((a) => a.status !== 'PENDING' && (t(a.resolved_at) ?? t(a.expires_at) ?? 0) >= today);
  const bad = openMismatches(incidents);
  const fixed = incidents.filter((i) => i.id.startsWith('outcome_') && i.status === 'RESOLVED' && (t(i.updated_at) ?? 0) >= today);

  // Read each action they’re about, newest asks first, so a long queue can’t stall the page.
  const ids = [...new Set([...pending, ...resolved].map((a) => a.action_id).concat([...bad, ...fixed].flatMap((i) => (i.action_id ? [i.action_id] : []))))];
  const acts = new Map<string, Act>();
  for (const id of ids.slice(-MOST)) {
    const a = await ctx.one(id).catch(() => null);
    if (a) acts.set(id, a);
  }
  if (ids.length > MOST) ctx.note(`Only the newest ${MOST} are shown in full.`);

  const items: NeedItem[] = [
    ...pending.map((ap) => approvalItem(ap, acts.get(ap.action_id), who, rs)),
    ...resolved.filter((ap) => ctx.now - (t(ap.resolved_at) ?? 0) < SETTLED_FOR).map((ap) => approvalItem(ap, acts.get(ap.action_id), who, rs)),
    ...bad.map((i) => mismatchItem(i, i.action_id ? acts.get(i.action_id) : undefined, who, ctx.now)),
    ...fixed.filter((i) => ctx.now - (t(i.updated_at) ?? 0) < SETTLED_FOR).map((i) => mismatchItem(i, i.action_id ? acts.get(i.action_id) : undefined, who, ctx.now)),
  ].sort((a, b) => a.requestedAt - b.requestedAt);

  const decided: (DecidedRow & { askedAt?: number; label: string; short: string })[] = [
    ...resolved.map((ap) => {
      const a = acts.get(ap.action_id);
      const did = a ? `“${didOf(a)}”` : 'an action';
      const at = t(ap.resolved_at) ?? t(ap.expires_at) ?? ctx.now;
      const asked = t(ap.requested_at) ?? undefined;
      const by: 'you' | 'lumni' = ap.status === 'EXPIRED' ? 'lumni' : 'you';
      const said = ap.status === 'APPROVED' ? 'You said yes to' : ap.status === 'DENIED' ? 'You said no to' : 'Nobody answered, so Lumni kept back';
      return {
        id: ap.id, at, by, text: `${said} ${did}`, waited: asked === undefined ? undefined : at - asked, askedAt: asked,
        more: { label: 'See the whole story', to: `/story/${ap.action_id}` },
        label: by === 'you' ? 'You chose' : 'Time ran out', short: ap.status === 'APPROVED' ? 'Yes' : ap.status === 'DENIED' ? 'No' : 'Kept back',
      };
    }),
    ...fixed.map((i) => {
      const a = i.action_id ? acts.get(i.action_id) : undefined;
      const at = t(i.updated_at) ?? ctx.now;
      return {
        id: i.id, at, by: 'you' as const, text: `${a ? `“${didOf(a)}”` : 'An action'} went through on a second look`,
        askedAt: t(i.created_at) ?? undefined, waited: undefined, more: a ? { label: 'Open it on Receipts', to: `/receipts/${a.id}` } : undefined,
        label: 'Went through', short: 'Matched',
      };
    }),
  ].sort((a, b) => b.at - a.at);

  const you = decided.filter((d) => d.by === 'you');
  const waits = resolved.filter((ap) => ap.status !== 'EXPIRED').flatMap((ap) => {
    const a = t(ap.requested_at);
    const b = t(ap.resolved_at);
    return a !== null && b !== null ? [b - a] : [];
  });

  const open = [...pending, ...bad];
  return {
    head: head(items.filter((i) => i.status === 'PENDING'), you.length),
    items,
    decided: decided.map(({ askedAt: _a, label: _l, short: _s, ...row }) => row),
    notify: 'here',
    ribbon: {
      now: z.hourOf(ctx.now),
      pins: decided
        .filter((d) => d.at >= today)
        .map((d): NeedPin => ({ id: d.id, at: d.at, h0: d.askedAt !== undefined && d.askedAt >= today ? z.hourOf(d.askedAt) : null, h: z.hourOf(d.at), by: d.by, label: d.label, short: d.short, text: d.text })),
      open: open.map((x) => {
        const isAp = 'reason_code' in x;
        const asked = t(isAp ? (x as GoApproval).requested_at : (x as GoIncident).created_at) ?? ctx.now;
        const until = isAp ? t((x as GoApproval).expires_at) : null;
        return {
          id: x.id,
          h0: asked >= today ? z.hourOf(asked) : 0,
          h1: until !== null && until < today + 24 * 60 * MIN ? z.hourOf(until) : null,
          kind: isAp ? ('approval' as const) : ('mismatch' as const),
          label: isAp ? `Asked ${ago(asked, ctx.now)}` : `Found ${ago(asked, ctx.now)}`,
          note: isAp ? 'Waits until you choose, or the ask runs out' : 'Waits until it’s checked again',
        };
      }),
    },
    tallies: { waiting: open.length, you: you.length, lumni: decided.length - you.length },
    answerIn: waits.length ? Math.max(1, Math.round(waits.reduce((a, b) => a + b, 0) / waits.length / MIN)) : null,
  };
}

function head(waiting: NeedItem[], decided: number): NeedsView['head'] {
  if (waiting.length > 1) {
    const [first, ...rest] = waiting.map((n) => n.subject);
    return { title: `${cap(words(waiting.length))} things need you`, sub: `${cap(first!)}, then ${rest.slice(0, 3).join(', then ')}${rest.length > 3 ? `, and ${fmt(rest.length - 3)} more` : ''}.` };
  }
  const one = waiting[0];
  if (one) return { title: one.title, sub: one.paragraph };
  const did = decided ? `You decided ${words(decided)} ${decided === 1 ? 'thing' : 'things'} today. ` : '';
  return { title: 'Nothing needs you', sub: `${did}Lumni asks you here when a rule says a person decides, or when an app doesn’t show what an AI said it did.` };
}

/** What’s worth knowing before deciding, from what the AI said about it. */
function facts(a: Act | undefined): { label: string; value: string }[] {
  if (!a) return [];
  const out: { label: string; value: string }[] = [];
  const app = appOf(a);
  out.push({ label: 'Where', value: app.name });
  const customer = customerOf(a);
  if (customer) out.push({ label: 'For', value: customer });
  const amount = amountOf(a);
  if (amount !== null) out.push({ label: 'Amount', value: money(amount) });
  if (a.risk) out.push({ label: 'Risk', value: cap(a.risk.toLowerCase()) });
  out.push({ label: 'Asked', value: time(a.at) });
  return out;
}

function approvalItem(ap: GoApproval, a: Act | undefined, who: WhoFn, rs: Rules): NeedItem {
  const by = who(a?.actor ?? null);
  const did = a ? didOf(a) : 'Do something';
  const rule = rs.match(a?.op ?? null, ap.reason_code, a?.decision?.policy);
  const requestedAt = ms(ap.requested_at) ?? 0;
  const status = ap.status;
  const resolved = status === 'APPROVED'
    ? { title: `You said yes to “${did}”`, note: `${by.name} can go ahead now.` }
    : status === 'DENIED'
      ? { title: `You said no to “${did}”`, note: `${by.name} won’t go ahead, and nothing changes.` }
      : status === 'EXPIRED'
        ? { title: 'Nobody answered in time', note: `The ask ran out, so ${by.name} didn’t go ahead.` }
        : undefined;
  return {
    id: ap.id,
    kind: 'approval',
    status,
    requestedAt,
    expiresAt: ms(ap.expires_at) ?? undefined,
    decidedAt: ms(ap.resolved_at) ?? undefined,
    decidedBy: status === 'PENDING' ? undefined : status === 'EXPIRED' ? 'lumni' : 'you',
    title: `${by.name} wants to ${lowerFirst(did)}`,
    paragraph: `It’s waiting for you, since ${reasonWords(ap.reason_code as never)}.`,
    subject: `“${did}”`,
    rec: ap.action_id,
    more: { label: 'See the whole story', to: `/story/${ap.action_id}` },
    ask: {
      who: by,
      did,
      app: a ? appOf(a) : undefined,
      note: rule ? `Your rule says: ${lowerFirst(rule.text)}.` : `Lumni asks a person first, since ${reasonWords(ap.reason_code as never)}.`,
      facts: facts(a),
    },
    resolved,
    why: rule ? { text: `${rule.text}.`, rule: { id: rule.rule.id, text: 'Edit rule' } } : { text: 'One of Lumni’s checks says a person decides this.' },
  };
}

/** What a check saw, in a few words: "refund re_1042", or nothing at all. */
function described(v: unknown): string {
  const o = obj(v);
  const parts = Object.entries(o)
    .filter(([, x]) => x !== null && x !== undefined && typeof x !== 'object')
    .slice(0, 3)
    .map(([k, x]) => `${k.replace(/[_-]+/g, ' ')} ${String(x)}`);
  return parts.join(', ');
}

function mismatchItem(i: GoIncident, a: Act | undefined, who: WhoFn, now: number): NeedItem {
  const by = who(a?.actor ?? null);
  const did = a ? didOf(a) : 'An action';
  const app = a ? appOf(a) : { id: 'other', name: 'The app' };
  const o = a?.outcome;
  const settled = i.status === 'RESOLVED';
  const retrying = !settled && o?.status === 'PENDING';
  const seen = described(o?.observed);
  const wanted = described(o?.expected);
  const evidence = obj(i.evidence);
  const reason = typeof evidence.reason === 'string' && evidence.reason ? evidence.reason : o?.reason || '';
  const outcomeId = o?.id ?? i.id.replace(/^outcome_/, '');
  return {
    id: i.id,
    kind: 'mismatch',
    status: settled ? 'RETRIED' : 'PENDING',
    requestedAt: ms(i.created_at) ?? now,
    decidedAt: settled ? ms(i.updated_at) ?? undefined : undefined,
    decidedBy: settled ? 'you' : undefined,
    title: `“${did}” didn’t happen`,
    paragraph: `${by.name} said it was done${a ? ` at ${time(a.at)}` : ''}, but ${app.name} doesn’t show it.`,
    subject: `“${did}”`,
    rec: a?.id ?? i.action_id ?? '',
    more: { label: 'Open it on Receipts', to: `/receipts/${a?.id ?? i.action_id ?? ''}` },
    mismatch: {
      said: { who: by, text: `“${did}.”`, note: a ? `At ${time(a.at)}` : '' },
      shows: settled
        ? { app, title: 'It shows it now', note: `Checked at ${time(ms(i.updated_at) ?? now)}` }
        : { app, title: seen ? cap(seen) : 'No record of it', note: wanted ? `Lumni looked for ${wanted}` : `Lumni checked ${words(Math.max(1, o?.attempts ?? 1))} times` },
      missed: reason ? `${cap(reason)}.` : `${app.name} doesn’t show it.`,
      retry: 'Check it again',
      post: `/v1/outcomes/${encodeURIComponent(outcomeId)}/retry`,
      retrying,
      checks: Math.max(1, o?.attempts ?? 1),
    },
    resolved: settled ? { title: `“${did}” went through`, note: `${app.name} shows it now.` } : undefined,
    why: { text: `Lumni checks what every AI says it did against ${app.name}. ${app.name} never showed this one.` },
  };
}
