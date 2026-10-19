// How real records read in the console’s words: what Lumni did about an
// action, why, and what came of it. The demo writes these by hand; here they
// come from the decision, the reason, and whatever the AI said it was doing.

import type { ReasonCode, StepKind, VerdictKind } from '../../src/domain/types';
import { money } from '../../src/domain/format';
import { verdictOf } from '../../src/domain/vocab';
import { didOf, stepDecision, type Act } from './model';

export const lowerFirst = (s: string) => (s && !/^[A-Z]{2}/.test(s) ? s[0]!.toLowerCase() + s.slice(1) : s);
export const cap = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** "a, b and c" */
export function and(parts: string[]): string {
  return parts.length < 2 ? (parts[0] ?? '') : `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}`;
}

/** Why Lumni stepped in, as the end of a sentence. */
const REASON: Partial<Record<ReasonCode, string>> = {
  EXACT_EFFECT_ALREADY_COMMITTED: 'it was already done',
  EXACT_DUPLICATE_ACTIVE: 'the same thing was already under way',
  IDEMPOTENCY_KEY_REUSED_DIFFERENT_PAYLOAD: 'it reused an id with different details',
  STALE_OBSERVATION: 'its info was out of date',
  VERSION_UNAVAILABLE: 'Lumni couldn’t tell which version is current',
  VERSION_SOURCE_CHANGED: 'it read the thing from somewhere else',
  CONFLICTING_ACTIVE_MUTATION: 'another AI was changing the same thing',
  WRITE_WRITE_CONFLICT: 'another AI was changing the same thing',
  DELETE_WRITE_CONFLICT: 'another AI was deleting the same thing',
  DOMAIN_OPERATION_CONFLICT: 'it clashed with work already under way',
  APPROVAL_REQUIRED: 'a rule says a person decides',
  APPROVAL_PENDING: 'it’s waiting for a person',
  APPROVAL_DENIED: 'you said no',
  APPROVAL_EXPIRED: 'nobody answered in time',
  RESOURCE_UNRESOLVED: 'Lumni couldn’t tell what it would change',
  RESOURCE_MAPPING_LOW_CONFIDENCE: 'Lumni wasn’t sure what it would change',
  LEASE_BUSY: 'something else was holding it',
  LEASE_EXPIRED: 'its turn ran out',
  FENCING_TOKEN_INVALID: 'its turn had already passed',
  POLICY_REJECTED: 'a rule doesn’t allow it',
  ACTION_RESULT_UNKNOWN: 'nobody knows if it happened',
  COORDINATOR_UNAVAILABLE: 'Lumni couldn’t check it in time',
  ACTOR_PAUSED: 'you paused this AI',
  CONCURRENCY_LIMIT: 'as many as you allow were already running',
  RATE_LIMITED: 'it hit the rate you set',
  BUDGET_PACING: 'it was spending ahead of the daily limit',
};

export function reasonWords(r: ReasonCode): string {
  return REASON[r] ?? 'one of Lumni’s checks caught it';
}

const quote = (s: string) => `“${s}”`;

/** What Lumni did, for the bold line of a step-in: “Stopped a repeat of …”. */
export function stepText(a: Act): string {
  const d = stepDecision(a);
  const did = didOf(a);
  if (!d) return did;
  const kind = verdictOf(d.d, d.r).kind;
  return STEP_TEXT[kind](quote(did));
}

const STEP_TEXT: Record<VerdictKind, (q: string) => string> = {
  allow: (q) => q,
  done: (q) => `Stopped a repeat of ${q}`,
  onit: (q) => `Held a second ${q} while the first one runs`,
  superseded: (q) => `Stopped ${q}, which was no longer needed`,
  recheck: (q) => `Sent ${q} back to look again`,
  wait: (q) => `Held ${q} until the way was clear`,
  turns: (q) => `Made ${q} take its turn`,
  slow: (q) => `Slowed ${q} down`,
  ask: (q) => `Asked you before ${q}`,
  stop: (q) => `Stopped ${q}`,
  bad: (q) => `${q} didn’t happen`,
};

/** What an AI would have been stopped from, when a rule is only watching. */
export function wouldText(a: Act): string {
  const d = stepDecision(a);
  const did = didOf(a);
  if (!d) return did;
  const kind = verdictOf(d.d, d.r).kind;
  return `Would have ${lowerFirst(STEP_TEXT[kind](quote(did)))}`;
}

/** An amount the action says it moves, when it says one. */
export function amountOf(a: Act): number | null {
  for (const k of ['amount', 'amount_usd', 'value', 'total']) {
    const v = a.meta[k];
    const n = typeof v === 'number' ? v : typeof v === 'string' ? Number(v.replace(/[$,]/g, '')) : NaN;
    if (Number.isFinite(n) && n > 0) return n;
  }
  return null;
}

/** Money moves that going out twice would cost. */
const MONEY = /refund|charge|payment|payout|invoice|transfer|credit/i;

/** Money a stopped repeat kept from going out. */
export function keptMoney(a: Act): number {
  const d = stepDecision(a);
  if (!d || d.d !== 'DEDUPLICATE' || !MONEY.test(`${a.op} ${didOf(a)}`)) return 0;
  return amountOf(a) ?? 0;
}

/** What came of it, for the right-hand column. */
export function keptText(a: Act, kind: StepKind): string {
  const money$ = keptMoney(a);
  if (money$) return `${money(money$)} kept`;
  switch (kind) {
    case 'stop':
      return stepDecision(a)?.d === 'DEDUPLICATE' ? 'Done once, not twice' : 'Nothing went out';
    case 'ask': {
      const s = a.approval?.status;
      if (s === 'APPROVED') return 'You said yes';
      if (s === 'DENIED') return 'You said no';
      if (s === 'EXPIRED') return 'Nobody answered';
      return 'Your call';
    }
    case 'recheck':
      return a.status === 'SUCCEEDED' ? 'Went ahead with the latest' : 'Looking again';
    case 'wait':
      return a.status === 'SUCCEEDED' ? 'Went ahead after' : a.status === 'PROPOSED' ? 'Still waiting' : 'Kept apart';
  }
}
