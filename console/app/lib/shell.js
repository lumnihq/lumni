// The console's shell: top bar, which data you're looking at, signing in, and
// the one object every screen reads.
//
// A screen calls
//   Shell.boot({ nav: 'Flow', onWorld(w, first) { ... } })
// and gets the World: the demo at a volume (app/lib/world-mock.js, built in the
// page), or your workspace (GET /v1/console/world, built by this console's
// server from the Lumni API). Both have the same shape, so a screen never
// branches on where it came from; it branches on what's in it.
//
// Which data: ?data=mock|real, then what this browser chose last, then what
// the server says. Real data needs the server to reach a Lumni API.

(() => {
const GROUPS = [
  ['Watch', [
    ['Start', '/start', 'Connect an agent with one command'],
    ['Flow', '/flow', 'The whole day as a living map'],
    ['Today', '/today', 'The day so far, and what’s happening now, live'],
    ['Day map', '/day-map', 'Who touched what, and when'],
    ['Journey', '/journey', 'One action, check by check'],
    ['Runtime', '/runtime', 'Loops, drift and what happens next'],
    ['Context', '/context', 'What each AI believes, and what’s true'],
  ]],
  ['Coordinate', [
    ['Tower', '/tower', 'Air traffic control for shared things'],
    ['Needs you', '/needs-you', 'Decisions, grouped by the rule that asked'],
    ['Fixes', '/fixes', 'Patterns Lumni found, and rules to fix them'],
    ['Autonomy', '/autonomy', 'How much each AI has earned'],
    ['Settings', '/settings', 'Keys, limits, alerts and history'],
  ]],
  ['Prove', [
    ['Money', '/money', 'Cost per proven outcome, waste and return'],
    ['Incident', '/incident', 'The worst minutes of the day, second by second'],
    ['Without Lumni', '/without', 'The same day, with nothing stepping in'],
    ['Wrapped', '/wrapped', 'Your fleet’s week, as a story'],
    ['Share', '/share', 'Send proof of one action'],
  ]],
];

const MODE_KEY = 'lumni.console.data';
const VOLUME_KEY = 'lumni.console.volume';
const VOLUMES = ['small', 'mid', 'large'];
const FALLBACK = { mode: 'mock', realAvailable: false, workspace: null, autoSignIn: false, adminKey: false };
/** Come back to a tab after this long and the real world is read again. */
const STALE_AFTER = 2 * 60_000;

const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch {} },
};
const params = new URLSearchParams(location.search);
const zone = (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'; } catch { return 'UTC'; } })();
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

// ─── talking to the server ──────────────────────────

class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let csrf = null;

async function call(method, path, body) {
  const headers = { accept: 'application/json', 'x-lumni-tz': zone };
  if (body !== undefined) headers['content-type'] = 'application/json';
  if (method !== 'GET' && csrf) headers['x-csrf-token'] = csrf;
  let res;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' });
  } catch {
    throw new ApiError(0, 'offline', 'Lumni can’t be reached. Check your connection and try again.');
  }
  const text = await res.text();
  let data;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    throw new ApiError(res.status || 502, 'bad_response', `The server sent something that isn’t JSON (${res.status}).`);
  }
  if (!res.ok) {
    const e = data?.error;
    if (res.status === 401 && path !== '/v1/auth/session') goSignIn();
    throw new ApiError(res.status, e?.code ?? 'unknown', e?.message ?? `The request failed (${res.status}).`);
  }
  if (path === '/v1/auth/session') csrf = data?.csrf_token ?? null;
  return data;
}

const api = {
  get: (p) => call('GET', p),
  post: (p, b = {}) => call('POST', p, b),
  put: (p, b = {}) => call('PUT', p, b),
  del: (p) => call('DELETE', p),
};

async function readConfig() {
  try {
    const r = await fetch('/console/config.json', { headers: { accept: 'application/json' } });
    if (r.ok) return { ...FALLBACK, ...(await r.json()) };
  } catch {}
  return FALLBACK;
}

function goSignIn() {
  if (location.pathname === '/sign-in') return;
  location.assign(`/sign-in?next=${encodeURIComponent(location.pathname + location.search)}`);
}

// ─── the world ──────────────────────────────────────

