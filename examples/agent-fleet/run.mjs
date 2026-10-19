#!/usr/bin/env node
// Run the fleet: twelve scenarios, one for each problem Lumni coordinates,
// acted out by small agents that check with Lumni before they change anything.
//
//   node run.mjs                              every scenario, once
//   node run.mjs --admin-key KEY              also the ones that need an admin key
//   node run.mjs --scenario stale-reasoning   one scenario (comma-separate for more)
//   node run.mjs --list                       what's here
//   node run.mjs --rounds 3 | --forever       again and again (Ctrl-C to stop)
//
// Reads LUMNI_URL and LUMNI_API_KEY from the environment or ./.env (lumni init writes it).

import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { LumniClient, guard, settings, shortId, sleep as wait, spend, why } from './lumni.mjs';
import { createApps } from './apps.mjs';
import { recordPriceVersion } from './scenarios/kit.mjs';
import { scenario as s01 } from './scenarios/01-duplicate-effect.mjs';
import { scenario as s02 } from './scenarios/02-stale-reasoning.mjs';
import { scenario as s03 } from './scenarios/03-human-collision.mjs';
import { scenario as s04 } from './scenarios/04-resource-contention.mjs';
import { scenario as s05 } from './scenarios/05-cross-agent-conflict.mjs';
import { scenario as s06 } from './scenarios/06-superseded-work.mjs';
import { scenario as s07 } from './scenarios/07-duplicate-work.mjs';
import { scenario as s08 } from './scenarios/08-dependency.mjs';
import { scenario as s09 } from './scenarios/09-false-completion.mjs';
import { scenario as s10 } from './scenarios/10-partial-completion.mjs';
import { scenario as s11 } from './scenarios/11-cost-waste.mjs';
import { scenario as s12 } from './scenarios/12-runaway-fleet.mjs';
import { scenario as loop } from './scenarios/extra-retry-loop.mjs';
import { scenario as approval } from './scenarios/extra-approval.mjs';

const here = dirname(fileURLToPath(import.meta.url));
export const SCENARIOS = [s01, s02, s03, s04, s05, s06, s07, s08, s09, s10, s11, s12, loop, approval];

function args(argv) {
  const o = {};
  for (let i = 0; i < argv.length; i++) {
    if (!argv[i].startsWith('--')) continue;
    const k = argv[i].slice(2);
    if (argv[i + 1] && !argv[i + 1].startsWith('--')) o[k] = argv[++i];
    else o[k] = true;
  }
  return o;
}

const tty = process.stdout.isTTY;
const paint = (code, s) => (tty ? `\x1b[${code}m${s}\x1b[0m` : s);
const PALETTE = [99, 214, 39, 170, 76, 203, 45, 141, 208, 118];
const colorOf = new Map();
const tag = (actor) => {
  if (!colorOf.has(actor)) colorOf.set(actor, PALETTE[colorOf.size % PALETTE.length]);
  return paint(`38;5;${colorOf.get(actor)}`, actor.padEnd(18));
};

/** "WAIT:LEASE_BUSY" → "wait (another agent is working on it)". */
const decisionWords = (s) => {
  const [d, r] = s.split(':');
  return `${d.replace(/^WOULD_/, '').toLowerCase().replace(/_/g, ' ')}${r ? ` (${why(r)})` : ''}`;
};

/** What happened in one scenario, from Lumni's decisions, its own notes and the apps' problems. */
export function verdict(sc, seen, notes, problems) {
  const real = [...seen].filter((s) => s !== 'ALLOW' && !s.startsWith('WOULD_'));
  const would = [...seen].filter((s) => s.startsWith('WOULD_'));
  const noted = notes.find((n) => n.kind === 'stepped-in' || n.kind === 'joined' || n.kind === 'noted');
  let kind;
  let text;
  if (real.length) {
    kind = 'stepped-in';
    text = `Lumni stepped in: ${[...new Set(real.map(decisionWords))].join(', ')}`;
  } else if (noted) {
    kind = noted.kind;
    text = noted.text;
  } else if (would.length) {
    kind = 'would-have';
    text = `Watch-only, so it went ahead. Lumni would have said: ${[...new Set(would.map(decisionWords))].join(', ')}`;
  } else {
    kind = 'nothing';
    text = notes.find((n) => n.kind === 'no-limit')?.text ?? 'Nothing for Lumni to do';
  }
  if (noted && real.length) text += `; ${noted.text}`;
  if (problems.length) text += `. ${problems.length} ${problems.length === 1 ? 'problem' : 'problems'} in the apps`;
  return { kind, text };
}

