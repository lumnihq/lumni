// Accounts: people sign in to the console as themselves.
//
// Each workspace is one tenant on Lumni's API. The console keeps that
// tenant's admin key (sealed) and gives every member their own API key,
// scoped by role, so the API enforces what each person may do and its audit
// log names them. Signing in then works as it always has underneath: the
// member's key is traded for an API session (lumni_session + lumni_csrf), so
// every screen, proxy and write is unchanged.
//
//   owner, admin   scope admin: everything
//   member         query, admission, ingest: watch, approve and deny, connect agents' data
//   viewer         query: watch only
//
// Routes (all JSON):
//   GET    /v1/auth/session               who you are, minting the API session when needed
//   DELETE /v1/auth/session               sign out
//   GET    /console/auth/state            what the sign-in page should offer
//   POST   /console/auth/setup            first run: the owner, joined to this server's workspace
//   POST   /console/auth/login            email and password
//   POST   /console/auth/signup           with an invite, or open sign-up (a new workspace)
//   GET    /console/auth/invite?token=    what an invite is for
//   POST   /console/auth/reset            a new password, with a reset link's token
//   GET    /console/auth/oauth/:p/start   GitHub or Google
//   GET    /console/auth/oauth/:p/callback
//   GET    /v1/console/account            you, your workspaces
//   PATCH  /v1/console/account            your name or password
//   POST   /v1/console/account/switch     another workspace
//   POST   /v1/console/account/sample     go to your sample workspace (npx lumniverse try), made on first use
//   DELETE /v1/console/account/sample     remove it
//   POST   /v1/console/account/accept     join a workspace with an invite, signed in
//   PATCH  /v1/console/workspace          rename it (owner, admin)
//   GET    /v1/console/members            people (everyone), invites (owner, admin)
//   POST   /v1/console/members/invite     a link to join (owner, admin)
//   DELETE /v1/console/members/invites/:id
//   PATCH  /v1/console/members/:user      their role
//   DELETE /v1/console/members/:user      remove them
//   POST   /v1/console/members/:user/reset  a password-reset link for them
//   GET    /v1/console/billing         the plan, trial and usage (Lumni Cloud; see billing.ts)
//   POST   /v1/console/billing/checkout | /portal
//   POST   /console/billing/webhook       Stripe's events

import type { Env } from '../env';
import { HttpError, clearCookie, cookies, json, problem, readSetCookie, setCookie } from '../http';
import { read, unreachable, type Fetch, type Upstream } from '../upstream';
import { Billing } from './billing';

/** The name every signed-up workspace starts with; owners rename it in Settings. */
const DEFAULT_WORKSPACE = 'Default';
/** Where `npx lumniverse try` runs its sample agents, apart from the real workspace. */
const SAMPLE_WORKSPACE = 'Sample agents';
import { ROLES, Sealer, Store, type Member, type Role, type User, type Workspace } from './store';

const USER = 'lumni_user';
const SESSION = 'lumni_session';
const CSRF = 'lumni_csrf';
/** Which workspace the API session belongs to, so a switch makes a new one. */
const WS = 'lumni_ws';
const OAUTH = 'lumni_oauth';

const DAY = 86_400_000;
const SESSION_TTL = 30 * DAY;
const INVITE_TTL = 7 * DAY;
const RESET_TTL = DAY;
const MIN_PASSWORD = 10;

export const SCOPES: Record<Role, string[]> = {
  owner: ['admin'],
  admin: ['admin'],
  member: ['query', 'admission', 'ingest'],
  viewer: ['query'],
};

const initialsOf = (name: string, email: string) => {
  const parts = (name || email).replace(/@.*/, '').split(/[\s._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? parts[0]?.[1] ?? '')).toUpperCase();
};
const emailOk = (e: unknown): e is string => typeof e === 'string' && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e.trim()) && e.length <= 254;
const text = (v: unknown, n = 120) => (typeof v === 'string' && v.trim() ? v.trim().slice(0, n) : '');

interface Signed { token: string; user: User; workspace: Workspace | null; member: Member | null }

export interface AccountMe { initials: string; email: string; name: string }

export class Accounts {
  readonly store: Store;
  /** Plans and Stripe, for Lumni Cloud. Null: nothing is billed. */
  readonly billing: Billing | null;
  private readonly sealer: Sealer;
  private attempts = new Map<string, number[]>();

  constructor(
    private readonly env: Env,
    private readonly up: Upstream,
    private readonly fetchFn: Fetch,
    private readonly now: () => number = Date.now,
    store?: Store,
  ) {
    this.sealer = new Sealer(env.secret);
    this.store = store ?? new Store(env.dbPath);
    this.billing = env.stripe && env.platformKey ? new Billing(env, this.store, fetchFn, now) : null;
  }