/** Real worlds arrive as JSON; give them the lookups the demo world has. */
function hydrate(w) {
  w.team = (id) => w.teams.find((t) => t.id === id) ?? { id, name: 'Everyone else', ai: '+', n: 0 };
  w.app = (id) => w.apps.find((a) => a.id === id) ?? { id, name: id ? id[0].toUpperCase() + id.slice(1) : 'An app', mark: '?', color: '#a3a7b3', n: 0 };
  w.LEVELS ??= ['Watch', 'Warn', 'Enforce', 'Autopilot'];
  w.loadedAt = Date.now();
  return w;
}

// ─── the shell ──────────────────────────────────────

let markReady;
const Shell = {
  /** Resolves once the first world has been handed to the screen. */
  ready: new Promise((r) => (markReady = r)),
  mode: 'mock',
  volume: 'mid',
  config: FALLBACK,
  session: null,
  world: null,
  api,
  ApiError,
  esc,
  /** Repaint the paused band from the world, e.g. after a page changed `world.paused`. */
  paintPaused: () => paintPaused(),
  paintSample: () => paintSample(),
  GROUPS,
  /** Which world: the demo's volume, or 'real'. */
  get key() { return this.mode === 'real' ? 'real' : this.volume; },
  get isReal() { return this.mode === 'real'; },

  async boot({ nav, onMode, onWorld }) {
    this.onWorld = onWorld;
    this.onMode = onMode;
    this.nav = nav;
    drawTop(nav);
    this.config = await readConfig();
    const asked = params.get('data');
    if (asked === 'mock' || asked === 'real') store.set(MODE_KEY, asked);
    let mode = store.get(MODE_KEY) ?? this.config.mode;
    if (mode === 'real' && !this.config.realAvailable) mode = 'mock';
    const v = params.get('volume') ?? store.get(VOLUME_KEY);
    this.volume = VOLUMES.includes(v) ? v : 'mid';
    if (VOLUMES.includes(params.get('volume'))) store.set(VOLUME_KEY, this.volume);
    this.mode = mode;
    document.body.dataset.mode = mode;
    await (document.fonts?.ready ?? Promise.resolve());
    paintSwitches();
    onMode?.(mode, true);

    if (mode === 'real') {
      busy(true, 'Signing in…');
      try {
        this.session = await api.get('/v1/auth/session');
      } catch (e) {
        return problem('Lumni can’t be reached', e.message, () => location.reload());
      }
      if (!this.session?.signed_in) return goSignIn();
      // Now we know who's signed in: their initials and menu.
      paintSwitches();
      paintSource();
    }
    await this.load(true);
    if (mode === 'real') listen();
    addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible' && this.isReal && this.world && Date.now() - this.world.loadedAt > STALE_AFTER) this.refresh();
    });
  },

  /** Read the world again and hand it to the screen. */
  async load(first = false) {
    if (this.mode === 'mock') {
      this.world = window.World(this.volume);
      busy(false);
      this.onWorld?.(this.world, first);
      markReady(this.world);
      return this.world;
    }
    busy(true, first ? 'Reading your workspace…' : 'Reading it again…');
    try {
      this.world = hydrate(await api.get('/v1/console/world'));
    } catch (e) {
      if (e.status === 401) return;
      return problem('Your workspace couldn’t be read', e.message, () => this.load(first));
    }
    busy(false);
    changes.fresh = 0;
    changes.agents = 0;
    paintSource();
    this.onWorld?.(this.world, first);
    markReady(this.world);
    return this.world;
  },

  refresh() {
    return this.load(false);
  },

  /** A screen that shows changes as they arrive (Today, live) has nothing for the bar to count. */
  seenChanges() {
    changes.fresh = 0;
    changes.agents = 0;
    paintFresh();
  },

  /** Hear about each change Lumni records (real mode): fn({ type, id }). Returns a way to stop. */
  onChange(fn) {
    changes.fns.add(fn);
    return () => changes.fns.delete(fn);
  },

  /** Switch worlds. Real data reloads the page, so nothing leaks between them. */
  setMode(m) {
    if (m === this.mode) return;
    if (m === 'real' && !this.config.realAvailable) {
      return this.toast('This console isn’t connected to a Lumni API. Set LUMNI_API_URL and an admin key, then restart it.');
    }
    store.set(MODE_KEY, m);
    const url = new URL(location.href);
    url.searchParams.delete('data');
    location.assign(url.pathname + url.search);
  },

  setVolume(v) {
    if (v === this.volume || this.mode !== 'mock') return;
    this.volume = v;
    store.set(VOLUME_KEY, v);
    paintSwitches();
    this.load(false);
  },

  async signOut() {
    try { await api.del('/v1/auth/session'); } catch {}
    store.set(MODE_KEY, 'mock');
    location.assign('/today');
  },

  /**
   * A write to your workspace. Resolves with the answer, or shows why it
   * couldn't and resolves with null, so a screen can simply skip its motion.
   */
  async write(method, path, body, { ok, reload = true } = {}) {
    this.lastError = null;
    try {
      const out = await call(method, path, body);
      if (ok) this.toast(ok);
      if (reload) this.refresh();
      return out ?? {};
    } catch (e) {
      // Kept so a screen can say why inline, not only in the toast.
      this.lastError = { status: e.status ?? 0, code: e.code ?? null, message: e.message };
      this.toast(e.status === 501 ? `${e.message} Lumni’s API can’t do this yet.` : e.message, 'bad');
      return null;
    }
  },

  toast(text, kind = '') {
    let t = document.getElementById('shell-toast');
    if (!t) {
      t = document.createElement('div');
      t.id = 'shell-toast';
      t.className = 'shell-toast';
      document.body.appendChild(t);
    }
    t.className = `shell-toast on ${kind}`;
    t.innerHTML = `<i>${kind === 'bad' ? '!' : '✓'}</i><span>${esc(text)}</span>`;
    clearTimeout(t._h);
    t._h = setTimeout(() => t.classList.remove('on'), 3600);
  },
};

