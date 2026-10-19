// Pieces several real views share: the day’s hill, who’s who, which apps are
// checked, and your rules with the mode each one is in.

import type { AppId, EnforcementMode, ReasonCode, Who } from '../../../src/domain/types';
import { fmt, money, type Zone } from '../../../src/domain/format';
import type { GoPolicyRule, GoVerifier } from '../go';
import type { Ctx } from '../load';
import { appIdOf, appName, opWords, proof, whoOf, type Act } from '../model';

/** Actions per quarter hour from midnight, against the busiest. */
export function hill(acts: Act[], from: number, quarters: number, z: Zone): number[] {
  const n = Math.max(1, quarters);
  const counts = new Array<number>(n).fill(0);
  for (const a of acts) {
    if (a.at < from) continue;
    const q = Math.min(n - 1, Math.floor(z.hourOf(a.at) * 4));
    counts[q]! += 1;
  }
  const peak = Math.max(1, ...counts);
  return counts.map((c) => c / peak);
}

/** How many quarter hours of the day have begun. */
export function quartersSoFar(nowH: number): number {
  return Math.min(96, Math.floor(nowH * 4) + 1);
}

export async function people(ctx: Ctx): Promise<(id: string | null) => Who> {
  const actors = await ctx.actors();
  return (id) => whoOf(actors, id);
}

/** The app a checker checks: its config says, or its id or name does. */
export function verifierApp(v: GoVerifier): AppId {
  const said = v.config?.app;
  if (typeof said === 'string' && said.trim()) return said.trim().toLowerCase();
  const id = v.id.replace(/^(v|ver|verifier|chk|check)[_-]/i, '').toLowerCase();
  return id || v.name.toLowerCase().split(/\s+/)[0]!;
}

export interface Checkers {
  byApp: Map<AppId, GoVerifier>;
  /** Apps whose checker is switched off. */
  off: Set<AppId>;
}

export async function checkers(ctx: Ctx): Promise<Checkers> {
  const list = await ctx.verifiers();
  const byApp = new Map<AppId, GoVerifier>();
  for (const v of list) {
    const app = verifierApp(v);
    // An enabled checker wins over an old disabled one for the same app.
    const cur = byApp.get(app);
    if (!cur || (!cur.enabled && v.enabled)) byApp.set(app, v);
  }
  const off = new Set<AppId>();
  for (const [app, v] of byApp) if (!v.enabled) off.add(app);
  return { byApp, off };
}

export function proofIn(c: Checkers) {
  return (a: Act) => proof(a, { off: c.off });
}

// ─── rules ─────────────────────────────────────────────

export interface RuleInfo {
  rule: GoPolicyRule;
  mode: EnforcementMode;
  /** One sentence, with the threshold where the editable word goes. */
  text: string;
  area: string;
}

export interface Rules {
  list: RuleInfo[];
  tenantMode: EnforcementMode;
  /** The mode Lumni’s own checks run in for an operation. */
  modeFor(op: string, reason: ReasonCode | string): EnforcementMode;
  /** The rule that most likely made a decision, by its reason and operation. */
  match(op: string | null, reason: string, policy?: string | null): RuleInfo | null;
}

export async function rules(ctx: Ctx): Promise<Rules> {
  const [policy, enforcement, config] = await Promise.all([ctx.policyRules(), ctx.enforcement(), ctx.config()]);
  const tenantMode = config.enforcement_mode ?? 'SHADOW';
  const find = (op: string, rule: string) => enforcement.find((e) => e.operation === op && e.rule === rule)?.mode;
  // The kernel’s order: the reason for this operation, then for every operation, then the operation, then the workspace.
  const modeFor = (op: string, reason: string): EnforcementMode =>
    find(op, reason) ?? find('*', reason) ?? (op !== '*' ? find(op, '*') : undefined) ?? tenantMode;
  const list = policy
    .filter((r) => r.enabled !== false)
    .map((r): RuleInfo => ({ rule: r, mode: modeFor(r.operation, r.reason_code), text: ruleText(r), area: areaOf(r) }));
  return {
    list,
    tenantMode,
    modeFor,
    match(op, reason, policyId) {
      if (policyId) {
        const hit = list.find((x) => x.rule.id === policyId);
        if (hit) return hit;
      }
      const same = list.filter((x) => x.rule.reason_code === reason && (x.rule.operation === '*' || !op || x.rule.operation === op));
      return same.find((x) => x.rule.operation === op) ?? same[0] ?? null;
    },
  };
}

/** What the threshold measures: money, or a count of something. */
export function thresholdIsMoney(r: GoPolicyRule): boolean {
  return /amount|price|total|cost|usd|value|refund|charge/i.test(r.argument_pointer ?? '');
}

export function thresholdLabel(r: GoPolicyRule, v: number): string {
  return thresholdIsMoney(r) ? money(v) : fmt(v);
}

/** "Stripe refund", or "any action" for a rule over every operation. */
export function ruleObject(r: GoPolicyRule): string {
  return r.operation === '*' ? 'any action' : `a ${opWords(r.operation)}`;
}

const LEAD: Record<GoPolicyRule['outcome'], string> = {
  REQUIRE_APPROVAL: 'Ask a person before',
  REJECT: 'Stop',
  WARN: 'Flag',
};

/** The parts of the sentence around the threshold. */
export function ruleParts(r: GoPolicyRule): (string | { token: string })[] {
  const parts: (string | { token: string })[] = [`${LEAD[r.outcome] ?? 'Check'} ${ruleObject(r)}`];
  if (r.argument_pointer && r.numeric_threshold !== null) {
    const field = r.argument_pointer.replace(/^\//, '').replace(/[/_]+/g, ' ');
    parts.push(thresholdIsMoney(r) ? ' over ' : ` with ${field} over `, { token: 'threshold' });
  }
  if (r.minimum_risk) parts.push(` when its risk is ${r.minimum_risk.toLowerCase()} or higher`);
  if (r.resource_type && r.resource_type !== '*') parts.push(` that touches a ${r.resource_type.replace(/[._]+/g, ' ')}`);
  return parts;
}

export function ruleText(r: GoPolicyRule): string {
  return ruleParts(r)
    .map((p) => (typeof p === 'string' ? p : thresholdLabel(r, r.numeric_threshold ?? 0)))
    .join('');
}

export function areaOf(r: GoPolicyRule): string {
  if (r.operation === '*') return 'Every action';
  const [app, ...rest] = r.operation.split('.');
  const what = rest.join(' ').replace(/[_-]+/g, ' ').trim();
  return what ? `${appName(app!)} ${what}s`.replace(/ss$/, 'ses') : appName(app!);
}

/** The app an action reached, by id and name. */
export function appRef(a: Act): { id: AppId; name: string } {
  const id = appIdOf(a);
  return { id, name: appName(id) };
}