  /** The routes this handles, or null for everything else. */
  owns(path: string): boolean {
    return path === '/v1/auth/session' || path.startsWith('/console/auth/') || path.startsWith('/v1/console/account') || path === '/v1/console/workspace' || path.startsWith('/v1/console/members') || path.startsWith('/v1/console/billing') || path === '/console/billing/webhook';
  }

  async handle(req: Request): Promise<Response> {
    const url = new URL(req.url);
    const path = url.pathname;
    const method = req.method.toUpperCase();
    try {
      if (path === '/v1/auth/session') {
        if (method === 'GET') return await this.whoAmI(req);
        if (method === 'DELETE') return await this.signOut(req);
        throw new HttpError(400, 'use_your_account', 'This console signs in with accounts, not keys.');
      }
      if (path === '/console/billing/webhook') {
        if (!this.billing || method !== 'POST') throw new HttpError(404, 'not_found', 'Not found.');
        return await this.billing.webhook(req);
      }
      if (path.startsWith('/console/auth/')) return await this.auth(req, url, method);
      return await this.signedIn(req, url, method);
    } catch (e) {
      if (e instanceof HttpError) return problem(e);
      console.error('accounts failed', e);
      return problem(new HttpError(500, 'internal', 'That didn’t work. The server log has the details.'));
    }
  }

  /** Who's asking, for the console's own views (approvals name the person). */
  me(req: Request): AccountMe | null {
    const t = cookies(req)[USER];
    const s = t ? this.store.session(t) : null;
    const u = s && s.expires_at > this.now() ? this.store.userById(s.user_id) : null;
    return u ? { initials: initialsOf(u.name, u.email), email: u.email, name: u.name } : null;
  }

  // ─── who am I ────────────────────────────────────────

  state() {
    return {
      setup: this.store.userCount() === 0,
      signup: this.env.signup === 'open' && !!this.env.platformKey ? 'open' : 'invite',
      providers: { password: true, github: !!this.env.github, google: !!this.env.google },
    };
  }

  private signed(req: Request): Signed | null {
    const token = cookies(req)[USER];
    if (!token) return null;
    const s = this.store.session(token);
    if (!s || s.expires_at < this.now()) return null;
    const user = this.store.userById(s.user_id);
    if (!user) return null;
    let workspace = s.workspace_id ? this.store.workspace(s.workspace_id) : null;
    let member = workspace ? this.store.member(workspace.id, user.id) : null;
    if (!member) {
      // Removed from it, or never chose: the first workspace they're in.
      const first = this.store.membershipsOf(user.id)[0];
      workspace = first ? this.store.workspace(first.workspace_id) : null;
      member = workspace ? this.store.member(workspace.id, user.id) : null;
      if (workspace) this.store.setSessionWorkspace(token, workspace.id);
    }
    return { token, user, workspace, member };
  }

