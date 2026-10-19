// Browser login for the onboarding command (an RFC 8628 device grant), so
// connecting an agent needs nothing copied:
//
//   1. `lumni init` asks for a login: POST /console/device (no sign-in). It
//      gets a short code for people (WDJB-MJHT) and a long secret for itself.
//   2. It opens /device?code=WDJB-MJHT. Signed in, you check the code matches
//      your terminal and press Connect. This server then asks Lumni's API for
//      a one-time onboarding code (or makes one itself, on older APIs).
//   3. The command, polling POST /console/device/token with its secret, gets
//      that one-time code, once, and trades it with the API for its key, as
//      if you'd pasted it. The key never passes through here.
//
// Logins last 10 minutes and live in this process. The secret is stored
// hashed; the short code only finds a login for someone already signed in.

import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { HttpError } from './http';
import { AGENT_SCOPES, TRY_SCOPES, type Onboarding } from './onboarding';

/** The command that runs the sample agents; its login gets more (TRY_SCOPES). */
const TRY_CLIENT = 'lumni try';
import type { Caller } from './upstream';

/** No vowels, no 0/O or 1/I: nothing spells a word, nothing is misread (RFC 8628 §6.1). */
const ALPHABET = 'BCDFGHJKLMNPQRSTVWXZ';
const LIFETIME = 10 * 60_000;
/** Seconds between the command's polls. */
export const INTERVAL = 3;
const STARTS_PER_ADDRESS = { n: 10, per: 60_000 };
const LINGER = 60 * 60_000;

type Status = 'pending' | 'approved' | 'denied' | 'used';

interface Login {
  id: string;
  deviceHash: string;
  userCode: string;
  host: string | null;
  project: string | null;
  client: string | null;
  address: string;
  createdAt: number;
  expiresAt: number;
  status: Status;
  lastPoll: number;
  /** Set on approval. */
  tenant: string | null;
  approvedAt: number | null;
  approvedBy: string | null;
  code: string | null;
  madeBy: 'api' | 'console' | null;
  actorHint: string | null;
}

const hash = (s: string) => createHash('sha256').update(s).digest('hex');
/** WDJB-MJHT, wdjbmjht and "wdjb mjht" are the same code. */
export const normalizeUserCode = (s: string) => s.toUpperCase().replace(/[^A-Z]/g, '');
const clip = (v: unknown, n = 120) => (typeof v === 'string' && v.trim() ? v.trim().replace(/[\r\n\0]/g, ' ').slice(0, n) : null);

function newUserCode(): string {
  const b = randomBytes(8);
  let s = '';
  for (const x of b) s += ALPHABET[x % ALPHABET.length];
  return `${s.slice(0, 4)}-${s.slice(4)}`;
}

export interface LoginView {
  user_code: string;
  host: string | null;
  project: string | null;
  client: string | null;
  address: string;
  created_at: string;
  expires_at: string;
  status: Status | 'expired';
  /** agent: connect an agent. try: run the sample agents with a temporary full-access key. */
  purpose: 'agent' | 'try';
}

export class DeviceLogin {
  private byDevice = new Map<string, Login>();
  private byUser = new Map<string, Login>();
  private starts = new Map<string, number[]>();

  constructor(
    private readonly onboarding: Onboarding,
    private readonly now: () => number = Date.now,
  ) {}

  /** The command's side: begin a login. */
  start(address: string, body: { host?: unknown; project?: unknown; client?: unknown }, consoleUrl: string) {
    const now = this.now();
    this.sweep();
    const recent = (this.starts.get(address) ?? []).filter((t) => now - t < STARTS_PER_ADDRESS.per);
    if (recent.length >= STARTS_PER_ADDRESS.n) throw new HttpError(429, 'RATE_LIMITED', 'Too many logins from this machine. Wait a minute.');
    recent.push(now);
    this.starts.set(address, recent);
    if (this.starts.size > 5000) this.starts.clear();

    const device = Array.from(randomBytes(32), (b) => b.toString(16).padStart(2, '0')).join('');
    let user = newUserCode();
    while (this.byUser.has(normalizeUserCode(user))) user = newUserCode();
    const l: Login = {
      id: randomUUID(), deviceHash: hash(device), userCode: user,
      host: clip(body.host), project: clip(body.project), client: clip(body.client, 40), address,
      createdAt: now, expiresAt: now + LIFETIME, status: 'pending', lastPoll: 0,
      tenant: null, approvedAt: null, approvedBy: null, code: null, madeBy: null, actorHint: null,
    };
    this.byDevice.set(l.deviceHash, l);
    this.byUser.set(normalizeUserCode(user), l);
    const page = `${consoleUrl.replace(/\/+$/, '')}/device`;
    return {
      device_code: device,
      user_code: user,
      verification_uri: page,
      verification_uri_complete: `${page}?code=${user}`,
      expires_in: LIFETIME / 1000,
      interval: INTERVAL,
    };
  }

