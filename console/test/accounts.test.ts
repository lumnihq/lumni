// Accounts: people sign in as themselves; each member gets their own API key,
// scoped by role; invites, roles, removal, resets, open sign-up, GitHub.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../server/env';
import { createBff } from '../server/index';
import { fakeFetch } from './real/fake-go';
import { busyDay } from './real/day';

type Res = { status: number; data: any; headers: Headers };

function setUp(vars: Record<string, string> = {}, extraFetch?: (url: string, init?: RequestInit) => Promise<Response> | null) {
  const w = busyDay(5);
  w.platformKey = 'platform_test_key';
  const fake = fakeFetch(w);
  const fetchFn = async (url: string, init?: RequestInit) => (extraFetch?.(url, init) ?? fake(url, init));
  const bff = createBff(readEnv({
    LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, LUMNI_CONSOLE_AUTH: 'accounts', LUMNI_CONSOLE_SECRET: 'test-secret-0123456789', LUMNI_CONSOLE_DB: ':memory:',
    LUMNI_CONSOLE_DATA_MODE: 'real', PORT: '5200', ...vars,
  }), fetchFn as never);
  /** A browser: its own cookies and CSRF token. */
  const browser = () => {
    const jar = new Map<string, string>();
    let csrf = '';
    const send = async (path: string, method = 'GET', body?: unknown, raw = false): Promise<Res> => {
      const headers: Record<string, string> = { host: 'localhost:5200' };
      if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
      if (method !== 'GET') { headers['content-type'] = 'application/json'; if (csrf) headers['x-csrf-token'] = csrf; }
      const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' })))!;
      for (const line of res.headers.getSetCookie()) {
        const [k, v] = line.split(';')[0]!.split('=');
        if (/Max-Age=0/.test(line)) jar.delete(k!); else jar.set(k!, v!);
      }
      const text = raw ? '' : await res.text();
      const data = text ? JSON.parse(text) : undefined;
      if (path === '/v1/auth/session' && data?.csrf_token) csrf = data.csrf_token;
      return { status: res.status, data, headers: res.headers };
    };
    return { send, jar };
  };
  return { w, browser };
}

const OWNER = { name: 'Ana Owner', email: 'ana@acme.dev', password: 'correct horse battery' };

async function withOwner(vars: Record<string, string> = {}) {
  const t = setUp(vars);
  const owner = t.browser();
  const setup = await owner.send('/console/auth/setup', 'POST', { ...OWNER, workspace: 'Acme' });
  expect(setup.status).toBe(200);
  const me = await owner.send('/v1/auth/session');
  return { ...t, owner, me };
}

async function invite(owner: ReturnType<ReturnType<typeof setUp>['browser']>, email: string | null, role = 'member') {
  const r = await owner.send('/v1/console/members/invite', 'POST', { email, role });
  expect(r.status).toBe(200);
  return new URL(r.data.link).searchParams.get('invite')!;
}

describe('first run and signing in', () => {
  test('a fresh console asks for its owner; the owner is joined to this server’s workspace', async () => {
    const { browser } = setUp();
    const b = browser();
    const out = await b.send('/v1/auth/session');
    expect(out.data).toMatchObject({ signed_in: false, accounts: { setup: true, signup: 'invite', providers: { password: true, github: false } } });
    expect((await b.send('/console/auth/setup', 'POST', { ...OWNER, password: 'short' })).data.error.code).toBe('weak_password');
    expect((await b.send('/console/auth/setup', 'POST', { ...OWNER, workspace: 'Acme' })).status).toBe(200);
    const me = await b.send('/v1/auth/session');
    expect(me.data).toMatchObject({ signed_in: true, user: { email: 'ana@acme.dev', role: 'owner', initials: 'AO' }, tenant: { id: 't_123', name: 'Acme' } });
    expect(me.data.csrf_token).toBeTruthy();
    // The screens work through the API session made from her own key.
    expect((await b.send('/v1/console/world')).status).toBe(200);
    expect((await browser().send('/console/auth/setup', 'POST', { ...OWNER, email: 'eve@x.dev' })).status).toBe(409);
  });

  test('email and password; the same answer for a wrong password and no account; sign out', async () => {
    const { browser } = await withOwner();
    const b = browser();
    expect((await b.send('/console/auth/login', 'POST', { email: 'ana@acme.dev', password: 'wrong password!' })).data.error.code).toBe('bad_login');
    expect((await b.send('/console/auth/login', 'POST', { email: 'nobody@acme.dev', password: 'whatever12345' })).data.error.code).toBe('bad_login');
    expect((await b.send('/console/auth/login', 'POST', { email: 'ANA@acme.dev', password: OWNER.password })).status).toBe(200);
    expect((await b.send('/v1/auth/session')).data.signed_in).toBe(true);
    await b.send('/v1/auth/session', 'DELETE');
    expect((await b.send('/v1/auth/session')).data.signed_in).toBe(false);
  });

  test('ten wrong passwords for one email, then a pause', async () => {
    const { browser } = await withOwner();
    const b = browser();
    for (let i = 0; i < 10; i++) await b.send('/console/auth/login', 'POST', { email: 'ana@acme.dev', password: `nope-${i}-xxxxx` });
    expect((await b.send('/console/auth/login', 'POST', { email: 'ana@acme.dev', password: OWNER.password })).status).toBe(429);
  });

  test('a key can’t be pasted in accounts mode', async () => {
    const { browser } = await withOwner();
    expect((await browser().send('/v1/auth/session', 'POST', { key: 'lumni_x' })).data.error.code).toBe('use_your_account');
  });
});