function drawTop(nav) {
  const top = document.getElementById('top');
  top.className = 'top';
  top.innerHTML = `
    <a class="wm" href="/today" aria-label="Lumni">lum<i>n</i>i</a>
    <nav class="nav">${GROUPS.map(([g, items]) => {
      const cur = items.find(([n]) => n === nav);
      return `<div class="grp-nav ${cur ? 'on' : ''}"><button>${g}${cur ? `<span class="cur">${cur[0]}</span>` : ''}<svg width="9" height="6" viewBox="0 0 9 6"><path d="M1 1l3.5 3.5L8 1" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button>
        <div class="menu">${items.map(([n, h, d]) => `<a href="${h}" class="${n === nav ? 'on' : ''}"><b>${n}</b><small>${d}</small></a>`).join('')}</div></div>`;
    }).join('')}</nav>
    <div class="top-right">
      <div class="world volume mock-only" role="group" aria-label="How busy">
        <span class="thumb"></span>
        <span class="vlabel">Volume</span>
        <button data-v="small" aria-pressed="false">2.2k</button>
        <button data-v="mid" aria-pressed="false">48k</button>
        <button data-v="large" aria-pressed="false">2.1M</button>
      </div>
      <div class="world which" role="group" aria-label="Which data">
        <span class="thumb"></span>
        <button data-mode="mock" aria-pressed="false"><span class="dot"></span>Demo</button>
        <button data-mode="real" aria-pressed="false"><span class="dot"></span>Your workspace</button>
      </div>
      <div class="me-wrap"><button class="me" id="me" aria-label="Account">·</button><div class="me-menu" id="me-menu"></div></div>
    </div>`;
  const source = document.createElement('div');
  source.className = 'source';
  source.id = 'source';
  top.after(source);
  const bar = document.createElement('div');
  bar.className = 'shell-busy';
  bar.id = 'shell-busy';
  bar.innerHTML = '<i></i><span></span>';
  document.body.appendChild(bar);

  top.querySelector('.world.volume').addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (b) Shell.setVolume(b.dataset.v);
  });
  top.querySelector('.world.which').addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (b) Shell.setMode(b.dataset.mode);
  });
  top.querySelector('#me').addEventListener('click', () => top.querySelector('.me-wrap').classList.toggle('open'));
  addEventListener('click', (e) => {
    if (!e.target.closest('.me-wrap')) top.querySelector('.me-wrap')?.classList.remove('open');
  });
}

