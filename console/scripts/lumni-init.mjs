#!/usr/bin/env node
// lumni init: put Lumni in front of an agent project with one command.
//
// The console serves this file at /init with its API URL filled in, so the
// command people copy from /start is:
//
//   curl -fsSL https://<console>/init | node --input-type=module - --code LUM-7Q4K-9P2X
//
// The code is single-use and lasts 15 minutes; the console trades it for a
// scoped API key, so no key appears in the command or your shell history.
// With a key you already have: LUMNI_API_KEY=lumni_… instead of --code.
//
// or, with the file on disk:
//
//   node lumni-init.mjs --key lumni_… [--url URL] [--dir .] [--actor name] [--yes] [--dry-run]
//
// It needs Node 22+ and nothing else. In order, it:
//   1. checks the key against your Lumni API;
//   2. looks at the project: Python, TypeScript/JavaScript or Go, and the
//      agent libraries it uses, plus any MCP client configs (Cursor, Claude, VS Code);
//   3. writes LUMNI_URL and LUMNI_API_KEY to .env (never overwriting what's
//      there) and makes sure .env is git-ignored;
//   4. wraps each stdio MCP server with Lumni's gateway, keeping a backup of
//      the config and putting the URL and key in the server's env block;
//   5. registers the agent and sends one harmless hello check, so the
//      console's /start page lights up;
//   6. prints the three lines to add to your code.
//
// It never reads credentials from MCP configs, never sends your code anywhere,
// and --dry-run changes nothing at all.