describe('people', () => {
  test('an invite makes a member, with their own key that can’t change settings', async () => {
    const { owner, browser } = await withOwner();
    const token = await invite(owner, 'bo@acme.dev', 'member');
    const anon = browser();
    expect((await anon.send(`/console/auth/invite?token=${token}`)).data).toMatchObject({ workspace: 'Acme', email: 'bo@acme.dev', role: 'member', invited_by: 'Ana Owner', status: 'open' });
    expect((await anon.send('/console/auth/signup', 'POST', { name: 'Bo', email: 'someone@else.dev', password: 'a long password', invite: token })).status).toBe(403);
    expect((await anon.send('/console/auth/signup', 'POST', { name: 'Bo Member', email: 'bo@acme.dev', password: 'a long password', invite: token })).status).toBe(200);
    const me = await anon.send('/v1/auth/session');
    expect(me.data.user).toMatchObject({ email: 'bo@acme.dev', role: 'member' });
    // Their own key, with the member's scopes, named after them.
    const keys = await owner.send('/v1/api-keys');
    const bo = keys.data.items.find((k: { name: string }) => k.name === 'Console: Bo Member <bo@acme.dev>');
    expect(bo.scopes).toEqual(['query', 'admission', 'ingest']);
    // Members see people, not invites, and can't change them.
    const list = await anon.send('/v1/console/members');
    expect(list.data.members.map((m: { email: string }) => m.email)).toEqual(['ana@acme.dev', 'bo@acme.dev']);
    expect(list.data.invites).toEqual([]);
    expect((await anon.send('/v1/console/members/invite', 'POST', { role: 'admin' })).status).toBe(403);
    // The invite was used once.
    expect((await browser().send('/console/auth/signup', 'POST', { name: 'Again', email: 'bo2@acme.dev', password: 'a long password', invite: token })).data.error.code).toBe('bad_invite');
  });

  test('changing a role revokes the old key; the next sign-in gets a key with the new scopes', async () => {
    const { owner, browser } = await withOwner();
    const token = await invite(owner, null, 'viewer');
    const v = browser();
    await v.send('/console/auth/signup', 'POST', { name: 'Vi Ewer', email: 'vi@acme.dev', password: 'a long password', invite: token });
    await v.send('/v1/auth/session');
    const id = (await owner.send('/v1/console/members')).data.members.find((m: { email: string }) => m.email === 'vi@acme.dev').id;
    expect((await owner.send(`/v1/console/members/${id}`, 'PATCH', { role: 'admin' })).status).toBe(200);
    const keys = (await owner.send('/v1/api-keys')).data.items.filter((k: { name: string }) => k.name.includes('vi@acme.dev'));
    expect(keys.find((k: { scopes: string[] }) => k.scopes[0] === 'query' && k.scopes.length === 1).revoked_at).toBeTruthy();
    // A fresh browser session for Vi: a new key, admin now.
    const v2 = browser();
    await v2.send('/console/auth/login', 'POST', { email: 'vi@acme.dev', password: 'a long password' });
    expect((await v2.send('/v1/auth/session')).data.user.role).toBe('admin');
    expect((await owner.send('/v1/api-keys')).data.items.some((k: { name: string; scopes: string[]; revoked_at: string | null }) => k.name.includes('vi@acme.dev') && k.scopes[0] === 'admin' && !k.revoked_at)).toBe(true);
  });

  test('removing someone signs them out of the workspace; the last owner stays', async () => {
    const { owner, browser, me } = await withOwner();
    const token = await invite(owner, 'cy@acme.dev');
    const c = browser();
    await c.send('/console/auth/signup', 'POST', { name: 'Cy', email: 'cy@acme.dev', password: 'a long password', invite: token });
    const cid = (await owner.send('/v1/console/members')).data.members.find((m: { email: string }) => m.email === 'cy@acme.dev').id;
    expect((await owner.send(`/v1/console/members/${cid}`, 'DELETE')).status).toBe(200);
    expect((await c.send('/v1/auth/session')).data).toMatchObject({ signed_in: false, accounts: { no_workspace: true } });
    expect((await owner.send(`/v1/console/members/${me.data.user.id}`, 'PATCH', { role: 'member' })).data.error.code).toBe('last_owner');
    expect((await owner.send(`/v1/console/members/${me.data.user.id}`, 'DELETE')).data.error.code).toBe('last_owner');
  });

  test('a reset link sets a new password once', async () => {
    const { owner, browser } = await withOwner();
    const token = await invite(owner, 'di@acme.dev');
    const d = browser();
    await d.send('/console/auth/signup', 'POST', { name: 'Di', email: 'di@acme.dev', password: 'first password!', invite: token });
    const id = (await owner.send('/v1/console/members')).data.members.find((m: { email: string }) => m.email === 'di@acme.dev').id;
    const link = (await owner.send(`/v1/console/members/${id}/reset`, 'POST', {})).data.link;
    const reset = new URL(link).searchParams.get('reset');
    const x = browser();
    expect((await x.send('/console/auth/reset', 'POST', { token: reset, password: 'second password!' })).status).toBe(200);
    expect((await x.send('/console/auth/reset', 'POST', { token: reset, password: 'third password!!' })).data.error.code).toBe('bad_reset');
    expect((await browser().send('/console/auth/login', 'POST', { email: 'di@acme.dev', password: 'first password!' })).status).toBe(400);
    expect((await browser().send('/console/auth/login', 'POST', { email: 'di@acme.dev', password: 'second password!' })).status).toBe(200);
  });

  test('writes need the CSRF token', async () => {
    const { browser } = await withOwner();
    const b = browser();
    await b.send('/console/auth/login', 'POST', { email: OWNER.email, password: OWNER.password });
    // Signed in, but this browser hasn't read its session (and token) yet.
    expect((await b.send('/v1/console/account', 'PATCH', { name: 'X' })).status).toBe(403);
    await b.send('/v1/auth/session');
    expect((await b.send('/v1/console/account', 'PATCH', { name: 'Ana O.' })).status).toBe(200);
    expect((await b.send('/v1/console/account')).data.user.name).toBe('Ana O.');
  });
});

