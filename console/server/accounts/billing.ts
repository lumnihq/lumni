// Lumni Cloud's billing. Every workspace made by sign-up gets a 14-day trial,
// then the Team plan through Stripe ($49 a month with 50,000 checked actions,
// then metered), or becomes watch-only if it doesn't subscribe. Stripe holds
// the products and prices (ops/stripe-setup.mjs makes them); this keeps each
// workspace's state, puts the plan into effect on Lumni's API
// (PUT /v1/tenants/{id}/plan), and reports checked actions to Stripe's meter.
// Off unless LUMNI_CONSOLE_STRIPE_* are set, so self-hosted consoles never bill.
//
//   GET  /v1/console/billing            the plan, trial days left, usage
//   POST /v1/console/billing/checkout   a Stripe Checkout link (owner, admin)
//   POST /v1/console/billing/portal     Stripe's billing portal (owner, admin)
//   POST /console/billing/webhook       Stripe's events, signed

import type { Env } from '../env';
import { HttpError, json } from '../http';
import type { Fetch } from '../upstream';
import type { BillingRow, Store, User, Workspace } from './store';

const DAY = 86_400_000;
export const TRIAL_DAYS = 14;

/** What each state allows on the API. */
export const PLANS = {
  trial: { limits: { checks_per_day: 2000, checks_per_second: 10 }, watch_only: false },
  paid: { limits: { checks_per_day: 1_000_000, checks_per_second: 50 }, watch_only: false },
  watch: { limits: { checks_per_day: 500, checks_per_second: 10 }, watch_only: true },
} as const;
type Plan = keyof typeof PLANS;

/** What the console shows; Stripe's prices are the source of truth for charges. */
export const TEAM = { name: 'Team', price: '$49 a month', included: 50_000, overage: '$3 per 1,000 more checks' };

/** Usage is reported for windows ending this long ago, so the last checks have landed. */
const SETTLE = 2 * 60_000;

const enc = new TextEncoder();
const hex = (b: ArrayBuffer) => Array.from(new Uint8Array(b), (x) => x.toString(16).padStart(2, '0')).join('');
const same = (a: string, b: string) => {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
};

