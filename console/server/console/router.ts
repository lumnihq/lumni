// /v1/console/* with real data: the same views and writes the demo answers in
// the page, built from the Go API for whoever is signed in.

import { makeZone } from '../../src/domain/format';
import type { Env } from '../env';
import { HttpError, cookies, json, notYet, problem } from '../http';
import type { Upstream } from '../upstream';
import { Ctx, newTenantState, type TenantState } from './load';
import { appsView } from './views/apps';
import { frameView } from './views/frame';
import { liveView } from './views/live';
import { mapView } from './views/map';
import { needsView } from './views/needs';
import { receiptDetail, receiptsView, type Query } from './views/receipts';
import { rulesView, watchView } from './views/rules';
import { spendView } from './views/spend';
import { storyView } from './views/story';
import { todayView } from './views/today';
import { addRule, editRule, protect, setChecker, setRuleMode } from './writes';
import { worldView } from './views/world';
import { journeyView } from './views/journey';
import { pulseView } from './views/pulse';
import type { Onboarding } from '../onboarding';
import type { DeviceLogin } from '../device';

interface Req {
  ctx: Ctx;
  onboarding: Onboarding | null;
  device: DeviceLogin | null;
  query: URLSearchParams;
  body: Record<string, unknown>;
  params: string[];
  me: { initials: string; email: string };
}

type Handler = (r: Req) => Promise<unknown> | unknown;

const routes: { method: string; re: RegExp; run: Handler }[] = [];
const on = (method: string, pattern: string, run: Handler) => {
  routes.push({ method, re: new RegExp(`^${pattern.replace(/:[a-z]+/g, '([^/]+)')}$`), run });
};

const num = (q: URLSearchParams, k: string) => {
  const v = q.get(k);
  return v === null || v === '' || !Number.isFinite(Number(v)) ? null : Number(v);
};
const FILTERS = ['checked', 'none', 'stopped', 'waiting', 'bad', 'stepins'] as const;

on('GET', '/v1/console/world', (r) => worldView(r.ctx));
on('POST', '/v1/console/onboarding/codes', (r) => {
  if (!r.onboarding) throw notYet('Onboarding codes');
  return r.onboarding.create(r.ctx.who, r.ctx.tenant, r.body);
});
on('GET', '/v1/console/onboarding/codes/:id', (r) => {
  if (!r.onboarding) throw notYet('Onboarding codes');
  return r.onboarding.status(r.params[0]!, r.ctx.tenant, r.ctx.who);
});
on('GET', '/v1/console/device', (r) => {
  if (!r.device) throw notYet('Browser login');
  return { items: r.device.approvedSince(r.ctx.tenant, Number(r.query.get('since') ?? 0)) };
});
on('GET', '/v1/console/device/:code', (r) => {
  if (!r.device) throw notYet('Browser login');
  return r.device.lookup(r.params[0]!);
});
on('POST', '/v1/console/device/:code/approve', (r) => {
  if (!r.device) throw notYet('Browser login');
  return r.device.approve(r.params[0]!, { ...r.ctx.who, name: r.me.email || r.me.initials }, r.ctx.tenant, r.body);
});
on('POST', '/v1/console/device/:code/deny', (r) => {
  if (!r.device) throw notYet('Browser login');
  return r.device.deny(r.params[0]!);
});
on('GET', '/v1/console/journey/:id', (r) => journeyView(r.ctx, r.params[0]!));
on('GET', '/v1/console/pulse', (r) => pulseView(r.ctx, r.query.get('after') ? Number(r.query.get('after')) : null));
on('GET', '/v1/console/frame', (r) => frameView(r.ctx, r.me));
on('GET', '/v1/console/today', (r) => todayView(r.ctx, r.query.get('day') === 'yesterday' ? 'yesterday' : 'today'));
on('GET', '/v1/console/live', (r) => liveView(r.ctx));
on('GET', '/v1/console/map', (r) => mapView(r.ctx, null));
on('GET', '/v1/console/map/:id', (r) => mapView(r.ctx, r.params[0]!));
on('GET', '/v1/console/receipts', (r) => {
  const q = r.query;
  const filter = q.get('filter');
  const query: Query = {
    day: q.get('day') === 'yesterday' ? 'yesterday' : 'today',
    filter: (FILTERS as readonly string[]).includes(filter ?? '') ? (filter as Query['filter']) : null,
    q: q.get('q') ?? '',
    ai: q.get('ai') || null,
    app: q.get('app') || null,
    from: num(q, 'from'),
    to: num(q, 'to'),
    limit: num(q, 'limit') ?? 0,
    focus: q.get('focus'),
  };
  return receiptsView(r.ctx, query);
});
on('GET', '/v1/console/receipts/:id', (r) => receiptDetail(r.ctx, r.params[0]!));
on('GET', '/v1/console/story/:id', (r) => storyView(r.ctx, r.params[0]!));
on('GET', '/v1/console/needs-you', (r) => needsView(r.ctx));
on('GET', '/v1/console/rules', (r) => rulesView(r.ctx));
on('GET', '/v1/console/watch-report', (r) => watchView(r.ctx));
on('GET', '/v1/console/spend', (r) => spendView(r.ctx));
on('GET', '/v1/console/apps', (r) => appsView(r.ctx));
on('GET', '/v1/console/playground', () => {
  throw notYet('The Playground');
});

