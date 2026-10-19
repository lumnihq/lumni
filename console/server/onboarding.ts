// Onboarding codes: connect an agent without an API key ever touching a shell
// command or a browser.
//
// Lumni's API makes codes itself now (POST /v1/onboarding/codes; D1). Then
// this server only asks for one and reads its status, and the agent trades it
// with the API directly, so the API's per-machine rate limit sees the
// agent's machine, not this server. The key is made at the trade, so an
// unused code leaves nothing to revoke. Against an older API, everything
// below runs here instead:
//
//   1. Signed in, /start asks for a code. This server makes a scoped API key
//      for the agent (POST /v1/api-keys, as you), keeps it in memory, and
//      answers with a short code like LUM-7Q4K-9P2X. The browser never sees
//      the key.
//   2. In the agent's folder, `curl …/init | node - --code LUM-7Q4K-9P2X`
//      trades the code for the key, once (POST /console/onboarding/exchange,
//      no sign-in: the code is the credential).
//   3. /start watches the code's status and lights up the moment it's used,
//      saying where it ran.
//
// A code works once and lasts 15 minutes. If it's never used, its key is
// revoked. Only a hash of each code is kept. Exchanges are rate-limited per
// address, and wrong guesses lock the endpoint briefly for everyone, so a
// code can't be brute-forced in its lifetime (32^8 possibilities).
//
// Codes live in this process. A restart forgets them, and their unused keys
// stay until revoked on the console's Settings page; run one console instance
// per workspace, or move this table to shared storage before scaling out.

import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { HttpError } from './http';
import type { Caller, Upstream } from './upstream';

/** No 0/O, 1/I/L: codes are read aloud and typed. */
const ALPHABET = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789';
const LIFETIME = 15 * 60_000;
/** Kept after use or expiry so /start can still read what happened. */
const LINGER = 60 * 60_000;
const PER_ADDRESS = { n: 10, per: 60_000 };
/** Wrong codes, from anyone, before exchanges pause. */
const WRONG = { n: 30, per: 60_000, pause: 60_000 };
/** `admission` is what lets an agent check actions; the rest report work and read it. */
export const AGENT_SCOPES = ['ingest', 'query', 'runtime', 'admission'];
/**
 * `lumniverse try`'s sample agents also play the checker (evidence needs
 * `verification`) and set up the workspace (a concurrency limit, a verifier:
 * `admin`). Only a browser login from that command asks for these; the
 * command revokes the key when it finishes.
 */
export const TRY_SCOPES = [...AGENT_SCOPES, 'verification', 'admin'];

/** A code the API made: this server only remembers it to read its status. */
interface Native {
  id: string;
  tenant: string;
  code: string;
  createdAt: number;
  expiresAt: number;
  by: Caller;
}

interface Entry {
  id: string;
  tenant: string;
  hash: string;
  /** The key, until it's handed over. */
  key: string | null;
  keyId: string;
  keyPrefix: string;
  createdAt: number;
  expiresAt: number;
  usedAt: number | null;
  host: string | null;
  project: string | null;
  actorHint: string | null;
  /** Who made it, so an unused key can be revoked as them. */
  by: Caller;
  revoked: boolean;
}

export interface CodeStatus {
  id: string;
  key_prefix: string | null;
  expires_at: string;
  used: boolean;
  used_at: string | null;
  host: string | null;
  project: string | null;
  /** The agent the code was for, once used (API-made codes only). */
  actor_id?: string | null;
  expired: boolean;
  revoked: boolean;
}

const hashOf = (code: string) => createHash('sha256').update(normalize(code)).digest('hex');

/** LUM-7Q4K-9P2X, lum7q4k9p2x and "LUM 7Q4K 9P2X" are the same code. */
export function normalize(code: string): string {
  return code.toUpperCase().replace(/[^A-Z0-9]/g, '').replace(/^LUM/, '');
}

export function newCode(): string {
  const bytes = randomBytes(8);
  let s = '';
  for (const b of bytes) s += ALPHABET[b % ALPHABET.length];
  return `LUM-${s.slice(0, 4)}-${s.slice(4)}`;
}

/** Answers that mean "this API predates onboarding codes". */
const tooOld = (e: unknown) => e instanceof HttpError && ([404, 405, 501].includes(e.status) || e.code === 'BAD_UPSTREAM');

