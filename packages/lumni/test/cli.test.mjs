// npx lumniverse: the demo server, init's default console, try, and help.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { demoHandler } from '../bin/demo.mjs';

function fakeAssets() {
  const a = mkdtempSync(join(tmpdir(), 'lumni-assets-'));
  mkdirSync(join(a, 'console', 'lib'), { recursive: true });
  writeFileSync(join(a, 'console', 'today.html'), '<html>today</html>');
  writeFileSync(join(a, 'console', 'lib', 'shell.js'), 'window.shell = 1;');
  // An onboarding command that says what it was given.
  writeFileSync(join(a, 'lumni-init.mjs'), 'export async function main(argv) { globalThis.__initArgs = argv; return 0; }');
  return a;
}

/** The demo's handler, called with an in-memory request (no port needed). */
async function get(handler, url) {
  return new Promise((ok) => {
    const res = { status: 0, headers: {}, body: '', writeHead(code, h = {}) { this.status = code; this.headers = h; return this; }, end(b = '') { this.body = String(b); ok(this); } };
    handler({ url }, res);
  });
}

test('demo: pages at clean paths, a demo config, and no API behind it', async () => {
  const a = fakeAssets();
  const h = demoHandler(join(a, 'console'));
  const today = await get(h, '/today?data=mock');
  assert.equal(today.status, 200);
  assert.match(today.headers['content-type'], /text\/html/);
  assert.equal(today.body, '<html>today</html>');
  assert.equal((await get(h, '/lib/shell.js?v=abc')).body, 'window.shell = 1;');
  const cfg = JSON.parse((await get(h, '/console/config.json')).body);
  assert.equal(cfg.mode, 'mock');
  assert.equal(cfg.realAvailable, false);
  assert.equal((await get(h, '/v1/actions')).status, 503);
  assert.equal((await get(h, '/nope')).status, 404);
  assert.notEqual((await get(h, '/../../etc/passwd')).status, 200);
  assert.notEqual((await get(h, '/..%2f..%2fetc%2fpasswd')).status, 200);
  assert.equal((await get(h, '/')).headers.location, '/today');
});

test('init: Lumni Cloud’s console unless another is given', async () => {
  process.env.LUMNI_ASSETS = fakeAssets();
  delete process.env.LUMNI_CONSOLE_URL;
  const { main, CLOUD_CONSOLE } = await import(`../bin/lumni.mjs?init=${Date.now()}`);
  assert.equal(CLOUD_CONSOLE, 'https://app.lumniverse.com');
  await main(['init', '--dry-run']);
  assert.deepEqual(globalThis.__initArgs, ['--console', 'https://app.lumniverse.com', '--dry-run']);
  await main(['init', '--console', 'https://app.acme.dev']);
  assert.deepEqual(globalThis.__initArgs, ['--console', 'https://app.acme.dev']);
});

test('no command: help, not an installer', async () => {
  const { main } = await import(`../bin/lumni.mjs?bare=${Date.now()}`);
  const logs = [];
  const log = console.log;
  console.log = (s) => logs.push(s);
  const err = console.error;
  console.error = () => {};
  try {
    assert.equal(await main([]), 0);
    assert.equal(await main(['--domain', 'acme.com']), 1);
  } finally {
    console.log = log;
    console.error = err;
  }
  assert.match(logs[0], /npx lumniverse init/);
  assert.match(logs[0], /self-hosting/);
});

test('try: approve in the browser, run the sample agents with the run’s key, then revoke it, even if the run fails', async () => {
  const { runTry } = await import('../bin/try.mjs');
  for (const crash of [false, true]) {
    const a = fakeAssets();
    writeFileSync(join(a, 'lumni-init.mjs'), `
      export async function deviceLogin(consoleUrl, project, o) { globalThis.__login = { consoleUrl, project, client: o.client }; return { code: 'LUM-ABC', api_url: 'https://api.acme.dev/' }; }
      export async function exchange(consoleUrl, code, project, fetchFn, api) { globalThis.__exchange = { code, api }; return { api_key: 'lm_run', key_id: 'key 1', workspace: 'Acme' }; }`);
    mkdirSync(join(a, 'fleet'));
    writeFileSync(join(a, 'fleet', 'run.mjs'), `export async function main(argv, env) { globalThis.__fleet = { argv, env }; if (${crash}) throw new Error('boom'); return 0; }`);
    const calls = [];
    const fetchFn = async (url, init) => { calls.push(`${init.method} ${url} ${init.headers.authorization}`); return new Response(null, { status: 200 }); };
    const run = runTry(a, 'https://app.acme.dev', ['--scenario', 'stale-reasoning'], { fetchFn, log: () => {} });
    if (crash) await assert.rejects(run, /boom/);
    else assert.equal(await run, 0);
    assert.deepEqual(globalThis.__login, { consoleUrl: 'https://app.acme.dev', project: 'lumni-try', client: 'lumni try' });
    assert.deepEqual(globalThis.__exchange, { code: 'LUM-ABC', api: 'https://api.acme.dev' });
    assert.deepEqual(globalThis.__fleet.argv, ['--admin-key', 'lm_run', '--scenario', 'stale-reasoning']);
    assert.deepEqual(globalThis.__fleet.env, { LUMNI_URL: 'https://api.acme.dev', LUMNI_API_KEY: 'lm_run' });
    assert.deepEqual(calls, ['DELETE https://api.acme.dev/v1/api-keys/key%201 Bearer lm_run']);
  }
});

test('help and version', async () => {
  const { main } = await import(`../bin/lumni.mjs?help=${Date.now()}`);
  const logs = [];
  const log = console.log;
  console.log = (s) => logs.push(s);
  try {
    assert.equal(await main(['--version']), 0);
    assert.equal(await main(['help']), 0);
  } finally {
    console.log = log;
  }
  assert.match(logs[0], /^\d+\.\d+\.\d+$/);
  assert.match(logs[1], /npx lumniverse init/);
});
