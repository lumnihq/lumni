// Calls to the Go API on behalf of one signed-in browser. Every call carries
// that browser’s session cookie, so the API decides what it may see; writes
// carry its CSRF token, so the API checks it the same way it would directly.

import { HttpError } from './http';

export type Fetch = (input: string, init?: RequestInit) => Promise<Response>;

/** Who is asking: the browser’s session, and its CSRF token for writes. */
export interface Caller {
  session: string;
  csrf?: string | null;
  /** Skip the second-long cache: the browser asked for fresh data. */
  refresh?: boolean;
}

interface Cached {
  at: number;
  value: Promise<unknown>;
}

/** Reads within this long of each other share one answer, so a screen’s views don’t each ask again. */
const FRESH = 1000;

export class Upstream {
  private cache = new Map<string, Cached>();

  constructor(
    readonly base: string,
    private readonly fetchFn: Fetch,
  ) {}

  async get<T>(path: string, who: Caller, opts: { fresh?: number } = {}): Promise<T> {
    const key = `${who.session}\u0000${path}`;
    const ttl = opts.fresh ?? FRESH;
    const hit = this.cache.get(key);
    const now = Date.now();
    if (hit && !who.refresh && now - hit.at < ttl) return hit.value as Promise<T>;
    const value = this.call<T>('GET', path, undefined, who);
    this.cache.set(key, { at: now, value });
    // A failed read isn’t worth remembering.
    value.catch(() => {
      if (this.cache.get(key)?.value === value) this.cache.delete(key);
    });
    if (this.cache.size > 2000) this.prune(now);
    return value;
  }

  send<T>(method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body: unknown, who: Caller): Promise<T> {
    // Anything this browser read before the write may be out of date now.
    this.forget(who.session);
    return this.call<T>(method, path, body, who);
  }

  /** Drop what one session has cached, after it writes or signs out. */
  forget(session: string): void {
    const prefix = `${session}\u0000`;
    for (const k of this.cache.keys()) if (k.startsWith(prefix)) this.cache.delete(k);
  }

  private prune(now: number): void {
    for (const [k, v] of this.cache) if (now - v.at > 60_000) this.cache.delete(k);
  }

  private async call<T>(method: string, path: string, body: unknown, who: Caller): Promise<T> {
    const headers: Record<string, string> = { accept: 'application/json', cookie: `lumni_session=${who.session}` };
    if (body !== undefined) headers['content-type'] = 'application/json';
    if (method !== 'GET' && who.csrf) headers['x-csrf-token'] = who.csrf;
    let res: Response;
    try {
      res = await this.fetchFn(`${this.base}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    } catch {
      throw unreachable(this.base);
    }
    return read<T>(res);
  }
}

export function unreachable(base: string): HttpError {
  return new HttpError(502, 'API_UNREACHABLE', `Lumni’s API at ${base} can’t be reached. Is it running?`);
}

/** The body of a Go response, or its error envelope as an HttpError. */
export async function read<T>(res: Response): Promise<T> {
  const text = await res.text();
  let data: unknown;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    throw new HttpError(502, 'BAD_UPSTREAM', `The Lumni API answered with something that isn’t JSON (${res.status}).`);
  }
  if (!res.ok) {
    const e = (data as { error?: { code?: string; message?: string; details?: Record<string, unknown> } } | undefined)?.error;
    throw new HttpError(res.status, e?.code ?? 'REQUEST_FAILED', e?.message ?? `The Lumni API said ${res.status}.`, e?.details);
  }
  return data as T;
}

// ─── shapes the API uses ───────────────────────────────

/** `{items: [...]}`, where some lists say `null` when they’re empty. */
export function items<T>(x: unknown): T[] {
  const v = (x as { items?: T[] | null } | null | undefined)?.items;
  return Array.isArray(v) ? v : [];
}

/** A timestamp, or null for missing ones and Go’s zero time. */
export function ms(v: unknown): number | null {
  if (typeof v !== 'string' || !v) return null;
  const t = Date.parse(v);
  if (!Number.isFinite(t) || t < Date.UTC(1971, 0, 1)) return null;
  return t;
}

export function str(v: unknown): string | null {
  return typeof v === 'string' && v ? v : null;
}

export function obj(v: unknown): Record<string, unknown> {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
}

export function num(v: unknown): number | null {
  const n = typeof v === 'number' ? v : typeof v === 'string' && v.trim() ? Number(v) : NaN;
  return Number.isFinite(n) ? n : null;
}
