// The fleet against a stand-in Lumni (stand-in.mjs) that decides the way the
// Go kernel does. Run: node --test
//
// Protect mode should leave the apps with no problems and Lumni stepping in on
// all twelve; watch-only mode should show exactly the trouble Lumni would
// have stopped.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { main } from './run.mjs';
import { standIn } from './stand-in.mjs';


async function run(mode, extra = [], { admin = true } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-'));
  const s = standIn(mode);
  const out = [];
  const argv = [...(admin ? ['--admin-key', 'admin'] : []), ...extra];
  const code = await main(argv, { LUMNI_URL: 'http://lumni.test', LUMNI_API_KEY: 'agent' }, { fetchFn: s.fetchFn, log: (l) => out.push(l), sleep: async () => {}, dir });
  const state = JSON.parse(readFileSync(join(dir, 'state.json'), 'utf8'));
  const text = out.join('\n');
  const coverage = text.slice(text.lastIndexOf('Coverage'));
  return { code, out: text, coverage, state, ...s };
}

const TWELVE = ['Duplicate effect', 'Stale reasoning', 'Human/agent collision', 'Resource contention', 'Cross-agent conflict', 'Superseded work', 'Duplicate work', 'Dependency', 'False completion', 'Partial completion', 'Cost waste', 'Unbounded agent fleet'];
const line = (coverage, problem) => coverage.split('\n').find((l) => l.includes(problem)) ?? '';

test('protected: Lumni steps in on all twelve, and the apps have no problems', async () => {
  const r = await run('ENFORCE');
  assert.equal(r.code, 0);
  for (const p of TWELVE) assert.match(line(r.coverage, p), /●/, `${p}: ${line(r.coverage, p)}`);
  assert.deepEqual(r.state.problems, []);
  assert.match(r.out, /Problems in the apps: none/);

  assert.match(line(r.coverage, 'Duplicate effect'), /deduplicate \(it was already done\)/);
  assert.match(line(r.coverage, 'Stale reasoning'), /replan \(what it read is out of date\)/);
  assert.match(line(r.coverage, 'Human/agent collision'), /replan \(what it read is out of date\)/);
  assert.match(line(r.coverage, 'Resource contention'), /wait \(another agent is working on it\)/);
  assert.match(line(r.coverage, 'Cross-agent conflict'), /wait \(another agent is working on it\)/);
  assert.match(line(r.coverage, 'Superseded work'), /replan \(the work it was doing was superseded\)/);
  assert.match(line(r.coverage, 'Duplicate work'), /joined support-agent/);
  assert.match(line(r.coverage, 'Dependency'), /wait \(what it depends on isn’t finished\)/);
  assert.match(line(r.coverage, 'False completion'), /incident/);
  assert.match(line(r.coverage, 'Partial completion'), /incident/);
  assert.match(line(r.coverage, 'Cost waste'), /deduplicate/);
  assert.match(line(r.coverage, 'Unbounded agent fleet'), /wait \(as many as you allow are already running\)/);

  // What the checker told Lumni, and what the money looked like.
  assert.deepEqual(r.evidence.map((e) => e.status).sort(), ['FAILED', 'MISMATCH', 'VERIFIED']);
  assert.ok(!r.costs.some((c) => c.amount_micros_usd === 7_200_000), 'the quote bot spent $7.20 on a quote that was already sent');
  assert.ok(r.state.research.maxAtOnce <= 5, `research ran ${r.state.research.maxAtOnce} at once`);
  assert.equal(r.state.billing.subscriptions.sub_initech.plan, 'Pro', 'a superseded upgrade went through');
  assert.equal([...r.work.entries()].find(([id]) => id.startsWith('order-8812-refund'))[1].actor_id, 'support-agent');
});

