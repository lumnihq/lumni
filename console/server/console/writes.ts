// The console’s own writes, done with the API calls that exist. Rules change
// through the enforcement and policy endpoints; checkers through verifiers.
// Writes with nothing behind them answer 501, and the screens show those
// controls as not available yet.

import { HttpError, notYet } from '../http';
import type { GoPolicyRule } from './go';
import type { Ctx } from './load';
import { checkers, rules } from './views/shared';
import { TEMPLATES } from './views/rules';

const MODES = new Set(['ENFORCE', 'SHADOW']);

/** The fields the policy endpoint takes, from a rule as it is now. */
function bodyOf(r: GoPolicyRule): Record<string, unknown> {
  return {
    name: r.name, operation: r.operation, resource_type: r.resource_type, minimum_risk: r.minimum_risk,
    argument_pointer: r.argument_pointer, numeric_threshold: r.numeric_threshold, outcome: r.outcome, reason_code: r.reason_code,
  };
}

async function ruleById(ctx: Ctx, id: string) {
  const rs = await rules(ctx);
  const r = rs.list.find((x) => x.rule.id === id);
  if (!r) throw new HttpError(404, 'not_found', 'No rule with that id.');
  return r;
}

export async function setRuleMode(ctx: Ctx, id: string, mode: unknown) {
  if (typeof mode !== 'string' || !MODES.has(mode)) throw new HttpError(400, 'invalid_mode', 'A rule either protects or only watches.');
  const r = await ruleById(ctx, id);
  // A rule’s mode lives on its operation and reason, the way the kernel looks it up.
  await ctx.send('PUT', '/v1/config/enforcement', { operation: r.rule.operation, rule: r.rule.reason_code, mode });
  if (mode === 'ENFORCE') ctx.state.protectedAt.set(id, ctx.now);
  else ctx.state.protectedAt.delete(id);
  return { id, mode };
}

export async function editRule(ctx: Ctx, id: string, body: { token?: unknown; value?: unknown; action?: unknown }) {
  const r = await ruleById(ctx, id);
  const drafts = ctx.state.drafts;
  if (body.action === 'undo') {
    drafts.delete(id);
    return { id };
  }
  if (body.action === 'preview') {
    if (body.token !== 'threshold' || r.rule.numeric_threshold === null) throw new HttpError(400, 'invalid_token', 'That word in the rule can’t be changed.');
    const v = Number(body.value);
    if (!Number.isFinite(v) || v <= 0) throw new HttpError(400, 'invalid_value', 'Pick an amount above zero.');
    drafts.set(id, { token: 'threshold', value: String(v) });
    return { id };
  }
  if (body.action === 'save') {
    const d = drafts.get(id);
    if (!d) throw new HttpError(409, 'nothing_to_save', 'There’s no change to save.');
    ctx.state.undo.set(id, bodyOf(r.rule));
    // The policy endpoint keeps one rule per name, so posting it again updates it.
    await ctx.send('POST', '/v1/config/policy-rules', { ...bodyOf(r.rule), numeric_threshold: Number(d.value) });
    drafts.delete(id);
    return { id };
  }
  throw new HttpError(400, 'invalid_action', 'Preview, save or undo.');
}

export async function addRule(ctx: Ctx, template: unknown) {
  const t = TEMPLATES.find((x) => x.id === template);
  if (!t) throw new HttpError(404, 'not_found', 'No rule like that.');
  const rs = await rules(ctx);
  if (rs.list.some((x) => x.rule.name === t.body.name)) throw new HttpError(409, 'already_added', 'That rule is already one of yours.');
  // New rules start out only watching. But a mode lives on an operation and
  // reason, which an existing rule may share: never weaken a rule that's
  // already protecting just to add another. Then the new one protects too.
  const shares = rs.list.find((x) => x.rule.operation === t.body.operation && x.rule.reason_code === t.body.reason_code && x.mode === 'ENFORCE');
  if (!shares) await ctx.send('PUT', '/v1/config/enforcement', { operation: t.body.operation, rule: t.body.reason_code, mode: 'SHADOW' });
  const out = await ctx.send<{ id: string }>('POST', '/v1/config/policy-rules', t.body);
  return { id: out.id, mode: shares ? 'ENFORCE' : 'SHADOW', sharesWith: shares?.rule.name ?? null };
}

export async function protect(ctx: Ctx, ids: unknown) {
  const rs = await rules(ctx);
  const chosen = Array.isArray(ids) ? rs.list.filter((r) => (ids as unknown[]).includes(r.rule.id)) : rs.list.filter((r) => r.mode !== 'ENFORCE');
  const todo = chosen.filter((r) => r.mode !== 'ENFORCE');
  if (!todo.length) throw new HttpError(409, 'nothing_to_protect', 'Everything here is already protecting.');
  for (const r of todo) {
    await ctx.send('PUT', '/v1/config/enforcement', { operation: r.rule.operation, rule: r.rule.reason_code, mode: 'ENFORCE' });
    ctx.state.protectedAt.set(r.rule.id, ctx.now);
  }
  return { protected: todo.map((r) => r.rule.id) };
}

/** Switch an app’s checker on or off. The API keeps one verifier per id, so posting it again updates it. */
export async function setChecker(ctx: Ctx, app: string, on: unknown) {
  const chk = await checkers(ctx);
  const v = chk.byApp.get(app);
  if (!v) {
    if (on === false) throw new HttpError(404, 'not_found', 'Lumni has no checker for that app.');
    throw notYet('Connecting a new app');
  }
  await ctx.send('POST', '/v1/verifiers', { id: v.id, name: v.name, kind: v.kind, version: v.version, config: v.config ?? {}, enabled: on !== false });
  return { app, on: on !== false };
}