  private async whoAmI(req: Request): Promise<Response> {
    const secure = this.secure(req);
    const s = this.signed(req);
    const c = cookies(req);
    if (!s || !s.workspace || !s.member) {
      const h = new Headers();
      if (c[SESSION]) for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, secure));
      if (c[USER] && !s) h.append('set-cookie', clearCookie(USER, secure));
      return json({ signed_in: false, accounts: { ...this.state(), no_workspace: !!s && !s.workspace } }, 200, h);
    }
    const now = this.now();
    this.store.touchSession(s.token, now, SESSION_TTL);
    if (!s.user.last_seen || now - s.user.last_seen > 60_000) this.store.updateUser(s.user.id, { last_seen: now });

    // The API session: reuse it while it works and belongs to this workspace.
    if (c[SESSION] && c[CSRF] && c[WS] === s.workspace.id) {
      try {
        await this.up.get('/v1/auth/session', { session: c[SESSION]!, refresh: true });
        return json(this.sessionJson(s, c[CSRF]!));
      } catch (e) {
        if (!(e instanceof HttpError) || e.status !== 401) throw e;
        this.up.forget(c[SESSION]!);
      }
    }
    return this.openApiSession(s, secure);
  }

  private sessionJson(s: Signed, csrf: string) {
    return {
      signed_in: true,
      csrf_token: csrf,
      user: { id: s.user.id, initials: initialsOf(s.user.name, s.user.email), email: s.user.email, name: s.user.name, role: s.member!.role },
      tenant: { id: s.workspace!.tenant_id, name: s.workspace!.name, sample: !!s.workspace!.sample },
      workspaces: this.store.membershipsOf(s.user.id).map((m) => ({ id: m.workspace_id, name: m.name, role: m.role, sample: !!m.sample, current: m.workspace_id === s.workspace!.id })),
    };
  }

  /** Trade the member's own key for an API session; make the key the first time. */
  private async openApiSession(s: Signed, secure: boolean, retried = false): Promise<Response> {
    const key = await this.memberKey(s.workspace!, s.member!, s.user);
    let res: Response;
    try {
      res = await this.fetchFn(`${this.env.api}/v1/auth/session`, { method: 'POST', headers: { authorization: `Bearer ${key}`, accept: 'application/json' } });
    } catch {
      throw unreachable(this.env.api);
    }
    if (res.status === 401 && !retried) {
      // Revoked on the API's side (Settings, or by hand): make a new one, once.
      this.store.clearMemberKey(s.workspace!.id, s.user.id);
      s.member = this.store.member(s.workspace!.id, s.user.id);
      return this.openApiSession(s, secure, true);
    }
    const g = await read<{ tenant_id: string; csrf_token?: string }>(res);
    const token = readSetCookie(res.headers.getSetCookie(), SESSION);
    if (!token?.value || !g.csrf_token) throw new HttpError(502, 'BAD_UPSTREAM', 'The API made a session but didn’t hand it over.');
    const maxAge = token.maxAge ?? 8 * 3600;
    const h = new Headers();
    h.append('set-cookie', setCookie(SESSION, token.value, { maxAge, secure }));
    h.append('set-cookie', setCookie(CSRF, g.csrf_token, { maxAge, secure }));
    h.append('set-cookie', setCookie(WS, s.workspace!.id, { maxAge, secure }));
    return json(this.sessionJson(s, g.csrf_token), 200, h);
  }

  private async adminCall<T>(ws: Workspace, method: string, path: string, body?: unknown): Promise<T> {
    const key = await this.sealer.open(ws.admin_key_sealed);
    let res: Response;
    try {
      res = await this.fetchFn(`${this.env.api}${path}`, { method, headers: { authorization: `Bearer ${key}`, accept: 'application/json', ...(body ? { 'content-type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
    } catch {
      throw unreachable(this.env.api);
    }
    if (res.status === 401) throw new HttpError(502, 'workspace_key_rejected', `The API no longer accepts ${ws.name}’s admin key. An owner has to reconnect the workspace.`);
    return read<T>(res);
  }

  private async memberKey(ws: Workspace, m: Member, u: User): Promise<string> {
    if (m.key_sealed) return this.sealer.open(m.key_sealed);
    const k = await this.adminCall<{ id: string; secret?: string }>(ws, 'POST', '/v1/api-keys', { name: `Console: ${u.name} <${u.email}>`, scopes: SCOPES[m.role] });
    if (!k.secret) throw new HttpError(502, 'BAD_UPSTREAM', 'The API made a key but didn’t hand it over.');
    this.store.setMemberKey(ws.id, u.id, k.id, await this.sealer.seal(k.secret));
    return k.secret;
  }

  private async revokeMemberKey(ws: Workspace, m: Member | null): Promise<void> {
    if (!m?.key_id) return;
    await this.adminCall(ws, 'DELETE', `/v1/api-keys/${encodeURIComponent(m.key_id)}`).catch(() => {});
  }

  private async signOut(req: Request): Promise<Response> {
    const c = cookies(req);
    const secure = this.secure(req);
    if (c[SESSION]) {
      await this.up.send('DELETE', '/v1/auth/session', undefined, { session: c[SESSION]!, csrf: req.headers.get('x-csrf-token') }).catch(() => {});
      this.up.forget(c[SESSION]!);
    }
    if (c[USER]) this.store.deleteSession(c[USER]!);
    const h = new Headers();
    for (const k of [USER, SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, secure));
    return json({ signed_in: false, accounts: this.state() }, 200, h);
  }

  // ─── signing in ──────────────────────────────────────

  private secure(req: Request): boolean {
    return new URL(req.url).protocol === 'https:' || req.headers.get('x-forwarded-proto') === 'https';
  }

  private origin(req: Request): string {
    if (this.env.publicConsole) return this.env.publicConsole;
    const u = new URL(req.url);
    return `${req.headers.get('x-forwarded-proto') ?? u.protocol.replace(':', '')}://${req.headers.get('host') ?? u.host}`;
  }

  /** Signed in: a session cookie, and where to go next. */
  private start(req: Request, user: User, workspaceId: string | null, body: Record<string, unknown> = {}): Response {
    const token = this.store.createSession(user.id, workspaceId, this.now(), SESSION_TTL);
    const h = new Headers();
    h.append('set-cookie', setCookie(USER, token, { maxAge: SESSION_TTL / 1000, secure: this.secure(req) }));
    for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, this.secure(req)));
    return json({ ok: true, ...body }, 200, h);
  }

  private limit(key: string, n: number, perMs: number): void {
    const now = this.now();
    const recent = (this.attempts.get(key) ?? []).filter((t) => now - t < perMs);
    if (recent.length >= n) throw new HttpError(429, 'RATE_LIMITED', 'Too many tries. Wait a few minutes.');
    recent.push(now);
    this.attempts.set(key, recent);
    if (this.attempts.size > 10_000) this.attempts.clear();
  }

  private address(req: Request): string {
    return (req.headers.get('x-forwarded-for') ?? '').split(',')[0]!.trim() || req.headers.get('x-real-ip') || 'peer';
  }

  private async password(v: unknown): Promise<string> {
    if (typeof v !== 'string' || v.length < MIN_PASSWORD) throw new HttpError(400, 'weak_password', `Use at least ${MIN_PASSWORD} characters.`);
    if (v.length > 200) throw new HttpError(400, 'weak_password', 'That password is too long.');
    return Bun.password.hash(v, { algorithm: 'argon2id' });
  }

  private async auth(req: Request, url: URL, method: string): Promise<Response> {
    const path = url.pathname;
    if (method === 'GET' && path === '/console/auth/state') return json(this.state());
    if (method === 'GET' && path === '/console/auth/invite') return json(this.inviteView(url.searchParams.get('token') ?? ''));
    const oauth = /^\/console\/auth\/oauth\/(github|google)\/(start|callback)$/.exec(path);
    if (oauth && method === 'GET') return oauth[2] === 'start' ? this.oauthStart(req, url, oauth[1] as 'github' | 'google') : this.oauthCallback(req, url, oauth[1] as 'github' | 'google');
    if (method !== 'POST') throw new HttpError(405, 'method_not_allowed', 'Not here.');
    const body = (await req.json().catch(() => ({}))) as Record<string, unknown>;
    if (path === '/console/auth/setup') return this.setup(req, body);
    if (path === '/console/auth/login') return this.login(req, body);
    if (path === '/console/auth/signup') return this.signup(req, body);
    if (path === '/console/auth/reset') return this.reset(req, body);
    throw new HttpError(404, 'not_found', 'No such route.');
  }

  /** First run: the first person becomes the owner of this server's workspace. */
  private async setup(req: Request, body: Record<string, unknown>): Promise<Response> {
    if (this.store.userCount() > 0) throw new HttpError(409, 'already_set_up', 'This console is already set up. Sign in instead.');
    if (!this.env.adminKey) throw new HttpError(400, 'no_admin_key', 'Set LUMNI_CONSOLE_ADMIN_KEY to the API’s bootstrap admin key, then restart the console.');
    const email = body.email, name = text(body.name, 80);
    if (!emailOk(email)) throw new HttpError(400, 'bad_email', 'Enter a valid email.');
    if (!name) throw new HttpError(400, 'no_name', 'Enter your name.');
    const hash = await this.password(body.password);
    const tenant = await this.tenantOf(this.env.adminKey);
    const now = this.now();
    const ws = this.store.workspaceByTenant(tenant) ?? this.store.createWorkspace(text(body.workspace, 80) || this.env.workspace || 'My workspace', tenant, await this.sealer.seal(this.env.adminKey), now);
    const user = this.store.createUser(email, name, hash, now);
    this.store.addMember(ws.id, user.id, 'owner', now);
    return this.start(req, user, ws.id);
  }

  /** Which tenant a key belongs to. */
  private async tenantOf(key: string): Promise<string> {
    let res: Response;
    try {
      res = await this.fetchFn(`${this.env.api}/v1/auth/session`, { method: 'GET', headers: { authorization: `Bearer ${key}`, accept: 'application/json' } });
    } catch {
      throw unreachable(this.env.api);
    }
    if (res.status === 401) throw new HttpError(400, 'invalid_key', 'The API doesn’t accept LUMNI_CONSOLE_ADMIN_KEY. It has to match the API’s LUMNI_BOOTSTRAP_ADMIN_KEY.');
    const s = await read<{ tenant_id?: string }>(res);
    if (!s.tenant_id) throw new HttpError(502, 'BAD_UPSTREAM', 'The API didn’t say which workspace the key is for.');
    return s.tenant_id;
  }

  private async login(req: Request, body: Record<string, unknown>): Promise<Response> {
    const email = typeof body.email === 'string' ? body.email.trim().toLowerCase() : '';
    this.limit(`ip:${this.address(req)}`, 30, 15 * 60_000);
    this.limit(`email:${email}`, 10, 15 * 60_000);
    const user = email ? this.store.userByEmail(email) : null;
    const pw = typeof body.password === 'string' ? body.password : '';
    // The same answer for "no such person" and "wrong password".
    const ok = user?.password_hash ? await Bun.password.verify(pw, user.password_hash) : (await Bun.password.verify(pw, await this.dummyHash()), false);
    if (!user || !ok) throw new HttpError(400, 'bad_login', 'That email and password don’t match.');
    if (typeof body.invite === 'string' && body.invite) this.join(user, body.invite);
    return this.start(req, user, null);
  }
  private dummy: Promise<string> | null = null;
  private dummyHash(): Promise<string> {
    return (this.dummy ??= Bun.password.hash('not-a-real-password', { algorithm: 'argon2id' }));
  }

  /** An invite becomes a membership. */
  private join(user: User, token: string): string {
    const inv = this.store.inviteByToken(token);
    const now = this.now();
    if (!inv || inv.accepted_at || inv.expires_at < now) throw new HttpError(400, 'bad_invite', 'This invite has expired or was already used. Ask for a new one.');
    if (inv.email && inv.email.toLowerCase() !== user.email.toLowerCase()) throw new HttpError(403, 'invite_for_someone_else', `This invite is for ${inv.email}.`);
    this.store.addMember(inv.workspace_id, user.id, inv.role, now);
    this.store.acceptInvite(inv.id, user.id, now);
    return inv.workspace_id;
  }

  private inviteView(token: string) {
    const inv = token ? this.store.inviteByToken(token) : null;
    if (!inv) throw new HttpError(404, 'not_found', 'No invite with that link.');
    const ws = this.store.workspace(inv.workspace_id);
    const by = this.store.userById(inv.invited_by);
    return {
      workspace: ws?.name ?? 'A workspace', email: inv.email, role: inv.role, invited_by: by?.name ?? null,
      status: inv.accepted_at ? 'used' : inv.expires_at < this.now() ? 'expired' : 'open',
      has_account: inv.email ? !!this.store.userByEmail(inv.email) : false,
    };
  }

  private async signup(req: Request, body: Record<string, unknown>): Promise<Response> {
    this.limit(`signup:${this.address(req)}`, 10, 60 * 60_000);
    const email = body.email, name = text(body.name, 80);
    if (!emailOk(email)) throw new HttpError(400, 'bad_email', 'Enter a valid email.');
    if (!name) throw new HttpError(400, 'no_name', 'Enter your name.');
    if (this.store.userByEmail(email)) throw new HttpError(409, 'has_account', 'There’s already an account with that email. Sign in instead.');
    const invite = typeof body.invite === 'string' ? body.invite : '';
    if (!invite && this.state().signup !== 'open') throw new HttpError(403, 'invite_only', 'Accounts on this console are by invitation. Ask a workspace owner for an invite link.');
    if (invite) {
      const inv = this.store.inviteByToken(invite);
      if (!inv || inv.accepted_at || inv.expires_at < this.now()) throw new HttpError(400, 'bad_invite', 'This invite has expired or was already used. Ask for a new one.');
      if (inv.email && inv.email.toLowerCase() !== email.trim().toLowerCase()) throw new HttpError(403, 'invite_for_someone_else', `This invite is for ${inv.email}.`);
    }
    const hash = await this.password(body.password);
    const user = this.store.createUser(email, name, hash, this.now());
    if (invite) return this.start(req, user, this.join(user, invite));
    // Everyone starts in “Default”; it can be renamed in Settings.
    const ws = await this.newWorkspace(text(body.workspace, 80) || DEFAULT_WORKSPACE, user);
    return this.start(req, user, ws.id);
  }

  /** Open sign-up: a new tenant on the API, owned by this person. */
  private async newWorkspace(name: string, owner: User, opts: { sample?: boolean } = {}): Promise<Workspace> {
    let res: Response;
    try {
      res = await this.fetchFn(`${this.env.api}/v1/tenants`, { method: 'POST', headers: { authorization: `Bearer ${this.env.platformKey}`, 'content-type': 'application/json', accept: 'application/json' }, body: JSON.stringify({ name }) });
    } catch {
      throw unreachable(this.env.api);
    }
    if (res.status === 404 || res.status === 405) throw new HttpError(501, 'no_tenants_api', 'This Lumni API can’t create workspaces yet, so sign-up is by invitation for now.');
    const t = await read<{ tenant_id: string; admin_key: string }>(res);
    const now = this.now();
    const ws = this.store.createWorkspace(name, t.tenant_id, await this.sealer.seal(t.admin_key), now, !!opts.sample);
    this.store.addMember(ws.id, owner.id, 'owner', now);
    // A sample workspace has no billing row, so it's never charged and never runs out.
    if (!opts.sample) this.billing?.startTrial(ws);
    return ws;
  }

  private async reset(req: Request, body: Record<string, unknown>): Promise<Response> {
    const hash = await this.password(body.password);
    const userId = typeof body.token === 'string' ? this.store.useReset(body.token, this.now()) : null;
    if (!userId) throw new HttpError(400, 'bad_reset', 'This reset link has expired or was already used. Ask for a new one.');
    this.store.updateUser(userId, { password_hash: hash });
    this.store.deleteSessionsOf(userId);
    return this.start(req, this.store.userById(userId)!, null);
  }

  // ─── GitHub and Google ───────────────────────────────

  private provider(p: 'github' | 'google') {
    const cfg = this.env[p];
    if (!cfg) throw new HttpError(404, 'not_configured', `Sign-in with ${p === 'github' ? 'GitHub' : 'Google'} isn’t set up on this console.`);
    return p === 'github'
      ? { ...cfg, authorize: 'https://github.com/login/oauth/authorize', token: 'https://github.com/login/oauth/access_token', scope: 'read:user user:email' }
      : { ...cfg, authorize: 'https://accounts.google.com/o/oauth2/v2/auth', token: 'https://oauth2.googleapis.com/token', scope: 'openid email profile' };
  }

  private oauthStart(req: Request, url: URL, p: 'github' | 'google'): Response {
    const cfg = this.provider(p);
    const state = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('');
    const next = url.searchParams.get('next') ?? '/today';
    const invite = url.searchParams.get('invite') ?? '';
    const q = new URLSearchParams({ client_id: cfg.id, redirect_uri: `${this.origin(req)}/console/auth/oauth/${p}/callback`, scope: cfg.scope, state, ...(p === 'google' ? { response_type: 'code', prompt: 'select_account' } : {}) });
    const h = new Headers({ location: `${cfg.authorize}?${q}` });
    h.append('set-cookie', setCookie(OAUTH, JSON.stringify({ state, next: next.startsWith('/') && !next.startsWith('//') ? next : '/today', invite }), { maxAge: 600, secure: this.secure(req), sameSite: 'Lax' }));
    return new Response(null, { status: 302, headers: h });
  }

  private async oauthCallback(req: Request, url: URL, p: 'github' | 'google'): Promise<Response> {
    const fail = (msg: string) => new Response(null, { status: 302, headers: { location: `/sign-in?error=${encodeURIComponent(msg)}` } });
    let saved: { state: string; next: string; invite: string };
    try {
      saved = JSON.parse(cookies(req)[OAUTH] ?? '');
    } catch {
      return fail('The sign-in took too long. Try again.');
    }
    if (!saved.state || saved.state !== url.searchParams.get('state')) return fail('The sign-in didn’t match. Try again.');
    const cfg = this.provider(p);
    const code = url.searchParams.get('code');
    if (!code) return fail('The sign-in was cancelled.');
    const tok = await this.fetchFn(cfg.token, {
      method: 'POST', headers: { accept: 'application/json', 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ client_id: cfg.id, client_secret: cfg.secret, code, redirect_uri: `${this.origin(req)}/console/auth/oauth/${p}/callback`, grant_type: 'authorization_code' }).toString(),
    }).then((r) => r.json() as Promise<{ access_token?: string }>).catch(() => ({} as { access_token?: string }));
    if (!tok.access_token) return fail(`${p === 'github' ? 'GitHub' : 'Google'} didn’t confirm the sign-in.`);
    const who = await this.profile(p, tok.access_token);
    if (!who) return fail(`${p === 'github' ? 'GitHub' : 'Google'} didn’t share a verified email.`);

    let user = this.store.userByIdentity(p, who.subject) ?? this.store.userByEmail(who.email);
    let workspace: string | null = null;
    let created = false;
    try {
      if (!user) {
        const first = this.store.userCount() === 0;
        if (first) return fail('Set up the owner account with a password first.');
        if (!saved.invite && this.state().signup !== 'open') return fail('There’s no account for that email. Ask a workspace owner for an invite.');
        user = this.store.createUser(who.email, who.name || who.email, null, this.now());
        if (!saved.invite) {
          workspace = (await this.newWorkspace(DEFAULT_WORKSPACE, user)).id;
          created = true;
        }
      }
      this.store.linkIdentity(p, who.subject, user.id);
      if (saved.invite) workspace = this.join(user, saved.invite);
    } catch (e) {
      return fail(e instanceof HttpError ? e.message : 'That sign-in didn’t work.');
    }
    const res = this.start(req, user, workspace);
    const h = new Headers(res.headers);
    // A brand-new workspace starts by connecting its first agent.
    // A new account starts on Start, unless it was on its way somewhere (approving a terminal, say).
    h.set('location', created && saved.next === '/today' ? '/start' : saved.next);
    h.delete('content-type');
    h.append('set-cookie', clearCookie(OAUTH, this.secure(req)));
    return new Response(null, { status: 302, headers: h });
  }

  private async profile(p: 'github' | 'google', token: string): Promise<{ subject: string; email: string; name: string } | null> {
    const get = (u: string) => this.fetchFn(u, { headers: { authorization: `Bearer ${token}`, accept: 'application/json', 'user-agent': 'lumni-console' } }).then((r) => (r.ok ? r.json() : null)).catch(() => null);
    if (p === 'github') {
      const [u, emails] = await Promise.all([get('https://api.github.com/user'), get('https://api.github.com/user/emails')]) as [{ id?: number; name?: string; login?: string } | null, { email: string; primary: boolean; verified: boolean }[] | null];
      const e = (emails ?? []).find((x) => x.primary && x.verified) ?? (emails ?? []).find((x) => x.verified);
      return u?.id && e ? { subject: String(u.id), email: e.email, name: u.name || u.login || '' } : null;
    }
    const u = (await get('https://openidconnect.googleapis.com/v1/userinfo')) as { sub?: string; email?: string; email_verified?: boolean; name?: string } | null;
    return u?.sub && u.email && u.email_verified ? { subject: u.sub, email: u.email, name: u.name ?? '' } : null;
  }

  // ─── signed in: your account, the workspace, its people ─

  private need(req: Request, method: string): Signed & { workspace: Workspace; member: Member } {
    const s = this.signed(req);
    if (!s) throw new HttpError(401, 'UNAUTHENTICATED', 'Sign in first.');
    if (method !== 'GET') {
      const c = cookies(req);
      if (!c[CSRF] || req.headers.get('x-csrf-token') !== c[CSRF]) throw new HttpError(403, 'CSRF_TOKEN_REQUIRED', 'Reload the page and try again.');
    }
    if (!s.workspace || !s.member) throw new HttpError(403, 'no_workspace', 'You’re not in a workspace. Ask for an invite.');
    return s as Signed & { workspace: Workspace; member: Member };
  }

  private canManage(m: Member) {
    return m.role === 'owner' || m.role === 'admin';
  }

  private async signedIn(req: Request, url: URL, method: string): Promise<Response> {
    const path = url.pathname;
    const body = method === 'GET' || method === 'DELETE' ? {} : ((await req.json().catch(() => ({}))) as Record<string, unknown>);
    const s = this.need(req, method);
    const now = this.now();

    if (path === '/v1/console/account' && method === 'GET') {
      return json({ user: { id: s.user.id, name: s.user.name, email: s.user.email, has_password: !!s.user.password_hash }, workspaces: this.store.membershipsOf(s.user.id).map((m) => ({ id: m.workspace_id, name: m.name, role: m.role, sample: !!m.sample, current: m.workspace_id === s.workspace.id })) });
    }
    if (path === '/v1/console/account' && method === 'PATCH') {
      const name = text(body.name, 80);
      if (name) this.store.updateUser(s.user.id, { name });
      if (body.password !== undefined) {
        if (s.user.password_hash && !(await Bun.password.verify(String(body.current_password ?? ''), s.user.password_hash))) throw new HttpError(400, 'bad_password', 'Your current password isn’t right.');
        this.store.updateUser(s.user.id, { password_hash: await this.password(body.password) });
      }
      return json({ ok: true });
    }
    if (path === '/v1/console/account/switch' && method === 'POST') {
      const id = String(body.workspace_id ?? '');
      if (!this.store.member(id, s.user.id)) throw new HttpError(404, 'not_found', 'You’re not in that workspace.');
      this.store.setSessionWorkspace(s.token, id);
      const h = new Headers();
      for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, this.secure(req)));
      return json({ ok: true }, 200, h);
    }
    // `npx lumniverse try`: its sample agents run in a workspace of their own, made on first use.
    if (path === '/v1/console/account/sample' && method === 'POST') {
      let id = this.store.membershipsOf(s.user.id).find((m) => m.sample && m.role === 'owner')?.workspace_id;
      if (!id) {
        if (!this.env.platformKey) throw new HttpError(501, 'no_sample_workspace', 'This console can’t make a separate sample workspace, so the sample agents run here.');
        this.limit(`sample:${s.user.id}`, 5, 60 * 60_000);
        id = (await this.newWorkspace(SAMPLE_WORKSPACE, s.user, { sample: true })).id;
      }
      this.store.setSessionWorkspace(s.token, id);
      const h = new Headers();
      for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, this.secure(req)));
      return json({ ok: true, workspace_id: id }, 200, h);
    }
    if (path === '/v1/console/account/sample' && method === 'DELETE') {
      const mine = this.store.membershipsOf(s.user.id);
      const sample = mine.find((m) => m.sample && m.role === 'owner');
      if (!sample) throw new HttpError(404, 'not_found', 'There’s no sample workspace to remove.');
      this.store.deleteWorkspace(sample.workspace_id);
      const real = mine.find((m) => !m.sample);
      if (real) this.store.setSessionWorkspace(s.token, real.workspace_id);
      const h = new Headers();
      for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, this.secure(req)));
      return json({ ok: true, workspace_id: real?.workspace_id ?? null }, 200, h);
    }
    if (path === '/v1/console/account/accept' && method === 'POST') {
      const id = this.join(s.user, String(body.token ?? ''));
      this.store.setSessionWorkspace(s.token, id);
      const h = new Headers();
      for (const k of [SESSION, CSRF, WS]) h.append('set-cookie', clearCookie(k, this.secure(req)));
      return json({ ok: true, workspace_id: id }, 200, h);
    }
    if (path === '/v1/console/billing' && method === 'GET') {
      return json(this.billing ? await this.billing.view(s.workspace, this.canManage(s.member)) : { enabled: false });
    }
    if ((path === '/v1/console/billing/checkout' || path === '/v1/console/billing/portal') && method === 'POST') {
      if (!this.billing) throw new HttpError(404, 'not_found', 'This console doesn’t bill.');
      if (!this.canManage(s.member)) throw new HttpError(403, 'forbidden', 'Only owners and admins can change the plan.');
      return json(path.endsWith('checkout') ? await this.billing.checkout(s.workspace, s.user, this.origin(req)) : await this.billing.portal(s.workspace, this.origin(req)));
    }
    if (path === '/v1/console/workspace' && method === 'PATCH') {
      if (!this.canManage(s.member)) throw new HttpError(403, 'forbidden', 'Only owners and admins can rename the workspace.');
      const name = text(body.name, 80);
      if (!name) throw new HttpError(400, 'no_name', 'Enter a name.');
      this.store.renameWorkspace(s.workspace.id, name);
      return json({ ok: true });
    }

    // People.
    if (path === '/v1/console/members' && method === 'GET') {
      const manage = this.canManage(s.member);
      return json({
        me: { id: s.user.id, role: s.member.role, can_manage: manage },
        members: this.store.members(s.workspace.id).map((m) => ({ id: m.user_id, name: m.name, email: m.email, role: m.role, initials: initialsOf(m.name, m.email), joined_at: new Date(m.created_at).toISOString(), last_seen: m.last_seen ? new Date(m.last_seen).toISOString() : null })),
        invites: manage ? this.store.pendingInvites(s.workspace.id, now).map((i) => ({ id: i.id, email: i.email, role: i.role, expires_at: new Date(i.expires_at).toISOString() })) : [],
      });
    }
    if (!this.canManage(s.member)) throw new HttpError(403, 'forbidden', 'Only owners and admins can change who’s in the workspace.');
    const role = (v: unknown): Role => {
      if (!ROLES.includes(v as Role)) throw new HttpError(400, 'bad_role', 'Pick owner, admin, member or viewer.');
      if (v === 'owner' && s.member.role !== 'owner') throw new HttpError(403, 'forbidden', 'Only an owner can make someone an owner.');
      return v as Role;
    };
    if (path === '/v1/console/members/invite' && method === 'POST') {
      const email = body.email ? String(body.email) : null;
      if (email && !emailOk(email)) throw new HttpError(400, 'bad_email', 'Enter a valid email, or leave it empty for a link anyone can use once.');
      if (email && this.store.userByEmail(email) && this.store.member(s.workspace.id, this.store.userByEmail(email)!.id)) throw new HttpError(409, 'already_member', 'They’re already in this workspace.');
      const { invite, token } = this.store.createInvite(s.workspace.id, email, role(body.role ?? 'member'), s.user.id, now, INVITE_TTL);
      return json({ id: invite.id, link: `${this.origin(req)}/sign-in?invite=${token}`, expires_at: new Date(invite.expires_at).toISOString() });
    }
    const inv = /^\/v1\/console\/members\/invites\/([^/]+)$/.exec(path);
    if (inv && method === 'DELETE') {
      this.store.deleteInvite(s.workspace.id, decodeURIComponent(inv[1]!));
      return json({ ok: true });
    }
    const one = /^\/v1\/console\/members\/([^/]+)(\/reset)?$/.exec(path);
    if (one) {
      const id = decodeURIComponent(one[1]!);
      const target = this.store.member(s.workspace.id, id);
      if (!target) throw new HttpError(404, 'not_found', 'They’re not in this workspace.');
      if (target.role === 'owner' && s.member.role !== 'owner') throw new HttpError(403, 'forbidden', 'Only an owner can change an owner.');
      if (one[2] && method === 'POST') {
        const token = this.store.createReset(id, s.user.id, now, RESET_TTL);
        return json({ link: `${this.origin(req)}/sign-in?reset=${token}`, expires_at: new Date(now + RESET_TTL).toISOString() });
      }
      if (method === 'PATCH') {
        const next = role(body.role);
        if (target.role === 'owner' && next !== 'owner' && this.store.owners(s.workspace.id) <= 1) throw new HttpError(409, 'last_owner', 'A workspace needs an owner. Make someone else an owner first.');
        await this.revokeMemberKey(s.workspace, target);
        this.store.setRole(s.workspace.id, id, next);
        return json({ ok: true });
      }
      if (method === 'DELETE') {
        if (target.role === 'owner' && this.store.owners(s.workspace.id) <= 1) throw new HttpError(409, 'last_owner', 'A workspace needs an owner. Make someone else an owner first.');
        await this.revokeMemberKey(s.workspace, target);
        this.store.removeMember(s.workspace.id, id);
        return json({ ok: true });
      }
    }
    throw new HttpError(404, 'not_found', 'No such route.');
  }
}
