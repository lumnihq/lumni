// A day of real-looking records for the fake API: routine work, repeats Lumni
// stopped, asks waiting on you, rules only watching, and things the app
// doesn’t show.

import { makeZone } from '../../src/domain/format';
import { world, type FakeAction, type FakeWorld } from './fake-go';

const MIN = 60_000;
const iso = (t: number) => new Date(t).toISOString();

export const ZONE = 'UTC';

/** Today in the zone, from midnight to a minute ago, with `n` actions spread across it. */
export function busyDay(n = 120, over: Partial<FakeWorld> = {}, zone = ZONE): FakeWorld {
  const z = makeZone(zone);
  const now = Date.now();
  const start = z.dayStart(now);
  const span = Math.max(10 * MIN, now - start - MIN);
  const w = world({
    actors: [
      { id: 'ai_support', type: 'AGENT', display_name: 'Support AI' },
      { id: 'ai_billing', type: 'AGENT', display_name: 'Billing AI' },
      { id: 'ai_sales', type: 'AGENT', display_name: 'Sales AI' },
    ],
    agents: [
      { id: 'ag_support', actor_id: 'ai_support', name: 'Support AI' },
      { id: 'ag_billing', actor_id: 'ai_billing', name: 'Billing AI' },
    ],
    verifiers: [
      { id: 'v_stripe', name: 'Stripe events', kind: 'EVENT', version: '1', config: { app: 'stripe' }, enabled: true, created_at: iso(start) },
      { id: 'v_gmail', name: 'Gmail events', kind: 'EVENT', version: '1', config: { app: 'gmail' }, enabled: false, created_at: iso(start) },
    ],
    rules: [
      { id: 'pr_refunds', name: 'Refunds over $100 need a person', operation: 'stripe.refund', resource_type: '*', minimum_risk: null, argument_pointer: '/amount', numeric_threshold: 100, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', enabled: true, version: 1, created_at: iso(start - 7 * 86_400_000) },
      { id: 'pr_email', name: 'Big email sends need a person', operation: 'gmail.send', resource_type: '*', minimum_risk: null, argument_pointer: '/recipients', numeric_threshold: 100, outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', enabled: true, version: 1, created_at: iso(start - 7 * 86_400_000) },
    ],
    enforcement: [{ operation: 'stripe.refund', rule: '*', mode: 'ENFORCE' }],
    ...over,
  });

  const actions: FakeAction[] = [];
  for (let i = 0; i < n; i++) {
    const at = start + Math.round(((i + 0.5) / n) * span);
    const id = `a_${i}`;
    const kind = i % 20;
    const base: FakeAction = {
      id, at, actor: i % 3 === 0 ? 'ai_support' : i % 3 === 1 ? 'ai_billing' : 'ai_sales', work: `w_${i}`, op: 'stripe.refund', status: 'SUCCEEDED',
      meta: { app: 'stripe', summary: `Refund $${20 + (i % 7) * 10} to Customer ${i}`, customer: `Customer ${i}`, amount: 20 + (i % 7) * 10 },
      decision: { d: 'ALLOW', r: 'NO_CONFLICT', evals: [{ stage: 'idempotency', outcome: 'PASS' }, { stage: 'policy', outcome: 'PASS' }] },
      outcome: { status: 'VERIFIED', verifier: 'v_stripe' },
      resources: [{ id: `stripe:charge:ch_${i}`, mode: 'WRITE', type: 'charge', name: `Order ${i}` }],
      cost: 40_000,
    };
    if (kind === 3) {
      // A repeat: the same refund again, stopped.
      base.decision = { d: 'DEDUPLICATE', r: 'EXACT_EFFECT_ALREADY_COMMITTED', evals: [{ stage: 'idempotency', outcome: 'PASS' }, { stage: 'exact_effect', outcome: 'DEDUPLICATE', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED' }] };
      base.outcome = undefined;
    } else if (kind === 7) {
      // Only watched: a big email.
      base.op = 'gmail.send';
      base.meta = { app: 'gmail', summary: `Email ${150 + i} customers` };
      base.decision = { d: 'WOULD_REQUIRE_APPROVAL', r: 'APPROVAL_REQUIRED' };
      base.outcome = { status: 'UNKNOWN', verifier: null };
      base.resources = [{ id: `gmail:thread:th_${i}`, mode: 'CREATE', type: 'thread' }];
    } else if (kind === 11) {
      base.outcome = { status: 'MISMATCH', verifier: 'v_stripe' };
      w.incidents.push({ id: `outcome_out_${id}`, kind: 'OUTCOME_MISMATCH', action_id: id, work_id: base.work, status: 'OPEN', title: 'OUTCOME_MISMATCH', evidence: { status: 'MISMATCH', reason: 'there’s no record of it' }, created_at: iso(at + 3000), updated_at: iso(at + 3000) });
    } else if (kind === 15) {
      base.decision = { d: 'WAIT', r: 'WRITE_WRITE_CONFLICT', evals: [{ stage: 'conflicts', outcome: 'WAIT', reason_code: 'WRITE_WRITE_CONFLICT' }] };
      base.status = 'PROPOSED';
      base.outcome = { status: 'PENDING', verifier: 'v_stripe' };
    }
    if (base.decision && base.decision.d !== 'ALLOW') {
      w.incidents.push({ id: `finding_rcpt_${id}`, kind: base.decision.r, action_id: id, work_id: base.work, status: 'OPEN', title: base.decision.r, evidence: { receipt_id: `rcpt_${id}`, action_id: id, decision: base.decision.d, reason_code: base.decision.r, enforced: !base.decision.d.startsWith('WOULD_') }, created_at: iso(at), updated_at: iso(at) });
    }
    actions.push(base);
  }
  // One ask still waiting on you, from a few minutes ago.
  const askAt = now - 5 * MIN;
  actions.push({
    id: 'a_ask', at: askAt, actor: 'ai_support', work: 'w_ask', op: 'stripe.refund', status: 'AWAITING_APPROVAL',
    meta: { app: 'stripe', summary: 'Refund $250 to Dana Lee', customer: 'Dana Lee', amount: 250 },
    decision: { d: 'REQUIRE_APPROVAL', r: 'APPROVAL_REQUIRED', evals: [{ stage: 'policy', outcome: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED' }] },
    outcome: { status: 'PENDING', verifier: 'v_stripe' },
    resources: [{ id: 'stripe:charge:ch_dana', mode: 'WRITE', type: 'charge', name: 'Dana’s order' }],
  });
  w.approvals.push({ id: 'ap_1', action_id: 'a_ask', status: 'PENDING', reason_code: 'APPROVAL_REQUIRED', requested_at: iso(askAt), expires_at: iso(askAt + 86_400_000), resolved_at: null });
  w.incidents.push({ id: 'finding_rcpt_a_ask', kind: 'APPROVAL_REQUIRED', action_id: 'a_ask', work_id: 'w_ask', status: 'OPEN', title: 'APPROVAL_REQUIRED', evidence: { receipt_id: 'rcpt_a_ask', action_id: 'a_ask', decision: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', enforced: true }, created_at: iso(askAt), updated_at: iso(askAt) });
  w.actions = actions;
  return w;
}