export class Onboarding {
  private byHash = new Map<string, Entry>();
  private byId = new Map<string, Entry>();
  private native = new Map<string, Native>();
  /** When the API last said it can't make codes; asked again after 5 minutes. */
  private oldApiAt = 0;
  private hits = new Map<string, number[]>();
  private wrong: number[] = [];
  private pausedUntil = 0;

  constructor(
    private readonly up: Upstream,
    private readonly now: () => number = Date.now,
  ) {}

  /** A code that stands for an agent key: made by the API when it can, else here. */
  async create(who: Caller, tenant: string, body: { actor_hint?: unknown; label?: unknown }, scopes: string[] = AGENT_SCOPES): Promise<{ id: string; code: string; expires_at: string; key_prefix: string | null; made_by: 'api' | 'console' }> {
    await this.sweep();
    const hint = typeof body.actor_hint === 'string' && body.actor_hint.trim() ? body.actor_hint.trim().slice(0, 80) : null;
    if (this.now() - this.oldApiAt > 5 * 60_000) {
      try {
        const made = await this.up.send<{ code: string; expires_at: string }>('POST', '/v1/onboarding/codes', { scopes, expires_in_minutes: LIFETIME / 60_000, ...(hint ? { actor_hint: hint } : {}) }, who);
        const n: Native = { id: randomUUID(), tenant, code: made.code, createdAt: this.now(), expiresAt: Date.parse(made.expires_at) || this.now() + LIFETIME, by: { session: who.session, csrf: who.csrf } };
        this.native.set(n.id, n);
        return { id: n.id, code: n.code, expires_at: new Date(n.expiresAt).toISOString(), key_prefix: null, made_by: 'api' };
      } catch (e) {
        if (!tooOld(e)) throw e;
        this.oldApiAt = this.now();
      }
    }
    return { ...(await this.createHere(who, tenant, hint, scopes)), made_by: 'console' };
  }

  private async createHere(who: Caller, tenant: string, hint: string | null, scopes: string[] = AGENT_SCOPES): Promise<{ id: string; code: string; expires_at: string; key_prefix: string }> {
    const day = new Date(this.now()).toISOString().slice(0, 10);
    const key = await this.up.send<{ id: string; secret?: string; prefix?: string }>('POST', '/v1/api-keys', { name: `Agent key${hint ? ` for ${hint}` : ''}, ${day}`, scopes }, who);
    if (!key.secret) throw new HttpError(502, 'BAD_UPSTREAM', 'Lumni made the key but didn’t hand it over.');
    let code = newCode();
    while (this.byHash.has(hashOf(code))) code = newCode();
    const e: Entry = {
      id: randomUUID(), tenant, hash: hashOf(code), key: key.secret, keyId: key.id, keyPrefix: key.prefix ?? key.secret.slice(0, 10),
      createdAt: this.now(), expiresAt: this.now() + LIFETIME, usedAt: null, host: null, project: null, actorHint: hint, by: { session: who.session, csrf: who.csrf }, revoked: false,
    };
    this.byHash.set(e.hash, e);
    this.byId.set(e.id, e);
    return { id: e.id, code, expires_at: new Date(e.expiresAt).toISOString(), key_prefix: e.keyPrefix };
  }

  /** What happened to a code, for whoever's workspace it belongs to. */
  async status(id: string, tenant: string, who?: Caller): Promise<CodeStatus> {
    const n = this.native.get(id);
    if (n) {
      if (n.tenant !== tenant) throw new HttpError(404, 'NOT_FOUND', 'No onboarding code with that id.');
      return this.nativeStatus(n, who ?? n.by);
    }
    const e = this.byId.get(id);
    if (!e || e.tenant !== tenant) throw new HttpError(404, 'NOT_FOUND', 'No onboarding code with that id.');
    return {
      id: e.id, key_prefix: e.keyPrefix, expires_at: new Date(e.expiresAt).toISOString(),
      used: e.usedAt !== null, used_at: e.usedAt ? new Date(e.usedAt).toISOString() : null,
      host: e.host, project: e.project, expired: e.usedAt === null && this.now() > e.expiresAt, revoked: e.revoked,
    };
  }