  private find(userCode: string): Login {
    const l = this.byUser.get(normalizeUserCode(userCode));
    if (!l) throw new HttpError(404, 'NOT_FOUND', 'No login with that code. Check it matches your terminal, or run the command again.');
    return l;
  }

  private view(l: Login): LoginView {
    const expired = l.status === 'pending' && this.now() > l.expiresAt;
    return {
      user_code: l.userCode, host: l.host, project: l.project, client: l.client, address: l.address,
      created_at: new Date(l.createdAt).toISOString(), expires_at: new Date(l.expiresAt).toISOString(),
      status: expired ? 'expired' : l.status,
      purpose: l.client === TRY_CLIENT ? 'try' : 'agent',
    };
  }

  /** The browser's side, signed in: what's asking to connect. */
  lookup(userCode: string): LoginView {
    return this.view(this.find(userCode));
  }

  /** Connect it: a one-time onboarding code, held for the command to collect. */
  async approve(userCode: string, who: Caller & { name?: string }, tenant: string, body: { actor_hint?: unknown }): Promise<LoginView> {
    const l = this.find(userCode);
    if (l.status !== 'pending' || this.now() > l.expiresAt) throw new HttpError(409, 'NOT_PENDING', l.status === 'pending' ? 'This login has expired. Run the command again.' : `This login was already ${l.status === 'denied' ? 'cancelled' : 'connected'}.`);
    const sample = l.client === TRY_CLIENT;
    // The sample agents name themselves.
    const hint = sample ? null : clip(body.actor_hint, 80) ?? (l.project ? l.project.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-|-$/g, '') || null : null);
    const made = await this.onboarding.create(who, tenant, { actor_hint: hint ?? undefined }, sample ? TRY_SCOPES : AGENT_SCOPES);
    Object.assign(l, { status: 'approved', tenant, approvedAt: this.now(), approvedBy: who.name ?? null, code: made.code, madeBy: made.made_by, actorHint: hint });
    return this.view(l);
  }

  deny(userCode: string): LoginView {
    const l = this.find(userCode);
    if (l.status === 'pending') l.status = 'denied';
    return this.view(l);
  }

  /** The command's side: poll until it's connected, cancelled or expired. RFC 8628 §3.5 errors. */
  token(body: { device_code?: unknown }): { code: string; made_by: 'api' | 'console'; actor_hint: string | null } {
    const device = typeof body.device_code === 'string' ? body.device_code : '';
    const l = device ? this.byDevice.get(hash(device)) : undefined;
    const now = this.now();
    if (!l || l.status === 'used' || (l.status === 'pending' && now > l.expiresAt)) throw new HttpError(400, 'expired_token', 'This login has expired. Run the command again.');
    if (l.status === 'denied') throw new HttpError(400, 'access_denied', 'The login was cancelled in the browser.');
    if (l.status === 'pending') {
      const tooSoon = now - l.lastPoll < (INTERVAL * 1000) - 250;
      l.lastPoll = now;
      throw new HttpError(400, tooSoon ? 'slow_down' : 'authorization_pending', tooSoon ? 'Polling too fast.' : 'Waiting for you to connect it in the browser.');
    }
    // Approved: hand over the one-time code, once.
    l.status = 'used';
    const out = { code: l.code!, made_by: l.madeBy!, actor_hint: l.actorHint };
    l.code = null;
    return out;
  }

  /** Logins approved in a workspace since a moment, for the Start page to light up. */
  approvedSince(tenant: string, since: number): { host: string | null; project: string | null; at: string }[] {
    return [...this.byUser.values()]
      .filter((l) => l.tenant === tenant && l.approvedAt && l.approvedAt >= since)
      .map((l) => ({ host: l.host, project: l.project, at: new Date(l.approvedAt!).toISOString() }));
  }

  sweep(): void {
    const now = this.now();
    for (const l of [...this.byUser.values()]) {
      if (now > l.expiresAt + LINGER) {
        this.byUser.delete(normalizeUserCode(l.userCode));
        this.byDevice.delete(l.deviceHash);
      }
    }
  }
}