export async function main(argv, env = process.env, { fetchFn = fetch, log = console.log, sleep = wait, dir = here } = {}) {
  const o = args(argv);
  if (o.help) {
    log('node run.mjs [--scenario a,b] [--admin-key KEY] [--rounds N | --forever] [--list]');
    return 0;
  }
  if (o.list) {
    for (const s of SCENARIOS) log(`${s.id.padEnd(22)} ${s.problem.padEnd(26)} ${s.story}${s.needsAdmin ? '  (needs --admin-key)' : ''}${s.onlyWhenAsked ? '  (only with --scenario)' : ''}`);
    return 0;
  }
  const { url, key } = settings(env, dir);
  if (!key) {
    log('No Lumni key here. In the console, open Start, press Make a code, and run its command in this folder.');
    log('Or set LUMNI_URL and LUMNI_API_KEY.');
    return 1;
  }
  const lumni = new LumniClient(url, key, undefined, fetchFn);
  try {
    await lumni.request('GET', '/v1/actors?limit=1');
  } catch (e) {
    log(e.status === 401 || e.status === 403 ? `Lumni at ${url} doesn’t accept this key (${e.message}).` : `Lumni can’t be reached at ${url}: ${e.message}`);
    return 1;
  }
  const admin = o['admin-key'] ? new LumniClient(url, String(o['admin-key']), undefined, fetchFn) : null;

  const asked = typeof o.scenario === 'string' ? o.scenario.split(',').map((s) => s.trim()) : null;
  const chosen = asked ? SCENARIOS.filter((s) => asked.includes(s.id)) : SCENARIOS.filter((s) => !s.onlyWhenAsked);
  if (!chosen.length) {
    log(`No scenario called ${o.scenario}. Run with --list to see them.`);
    return 1;
  }
  const rounds = o.forever ? Infinity : Math.max(1, Math.min(100, Number(o.rounds ?? 1) || 1));
  // The apps remember their price list between runs (state.json), as real ones would.
  let kept = {};
  try {
    if (!o.fresh && existsSync(join(dir, 'state.json'))) kept = { priceList: JSON.parse(readFileSync(join(dir, 'state.json'), 'utf8')).salesforce?.priceList };
  } catch {}
  const apps = createApps(kept);

  const actors = new Map();
  for (const s of chosen) for (const a of s.actors) actors.set(a.id, a);
  for (const a of actors.values()) {
    await lumni.request('POST', '/v1/actors', { id: a.id, type: a.type ?? 'AGENT', display_name: a.name, runtime: a.type === 'HUMAN' ? undefined : 'node', metadata: { source: 'agent-fleet' } });
  }
  await recordPriceVersion(lumni, apps.salesforce.readPriceList());
  const people = [...actors.values()].filter((a) => a.type === 'HUMAN').length;
  log(`${actors.size - people} agents${people ? ` and ${people} ${people === 1 ? 'person' : 'people'}` : ''}, ${chosen.length} scenarios, checked by Lumni at ${url}.${admin ? '' : ' (No --admin-key: scenarios that need one are skipped.)'}`);

  let stop = false;
  const onStop = () => { stop = true; log('\nStopping after this scenario…'); };
  if (o.forever) process.once('SIGINT', onStop);

  const results = [];
  for (let round = 0; round < rounds && !stop; round++) {
    const runTag = shortId().slice(0, 4);
    apps.resetCustomers();
    if (rounds > 1) log(paint('1', `\n═══ Round ${round + 1}${rounds === Infinity ? '' : ` of ${rounds}`} ═══`));
    for (const sc of chosen) {
      if (stop) break;
      log(`\n${paint('1', `▸ ${sc.problem}`)}  ${paint('2', sc.story)}`);
      if (sc.needsAdmin && !admin) {
        log(`  ${paint('2', 'Skipped: needs --admin-key (the checker posts evidence with the verification scope)')}`);
        results.push({ sc, kind: 'skipped', text: 'Skipped: needs --admin-key' });
        continue;
      }
      const seen = new Set();
      const notes = [];
      const before = apps.state.problems.length;
      const say = (actor, text) => log(`  ${tag(actor)} ${text}`);
      const ctx = {
        lumni, admin, apps, say,
        // Fresh ids each run, so a second run against the same Lumni tells the same story.
        run: runTag,
        note: (kind, text) => notes.push({ kind, text }),
        spend: (x) => spend(lumni, x),
        // Lumni identifies a change by its operation, target and arguments, so the same
        // change in a later run would rightly be "already done". Each run's changes carry
        // the run, the way real work carries a request or ticket id; repeats within a run
        // are still repeats.
        guard: (input, act, opts = {}) => guard(lumni, { ...input, arguments: { ...(input.arguments ?? {}), batch: runTag } }, act, { seen, ...opts, say: opts.quietWaits ? () => {} : (t) => say(input.actor_id, t) }),
      };
      try {
        await sc.run.call(sc, ctx);
      } catch (e) {
        say('fleet', paint('31', `failed: ${e.message}`));
        notes.push({ kind: 'noted', text: `Failed: ${e.message}` });
      }
      const problems = apps.state.problems.slice(before);
      for (const p of problems) log(`  ${paint('31', '✗')} ${p.text}`);
      const v = verdict(sc, seen, notes, problems);
      log(`  ${v.kind === 'stepped-in' || v.kind === 'joined' ? paint('38;5;99', '●') : paint('2', '○')} ${v.text}`);
      results.push({ sc, ...v, problems: problems.length });
    }
    if (round + 1 < rounds && !stop) await sleep(o.forever ? 20_000 : 3000);
  }
  process.off?.('SIGINT', onStop);

  // The table, once per scenario (the last round's verdict).
  const last = new Map(results.map((r) => [r.sc.id, r]));
  log(paint('1', '\nCoverage'));
  for (const r of last.values()) {
    const mark = r.kind === 'stepped-in' || r.kind === 'joined' ? paint('38;5;99', '●') : r.kind === 'skipped' ? paint('2', '–') : paint('2', '○');
    log(`  ${mark} ${r.sc.problem.padEnd(26)} ${r.text}`);
  }
  const all = apps.state.problems;
  log(paint('1', `\nProblems in the apps: ${all.length ? all.length : 'none'}`));
  for (const p of all) log(`  ${paint('31', '✗')} ${p.text}`);
  if (!o['no-save']) {
    apps.save(join(dir, 'state.json'));
    log(paint('2', '\nEverything the apps saw is in state.json. The console shows Lumni’s side: Today, Needs you, Tower, Context, Runtime, Money.'));
  }
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main(process.argv.slice(2)).then((c) => process.exit(c), (e) => {
    console.error(e.message);
    process.exit(1);
  });
}