function paintSwitches() {
  const top = document.getElementById('top');
  const slide = (group, on) => {
    const thumb = group.querySelector('.thumb');
    for (const b of group.querySelectorAll('button')) {
      const pressed = on(b);
      b.setAttribute('aria-pressed', String(pressed));
      if (pressed) {
        thumb.style.left = b.offsetLeft + 'px';
        thumb.style.width = b.offsetWidth + 'px';
      }
    }
  };
  slide(top.querySelector('.world.which'), (b) => b.dataset.mode === Shell.mode);
  if (Shell.mode === 'mock') slide(top.querySelector('.world.volume'), (b) => b.dataset.v === Shell.volume);
  const real = top.querySelector('[data-mode="real"]');
  real.classList.toggle('off', !Shell.config.realAvailable);
  real.title = Shell.config.realAvailable ? 'Your workspace, from the Lumni API' : 'This console isn’t connected to a Lumni API';
  const me = top.querySelector('#me');
  const menu = top.querySelector('#me-menu');
  if (Shell.mode === 'real' && Shell.session?.signed_in) {
    const u = Shell.session.user;
    me.textContent = u?.initials ?? 'AK';
    // With accounts: who you are, your role, and your other workspaces to switch to.
    const others = (Shell.session.workspaces ?? []).filter((x) => !x.current);
    const role = u?.role ? ` · ${u.role[0].toUpperCase()}${u.role.slice(1)}` : '';
    menu.innerHTML = `<div class="me-h"><b>${esc(u?.name ?? Shell.session.tenant?.name ?? 'Your workspace')}</b><small>${esc(u?.email ?? 'Signed in')}</small>${u?.name ? `<small>${esc(Shell.session.tenant?.name ?? '')}${esc(role)}</small>` : ''}</div>
      ${others.map((x) => `<button data-ws="${esc(x.id)}">Switch to ${esc(x.name)}</button>`).join('')}
      <button data-act="settings">Settings</button><button data-act="refresh">Read it again</button><button data-act="out">Sign out</button>`;
  } else {
    me.textContent = 'AK';
    menu.innerHTML = `<div class="me-h"><b>Demo world</b><small>Acme, Northwind or Globex, made up</small></div>
      <button data-act="settings">Settings</button>
      ${Shell.config.realAvailable ? '<button data-act="real">See your workspace</button>' : '<div class="me-note">Connect a Lumni API to see your own workspace.</div>'}`;
  }
  menu.onclick = async (e) => {
    const ws = e.target.closest('button')?.dataset.ws;
    if (ws) {
      const out = await Shell.write('POST', '/v1/console/account/switch', { workspace_id: ws }, { reload: false });
      if (out) location.reload();
      return;
    }
    const a = e.target.closest('button')?.dataset.act;
    if (a === 'refresh') Shell.refresh();
    if (a === 'out') Shell.signOut();
    if (a === 'real') Shell.setMode('real');
    if (a === 'settings') location.assign('/settings');
  };
}

// ─── live changes ─────────────────────────────────────
//
// Lumni's change stream says when an agent is made or an action is checked.
// Screens aren't re-read under you (that would close what you have open):
// the source bar counts what's new and offers to read it again.

const changes = { fresh: 0, agents: 0, fns: new Set(), es: null, cursor: null, wait: 2000 };

/**
 * One stream at a time. It reconnects by itself, with the last event it saw
 * in the URL (`cursor`), since a proxy can drop the Last-Event-ID header and
 * the stream would then skip what happened while it was away.
 */
function listen() {
  if (!window.EventSource || changes.es) return;
  const at = changes.cursor ? `&cursor=${encodeURIComponent(changes.cursor)}` : '';
  const es = new EventSource(`/v1/graph/stream?types=action.checked,actor.created${at}`);
  changes.es = es;
  es.addEventListener('ready', (ev) => {
    changes.wait = 2000;
    if (ev.lastEventId) changes.cursor = ev.lastEventId;
  });
  es.addEventListener('reset', (ev) => {
    if (ev.lastEventId) changes.cursor = ev.lastEventId;
  });
  es.addEventListener('invalidate', (ev) => {
    if (ev.lastEventId) changes.cursor = ev.lastEventId;
    let d = {};
    try { d = JSON.parse(ev.data); } catch {}
    const type = d.event_type ?? 'change';
    for (const fn of changes.fns) { try { fn({ type, id: d.entity_id ?? null }); } catch {} }
    if (type === 'action.checked') changes.fresh++;
    if (type === 'actor.created') changes.agents++;
    paintFresh();
  });
  es.onerror = () => {
    es.close();
    changes.es = null;
    setTimeout(listen, changes.wait);
    // An API without the stream fails every time: try less and less often.
    changes.wait = Math.min(60_000, changes.wait * 1.5);
  };
}

function paintFresh() {
  const b = document.getElementById('src-refresh');
  if (!b) return;
  const n = (k, one, many) => (k ? `${k.toLocaleString('en-US')} new ${k === 1 ? one : many}` : '');
  const what = [n(changes.agents, 'agent', 'agents'), n(changes.fresh, 'check', 'checks')].filter(Boolean).join(', ');
  b.classList.toggle('fresh', !!what);
  b.textContent = what ? `${what}, read again` : 'Read it again';
}

