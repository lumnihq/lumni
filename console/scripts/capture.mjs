#!/usr/bin/env bun
// Screenshots of every screen, demo and real, with headless Chrome over a CDP
// pipe. No ports: Chrome asks for http://console.test/..., and every request
// is answered in this process by the console's own code (server/app.ts and
// the BFF in server/), with the fake Lumni API (test/real) behind real mode.
// So it runs anywhere, sandboxes included, and the real screens are the real
// code path end to end.
//
//   bun scripts/capture.mjs                 2x PNGs of every shot, in shots/
//   bun scripts/capture.mjs flow today      only shots whose name starts with these
//   bun scripts/capture.mjs --review        1x PNGs in $TMPDIR
//   bun scripts/capture.mjs --shot name=/path?query[@ms] ...   your own shots

import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { appHandler } from '../server/app.ts';
import { readEnv } from '../server/env.ts';
import { createBff } from '../server/index.ts';
import { fakeFetch } from '../test/real/fake-go.ts';
import { richDay } from '../test/real/rich.ts';

const here = dirname(fileURLToPath(import.meta.url));
const ORIGIN = 'http://console.test';
const W = 1440;
const H = 900;

const SCREENS = ['start', 'flow', 'today', 'day-map', 'journey', 'runtime', 'context', 'tower', 'needs-you', 'fixes', 'autonomy', 'money', 'incident', 'without', 'wrapped', 'share', 'settings'];
const argv = process.argv.slice(2);
const review = argv.includes('--review');
const custom = argv.filter((a, i) => argv[i - 1] === '--shot').map((s) => {
  const eq = s.indexOf('=');
  const name = s.slice(0, eq);
  const at = s.lastIndexOf('@');
  const path = at > eq ? s.slice(eq + 1, at) : s.slice(eq + 1);
  const ms = at > eq ? s.slice(at + 1) : undefined;
  return [name, path, Number(ms ?? 3500)];
});
const only = argv.filter((a, i) => !a.startsWith('--') && argv[i - 1] !== '--shot');
const ALL = [
  ...SCREENS.flatMap((p) => [
    [`${p}-demo`, `/${p}?data=mock&volume=large`, 3500],
    [`${p}-real`, `/${p}?data=real`, 3500],
  ]),
  ['sign-in', '/sign-in', 800],
];
const shots = custom.length ? custom : ALL.filter(([n]) => !only.length || only.some((o) => n.startsWith(o)));

// ─── the console, in process ───────────────────────

const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
// FAKE_API_IDLE_MIN: the day's last action was this many minutes ago (a quiet workspace).
const fake = richDay(Number(process.env.FAKE_API_ACTIONS ?? 900), { key: 'capture_key', perSnapshot: 400 }, localZone, Date.now() - Number(process.env.FAKE_API_IDLE_MIN ?? 0) * 60_000);
// CAPTURE_AUTH=accounts: people sign in as themselves. CAPTURE_SEED=1 also
// makes an owner (signed in for the shots) and a few people and invites.
const accountsMode = process.env.CAPTURE_AUTH === 'accounts';
const env = readEnv({
  LUMNI_CONSOLE_DATA_MODE: 'mock', LUMNI_API_URL: 'http://fake.test', LUMNI_CONSOLE_ADMIN_KEY: 'capture_key', LUMNI_CONSOLE_WORKSPACE_NAME: 'Fake workspace', LUMNI_CONSOLE_HOSTS: 'console.test', PORT: '80',
  ...(accountsMode ? { LUMNI_CONSOLE_AUTH: 'accounts', LUMNI_CONSOLE_SECRET: 'capture-secret-0123456789', LUMNI_CONSOLE_DB: ':memory:', LUMNI_CONSOLE_GITHUB_CLIENT_ID: 'gh', LUMNI_CONSOLE_GITHUB_CLIENT_SECRET: 'gh' } : {}),
});
const bff = createBff(env, fakeFetch(fake));
const seededCookies = [];
if (accountsMode && process.env.CAPTURE_SEED) {
  const call = async (path, method, body, cookie = '', csrf = '') => {
    const res = await bff.handle(new Request(`${ORIGIN}${path}`, { method, headers: { host: 'console.test', 'content-type': 'application/json', ...(cookie ? { cookie } : {}), ...(csrf ? { 'x-csrf-token': csrf } : {}) }, body: body ? JSON.stringify(body) : undefined }));
    return { res, data: await res.clone().json().catch(() => null) };
  };
  const cookieOf = (res) => res.headers.getSetCookie().map((l) => l.split(';')[0]).filter((kv) => !kv.endsWith('=')).join('; ');
  const setup = await call('/console/auth/setup', 'POST', { name: 'Anurag Vishwa', email: 'anurag@lumniverse.com', password: 'capture password', workspace: 'Fake workspace' });
  let jar = cookieOf(setup.res);
  const me = await call('/v1/auth/session', 'GET', null, jar);
  jar = [jar, cookieOf(me.res)].filter(Boolean).join('; ');
  const csrf = me.data.csrf_token;
  for (const [name, email, role] of [['Jess Park', 'jess@lumniverse.com', 'admin'], ['Omar Haddad', 'omar@lumniverse.com', 'member'], ['Lena Fischer', 'lena@lumniverse.com', 'viewer']]) {
    const inv = await call('/v1/console/members/invite', 'POST', { email, role }, jar, csrf);
    const token = new URL(inv.data.link).searchParams.get('invite');
    await call('/console/auth/signup', 'POST', { name, email, password: 'capture password', invite: token });
  }
  await call('/v1/console/members/invite', 'POST', { email: 'sam@lumniverse.com', role: 'member' }, jar, csrf);
  for (const kv of jar.split('; ')) { const i = kv.indexOf('='); seededCookies.push({ name: kv.slice(0, i), value: kv.slice(i + 1) }); }
}
const app = appHandler(join(here, '..', 'app'), 'no-cache', { publicApi: 'http://lumni.example:8080' });