import { existsSync, readFileSync, writeFileSync, copyFileSync } from 'node:fs';
import { basename, join, resolve } from 'node:path';
import { homedir, hostname } from 'node:os';
import { execFileSync, spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';

/** Filled in by the console when it serves this file. */
const SERVED_URL = '__LUMNI_URL__';
/** The console that served this file, where onboarding codes are traded. */
const SERVED_CONSOLE = '__LUMNI_CONSOLE__';

// ─── looking at the project ─────────────────────────

const LIBS = {
  python: [['langgraph', 'LangGraph'], ['langchain', 'LangChain'], ['openai', 'OpenAI'], ['anthropic', 'Anthropic'], ['crewai', 'CrewAI'], ['autogen', 'AutoGen'], ['temporalio', 'Temporal'], ['restate', 'Restate']],
  node: [['@langchain/langgraph', 'LangGraph'], ['langchain', 'LangChain'], ['openai', 'OpenAI'], ['@anthropic-ai/sdk', 'Anthropic'], ['ai', 'Vercel AI SDK'], ['@modelcontextprotocol/sdk', 'MCP SDK'], ['@temporalio/client', 'Temporal'], ['@restatedev/restate-sdk', 'Restate']],
  go: [['go.temporal.io/sdk', 'Temporal'], ['github.com/sashabaranov/go-openai', 'OpenAI'], ['github.com/anthropics/anthropic-sdk-go', 'Anthropic']],
};

/** Where MCP clients keep their server lists, project first. */
export function mcpConfigPaths(dir, home = homedir()) {
  return [
    join(dir, '.mcp.json'),
    join(dir, '.cursor', 'mcp.json'),
    join(dir, '.vscode', 'mcp.json'),
    join(home, '.cursor', 'mcp.json'),
    join(home, 'Library', 'Application Support', 'Claude', 'claude_desktop_config.json'),
    join(home, '.config', 'Claude', 'claude_desktop_config.json'),
  ];
}

const read = (p) => {
  try {
    return readFileSync(p, 'utf8');
  } catch {
    return null;
  }
};

/** What kind of project this is, and which agent libraries it uses. */
export function detect(dir, home = homedir()) {
  const out = { languages: [], libraries: [], mcp: [] };
  const pkg = read(join(dir, 'package.json'));
  if (pkg) {
    let deps = {};
    try {
      const j = JSON.parse(pkg);
      deps = { ...j.dependencies, ...j.devDependencies };
    } catch {}
    out.languages.push(existsSync(join(dir, 'tsconfig.json')) || deps.typescript ? 'typescript' : 'javascript');
    for (const [k, name] of LIBS.node) if (deps[k]) out.libraries.push(name);
  }
  const py = [read(join(dir, 'pyproject.toml')), read(join(dir, 'requirements.txt')), read(join(dir, 'Pipfile'))].filter(Boolean).join('\n');
  if (py) {
    out.languages.push('python');
    for (const [k, name] of LIBS.python) if (new RegExp(`(^|[\\s"'\\[,])${k}([\\s<>=~!\\[;"',]|$)`, 'im').test(py)) out.libraries.push(name);
  }
  const gomod = read(join(dir, 'go.mod'));
  if (gomod) {
    out.languages.push('go');
    for (const [k, name] of LIBS.go) if (gomod.includes(k)) out.libraries.push(name);
  }
  out.libraries = [...new Set(out.libraries)];
  for (const path of mcpConfigPaths(dir, home)) {
    const text = read(path);
    if (!text) continue;
    let cfg;
    try {
      cfg = JSON.parse(text);
    } catch {
      continue;
    }
    // VS Code calls it "servers"; the others "mcpServers".
    const key = cfg.mcpServers ? 'mcpServers' : cfg.servers ? 'servers' : null;
    if (!key) continue;
    const servers = Object.entries(cfg[key]).map(([name, s]) => ({
      name,
      stdio: typeof s?.command === 'string',
      wrapped: typeof s?.command === 'string' && basename(s.command) === 'gateway',
    }));
    out.mcp.push({ path, key, servers });
  }
  return out;
}

// ─── writing ────────────────────────────────────────

/** Add LUMNI_URL and LUMNI_API_KEY to .env, keeping anything already set. */
export function planEnv(existing, url, key) {
  const lines = (existing ?? '').split(/\r?\n/);
  const has = (k) => lines.some((l) => new RegExp(`^\\s*(export\\s+)?${k}\\s*=`).test(l));
  const add = [];
  if (!has('LUMNI_URL')) add.push(`LUMNI_URL=${url}`);
  if (!has('LUMNI_API_KEY')) add.push(`LUMNI_API_KEY=${key}`);
  if (!add.length) return { text: existing, added: [] };
  const base = existing && !existing.endsWith('\n') ? existing + '\n' : existing ?? '';
  return { text: `${base}${base ? '\n' : ''}# Lumni, added by lumni init\n${add.join('\n')}\n`, added: add.map((l) => l.split('=')[0]) };
}

/** Make sure .env never gets committed. */
export function planGitignore(existing) {
  const lines = (existing ?? '').split(/\r?\n/).map((l) => l.trim());
  if (lines.includes('.env') || lines.includes('.env*') || lines.includes('*.env')) return null;
  const base = existing && !existing.endsWith('\n') ? existing + '\n' : existing ?? '';
  return `${base}.env\n`;
}

/**
 * Wrap each stdio MCP server with Lumni's gateway, the way `lumni gateway
 * connect` does, and also give it the URL, key and an actor name, which the
 * gateway reads from its environment.
 */
export function planMcp(text, key, gatewayPath, env) {
  const cfg = JSON.parse(text);
  const list = cfg[key];
  const wrapped = [];
  const skipped = [];
  for (const [name, s] of Object.entries(list)) {
    if (typeof s?.command !== 'string') {
      skipped.push({ name, why: 'not a stdio server; use `gateway run --transport http --upstream` for it' });
      continue;
    }
    if (basename(s.command) === 'gateway') {
      skipped.push({ name, why: 'already wrapped' });
      continue;
    }
    list[name] = {
      ...s,
      command: gatewayPath,
      args: ['--transport', 'stdio', '--command', s.command, '--', ...(Array.isArray(s.args) ? s.args : [])],
      env: { ...(s.env ?? {}), LUMNI_URL: env.url, LUMNI_API_KEY: env.key, LUMNI_GATEWAY_ACTOR: `${env.actor}-${name}` },
    };
    wrapped.push(name);
  }
  return { text: JSON.stringify(cfg, null, 2) + '\n', wrapped, skipped };
}

/** The lines to add to the agent's code, for what we found. */
export function snippet(lang, actor) {
  if (lang === 'python') {
    return `from lumni import Client, should_execute
lumni = Client(os.environ["LUMNI_URL"], os.environ["LUMNI_API_KEY"])

decision = lumni.check_action({"actor_id": "${actor}", "work_id": run_id, "operation": "stripe.refund",
    "arguments": {"charge": charge_id, "amount": 60}, "resources": [{"type": "charge", "external_id": charge_id, "mode": "WRITE"}]})
if should_execute(decision):
    refund = stripe.Refund.create(charge=charge_id, amount=6000)`;
  }
  if (lang === 'go') {
    return `client := lumni.New(os.Getenv("LUMNI_URL"), os.Getenv("LUMNI_API_KEY"))
d, err := client.CheckAction(ctx, protocol.ActionCheckInput{ActorID: "${actor}", WorkID: runID, Operation: "stripe.refund",
    Resources: []protocol.ResourceRef{{Type: "charge", ExternalID: chargeID, Mode: "WRITE"}}})
if err == nil && (d.Decision == "ALLOW" || d.Decision == "WARN") { /* do the refund */ }`;
  }
  const bang = lang === 'typescript' ? '!' : '';
  return `import { LumniClient, shouldExecute } from '@lumniverse/sdk';
const lumni = new LumniClient(process.env.LUMNI_URL${bang}, process.env.LUMNI_API_KEY${bang});

const d = await lumni.checkAction({ actor_id: '${actor}', work_id: runId, operation: 'stripe.refund',
  arguments: { charge: chargeId, amount: 60 }, resources: [{ type: 'charge', external_id: chargeId, mode: 'WRITE' }] });
if (shouldExecute(d)) await stripe.refunds.create({ charge: chargeId });`;
}

// ─── talking to Lumni ───────────────────────────────

export function api(url, key, fetchFn = fetch) {
  return async (method, path, body) => {
    let res;
    try {
      res = await fetchFn(`${url}${path}`, {
        method,
        headers: { authorization: `Bearer ${key}`, accept: 'application/json', ...(body ? { 'content-type': 'application/json' } : {}) },
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch {
      throw new Error(`Lumni can’t be reached at ${url}. Is it running, and is that the right URL?`);
    }
    const text = await res.text();
    let data;
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      data = {};
    }
    if (!res.ok) {
      const msg = data?.error?.message ?? `${method} ${path} failed (${res.status})`;
      const err = new Error(res.status === 401 ? 'That key isn’t one Lumni knows. Make a new one on the console’s Start page.' : msg);
      err.status = res.status;
      throw err;
    }
    return data;
  };
}

/** Trade an onboarding code for the agent's key, at the console that made it. */
/**
 * Lumni's API makes long codes (LUM- and 26 letters) and trades them itself;
 * a console without that API makes short ones (LUM-XXXX-XXXX) and trades them.
 */
export const madeByApi = (code) => String(code).toUpperCase().replace(/[^A-Z0-9]/g, '').replace(/^LUM/, '').length > 8;

export async function exchange(consoleUrl, code, project, fetchFn = fetch, apiUrl = '') {
  const byApi = madeByApi(code);
  const where = byApi ? `${apiUrl}/v1/onboarding/exchange` : `${consoleUrl}/console/onboarding/exchange`;
  let res;
  try {
    res = await fetchFn(where, {
      method: 'POST', headers: { 'content-type': 'application/json', accept: 'application/json' },
      body: JSON.stringify({ code, host: hostname(), project }),
    });
  } catch {
    throw new Error(byApi ? `Lumni can’t be reached at ${apiUrl}.` : `The console can’t be reached at ${consoleUrl}.`);
  }
  const data = await res.json().catch(() => ({}));
  if (res.status === 404) throw new Error('That code isn’t valid. Codes work once and last 15 minutes; make a new one on the console’s Start page.');
  if (!res.ok) throw new Error(data?.error?.message ?? `The code couldn’t be traded (${res.status}).`);
  // The API says tenant_name; the console says workspace.
  return { ...data, workspace: data.workspace ?? data.tenant_name };
}

/** Register the agent and send one harmless check, so the console sees it. */
export async function hello(call, actor, runtime) {
  await call('POST', '/v1/actors', { id: actor, type: 'AGENT', display_name: actor, runtime, metadata: { source: 'lumni init' } });
  const run = `${actor}-hello-${randomUUID().slice(0, 8)}`;
  await call('POST', '/v1/work', { id: run, actor_id: actor, goal: 'Say hello to Lumni', risk: 'LOW' });
  const d = await call('POST', '/v1/actions/check', {
    action_id: run, work_id: run, actor_id: actor, operation: 'lumni.hello', arguments: { from: 'lumni init' },
    resources: [{ type: 'lumni_hello', external_id: run, mode: 'WRITE' }], risk: 'LOW',
  });
  // Finish it when Lumni handed out a lease; in watch-only mode there may be none.
  if (d.lease_token) {
    try {
      await call('POST', `/v1/actions/${d.action_id}/start`, { lease_token: d.lease_token });
      await call('POST', `/v1/actions/${d.action_id}/result`, { status: 'SUCCEEDED', lease_token: d.lease_token, output: { hello: true } });
    } catch {}
  }
  return d;
}

// ─── the command ────────────────────────────────────

function args(argv) {
  const o = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith('--')) continue;
    const [k, v] = a.slice(2).split('=');
    if (v !== undefined) o[k] = v;
    else if (argv[i + 1] && !argv[i + 1].startsWith('--')) o[k] = argv[++i];
    else o[k] = true;
  }
  return o;
}

const tty = process.stdout.isTTY;
const c = (code) => (s) => (tty ? `\x1b[${code}m${s}\x1b[0m` : s);
const violet = c('38;5;99'), dim = c('2'), bold = c('1'), red = c('31'), green = c('32');
/** Where the command writes; tests swap it. */
let say = (s) => console.log(s);
const ok = (s) => say(`  ${violet('✓')} ${s}`);
const note = (s) => say(`    ${dim(s)}`);

function which(cmd) {
  try {
    return execFileSync(process.platform === 'win32' ? 'where' : 'which', [cmd], { encoding: 'utf8' }).split('\n')[0].trim() || null;
  } catch {
    return null;
  }
}

/** Open a page in the person's browser; quietly does nothing where it can't. */
function openBrowser(url) {
  try {
    const [cmd, args] = process.platform === 'darwin' ? ['open', [url]] : process.platform === 'win32' ? ['cmd', ['/c', 'start', '', url]] : ['xdg-open', [url]];
    spawn(cmd, args, { stdio: 'ignore', detached: true }).on('error', () => {}).unref();
  } catch {}
}

/**
 * Log in through the browser (an RFC 8628 device grant at the console): show a
 * short code, open the console's /device page, and wait for the person to
 * press Connect. Resolves with a one-time onboarding code, to trade as usual.
 */
export async function deviceLogin(consoleUrl, project, { fetchFn = fetch, sleep = (ms) => new Promise((r) => setTimeout(r, ms)), open = openBrowser, browser = true, client = 'lumni init', ask = 'Connect this folder in your browser' } = {}) {
  const post = async (path, body) => {
    let res;
    try {
      res = await fetchFn(`${consoleUrl}${path}`, { method: 'POST', headers: { 'content-type': 'application/json', accept: 'application/json' }, body: JSON.stringify(body) });
    } catch {
      throw new Error(`The console can’t be reached at ${consoleUrl}.`);
    }
    return { ok: res.ok, status: res.status, data: await res.json().catch(() => ({})) };
  };
  const start = await post('/console/device', { host: hostname(), project, client });
  if (!start.ok) throw new Error(start.status === 404 ? 'This console doesn’t offer browser login yet. Make a code on its Start page and run the command with --code.' : start.data?.error?.message ?? `The login couldn’t start (${start.status}).`);
  const s = start.data;
  say(`  ${ask}. Check the code there matches:\n`);
  say(`      ${bold(violet(s.user_code))}\n`);
  say(`    ${dim(browser ? `Opening ${s.verification_uri_complete}` : `Open ${s.verification_uri_complete}`)}`);
  say(`    ${dim('(Any browser where you’re signed in to the console works, even on another machine.)')}\n`);
  if (browser) open(s.verification_uri_complete);
  let wait = Math.max(1, s.interval ?? 3) * 1000;
  const until = Date.now() + (s.expires_in ?? 600) * 1000;
  while (Date.now() < until) {
    await sleep(wait);
    const r = await post('/console/device/token', { device_code: s.device_code });
    if (r.ok) return r.data;
    const why = r.data?.error?.code;
    if (why === 'authorization_pending') continue;
    if (why === 'slow_down') { wait += 2000; continue; }
    if (why === 'access_denied') throw new Error('The login was cancelled in the browser. Nothing was changed.');
    throw new Error(why === 'expired_token' ? 'The login expired. Run the command again.' : r.data?.error?.message ?? `The login failed (${r.status}).`);
  }
  throw new Error('The login expired. Run the command again.');
}

/** LUMNI_URL and LUMNI_API_KEY from a .env file, if it has them. */
function envFile(dir) {
  const out = {};
  try {
    for (const line of readFileSync(join(dir, '.env'), 'utf8').split('\n')) {
      const m = /^\s*(LUMNI_URL|LUMNI_API_KEY)\s*=\s*(.*?)\s*$/.exec(line);
      if (m) out[m[1]] = m[2].replace(/^['"]|['"]$/g, '');
    }
  } catch {}
  return out;
}

export async function main(argv = process.argv.slice(2), env = process.env, { fetchFn = fetch, home = homedir(), log = console.log, sleep, open } = {}) {
  say = log;
  const o = args(argv);
  if (o.help) {
    say('lumni init [--code LUM-… | --key KEY | --login] [--console URL] [--url URL] [--dir DIR] [--actor NAME] [--gateway PATH] [--no-mcp] [--no-hello] [--no-browser] [--dry-run]');
    say('With no code and no key, it logs in through your browser.');
    return 0;
  }
  let url = String(o.url ?? env.LUMNI_URL ?? (SERVED_URL.startsWith('__') ? 'http://localhost:8080' : SERVED_URL)).replace(/\/+$/, '');
  let code = String(o.code ?? env.LUMNI_CODE ?? '');
  const consoleUrl = String(o.console ?? env.LUMNI_CONSOLE_URL ?? (SERVED_CONSOLE.startsWith('__') ? '' : SERVED_CONSOLE)).replace(/\/+$/, '');
  const dir = resolve(String(o.dir ?? '.'));
  // Already connected? A key in this folder's .env is used, unless asked to log in again.
  const saved = o.login || code ? {} : envFile(dir);
  let key = String(o.key ?? env.LUMNI_API_KEY ?? saved.LUMNI_API_KEY ?? '');
  if (saved.LUMNI_API_KEY && !o.key && !env.LUMNI_API_KEY && saved.LUMNI_URL && !o.url) url = saved.LUMNI_URL.replace(/\/+$/, '');
  let actor = typeof o.actor === 'string' && o.actor.trim() ? o.actor.trim() : null;
  const dry = !!o['dry-run'];

  say(`\n  ${bold('lum')}${violet('n')}${bold('i')} ${dim('init')}${dry ? dim('  (dry run: nothing will change)') : ''}\n`);

  if (saved.LUMNI_API_KEY && key === saved.LUMNI_API_KEY) ok(`Already connected: using the key in .env ${dim('(--login to connect again)')}`);

  // No code and no key: log in through the browser, which ends in a one-time code.
  if (!code && !key && consoleUrl) {
    if (dry) ok('Would ask you to connect this folder in your browser');
    else {
      const got = await deviceLogin(consoleUrl, basename(dir), { fetchFn, sleep, open, browser: !o['no-browser'] });
      code = got.code;
      actor ||= got.actor_hint || null;
      // The console says where its API is, unless this run was told already.
      if (got.api_url && !o.url && !env.LUMNI_URL) url = String(got.api_url).replace(/\/+$/, '');
      ok('Connected in the browser');
    }
  }

  // A code (from the Start page, or the login) stands for a key: trade it, once.
  if (code && !key) {
    if (!consoleUrl && !madeByApi(code)) {
      say(`  ${red('No console.')} Run the command exactly as the Start page shows it, or add --console URL.\n`);
      return 1;
    }
    if (dry) ok(`Would trade code ${bold(code)} for a key at ${madeByApi(code) ? url : consoleUrl}`);
    else {
      const got = await exchange(consoleUrl, code, basename(dir), fetchFn, url);
      key = got.api_key;
      if (got.api_url) url = String(got.api_url).replace(/\/+$/, '');
      // The API sends an empty hint when the code was made without one.
      actor ||= got.actor_hint || null;
      ok(`Traded your code for a key${got.workspace ? ` to ${bold(got.workspace)}` : ''}${got.key_prefix ? ` ${dim(`(${got.key_prefix}…)`)}` : ''}`);
    }
  }
  actor ||= basename(dir).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-|-$/g, '') || 'my-agent';
  if (!key && !(code && dry) && !(dry && consoleUrl)) {
    say(`  ${red('No key.')} Run the command your console’s Start page shows, or pass --code or --key.\n`);
    return 1;
  }

  const call = api(url, key || 'dry-run', fetchFn);
  if (!dry) {
    await call('GET', '/v1/actors');
    ok(`Your key works with ${bold(url)}`);
  } else ok(`Would check the key with ${url}`);

  const found = detect(dir, home);
  const lang = found.languages[0] ?? null;
  if (lang) ok(`Found a ${bold(lang)} project${found.libraries.length ? ` using ${found.libraries.join(', ')}` : ''}`);
  else ok('No package.json, pyproject.toml or go.mod here, so no code changes');

  // .env
  const envPath = join(dir, '.env');
  const planned = planEnv(read(envPath), url, key);
  if (planned.added.length) {
    if (!dry) writeFileSync(envPath, planned.text, { mode: 0o600 });
    ok(`${dry ? 'Would add' : 'Added'} ${planned.added.join(' and ')} to .env`);
  } else ok('.env already has LUMNI_URL and LUMNI_API_KEY; left as it is');
  const gi = planGitignore(read(join(dir, '.gitignore')));
  if (gi !== null && existsSync(join(dir, '.git'))) {
    if (!dry) writeFileSync(join(dir, '.gitignore'), gi);
    ok(`${dry ? 'Would add' : 'Added'} .env to .gitignore`);
  }

  // MCP
  if (!o['no-mcp'] && found.mcp.length) {
    const gateway = typeof o.gateway === 'string' ? o.gateway : which('gateway') ?? which('lumni-gateway');
    for (const m of found.mcp) {
      const stdio = m.servers.filter((s) => s.stdio && !s.wrapped);
      if (!stdio.length) continue;
      if (!gateway) {
        ok(`Found ${stdio.length} MCP ${stdio.length === 1 ? 'server' : 'servers'} in ${m.path}`);
        note('Lumni’s gateway isn’t on your PATH, so they weren’t wrapped. Install it, then run this again with --gateway PATH.');
        continue;
      }
      const plan = planMcp(read(m.path), m.key, gateway, { url, key, actor });
      if (!dry) {
        const backup = `${m.path}.lumni-backup`;
        if (!existsSync(backup)) copyFileSync(m.path, backup);
        writeFileSync(m.path, plan.text, { mode: 0o600 });
      }
      ok(`${dry ? 'Would wrap' : 'Wrapped'} ${plan.wrapped.join(', ')} in ${m.path}`);
      if (!dry) note(`The original is in ${m.path}.lumni-backup. Restart your MCP client to pick it up.`);
      for (const s of plan.skipped) note(`${s.name}: ${s.why}`);
    }
  }

  // Hello
  if (!o['no-hello'] && !dry) {
    try {
      const d = await hello(call, actor, lang === 'python' ? 'python' : lang === 'go' ? 'go' : 'node');
      ok(`Registered ${bold(actor)} and sent a hello check: ${d.decision}`);
    } catch (e) {
      say(`  ${red('!')} The hello check didn’t go through: ${e.message}`);
      note('Everything else is set up. Your first real check will show up on the console instead.');
    }
  }

  say(`\n  ${green('Done.')} Add this where your agent acts, then watch it on the console’s Start page:\n`);
  for (const line of snippet(lang, actor).split('\n')) say(`    ${line}`);
  if (lang === 'python') note('\n    pip install lumni-sdk');
  if (lang === 'typescript' || lang === 'javascript') note('\n    npm install @lumniverse/sdk');
  say('');
  return 0;
}

// Run when executed, not when imported by tests.
const asScript = import.meta.url === `file://${process.argv[1]}` || process.argv[1] === '-' || !process.argv[1];
if (asScript) {
  main().then((code) => process.exit(code), (e) => {
    console.error(`\n  ${red('Stopped:')} ${e.message}\n`);
    process.exit(1);
  });
}