/** A note when some of today's step-ins were only watched, not done. */
function watched(w) {
  const n = w?.would ? Object.values(w.would).reduce((a, b) => a + b, 0) : 0;
  return n ? ` ${n.toLocaleString('en-US')} ${n === 1 ? 'rule is' : 'times, rules were'} only watching, so ${n === 1 ? 'that one is' : 'those are'} what Lumni would have done.` : '';
}

function paintSource() {
  const el = document.getElementById('source');
  if (!el || Shell.mode !== 'real') return;
  const w = Shell.world;
  const name = Shell.session?.tenant?.name ?? Shell.config.workspace ?? 'your workspace';
  el.innerHTML = `<span class="live-dot"></span>
    <span>Live from <b>${esc(name)}</b>${w ? `, read at ${esc(w.timeLabel)}` : ''}.${w?.partial ? ' Today is very busy, so some of it was sampled.' : ''}${w?.notes?.length ? ' ' + esc(w.notes[0]) : ''}${watched(w)}</span>
    <button class="src-btn" id="src-refresh">Read it again</button>`;
  el.querySelector('#src-refresh').onclick = () => Shell.refresh();
  paintFresh();
  paintPaused();
  paintSample();
}

/** Real mode, in the sample workspace (`npx lumniverse try`): say so, and point to the real one. */
function paintSample() {
  const src = document.getElementById('source');
  if (!src) return;
  let band = document.getElementById('sample-band');
  const ses = Shell.session;
  if (Shell.mode !== 'real' || !ses?.tenant?.sample) {
    band?.remove();
    return;
  }
  if (!document.getElementById('sample-style')) {
    const st = document.createElement('style');
    st.id = 'sample-style';
    st.textContent = `.sample-band { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; padding: 10px 40px; background: var(--accent-soft); border-bottom: 1px solid var(--accent-line); font-size: 13.5px; animation: rise .4s var(--ease) both; }
      .sample-band b { color: var(--accent); font-weight: 700; }
      .sample-band span { color: var(--ink2); flex: 1 1 320px; min-width: 0; line-height: 1.6; }
      .sample-band code { font-family: var(--mono); font-size: 12.5px; background: var(--paper); border: 1px solid var(--accent-line); border-radius: 7px; padding: 3px 8px; color: var(--ink); cursor: copy; }
      .sample-band .sb-go { margin-left: auto; display: flex; align-items: center; gap: 8px; }
      .sample-band button { height: 28px; padding: 0 12px; border-radius: 8px; font-size: 12.5px; font-weight: 650; background: var(--accent); color: #fff; }
      .sample-band button.sb-quiet { background: transparent; color: var(--ink2); padding: 0 6px; }
      .sample-band button.sb-quiet.arm { color: var(--bad); }
      .sample-band button:disabled { opacity: .6; cursor: default; }`;
    document.head.appendChild(st);
  }
  if (!band) {
    band = document.createElement('div');
    band.id = 'sample-band';
    band.className = 'sample-band';
    band.setAttribute('role', 'status');
    src.after(band);
  }
  const real = (ses.workspaces ?? []).find((w) => !w.sample);
  band.innerHTML = `<b>Sample workspace</b><span>These agents come from <code title="Copy">npx lumniverse try</code>. Ready for your real agent? Run <code title="Copy">npx lumniverse init</code> in its folder${real ? `; it connects to <b>${esc(real.name)}</b>, which is empty and separate` : ''}.</span>
    <div class="sb-go">${real ? `<button id="sb-real">Go to ${esc(real.name)}</button>` : ''}<button class="sb-quiet" id="sb-remove">Remove sample workspace</button></div>`;
  band.querySelectorAll('code').forEach((c) => { c.onclick = async () => { try { await navigator.clipboard.writeText(c.textContent); } catch {} Shell.toast?.('Copied'); }; });
  band.querySelector('#sb-real')?.addEventListener('click', async () => {
    const out = await Shell.write('POST', '/v1/console/account/switch', { workspace_id: real.id }, { reload: false });
    if (out) location.assign('/start');
  });
  const rm = band.querySelector('#sb-remove');
  let armed = false;
  rm.onclick = async () => {
    if (!armed) { armed = true; rm.textContent = 'Confirm: remove it and its sample data'; rm.classList.add('arm'); return; }
    rm.disabled = true;
    const out = await Shell.write('DELETE', '/v1/console/account/sample', undefined, { reload: false });
    if (out) location.assign('/start');
    else rm.disabled = false;
  };
}