test('watch-only: everything goes ahead, and the trouble shows up in the apps', async () => {
  const r = await run('SHADOW');
  assert.equal(r.code, 0);
  const problems = r.state.problems.map((p) => p.scenario);
  for (const s of ['duplicate-effect', 'stale-reasoning', 'human-collision', 'resource-contention', 'cross-agent-conflict', 'superseded-work', 'dependency', 'cost-waste', 'runaway-fleet']) {
    assert.ok(problems.includes(s), `expected a problem from ${s}`);
    assert.match(r.out, /watch-only, so it goes ahead/);
  }
  assert.match(line(r.coverage, 'Stale reasoning'), /Watch-only, so it went ahead. Lumni would have said: replan \(what it read is out of date\)/);
  assert.ok(r.costs.some((c) => c.amount_micros_usd === 7_200_000), 'watch-only: the quote bot spends the $7.20');
  // Joining doesn't depend on the mode, and checkers still report.
  assert.match(line(r.coverage, 'Duplicate work'), /joined/);
  assert.match(line(r.coverage, 'False completion'), /incident/);
});

test('without an admin key: the checker scenarios are skipped, and nothing caps the swarm', async () => {
  const r = await run('ENFORCE', [], { admin: false });
  assert.match(line(r.coverage, 'False completion'), /Skipped: needs --admin-key/);
  assert.match(line(r.coverage, 'Partial completion'), /Skipped/);
  assert.ok(r.state.research.maxAtOnce > 5);
  assert.ok(r.state.problems.some((p) => p.scenario === 'runaway-fleet'));
});

test('one scenario, and the list', async () => {
  const r = await run('ENFORCE', ['--scenario', 'dependency']);
  assert.equal((r.out.match(/▸ /g) ?? []).length, 1);
  const out = [];
  await main(['--list'], {}, { log: (l) => out.push(l) });
  assert.equal(out.length, 14);
  assert.match(out.join('\n'), /approval.*only with --scenario/);
});

test('every agent works under its own id with the agent key; admin only for setup and evidence', async () => {
  const r = await run('ENFORCE');
  const checks = r.calls.filter((c) => c.path === '/v1/actions/check');
  assert.ok(checks.length > 50);
  assert.ok(checks.every((c) => c.auth === 'Bearer agent'));
  const adminCalls = r.calls.filter((c) => c.auth === 'Bearer admin').map((c) => c.path.replace(/\/[^/]*(evidence|cap-web-research)$/, '/…$1'));
  assert.ok(adminCalls.every((p) => /^\/v1\/(verifiers|outcomes\/.*evidence|config\/limits\/)/.test(p)), adminCalls.join(', '));
  const humans = r.calls.filter((c) => c.path === '/v1/actors' && c.body?.type === 'HUMAN').map((c) => c.body.id);
  assert.deepEqual(humans, ['jess']);
});

test('run it again against the same Lumni: the same twelve, still no problems, and the price list moves on', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-'));
  const s = standIn('ENFORCE');
  const go = async () => {
    const out = [];
    await main(['--admin-key', 'admin'], { LUMNI_URL: 'http://lumni.test', LUMNI_API_KEY: 'agent' }, { fetchFn: s.fetchFn, log: (l) => out.push(l), sleep: async () => {}, dir });
    return { out: out.join('\n'), state: JSON.parse(readFileSync(join(dir, 'state.json'), 'utf8')) };
  };
  const first = await go();
  const second = await go();
  const coverage = second.out.slice(second.out.lastIndexOf('Coverage'));
  const firstCoverage = first.out.slice(first.out.lastIndexOf('Coverage'));
  // The same reasons, not just "stepped in": a repeat run mustn't turn into "already done".
  for (const p of TWELVE) assert.equal(line(coverage, p), line(firstCoverage, p), `second run, ${p}`);
  assert.deepEqual(second.state.problems, []);
  assert.ok(second.state.salesforce.priceList.version > first.state.salesforce.priceList.version);
});

test('no key: says how to get one, and calls nothing', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-'));
  const s = standIn('ENFORCE');
  const out = [];
  const code = await main([], {}, { fetchFn: s.fetchFn, log: (l) => out.push(l), dir });
  assert.equal(code, 1);
  assert.match(out.join('\n'), /Make a code/);
  assert.equal(s.calls.length, 0);
});

test('a bad key stops before any agent starts', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-'));
  const s = standIn('ENFORCE');
  const out = [];
  const code = await main([], { LUMNI_URL: 'http://lumni.test', LUMNI_API_KEY: 'bad' }, { fetchFn: s.fetchFn, log: (l) => out.push(l), dir });
  assert.equal(code, 1);
  assert.match(out.join('\n'), /doesn’t accept this key/);
});
