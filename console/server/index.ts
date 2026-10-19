// The console’s own server, for real data. It answers /console/config.json,
// signs in through the Go API, builds /v1/console/* views, and passes every
// other /v1 call through. With demo data the page answers /v1 itself, so none
// of this runs.

import { publicConfig, type Env } from './env';
import { HttpError, cookies, json, problem } from './http';
import { Memory, consoleRoute } from './console/router';
import { Accounts } from './accounts/service';
import { DeviceLogin } from './device';
import { Onboarding } from './onboarding';
import { proxy } from './proxy';
import { SessionBridge, meOf } from './session';
import { Upstream, type Fetch } from './upstream';

export interface Bff {
  /** A response for console and API paths, or null for the app to serve. */
  handle(req: Request, keepOpen?: () => void): Promise<Response | null>;
}

const READS = new Set(['GET', 'HEAD', 'OPTIONS']);

export function createBff(env: Env, fetchFn: Fetch = fetch): Bff {
  const up = new Upstream(env.api, fetchFn);
  const bridge = new SessionBridge(env, up, fetchFn);
  const hosts = new Set(env.hosts.map((h) => h.toLowerCase()));
  const memory = new Memory();
  const onboarding = new Onboarding(up);
  memory.onboarding = onboarding;
  // People sign in as themselves (production), or with a key (development).
  const accounts = env.auth === 'accounts' ? new Accounts(env, up, fetchFn) : null;
  if (accounts) {
    const sweep = setInterval(() => {
      accounts.store.sweep(Date.now());
      // Trials that ended, plans to retry, usage for Stripe.
      void accounts.billing?.sweep().catch((e) => console.error('billing sweep failed', e));
    }, 3_600_000);
    (sweep as { unref?: () => void }).unref?.();
  }
  const device = new DeviceLogin(onboarding);
  memory.device = device;
  // Unused codes' keys are revoked within a minute of expiring.
  const sweeper = setInterval(() => void onboarding.sweep().catch(() => {}), 60_000);
  (sweeper as { unref?: () => void }).unref?.();

  // Real data is only on offer when the API answers. Asked at most every 15 s.
  let health: { at: number; ok: Promise<boolean> } | null = null;
  const reachable = () => {
    if (health && Date.now() - health.at < 15_000) return health.ok;
    const ok = fetchFn(`${env.api}/v1/health/ready`, { signal: AbortSignal.timeout(1500) })
      .then((r) => r.ok)
      .catch(() => false);
    health = { at: Date.now(), ok };
    return ok;
  };

  return {
    async handle(req, keepOpen) {
      const url = new URL(req.url);
      const path = url.pathname;
      const ours = path === '/console/config.json' || path === '/console/onboarding/exchange' || path === '/console/device' || path === '/console/device/token' || path.startsWith('/console/auth/') || path === '/console/billing/webhook' || path.startsWith('/v1/');
      if (!ours) return null;

      // Another name pointed at this machine can’t borrow the admin session.
      const host = (req.headers.get('host') ?? url.host).toLowerCase();
      if (!hosts.has(host)) {
        return problem(new HttpError(421, 'unknown_host', `This console answers on ${env.hosts[0]}. Set LUMNI_CONSOLE_HOSTS to add ${host}.`));
      }
      // Writes come from the console’s own pages.
      const origin = req.headers.get('origin');
      if (!READS.has(req.method.toUpperCase()) && origin) {
        let from = '';
        try {
          from = new URL(origin).host.toLowerCase();
        } catch {
          from = '';
        }
        if (!hosts.has(from)) return problem(new HttpError(403, 'foreign_origin', 'Writes have to come from the console itself.'));
      }

      // The agent's side of onboarding: no session, the code is the credential.
      if (path === '/console/onboarding/exchange') {
        if (req.method.toUpperCase() !== 'POST') return problem(new HttpError(405, 'method_not_allowed', 'POST a code here.'));
        try {
          const body = (await req.json().catch(() => ({}))) as Record<string, unknown>;
          const address = (req.headers.get('x-forwarded-for') ?? '').split(',')[0]!.trim() || req.headers.get('x-real-ip') || 'peer';
          const out = onboarding.exchange(address, body);
          return json({ ...out, api_url: env.publicApi, workspace: env.workspace }, 200);
        } catch (e) {
          return problem(e instanceof HttpError ? e : new HttpError(500, 'internal', 'The code couldn’t be checked.'));
        }
      }
      // Browser login for the onboarding command: begin, then poll. No sign-in; the device secret is the credential.
      if (path === '/console/device' || path === '/console/device/token') {
        if (req.method.toUpperCase() !== 'POST') return problem(new HttpError(405, 'method_not_allowed', 'POST here.'));
        try {
          const body = (await req.json().catch(() => ({}))) as Record<string, unknown>;
          if (path === '/console/device') {
            const address = (req.headers.get('x-forwarded-for') ?? '').split(',')[0]!.trim() || req.headers.get('x-real-ip') || 'peer';
            return json(device.start(address, body, env.publicConsole || `${url.protocol}//${host}`), 200);
          }
          return json({ ...device.token(body), api_url: env.publicApi, workspace: env.workspace }, 200);
        } catch (e) {
          return problem(e instanceof HttpError ? e : new HttpError(500, 'internal', 'The login couldn’t be read.'));
        }
      }
      if (path === '/console/config.json') {
        return new Response(JSON.stringify(publicConfig(env, await reachable())), { headers: { 'content-type': 'application/json', 'cache-control': 'no-store' } });
      }
      if (accounts?.owns(path)) return accounts.handle(req);
      if (path.startsWith('/console/auth/')) return problem(new HttpError(404, 'not_found', 'This console signs in with keys (LUMNI_CONSOLE_AUTH=key).'));
      if (path === '/v1/auth/session') return bridge.handle(req);
      if (path.startsWith('/v1/console/')) return consoleRoute(req, up, env, accounts ? (r) => accounts.me(r) ?? meOf(r) : meOf, memory);
      // A write through to the API makes this session's cached reads stale.
      if (!READS.has(req.method.toUpperCase())) {
        const session = cookies(req).lumni_session;
        if (session) up.forget(session);
      }
      return proxy(req, env.api, fetchFn, keepOpen);
    },
  };
}
