// How every agent in this fleet talks to Lumni.
//
// The rule from docs/sdk.md, in one place (`guard`):
//   ALLOW, WARN and every WOULD_*  go ahead (WOULD_* means watch-only mode)
//   WAIT                           wait retry_after_ms, then ask again
//   REPLAN                         read the thing again, then ask again;
//                                  or stop, when the work itself was superseded
//   REQUIRE_APPROVAL               wait for a person, then ask again
//   DEDUPLICATE                    it's already done: don't do it twice
//   REJECT                         don't do it

import { existsSync, readFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

/** The published SDK when it's installed; until then, the one in this repository. */
async function loadSdk() {
  try {
    return await import('@lumniverse/sdk');
  } catch {
    return import('../../sdk/typescript/dist/index.js');
  }
}
const sdk = await loadSdk();
export const { LumniClient, shouldExecute } = sdk;

/** KEY=value lines from .env, which `lumni init` writes. The environment wins. */
export function settings(env = process.env, dir = here) {
  const file = join(dir, '.env');
  const dot = {};
  if (existsSync(file)) {
    for (const line of readFileSync(file, 'utf8').split('\n')) {
      const m = /^\s*([A-Z0-9_]+)\s*=\s*(.*?)\s*$/.exec(line);
      if (m) dot[m[1]] = m[2].replace(/^['"]|['"]$/g, '');
    }
  }
  return {
    url: (env.LUMNI_URL ?? dot.LUMNI_URL ?? 'http://localhost:8080').replace(/\/+$/, ''),
    key: env.LUMNI_API_KEY ?? dot.LUMNI_API_KEY ?? '',
  };
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
export const shortId = () => randomUUID().slice(0, 8);

/** Why, in plain words. */
const WHY = {
  LEASE_BUSY: 'another agent is working on it',
  CONFLICTING_ACTIVE_MUTATION: 'another agent is changing it',
  WRITE_WRITE_CONFLICT: 'another agent is changing it',
  DELETE_WRITE_CONFLICT: 'another agent is deleting it',
  DOMAIN_OPERATION_CONFLICT: 'it clashes with work already under way',
  CONCURRENCY_LIMIT: 'as many as you allow are already running',
  RATE_LIMITED: 'it hit the rate you set',
  BUDGET_PACING: 'it’s spending ahead of the daily limit',
  STALE_OBSERVATION: 'what it read is out of date',
  VERSION_SOURCE_CHANGED: 'it read the thing from somewhere else',
  EXACT_EFFECT_ALREADY_COMMITTED: 'it was already done',
  EXACT_DUPLICATE_ACTIVE: 'the same thing is already under way',
  DEPENDENCY_INCOMPLETE: 'what it depends on isn’t finished',
  ACTION_RESULT_UNKNOWN: 'an earlier try may have done it; Lumni is finding out',
  WORK_SUPERSEDED: 'the work it was doing was superseded',
  WORK_CANCELLED: 'the work it was doing was cancelled',
  ACTION_SUPERSEDED: 'this step was superseded',
  ACTOR_PAUSED: 'you paused this agent',
  POLICY_REJECTED: 'a rule doesn’t allow it',
  APPROVAL_DENIED: 'a person said no',
  APPROVAL_EXPIRED: 'nobody answered in time',
};
export const why = (code) => WHY[code] ?? String(code ?? 'no reason given').toLowerCase().replace(/_/g, ' ');

const WOULD = {
  WAIT: 'would have made it wait',
  REPLAN: 'would have sent it back',
  DEDUPLICATE: 'would have stopped it',
  REQUIRE_APPROVAL: 'would have asked a person first',
  REJECT: 'would have refused it',
};
/** REPLAN reasons that mean the work is no longer wanted: stop, don't re-read. */
const GONE = new Set(['WORK_SUPERSEDED', 'WORK_CANCELLED', 'ACTION_SUPERSEDED']);

/**
 * Check an action with Lumni, and do it only when Lumni says so.
 *
 *   input     what the agent wants to do (actor_id, work_id, operation, resources, …)
 *   act       the side effect, run once Lumni allows it; its return value is reported as the result
 *   onReplan  re-read what went stale; returns the new input
 *   seen      a Set that collects each decision ("ALLOW", "WAIT:LEASE_BUSY", …)
 *
 * Waits (WAIT) are bounded by time, since a queue or a dependency can take a
 * while; everything else by a count.
 *
 * Resolves with { done, skipped?, refused?, superseded?, output?, decision }.
 */
export async function guard(lumni, input, act, { say = () => {}, onReplan, seen, approvalWaitMs = 120_000, pollMs = 3000, waitBudgetMs = 90_000, maxTries = 8 } = {}) {
  let ask = { ...input, action_id: input.action_id ?? `${input.actor_id}-${shortId()}` };
  const until = Date.now() + waitBudgetMs;
  let tries = 0;
  let lastWait = '';
  while (tries < maxTries) {
    const d = await lumni.checkAction(ask);
    const raw = String(d.decision);
    const kind = raw.replace(/^WOULD_/, '');
    seen?.add(raw === 'ALLOW' ? 'ALLOW' : `${raw}:${d.reason_code}`);

    if (shouldExecute(d)) {
      if (raw.startsWith('WOULD_') && WOULD[kind]) say(`Lumni ${WOULD[kind]}: ${why(d.reason_code)} (watch-only, so it goes ahead)`);
      if (kind === 'WARN') say(`Lumni warns: ${why(d.reason_code)}`);
      return { done: true, output: await perform(lumni, d, act, say), decision: d };
    }
    if (kind === 'DEDUPLICATE') {
      say(`Lumni stopped it: ${why(d.reason_code || 'EXACT_EFFECT_ALREADY_COMMITTED')}`);
      return { done: false, skipped: true, decision: d };
    }
    if (kind === 'WAIT') {
      if (Date.now() > until) break;
      // Say it once per reason, not on every retry.
      if (lastWait !== d.reason_code) say(`Lumni says wait: ${why(d.reason_code)}`);
      lastWait = d.reason_code;
      await sleep(Math.min(5_000, Math.max(300, d.retry_after_ms ?? 1000)));
      continue;
    }
    tries++;
    if (kind === 'REPLAN') {
      if (GONE.has(d.reason_code)) {
        say(`Lumni stopped it: ${why(d.reason_code)}`);
        return { done: false, superseded: true, decision: d };
      }
      say(`Lumni sent it back: ${why(d.reason_code)}. Reading again`);
      if (!onReplan) return { done: false, refused: true, decision: d };
      ask = { ...(await onReplan(ask)), action_id: `${input.actor_id}-${shortId()}` };
      continue;
    }
    if (kind === 'REQUIRE_APPROVAL') {
      say('Waiting for a person to decide (open Needs you in the console)');
      const status = await waitForPerson(lumni, d.approval_id, approvalWaitMs, pollMs);
      if (status !== 'APPROVED') {
        say(status === 'DENIED' ? 'A person said no. Not doing it' : 'Nobody decided in time. Not doing it');
        return { done: false, refused: true, decision: d };
      }
      say('A person approved it');
      continue; // the same action_id, checked again, is now allowed
    }
    say(`Lumni refused it: ${why(d.reason_code)}`);
    return { done: false, refused: true, decision: d };
  }
  say('Gave up waiting');
  return { done: false, refused: true };
}

async function waitForPerson(lumni, approvalId, waitMs, pollMs) {
  if (!approvalId) return 'UNKNOWN';
  const until = Date.now() + waitMs;
  while (Date.now() < until) {
    const a = await lumni.approval(approvalId).catch(() => null);
    const status = String(a?.status ?? 'PENDING').toUpperCase();
    if (status !== 'PENDING') return status;
    await sleep(pollMs);
  }
  return 'TIMED_OUT';
}

/** Start, do it, and say how it went. A thrown error is reported as FAILED. */
async function perform(lumni, d, act, say) {
  const lease = d.lease_token;
  await lumni.startAction(d.action_id, lease).catch((e) => say(`(couldn’t mark it started: ${e.message})`));
  try {
    const output = await act(d);
    await lumni.reportResult(d.action_id, { status: 'SUCCEEDED', lease_token: lease, output: output ?? {} }).catch((e) => say(`(couldn’t report the result: ${e.message})`));
    return output;
  } catch (e) {
    await lumni.reportResult(d.action_id, { status: 'FAILED', lease_token: lease, output: { error: e.message } }).catch(() => {});
    return { failed: e.message };
  }
}

/** Lumni's id for a thing, so its version can be recorded. */
export async function resourceId(lumni, ref) {
  const r = await lumni.request('POST', '/v1/resources/resolve', ref, true);
  return r.id;
}

/** What a model call cost, so Lumni's FinOps can tell useful spend from waste. */
export function spend(lumni, { actor, work, action, usd, model = 'gpt-4.1', tokens = 0 }) {
  return lumni.recordCost({
    actor_id: actor, work_id: work, ...(action ? { action_id: action } : {}),
    provider: 'openai', model, cost_type: 'MODEL', input_units: tokens, output_units: Math.round(tokens / 4),
    amount_micros_usd: Math.round(usd * 1e6),
  }).catch(() => {});
}