async function answer(req) {
  const api = await bff.handle(req);
  if (api) return api;
  const u = new URL(req.url);
  const a = await app(u.pathname, u.origin);
  return a ?? new Response('Not found', { status: 404 });
}

function findChrome() {
  if (process.env.CHROME_PATH) return process.env.CHROME_PATH;
  const rel = 'chrome-headless-shell-mac-arm64/chrome-headless-shell';
  const roots = [
    join(homedir(), '.cache/puppeteer/chrome-headless-shell'),
    join(homedir(), 'Library/Caches/ms-playwright'),
  ];
  for (const root of roots) {
    if (!existsSync(root)) continue;
    const found = readdirSync(root)
      .filter((d) => existsSync(join(root, d, rel)))
      .sort((a, b) => b.localeCompare(a, undefined, { numeric: true }));
    if (found.length) return join(root, found[0], rel);
  }
  throw new Error('No chrome-headless-shell found. Set CHROME_PATH.');
}

// Chrome DevTools Protocol over --remote-debugging-pipe: NUL-delimited JSON.
class Cdp {
  #id = 0;
  #pending = new Map();
  #listeners = new Map();
  #chunks = [];

  constructor(writable, readable) {
    this.writable = writable;
    readable.on('data', (chunk) => this.#onData(chunk));
  }

  #onData(chunk) {
    let start = 0;
    let nul;
    while ((nul = chunk.indexOf(0, start)) !== -1) {
      this.#chunks.push(chunk.subarray(start, nul));
      const msg = JSON.parse(Buffer.concat(this.#chunks).toString('utf8'));
      this.#chunks = [];
      this.#dispatch(msg);
      start = nul + 1;
    }
    if (start < chunk.length) this.#chunks.push(chunk.subarray(start));
  }

  #dispatch(msg) {
    if (msg.id === undefined) {
      for (const fn of this.#listeners.get(msg.method) ?? []) fn(msg.params, msg.sessionId);
      return;
    }
    const call = this.#pending.get(msg.id);
    this.#pending.delete(msg.id);
    if (msg.error) call.reject(new Error(`${call.method}: ${msg.error.message}`));
    else call.resolve(msg.result);
  }

  send(method, params = {}, sessionId) {
    const id = ++this.#id;
    const msg = { id, method, params };
    if (sessionId) msg.sessionId = sessionId;
    this.writable.write(JSON.stringify(msg) + '\0');
    return new Promise((resolve, reject) => this.#pending.set(id, { resolve, reject, method }));
  }

  on(method, fn) {
    const list = this.#listeners.get(method) ?? [];
    list.push(fn);
    this.#listeners.set(method, list);
  }

  once(method) {
    return new Promise((resolve) => {
      const fn = (params) => {
        const list = this.#listeners.get(method);
        list.splice(list.indexOf(fn), 1);
        resolve(params);
      };
      this.on(method, fn);
    });
  }
}


async function openBrowser({ scale }) {
  const proc = spawn(
    findChrome(),
    [
      '--remote-debugging-pipe',
      `--user-data-dir=${join(tmpdir(), 'lumni-console-chrome')}`,
      '--use-mock-keychain', '--hide-scrollbars', '--mute-audio', '--disable-crash-reporter',
      '--force-color-profile=srgb',
      // The macOS sandbox denies Mach port rendezvous, so keep one process.
      '--single-process', '--no-zygote', '--in-process-gpu', '--disable-gpu',
      '--enable-features=NetworkServiceInProcess2',
      'about:blank',
    ],
    { stdio: ['ignore', 'ignore', 'pipe', 'pipe', 'pipe'], env: { ...process.env, MAC_CHROMIUM_TMPDIR: tmpdir() } },
  );
  let stderr = '';
  let closing = false;
  proc.stderr.on('data', (d) => (stderr += d));
  proc.on('exit', (code) => {
    if (closing) return;
    console.error(stderr.split('\n').slice(-20).join('\n'));
    console.error(`chrome exited early (code ${code})`);
    process.exit(1);
  });

  const cdp = new Cdp(proc.stdio[3], proc.stdio[4]);
  const { targetInfos } = await cdp.send('Target.getTargets');
  const target = targetInfos.find((t) => t.type === 'page');
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId: target.targetId, flatten: true });
  const send = (method, params) => cdp.send(method, params, sessionId);

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Network.enable');
  await send('Network.clearBrowserCookies');
  for (const c of seededCookies) await send('Network.setCookie', { name: c.name, value: c.value, url: ORIGIN, httpOnly: true, sameSite: 'Strict' });
  cdp.on('Runtime.exceptionThrown', (e) => console.error('page error:', e.exceptionDetails?.exception?.description ?? e));
  cdp.on('Runtime.consoleAPICalled', (e) => {
    if (e.type === 'error') console.error('console error:', e.args.map((a) => a.value ?? a.description).join(' '));
  });
  await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: scale, mobile: false });
  // Headless Chrome reports reduced motion; shots should show the real animations.
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });

  // Every request goes through this process.
  await send('Fetch.enable', { patterns: [{ urlPattern: `${ORIGIN}/*`, requestStage: 'Request' }] });
  cdp.on('Fetch.requestPaused', async (e) => {
    const r = e.request;
    const headers = new Headers(r.headers);
    headers.set('host', 'console.test');
    try {
      const res = await answer(new Request(r.url, { method: r.method, headers, body: r.method === 'GET' || r.method === 'HEAD' ? undefined : r.postData ?? '' }));
      const body = Buffer.from(await res.arrayBuffer()).toString('base64');
      const responseHeaders = [];
      res.headers.forEach((v, k) => { if (k !== 'set-cookie') responseHeaders.push({ name: k, value: v }); });
      for (const c of res.headers.getSetCookie?.() ?? []) responseHeaders.push({ name: 'set-cookie', value: c });
      await send('Fetch.fulfillRequest', { requestId: e.requestId, responseCode: res.status, responseHeaders, body });
    } catch (err) {
      console.error('request failed', r.url, err);
      await send('Fetch.failRequest', { requestId: e.requestId, errorReason: 'Failed' }).catch(() => {});
    }
  });

  const evaluate = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };

  return {
    async shoot(path, settle) {
      await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: scale, mobile: false });
      const loaded = cdp.once('Page.loadEventFired');
      await send('Page.navigate', { url: `${ORIGIN}${path}` });
      await loaded;
      await evaluate('Promise.race([window.Shell ? window.Shell.ready : document.fonts.ready, new Promise((r) => setTimeout(r, 15000))])');
      await new Promise((r) => setTimeout(r, settle));
      const height = Math.max(H, await evaluate('document.documentElement.scrollHeight'));
      await send('Emulation.setDeviceMetricsOverride', { width: W, height, deviceScaleFactor: scale, mobile: false });
      await evaluate('new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))');
      const { data } = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: W, height, scale: 1 }, captureBeyondViewport: true });
      return Buffer.from(data, 'base64');
    },
    evaluate,
    close() {
      closing = true;
      proc.kill();
    },
  };
}

const outDir = review ? tmpdir() : join(here, '..', 'shots');
mkdirSync(outDir, { recursive: true });
const browser = await openBrowser({ scale: review ? 1 : 2 });
for (const [name, path, settle] of shots) {
  const png = await browser.shoot(path, settle);
  const file = join(outDir, `${review ? 'ff-' : ''}${name}.png`);
  writeFileSync(file, png);
  console.log(file);
}
browser.close();
process.exit(0);