describe('sign-up', () => {
  test('by invitation by default', async () => {
    const { browser } = await withOwner();
    expect((await browser().send('/console/auth/signup', 'POST', { name: 'Ed', email: 'ed@x.dev', password: 'a long password' })).data.error.code).toBe('invite_only');
  });

  test('open sign-up makes a new workspace on the API, and people can switch between theirs', async () => {
    const { owner, browser } = await withOwner({ LUMNI_CONSOLE_SIGNUP: 'open', LUMNI_CONSOLE_PLATFORM_KEY: 'platform_test_key' });
    const e = browser();
    expect((await e.send('/console/auth/signup', 'POST', { name: 'Ed Founder', email: 'ed@startup.dev', password: 'a long password', workspace: 'Startup' })).status).toBe(200);
    const me = await e.send('/v1/auth/session');
    expect(me.data).toMatchObject({ user: { role: 'owner' }, tenant: { name: 'Startup' } });
    expect(me.data.tenant.id).not.toBe('t_123');
    // Ana invites Ed into Acme; Ed accepts signed in, and switches.
    const token = await invite(owner, 'ed@startup.dev', 'viewer');
    expect((await e.send('/v1/console/account/accept', 'POST', { token })).status).toBe(200);
    const after = await e.send('/v1/auth/session');
    expect(after.data.tenant.name).toBe('Acme');
    expect(after.data.workspaces.map((x: { name: string }) => x.name).sort()).toEqual(['Acme', 'Startup']);
    const startup = after.data.workspaces.find((x: { name: string }) => x.name === 'Startup').id;
    expect((await e.send('/v1/console/account/switch', 'POST', { workspace_id: startup })).status).toBe(200);
    expect((await e.send('/v1/auth/session')).data.tenant.name).toBe('Startup');
  });
});