/** Real mode: a band under the source bar while every AI is paused, with a way back. */
function paintPaused() {
  const src = document.getElementById('source');
  if (!src) return;
  let band = document.getElementById('paused-band');
  if (Shell.mode !== 'real' || !Shell.world?.paused) {
    band?.remove();
    return;
  }
  if (!document.getElementById('paused-style')) {
    const st = document.createElement('style');
    st.id = 'paused-style';
    st.textContent = `.paused-band { display: flex; align-items: center; gap: 10px; height: 40px; padding: 0 40px; background: var(--ink); color: #fff; font-size: 13.5px; font-weight: 600; animation: rise .4s var(--ease) both; }
      .paused-band .pb-icon { flex: none; display: inline-flex; gap: 3px; }
      .paused-band .pb-icon i { width: 3px; height: 11px; border-radius: 2px; background: var(--pop); }
      .paused-band span { color: #d6d8e2; font-weight: 550; }
      .paused-band b { color: #fff; font-weight: 650; }
      .paused-band .pb-go { margin-left: auto; display: flex; align-items: center; gap: 8px; }
      .paused-band button { height: 26px; padding: 0 12px; border-radius: 8px; font-size: 12.5px; font-weight: 650; background: #fff; color: var(--ink); }
      .paused-band button.arm { background: var(--pop); }
      .paused-band button.pb-no { background: transparent; color: #b9bccb; padding: 0 6px; }
      .paused-band button:disabled { opacity: .6; cursor: default; }`;
    document.head.appendChild(st);
  }
  if (!band) {
    band = document.createElement('div');
    band.id = 'paused-band';
    band.className = 'paused-band';
    band.setAttribute('role', 'status');
    src.after(band);
  }
  band.innerHTML = `<span class="pb-icon" aria-hidden="true"><i></i><i></i></span><b>Every AI is paused.</b><span>New actions wait.</span>
    <div class="pb-go"><button class="pb-no" id="pb-no" hidden>Cancel</button><button id="pb-resume">Resume</button></div>`;
  const go = band.querySelector('#pb-resume');
  const no = band.querySelector('#pb-no');
  let armed = false, timer = 0;
  const disarm = () => { armed = false; clearTimeout(timer); go.textContent = 'Resume'; go.classList.remove('arm'); no.hidden = true; };
  no.onclick = disarm;
  go.onclick = async () => {
    if (!armed) {
      armed = true;
      go.textContent = 'Confirm, let every AI act';
      go.classList.add('arm');
      no.hidden = false;
      timer = setTimeout(disarm, 6000);
      return;
    }
    clearTimeout(timer);
    go.disabled = true;
    go.textContent = 'Resuming…';
    const out = await Shell.write('POST', '/v1/config/pause', { paused: false }, { ok: 'Every AI can act again.', reload: true });
    if (!out) { go.disabled = false; disarm(); }
  };
}

function busy(on, text = '') {
  const b = document.getElementById('shell-busy');
  if (!b) return;
  b.classList.toggle('on', on);
  b.querySelector('span').textContent = text;
  document.body.classList.toggle('shell-loading', on && !Shell.world);
}

function problem(title, text, retry) {
  busy(false);
  document.getElementById('shell-problem')?.remove();
  const d = document.createElement('div');
  d.id = 'shell-problem';
  d.className = 'shell-problem';
  d.innerHTML = `<div class="card"><b>${esc(title)}</b><p>${esc(text)}</p><div class="row"><button class="btn primary" id="sp-retry">Try again</button><button class="btn" id="sp-demo">Use the demo</button></div></div>`;
  document.body.appendChild(d);
  d.querySelector('#sp-retry').onclick = () => { d.remove(); retry(); };
  d.querySelector('#sp-demo').onclick = () => Shell.setMode('mock');
}

// Older screens call it Concept.
window.Shell = Shell;
window.Concept = Shell;

// Small helpers the screens share.
window.$ = (s, r = document) => r.querySelector(s);
window.$$ = (s, r = document) => [...r.querySelectorAll(s)];
window.sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Counts a number up into an element. */
window.countUp = (el, to, { ms = 1100, prefix = '', suffix = '', from = 0 } = {}) => {
  const t0 = performance.now();
  const fmt = (v) => prefix + Math.round(v).toLocaleString('en-US') + suffix;
  const step = (t) => {
    const p = Math.min(1, (t - t0) / ms);
    const e = 1 - Math.pow(1 - p, 4);
    el.textContent = fmt(from + (to - from) * e);
    if (p < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
};
})();
