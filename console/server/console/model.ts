// One action as the console reads it, joined from the API’s records: what the
// AI asked to do, who asked, what Lumni decided, and whether the app agrees it
// happened. Every real view is built from these rows.

import type {
  ActionStatus, AppId, DecisionEvaluation, DecisionType, EvaluationStage, OutcomeStatus, ProofV, ReasonCode, StepKind, VerdictV, Who,
} from '../../src/domain/types';
import { isWould, proofOf, stepOf, verdictOf } from '../../src/domain/vocab';
import type { GoActor, GoEvaluation } from './go';

export interface Dec {
  receipt: string;
  d: DecisionType;
  r: ReasonCode;
  at: number;
  enforced: boolean | null;
  /** The kernel’s own stages, in the order it ran them. */
  evals: GoEvaluation[];
  /** The policy rule behind it, when a rule decided. */
  policy: string | null;
}

export interface Out {
  id: string;
  status: OutcomeStatus;
  verifier: string | null;
  attempts: number;
  reason: string;
  updatedAt: number | null;
  expected?: unknown;
  observed?: unknown;
}

export interface Act {
  id: string;
  at: number;
  op: string;
  tool: string | null;
  status: ActionStatus;
  risk: string | null;
  meta: Record<string, unknown>;
  actor: string | null;
  work: string | null;
  /** Micro-dollars spent on it. */
  cost: number;
  /** The latest decision. */
  decision: Dec | null;
  /**
   * Where Lumni stepped in earlier, when the latest decision let it go: held
   * and then let through, or asked and then approved.
   */
  earlier: Dec | null;
  outcome: Out | null;
  resources: { id: string; mode: string }[];
  approval: { id: string; status: string; at: number; resolvedAt: number | null; expiresAt: number | null } | null;
}

// ─── who ───────────────────────────────────────────────

/** "Su" for Support AI, the way the demo writes an AI’s square; "JP" for Jess Park. */
export function initials(name: string, human: boolean): string {
  const words = name.replace(/[^\p{L}\p{N}\s]/gu, ' ').split(/\s+/).filter(Boolean);
  if (!words.length) return '?';
  if (human) return words.slice(0, 2).map((w) => w[0]!.toUpperCase()).join('');
  const first = words[0]!;
  return first.length > 1 ? first[0]!.toUpperCase() + first[1]!.toLowerCase() : first.toUpperCase();
}

/** An id as a name, when the API has none: "ai_support" reads "Ai support". */
export function nameFromId(id: string): string {
  const s = id.replace(/^(actor|act|ai|agent|ag)[_-]/i, '').replace(/[_-]+/g, ' ').trim() || id;
  return s[0]!.toUpperCase() + s.slice(1);
}

export function whoOf(actors: Map<string, GoActor>, id: string | null): Who {
  if (!id) return { id: 'unknown', name: 'An AI', initials: 'AI', human: false };
  const a = actors.get(id);
  const human = a?.type === 'HUMAN';
  const name = a?.display_name?.trim() || nameFromId(id);
  return { id, name, initials: initials(name, human), human };
}

// ─── where ─────────────────────────────────────────────

const APP_NAMES: Record<string, string> = {
  stripe: 'Stripe', salesforce: 'Salesforce', gmail: 'Gmail', zendesk: 'Zendesk', quickbooks: 'QuickBooks', vercel: 'Vercel',
  github: 'GitHub', slack: 'Slack', hubspot: 'HubSpot', jira: 'Jira', shopify: 'Shopify', intercom: 'Intercom', notion: 'Notion',
  linear: 'Linear', linkedin: 'LinkedIn', aws: 'AWS', gcp: 'Google Cloud', postgres: 'Postgres', s3: 'S3', twilio: 'Twilio',
  sendgrid: 'SendGrid', pagerduty: 'PagerDuty', datadog: 'Datadog', freshdesk: 'Freshdesk', xero: 'Xero',
};

export function appName(id: AppId): string {
  return APP_NAMES[id] ?? (id ? id[0]!.toUpperCase() + id.slice(1) : 'An app');
}

/** The app an action reached: what it says, else the start of its operation or tool. */
export function appIdOf(a: Pick<Act, 'meta' | 'op' | 'tool'>): AppId {
  const said = a.meta.app;
  if (typeof said === 'string' && said.trim()) return said.trim().toLowerCase();
  const head = (a.op || a.tool || '').split(/[.:/]/)[0]?.trim().toLowerCase();
  return head || 'other';
}

export function appOf(a: Pick<Act, 'meta' | 'op' | 'tool'>): { id: AppId; name: string } {
  const id = appIdOf(a);
  return { id, name: appName(id) };
}

// ─── what ──────────────────────────────────────────────

const cap = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** "stripe.refund" reads "Stripe refund"; "salesforce.update_account" reads "Salesforce update account". */
export function opWords(op: string): string {
  const [head, ...rest] = op.split('.');
  const tail = rest.join(' ').replace(/[_-]+/g, ' ').trim();
  return tail ? `${appName(head!.toLowerCase())} ${tail}` : cap(op.replace(/[_-]+/g, ' '));
}

/** What the AI did, in its words when it gave some. */
export function didOf(a: Pick<Act, 'meta' | 'op'>): string {
  for (const k of ['summary', 'did', 'title', 'description']) {
    const v = a.meta[k];
    if (typeof v === 'string' && v.trim()) return cap(v.trim());
  }
  return opWords(a.op);
}

export function customerOf(a: Pick<Act, 'meta'>): string | null {
  const v = a.meta.customer;
  return typeof v === 'string' && v.trim() ? v.trim() : null;
}

// ─── what Lumni decided ────────────────────────────────