describe('GitHub', () => {
  const github = (email: string) => (url: string): Promise<Response> | null => {
    if (url === 'https://github.com/login/oauth/access_token') return Promise.resolve(Response.json({ access_token: 'gh_token' }));
    if (url === 'https://api.github.com/user') return Promise.resolve(Response.json({ id: 4242, login: 'octo', name: 'Octo Cat' }));
    if (url === 'https://api.github.com/user/emails') return Promise.resolve(Response.json([{ email, primary: true, verified: true }]));
    return null;
  };

  test('an invited person signs in with GitHub and lands in the workspace', async () => {
    const t = setUp({ LUMNI_CONSOLE_GITHUB_CLIENT_ID: 'gh_id', LUMNI_CONSOLE_GITHUB_CLIENT_SECRET: 'gh_secret' }, github('octo@acme.dev'));
    const owner = t.browser();
    await owner.send('/console/auth/setup', 'POST', { ...OWNER, workspace: 'Acme' });
    await owner.send('/v1/auth/session');
    const token = await invite(owner, 'octo@acme.dev');
    const g = t.browser();
    const start = await g.send(`/console/auth/oauth/github/start?invite=${token}&next=/start`, 'GET', undefined, true);
    expect(start.status).toBe(302);
    const to = new URL(start.headers.get('location')!);
    expect(to.origin + to.pathname).toBe('https://github.com/login/oauth/authorize');
    expect(to.searchParams.get('redirect_uri')).toBe('http://localhost:5200/console/auth/oauth/github/callback');
    const back = await g.send(`/console/auth/oauth/github/callback?code=abc&state=${to.searchParams.get('state')}`, 'GET', undefined, true);
    expect(back.status).toBe(302);
    expect(back.headers.get('location')).toBe('/start');
    expect((await g.send('/v1/auth/session')).data).toMatchObject({ signed_in: true, user: { email: 'octo@acme.dev', role: 'member' }, tenant: { name: 'Acme' } });
    // A state that doesn't match is turned away.
    const bad = await t.browser().send('/console/auth/oauth/github/callback?code=abc&state=nope', 'GET', undefined, true);
    expect(bad.headers.get('location')).toContain('/sign-in?error=');
  });

  test('no invite and invite-only: turned away, with a reason', async () => {
    const t = setUp({ LUMNI_CONSOLE_GITHUB_CLIENT_ID: 'gh_id', LUMNI_CONSOLE_GITHUB_CLIENT_SECRET: 'gh_secret' }, github('stranger@x.dev'));
    const owner = t.browser();
    await owner.send('/console/auth/setup', 'POST', { ...OWNER });
    const g = t.browser();
    const start = await g.send('/console/auth/oauth/github/start', 'GET', undefined, true);
    const state = new URL(start.headers.get('location')!).searchParams.get('state');
    const back = await g.send(`/console/auth/oauth/github/callback?code=abc&state=${state}`, 'GET', undefined, true);
    expect(decodeURIComponent(back.headers.get('location')!)).toContain('Ask a workspace owner for an invite');
  });

  test('open sign-up: a newcomer gets their own workspace and starts by connecting an agent', async () => {
    const t = setUp({ LUMNI_CONSOLE_GITHUB_CLIENT_ID: 'gh_id', LUMNI_CONSOLE_GITHUB_CLIENT_SECRET: 'gh_secret', LUMNI_CONSOLE_SIGNUP: 'open', LUMNI_CONSOLE_PLATFORM_KEY: 'platform_test_key' }, github('new@startup.dev'));
    await t.browser().send('/console/auth/setup', 'POST', { ...OWNER, workspace: 'Acme' });
    const g = t.browser();
    const start = await g.send('/console/auth/oauth/github/start?next=/today', 'GET', undefined, true);
    const state = new URL(start.headers.get('location')!).searchParams.get('state');
    const back = await g.send(`/console/auth/oauth/github/callback?code=abc&state=${state}`, 'GET', undefined, true);
    expect(back.headers.get('location')).toBe('/start');
    const me = (await g.send('/v1/auth/session')).data;
    expect(me).toMatchObject({ signed_in: true, user: { email: 'new@startup.dev', role: 'owner' }, tenant: { name: 'Octo’s workspace' } });
    expect(me.tenant.id).not.toBe('t_123');
  });
});
