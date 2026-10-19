// The protocol speaks in decision types and reason codes. The console speaks
// in a dozen plain words. This file is the only place that maps one to the
// other, so every screen says the same thing for the same decision.

import type { DecisionType, OutcomeStatus, ProofKind, ReasonCode, StepKind, VerdictKind, VerdictV } from './types';

export const WORDS: Record<VerdictKind, string> = {
  allow: 'Go ahead',
  done: 'Already done',
  onit: 'Already on it',
  superseded: 'No longer needed',
  recheck: 'Recheck',
  wait: 'Wait',
  turns: 'Take turns',
  slow: 'Slow down',
  ask: 'Ask a human',
  stop: 'Stop',
  bad: "Didn’t happen",
};

/** The chip’s colour class in kit.css. Lime is only for Lumni stopping something. */
export const CHIP: Record<VerdictKind, string> = {
  allow: 'allow',
  done: 'dedupe',
  onit: 'dedupe',
  superseded: 'dedupe',
  recheck: 'replan',
  wait: 'wait',
  turns: 'wait',
  slow: 'wait',
  ask: 'ask',
  stop: 'stop',
  bad: 'bad',
};

export function isWould(decision: DecisionType): boolean {
  return decision.startsWith('WOULD_');
}

export function verdictOf(decision: DecisionType, reason: ReasonCode): VerdictV {
  const would = isWould(decision);
  const base = (would ? decision.slice('WOULD_'.length) : decision) as DecisionType;
  return { kind: kindOf(base, reason), would };
}

function kindOf(base: DecisionType, reason: ReasonCode): VerdictKind {
  switch (base) {
    case 'WAIT':
      // The same action is already under way, so this one waits on it.
      if (reason === 'EXACT_DUPLICATE_ACTIVE') return 'onit';
      if (reason === 'CONCURRENCY_LIMIT') return 'turns';
      if (reason === 'RATE_LIMITED' || reason === 'BUDGET_PACING') return 'slow';
      return 'wait';
    case 'REPLAN':
      return 'recheck';
    case 'DEDUPLICATE':
      return reason === 'EXACT_DUPLICATE_ACTIVE' ? 'onit' : 'done';
    case 'REQUIRE_APPROVAL':
      return 'ask';
    case 'REJECT':
      return reason === 'WORK_SUPERSEDED' ? 'superseded' : 'stop';
    default:
      return 'allow';
  }
}

/** Which of Today’s four step-in groups a verdict belongs to, if any. */
export function stepOf(kind: VerdictKind): StepKind | null {
  switch (kind) {
    case 'done':
    case 'onit':
    case 'superseded':
    case 'stop':
      return 'stop';
    case 'wait':
    case 'turns':
    case 'slow':
      return 'wait';
    case 'recheck':
      return 'recheck';
    case 'ask':
      return 'ask';
    default:
      return null;
  }
}

export const STEP_LABEL: Record<StepKind, string> = {
  stop: 'stopped',
  wait: 'held',
  recheck: 'rechecked',
  ask: 'asked you',
};

/** Did the action really happen? The outcome, as a proof mark. */
export function proofOf(status: OutcomeStatus | null): { kind: ProofKind; text: string } {
  switch (status) {
    case 'VERIFIED':
      return { kind: 'ok', text: 'Checked' };
    case 'MISMATCH':
    case 'FAILED':
      return { kind: 'bad', text: "Didn’t happen" };
    case 'PARTIAL':
      return { kind: 'bad', text: 'Half done' };
    case 'PENDING':
      return { kind: 'busy', text: 'Checking…' };
    default:
      return { kind: 'none', text: "Can’t check" };
  }
}

/** The protocol value behind each plain word, for the API reference in the README. */
export const DECISION_FOR: Record<VerdictKind, { decision: DecisionType; reason: ReasonCode }> = {
  allow: { decision: 'ALLOW', reason: 'NO_CONFLICT' },
  done: { decision: 'DEDUPLICATE', reason: 'EXACT_EFFECT_ALREADY_COMMITTED' },
  onit: { decision: 'DEDUPLICATE', reason: 'EXACT_DUPLICATE_ACTIVE' },
  superseded: { decision: 'REJECT', reason: 'WORK_SUPERSEDED' },
  recheck: { decision: 'REPLAN', reason: 'STALE_OBSERVATION' },
  wait: { decision: 'WAIT', reason: 'LEASE_BUSY' },
  turns: { decision: 'WAIT', reason: 'CONCURRENCY_LIMIT' },
  slow: { decision: 'WAIT', reason: 'RATE_LIMITED' },
  ask: { decision: 'REQUIRE_APPROVAL', reason: 'APPROVAL_REQUIRED' },
  stop: { decision: 'REJECT', reason: 'POLICY_REJECTED' },
  bad: { decision: 'ALLOW', reason: 'ACTION_RESULT_UNKNOWN' },
};

/** Watch-only turns a decision into its WOULD_ twin. */
export function wouldOf(decision: DecisionType): DecisionType {
  if (decision === 'ALLOW' || decision === 'WARN' || isWould(decision)) return decision;
  return `WOULD_${decision}` as DecisionType;
}