/** The decision that counts for the console: where Lumni stepped in, if it did. */
export function stepDecision(a: Act): Dec | null {
  const d = a.decision;
  if (d && d.d !== 'ALLOW' && d.d !== 'WOULD_ALLOW') return d;
  return a.earlier;
}

export function verdict(a: Act): VerdictV {
  const d = stepDecision(a);
  return d ? verdictOf(d.d, d.r) : { kind: 'allow', would: false };
}

/** Which of Today’s groups this falls in, when Lumni really stepped in. */
export function stepKind(a: Act): StepKind | null {
  const d = stepDecision(a);
  if (!d || isWould(d.d)) return null;
  return stepOf(verdictOf(d.d, d.r).kind);
}

/** What a watching rule would have done here, if it were protecting. */
export function wouldKind(a: Act): StepKind | null {
  const d = stepDecision(a);
  if (!d || !isWould(d.d)) return null;
  return stepOf(verdictOf(d.d, d.r).kind);
}

const BAD: OutcomeStatus[] = ['MISMATCH', 'PARTIAL', 'FAILED'];

/** Didn’t happen, by the app’s own records. */
export function isBad(a: Act): boolean {
  return !!a.outcome && BAD.includes(a.outcome.status);
}

export interface ProofCtx {
  /** Apps whose checker is switched off, so the proof can offer to switch it on. */
  off: Set<AppId>;
}

/** Did it really happen? What the console shows after the action. */
export function proof(a: Act, c?: ProofCtx): ProofV {
  const step = stepKind(a);
  const latest = a.decision;
  if (step === 'stop' && latest && latest === stepDecision(a)) return { kind: 'stopped', text: 'Stopped' };
  // A no, or an ask nobody answered, keeps it from going out.
  if (a.approval?.status === 'DENIED' && a.status !== 'SUCCEEDED') return { kind: 'stopped', text: 'You said no' };
  if (a.approval?.status === 'EXPIRED' && a.status !== 'SUCCEEDED') return { kind: 'stopped', text: 'Nobody answered' };
  if (a.status === 'AWAITING_APPROVAL') return { kind: 'wait', text: 'Waiting for you' };
  if (a.status === 'PROPOSED' && latest && !isWould(latest.d) && latest.d !== 'ALLOW') {
    if (latest.d === 'WAIT') return { kind: 'wait', text: 'Waiting its turn' };
    if (latest.d === 'REPLAN') return { kind: 'wait', text: 'Sent back to look again' };
  }
  if (a.status === 'RESERVED' || a.status === 'ACTIVE') return { kind: 'busy', text: 'In progress' };
  if (a.status === 'UNKNOWN') return { kind: 'none', text: 'Result unknown' };
  const o = a.outcome?.status ?? null;
  if (o === 'VERIFIED') return { kind: 'ok', text: 'Checked' };
  if (o === 'PENDING') return { kind: 'busy', text: 'Checking…' };
  if (o && BAD.includes(o)) return proofOf(o);
  if (a.status === 'EXPIRED' || a.status === 'CANCELLED') return { kind: 'none', text: 'Never ran' };
  if (a.status === 'FAILED') return { kind: 'bad', text: 'Failed' };
  if (a.status === 'PROPOSED') return { kind: 'none', text: 'Not started' };
  const app = appOf(a);
  if (c?.off.has(app.id)) return { kind: 'none', text: 'Can’t check', link: { label: `Check ${app.name}`, to: '/apps' } };
  return { kind: 'none', text: 'Can’t check' };
}

// ─── the four checks ───────────────────────────────────

/** The kernel’s stages, folded into the four checks the console explains. */
const STAGE: Record<string, EvaluationStage> = {
  resource_versions: 'FRESHNESS',
  idempotency: 'IDEMPOTENCY',
  exact_effect: 'IDEMPOTENCY',
  conflicts: 'LEASE',
  leases: 'LEASE',
  policy: 'POLICY',
};

const ORDER: EvaluationStage[] = ['FRESHNESS', 'IDEMPOTENCY', 'LEASE', 'POLICY'];

/** A stage caught it when its outcome is a decision rather than a pass. */
function caught(outcome: string): boolean {
  return !['PASS', 'SKIPPED', 'NOT_REQUIRED', 'ACQUIRED', ''].includes(outcome.toUpperCase());
}

export function evaluations(d: Dec | null): DecisionEvaluation[] {
  const out = new Map<EvaluationStage, DecisionEvaluation>();
  for (const s of ORDER) out.set(s, { stage: s, outcome: 'PASS' });
  for (const e of d?.evals ?? []) {
    const stage = STAGE[e.stage];
    if (!stage || !caught(e.outcome)) continue;
    out.set(stage, { stage, outcome: 'HIT', reason_code: (e.reason_code as ReasonCode) || d!.r, subject_id: e.subject_id, details: e.details });
  }
  // A decision with no stage marked still stopped somewhere: say where its reason points.
  if (d && d.d !== 'ALLOW' && d.d !== 'WOULD_ALLOW' && ![...out.values()].some((e) => e.outcome === 'HIT')) {
    const stage = stageOfReason(d.r);
    out.set(stage, { stage, outcome: 'HIT', reason_code: d.r });
  }
  return ORDER.map((s) => out.get(s)!);
}

export function stageOfReason(r: ReasonCode): EvaluationStage {
  if (/STALE|VERSION/.test(r)) return 'FRESHNESS';
  if (/DUPLICATE|EFFECT|IDEMPOTENCY/.test(r)) return 'IDEMPOTENCY';
  if (/CONFLICT|LEASE|FENCING/.test(r)) return 'LEASE';
  return 'POLICY';
}

// ─── sorting a day ─────────────────────────────────────

/** Newest first. */
export const byNewest = (a: Act, b: Act) => b.at - a.at || (a.id < b.id ? 1 : -1);