on('POST', '/v1/console/seen', (r) => {
  r.ctx.state.seen.add(String(r.body.screen ?? ''));
  return { ok: true };
});
on('PUT', '/v1/console/rules/:id/mode', (r) => setRuleMode(r.ctx, r.params[0]!, r.body.mode));
on('POST', '/v1/console/rules/:id/edit', (r) => editRule(r.ctx, r.params[0]!, r.body));
on('POST', '/v1/console/rules', (r) => addRule(r.ctx, r.body.template));
on('POST', '/v1/console/protect', (r) => protect(r.ctx, r.body.rules));
on('POST', '/v1/console/apps/:app/checker', (r) => setChecker(r.ctx, r.params[0]!, r.body.on));

// Nothing in the API does these yet.
on('POST', '/v1/console/pause', () => {
  throw notYet('Pausing AIs');
});
on('PUT', '/v1/console/spend-limit', () => {
  throw notYet('A daily spend limit');
});
on('PUT', '/v1/console/notify', () => {
  throw notYet('Choosing where Lumni asks you');
});
on('POST', '/v1/console/needs/:id/handled', () => {
  throw notYet('Marking something handled');
});
on('POST', '/v1/console/map/:key/retry', () => {
  throw notYet('Retrying one app');
});
on('POST', '/v1/console/playground/:id/run', () => {
  throw notYet('The Playground');
});
on('POST', '/v1/console/demo/clock', () => {
  throw notYet('The demo clock');
});
on('POST', '/v1/console/demo/reset', () => {
  throw notYet('Resetting the demo');
});

// ─── per workspace, while the server runs ──────────────

/** What one server remembers: each workspace’s state, and which workspace each session is in. */
export class Memory {
  private tenants = new Map<string, TenantState>();
  /** Onboarding codes, when this server hands them out. */
  onboarding: Onboarding | null = null;
  device: DeviceLogin | null = null;
  /** Which workspace a session belongs to never changes, so it’s asked once. */
  readonly tenantOf = new Map<string, Promise<string>>();

  stateFor(tenant: string): TenantState {
    let s = this.tenants.get(tenant);
    if (!s) {
      s = newTenantState();
      this.tenants.set(tenant, s);
    }
    return s;
  }
}

export interface Who {
  initials: string;
  email: string;
}

export async function consoleRoute(req: Request, up: Upstream, env: Env, me: (req: Request) => Who, memory: Memory): Promise<Response> {
  const tenantOf = memory.tenantOf;
  const url = new URL(req.url);
  const method = req.method.toUpperCase();
  const route = routes.find((r) => r.method === method && r.re.test(url.pathname));
  if (!route) {
    const known = routes.some((r) => r.re.test(url.pathname));
    return problem(known ? new HttpError(405, 'method_not_allowed', `${method} isn’t allowed here.`) : new HttpError(404, 'not_found', `No route for ${url.pathname}.`));
  }
  const session = cookies(req).lumni_session;
  if (!session) return problem(new HttpError(401, 'UNAUTHENTICATED', 'Sign in to continue.'));
  const caller = { session, csrf: req.headers.get('x-csrf-token'), refresh: req.headers.has('x-lumni-refresh') };

  try {
    let tenant = tenantOf.get(session);
    if (!tenant) {
      tenant = up.get<{ tenant_id: string }>('/v1/auth/session', caller).then((s) => s.tenant_id);
      tenantOf.set(session, tenant);
      tenant.catch(() => tenantOf.delete(session));
      if (tenantOf.size > 1000) tenantOf.delete(tenantOf.keys().next().value!);
    }
    const t = await tenant;
    let body: Record<string, unknown> = {};
    if (method !== 'GET') {
      const text = await req.text();
      if (text) {
        try {
          body = JSON.parse(text) as Record<string, unknown>;
        } catch {
          throw new HttpError(400, 'invalid_json', 'The request body isn’t valid JSON.');
        }
      }
    }
    // Rounded to the second, so views built together read the same snapshots.
    const now = Math.floor(Date.now() / 1000) * 1000;
    const ctx = new Ctx(up, caller, t, memory.stateFor(t), makeZone(req.headers.get('x-lumni-tz')), now, env);
    const params = route.re.exec(url.pathname)!.slice(1).map(decodeURIComponent);
    const out = await route.run({ ctx, onboarding: memory.onboarding, device: memory.device, query: url.searchParams, body, params, me: me(req) });
    const headers: Record<string, string> = { 'x-demo-now': String(Date.now()), 'x-demo-paused': '0' };
    if (ctx.partial.length) headers['x-lumni-partial'] = encodeURIComponent(ctx.partial.join(' '));
    return json(out, 200, headers);
  } catch (e) {
    if (e instanceof HttpError) {
      if (e.status === 401) tenantOf.delete(session);
      return problem(e);
    }
    console.error('console route failed', url.pathname, e);
    return problem(new HttpError(500, 'internal', 'Something went wrong building this page. The server log has the details.'));
  }
}