  /**
   * The API's status. Newer APIs say where the code was used (host, project)
   * and which key it made; before that, the made key's name
   * ("onboarding: <project> @ <host>") is the only record, so it's read instead.
   */
  private async nativeStatus(n: Native, who: Caller): Promise<CodeStatus> {
    const st = await this.up.get<{ used: boolean; used_at: string | null; actor_id: string | null; host?: string | null; project?: string | null; key_id?: string | null }>(`/v1/onboarding/codes/${encodeURIComponent(n.code)}/status`, who, { fresh: 0 });
    let host = st.host ?? null;
    let project = st.project ?? null;
    let prefix: string | null = null;
    if (st.used) {
      const keys = await this.up.get<{ items?: { id?: string; name?: string; prefix?: string; created_at?: string }[] | null }>('/v1/api-keys', who, { fresh: 0 }).catch(() => null);
      const list = keys?.items ?? [];
      let k = st.key_id ? list.find((x) => x.id === st.key_id) : undefined;
      if (!k && !('key_id' in st)) {
        const usedAt = st.used_at ? Date.parse(st.used_at) : this.now();
        k = list
          .filter((x) => x.name?.startsWith('onboarding: ') && x.created_at && Math.abs(Date.parse(x.created_at) - usedAt) < 60_000)
          .sort((a, b) => Math.abs(Date.parse(a.created_at!) - usedAt) - Math.abs(Date.parse(b.created_at!) - usedAt))[0];
        const m = k ? /^onboarding: (.*?) @ (.*)$/.exec(k.name!) : null;
        project ??= m?.[1]?.trim() || null;
        host ??= m?.[2]?.trim() || null;
      }
      prefix = k?.prefix ?? null;
    }
    return {
      id: n.id, key_prefix: prefix, expires_at: new Date(n.expiresAt).toISOString(),
      used: st.used, used_at: st.used_at, host, project, actor_id: st.actor_id,
      expired: !st.used && this.now() > n.expiresAt, revoked: false,
    };
  }

  /** The agent's side: trade a code for its key. No sign-in; the code is the credential. */
  exchange(address: string, body: { code?: unknown; host?: unknown; project?: unknown }): { api_key: string; key_prefix: string; actor_hint: string | null } {
    const now = this.now();
    if (now < this.pausedUntil) throw new HttpError(429, 'RATE_LIMITED', 'Too many wrong codes just now. Try again in a minute.');
    const recent = (this.hits.get(address) ?? []).filter((t) => now - t < PER_ADDRESS.per);
    if (recent.length >= PER_ADDRESS.n) throw new HttpError(429, 'RATE_LIMITED', 'Too many tries from this machine. Wait a minute.');
    recent.push(now);
    this.hits.set(address, recent);
    if (this.hits.size > 5000) this.hits.clear();

    const code = typeof body.code === 'string' ? body.code : '';
    const e = code ? this.byHash.get(hashOf(code)) : undefined;
    // Unknown, used and expired all read the same, so a guess learns nothing.
    if (!e || e.usedAt !== null || now > e.expiresAt || !e.key || e.revoked) {
      this.wrong = this.wrong.filter((t) => now - t < WRONG.per);
      this.wrong.push(now);
      if (this.wrong.length >= WRONG.n) {
        this.pausedUntil = now + WRONG.pause;
        this.wrong = [];
      }
      throw new HttpError(404, 'NOT_FOUND', 'That code isn’t valid. Codes work once and last 15 minutes; make a new one on the console’s Start page.');
    }
    const key = e.key;
    e.key = null;
    e.usedAt = now;
    e.host = typeof body.host === 'string' ? body.host.slice(0, 120) : null;
    e.project = typeof body.project === 'string' ? body.project.slice(0, 120) : null;
    return { api_key: key, key_prefix: e.keyPrefix, actor_hint: e.actorHint };
  }

  /** Revoke the keys of codes nobody used, and forget old entries. */
  async sweep(): Promise<number> {
    const now = this.now();
    let revoked = 0;
    for (const e of [...this.byId.values()]) {
      if (e.usedAt === null && !e.revoked && now > e.expiresAt) {
        e.key = null;
        try {
          await this.up.send('DELETE', `/v1/api-keys/${encodeURIComponent(e.keyId)}`, undefined, e.by);
          e.revoked = true;
          revoked++;
        } catch {
          // The session may be gone; the key stays listed on Settings to revoke by hand.
        }
      }
      if (now > Math.max(e.expiresAt, e.usedAt ?? 0) + LINGER) {
        this.byId.delete(e.id);
        this.byHash.delete(e.hash);
      }
    }
    for (const n of [...this.native.values()]) if (now > n.expiresAt + LINGER) this.native.delete(n.id);
    return revoked;
  }
}