/** Stripe's form encoding for flat keys (`line_items[0][price]` and so on). */
const form = (fields: Record<string, string | undefined>) =>
  Object.entries(fields).filter(([, v]) => v !== undefined).map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v!)}`).join('&');

export class Billing {
  constructor(
    private readonly env: Env,
    private readonly store: Store,
    private readonly fetchFn: Fetch,
    private readonly now: () => number = Date.now,
  ) {}

  private get stripeEnv() {
    return this.env.stripe!;
  }

  // ─── a new workspace ─────────────────────────────────

  /** Starts the trial. The API made the workspace with trial limits already. */
  startTrial(workspace: Workspace): void {
    const now = this.now();
    this.store.startTrial(workspace.id, now, now + TRIAL_DAYS * DAY, 'trial');
  }

  // ─── what the console shows ──────────────────────────

  async view(workspace: Workspace, canManage: boolean) {
    const b = this.store.billing(workspace.id);
    if (!b) return { enabled: true, status: 'exempt' as const };
    const now = this.now();
    const paying = b.status === 'active' || b.status === 'past_due';
    const from = paying ? new Date(new Date(now).toISOString().slice(0, 8) + '01T00:00:00Z').getTime() : new Date(new Date(now).toISOString().slice(0, 10) + 'T00:00:00Z').getTime();
    const checks = await this.usage(workspace.tenant_id, from, now).catch(() => null);
    return {
      enabled: true,
      status: b.status,
      can_manage: canManage,
      trial_ends_at: new Date(b.trial_ends_at).toISOString(),
      days_left: b.status === 'trial' ? Math.max(0, Math.ceil((b.trial_ends_at - now) / DAY)) : 0,
      plan: TEAM,
      limits: PLANS[this.desired(b)].limits,
      watch_only: this.desired(b) === 'watch',
      usage: { checks, since: new Date(from).toISOString(), period: paying ? 'month' : 'day' },
      has_customer: Boolean(b.stripe_customer),
      cancel_at: b.cancel_at ? new Date(b.cancel_at).toISOString() : null,
    };
  }

  // ─── Stripe ──────────────────────────────────────────

  private async stripe<T>(path: string, fields: Record<string, string | undefined>): Promise<T> {
    let res: Response;
    try {
      res = await this.fetchFn(`https://api.stripe.com/v1${path}`, {
        method: 'POST',
        headers: { authorization: `Bearer ${this.stripeEnv.secretKey}`, 'content-type': 'application/x-www-form-urlencoded' },
        body: form(fields),
      });
    } catch {
      throw new HttpError(502, 'stripe_unreachable', 'Stripe can’t be reached right now. Try again in a minute.');
    }
    const data = (await res.json().catch(() => ({}))) as { error?: { message?: string } } & T;
    if (!res.ok) throw new HttpError(502, 'stripe_error', data.error?.message ?? `Stripe answered ${res.status}.`);
    return data;
  }

  /** A Checkout page for the Team plan: the monthly price and the metered one. */
  async checkout(workspace: Workspace, user: User, origin: string): Promise<{ url: string }> {
    const b = this.store.billing(workspace.id);
    if (!b) throw new HttpError(400, 'not_billed', 'This workspace isn’t on a plan.');
    if (b.status === 'active' || b.status === 'past_due') throw new HttpError(409, 'already_subscribed', 'This workspace is subscribed already. Manage it from the billing portal.');
    const s = await this.stripe<{ url: string }>('/checkout/sessions', {
      mode: 'subscription',
      'line_items[0][price]': this.stripeEnv.pricePlan,
      'line_items[0][quantity]': '1',
      'line_items[1][price]': this.stripeEnv.priceUsage,
      client_reference_id: workspace.id,
      'metadata[workspace]': workspace.id,
      'subscription_data[metadata][workspace]': workspace.id,
      ...(b.stripe_customer ? { customer: b.stripe_customer } : { customer_email: user.email }),
      allow_promotion_codes: 'true',
      success_url: `${origin}/settings?billing=done#sec-plan`,
      cancel_url: `${origin}/settings#sec-plan`,
    });
    return { url: s.url };
  }

  /** Stripe's own page for cards, invoices and cancelling. */
  async portal(workspace: Workspace, origin: string): Promise<{ url: string }> {
    const b = this.store.billing(workspace.id);
    if (!b?.stripe_customer) throw new HttpError(400, 'no_customer', 'Subscribe first; the billing portal opens after.');
    const s = await this.stripe<{ url: string }>('/billing_portal/sessions', { customer: b.stripe_customer, return_url: `${origin}/settings#sec-plan` });
    return { url: s.url };
  }

  // ─── Stripe's events ─────────────────────────────────

  /** Checks Stripe-Signature (HMAC-SHA256 of `t.body`, within five minutes). */
  private async verified(req: Request): Promise<{ type: string; data: { object: Record<string, unknown> } }> {
    const body = await req.text();
    const header = req.headers.get('stripe-signature') ?? '';
    const parts = header.split(',').map((p) => p.split('='));
    const t = parts.find(([k]) => k === 't')?.[1];
    const sigs = parts.filter(([k]) => k === 'v1').map(([, v]) => v ?? '');
    const secret = this.stripeEnv.webhookSecret;
    if (!secret || !t || !sigs.length) throw new HttpError(400, 'bad_signature', 'Missing Stripe signature.');
    if (Math.abs(this.now() / 1000 - Number(t)) > 300) throw new HttpError(400, 'bad_signature', 'Stale Stripe event.');
    const key = await crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    const want = hex(await crypto.subtle.sign('HMAC', key, enc.encode(`${t}.${body}`)));
    if (!sigs.some((s) => same(s, want))) throw new HttpError(400, 'bad_signature', 'Stripe signature doesn’t match.');
    try {
      return JSON.parse(body);
    } catch {
      throw new HttpError(400, 'bad_event', 'Unreadable event.');
    }
  }

  async webhook(req: Request): Promise<Response> {
    const event = await this.verified(req);
    const o = event.data?.object ?? {};
    const meta = (o.metadata ?? {}) as Record<string, string>;
    const now = this.now();
    if (event.type === 'checkout.session.completed') {
      const id = String(o.client_reference_id ?? meta.workspace ?? '');
      if (this.store.billing(id)) {
        this.store.setBilling(id, { status: 'active', stripe_customer: String(o.customer ?? '') || null, stripe_subscription: String(o.subscription ?? '') || null, reported_until: now }, now);
        await this.settle(id);
      }
    } else if (event.type.startsWith('customer.subscription.')) {
      const sub = String(o.id ?? '');
      const row = this.store.billingBySubscription(sub) ?? (meta.workspace ? this.store.billing(meta.workspace) : null);
      if (row) {
        const status = event.type === 'customer.subscription.deleted' ? 'canceled' : String(o.status ?? '');
        const next: BillingRow['status'] | null =
          status === 'active' || status === 'trialing' ? 'active'
            : status === 'past_due' ? 'past_due'
              : status === 'canceled' || status === 'unpaid' || status === 'incomplete_expired' ? (row.trial_ends_at > now ? 'trial' : 'watch')
                : null;
        if (next) {
          const starting = next === 'active' && row.status !== 'active' && row.status !== 'past_due';
          // Set to end (cancelled at period end, or on a date): when.
          const items = (o.items as { data?: { current_period_end?: number }[] } | undefined)?.data;
          const end = typeof o.cancel_at === 'number' ? o.cancel_at : o.cancel_at_period_end ? Number(o.current_period_end ?? items?.[0]?.current_period_end ?? 0) || null : null;
          const cancelAt = next === 'active' || next === 'past_due' ? (end ? end * 1000 : null) : null;
          this.store.setBilling(row.workspace_id, { status: next, cancel_at: cancelAt, stripe_subscription: sub || row.stripe_subscription, stripe_customer: String(o.customer ?? '') || row.stripe_customer, ...(starting ? { reported_until: now } : {}) }, now);
          await this.settle(row.workspace_id);
        }
      }
    }
    return json({ received: true });
  }

  // ─── putting a plan into effect ──────────────────────

  private desired(b: BillingRow): Plan {
    return b.status === 'active' || b.status === 'past_due' ? 'paid' : b.status === 'trial' ? 'trial' : 'watch';
  }

  /** Makes the API match the workspace's state; remembers what it applied. */
  private async settle(workspaceId: string): Promise<void> {
    const b = this.store.billing(workspaceId);
    const w = this.store.workspace(workspaceId);
    if (!b || !w) return;
    const plan = this.desired(b);
    if (b.applied === plan) return;
    try {
      const res = await this.fetchFn(`${this.env.api}/v1/tenants/${encodeURIComponent(w.tenant_id)}/plan`, {
        method: 'PUT',
        headers: { authorization: `Bearer ${this.env.platformKey}`, 'content-type': 'application/json' },
        body: JSON.stringify({ plan, ...PLANS[plan] }),
      });
      if (res.ok) this.store.setBilling(workspaceId, { applied: plan }, this.now());
      else console.error(`billing: the API refused plan ${plan} for ${workspaceId} (${res.status})`);
    } catch (e) {
      console.error(`billing: couldn’t apply plan ${plan} for ${workspaceId}`, e);
    }
  }

  private async usage(tenant: string, from: number, to: number): Promise<number> {
    const q = new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString() });
    const res = await this.fetchFn(`${this.env.api}/v1/tenants/${encodeURIComponent(tenant)}/usage?${q}`, { headers: { authorization: `Bearer ${this.env.platformKey}` } });
    if (!res.ok) throw new Error(`usage ${res.status}`);
    return Number(((await res.json()) as { checks?: number }).checks ?? 0);
  }

  /** Reports a subscribed workspace's checks since the last report to Stripe's meter. */
  private async report(b: BillingRow): Promise<void> {
    const w = this.store.workspace(b.workspace_id);
    if (!w || !b.stripe_customer) return;
    const from = b.reported_until ?? b.updated_at;
    const to = this.now() - SETTLE;
    if (to - from < 60_000) return;
    const checks = await this.usage(w.tenant_id, from, to);
    if (checks > 0) {
      await this.stripe('/billing/meter_events', {
        event_name: this.stripeEnv.meterEvent,
        'payload[stripe_customer_id]': b.stripe_customer,
        'payload[value]': String(checks),
        identifier: `${b.workspace_id}-${from}-${to}`,
        timestamp: String(Math.floor(to / 1000)),
      });
    }
    this.store.setBilling(b.workspace_id, { reported_until: to }, this.now());
  }

  /** Hourly: end trials, retry plans the API didn't take, report usage. */
  async sweep(): Promise<void> {
    const now = this.now();
    for (const b of this.store.billingAll()) {
      if (b.status === 'trial' && b.trial_ends_at <= now) this.store.setBilling(b.workspace_id, { status: 'watch' }, now);
      await this.settle(b.workspace_id);
      const fresh = this.store.billing(b.workspace_id);
      if (fresh && (fresh.status === 'active' || fresh.status === 'past_due')) {
        await this.report(fresh).catch((e) => console.error(`billing: usage report failed for ${b.workspace_id}`, e));
      }
    }
  }
}
