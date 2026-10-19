// Signing in with real data. The Go API turns a key into a browser session:
// POST /v1/auth/session with the key as a Bearer token sets an HttpOnly
// cookie and returns a CSRF token. This bridge does that exchange on the
// server, with the admin key from the environment or a key you paste, and
// keeps the CSRF token in a second HttpOnly cookie, since the API’s own GET
// doesn’t return it. Your browser still sends the token on every write, and
// the API still checks it.
//
// GET never answers 401: a 401 there would sign the page out again and again.

import type { Session } from '../src/domain/types';
import type { Env } from './env';
import { HttpError, clearCookie, cookies, json, problem, readSetCookie, setCookie } from './http';
import { read, unreachable, type Fetch, type Upstream } from './upstream';
import type { GoSession } from './console/go';

const SESSION = 'lumni_session';
const CSRF = 'lumni_csrf';
/** Set when you sign out, so the server doesn’t sign you straight back in. */
const OUT = 'lumni_signed_out';
/** How you signed in: with the admin key, or a key you pasted. */
const HOW = 'lumni_how';

export interface Me {
  initials: string;
  email: string;
}

export function meOf(req: Request): Me {
  return cookies(req)[HOW] === 'key' ? { initials: 'K', email: 'Signed in with an API key' } : { initials: 'AD', email: 'Signed in as the admin' };
}

function sessionJson(env: Env, tenant: string, csrf: string | null, how: string | undefined): Session {
  const key = how === 'key';
  return {
    signed_in: true,
    csrf_token: csrf ?? undefined,
    user: key ? { id: 'key', initials: 'K', email: 'Signed in with an API key' } : { id: 'admin', initials: 'AD', email: 'Signed in as the admin' },
    tenant: { id: tenant, name: env.workspace ?? 'Your workspace' },
  };
}

export class SessionBridge {
  constructor(
    private readonly env: Env,
    private readonly up: Upstream,
    private readonly fetchFn: Fetch,
  ) {}

  async handle(req: Request): Promise<Response> {
    const method = req.method.toUpperCase();
    try {
      if (method === 'GET') return await this.get(req);
      if (method === 'POST') return await this.post(req);
      if (method === 'DELETE') return await this.del(req);
      return problem(new HttpError(405, 'method_not_allowed', `${method} isn’t allowed here.`));
    } catch (e) {
      if (e instanceof HttpError) return problem(e);
      console.error('session bridge failed', e);
      return problem(new HttpError(500, 'internal', 'Signing in didn’t work. The server log has the details.'));
    }
  }

  private secure(req: Request): boolean {
    return new URL(req.url).protocol === 'https:';
  }

  private async get(req: Request): Promise<Response> {
    const c = cookies(req);
    const secure = this.secure(req);
    if (c[SESSION]) {
      try {
        const s = await this.up.get<GoSession>('/v1/auth/session', { session: c[SESSION]!, refresh: true });
        // A session made elsewhere has no token here, so writes would fail; make a fresh one if the server can.
        if (c[CSRF] || !this.canSignIn(c)) return json(sessionJson(this.env, s.tenant_id, c[CSRF] ?? null, c[HOW]));
      } catch (e) {
        if (!(e instanceof HttpError) || e.status !== 401) throw e;
        this.up.forget(c[SESSION]!);
      }
    }
    if (this.canSignIn(c)) return this.exchange(this.env.adminKey, 'admin', secure);
    const headers = new Headers();
    if (c[SESSION]) {
      headers.append('set-cookie', clearCookie(SESSION, secure));
      headers.append('set-cookie', clearCookie(CSRF, secure));
    }
    return json({ signed_in: false } satisfies Session, 200, headers);
  }

  private canSignIn(c: Record<string, string>): boolean {
    return this.env.autoSignIn && !!this.env.adminKey && !c[OUT];
  }

  private async post(req: Request): Promise<Response> {
    const text = await req.text();
    let body: { key?: unknown } = {};
    if (text) {
      try {
        body = JSON.parse(text) as { key?: unknown };
      } catch {
        throw new HttpError(400, 'invalid_json', 'The request body isn’t valid JSON.');
      }
    }
    const pasted = typeof body.key === 'string' ? body.key.trim() : '';
    if (pasted) return this.exchange(pasted, 'key', this.secure(req));
    if (!this.env.adminKey) {
      throw new HttpError(400, 'no_admin_key', 'This console has no admin key. Paste an API key, or set LUMNI_CONSOLE_ADMIN_KEY in console/.env.');
    }
    return this.exchange(this.env.adminKey, 'admin', this.secure(req));
  }

  /** Trade a key for a session, and keep both cookies on this origin. */
  private async exchange(key: string, how: 'admin' | 'key', secure: boolean): Promise<Response> {
    let res: Response;
    try {
      res = await this.fetchFn(`${this.env.api}/v1/auth/session`, { method: 'POST', headers: { authorization: `Bearer ${key}`, accept: 'application/json' } });
    } catch {
      throw unreachable(this.env.api);
    }
    if (res.status === 401) {
      // Not a 401: that would sign the page out, and this page is signing in.
      throw new HttpError(400, 'invalid_key', how === 'admin' ? 'The admin key in console/.env isn’t one the API knows.' : 'That key isn’t one the API knows.');
    }
    const s = await read<GoSession>(res);
    const token = readSetCookie(res.headers.getSetCookie(), SESSION);
    if (!token?.value || !s.csrf_token) throw new HttpError(502, 'BAD_UPSTREAM', 'The API made a session but didn’t hand it over.');
    const maxAge = token.maxAge ?? 8 * 3600;
    const headers = new Headers();
    headers.append('set-cookie', setCookie(SESSION, token.value, { maxAge, secure }));
    headers.append('set-cookie', setCookie(CSRF, s.csrf_token, { maxAge, secure }));
    headers.append('set-cookie', setCookie(HOW, how, { maxAge, secure }));
    headers.append('set-cookie', clearCookie(OUT, secure));
    return json(sessionJson(this.env, s.tenant_id, s.csrf_token, how), 200, headers);
  }

  private async del(req: Request): Promise<Response> {
    const c = cookies(req);
    const secure = this.secure(req);
    if (c[SESSION]) {
      // Revoke it at the API too. Whatever it says, this browser is signed out.
      await this.up.send('DELETE', '/v1/auth/session', undefined, { session: c[SESSION]!, csrf: req.headers.get('x-csrf-token') }).catch(() => {});
      this.up.forget(c[SESSION]!);
    }
    const headers = new Headers();
    headers.append('set-cookie', clearCookie(SESSION, secure));
    headers.append('set-cookie', clearCookie(CSRF, secure));
    headers.append('set-cookie', clearCookie(HOW, secure));
    headers.append('set-cookie', setCookie(OUT, '1', { secure }));
    return json({ signed_in: false } satisfies Session, 200, headers);
  }
}
