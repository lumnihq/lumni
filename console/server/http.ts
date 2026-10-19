// Small pieces every handler shares: JSON responses in the API’s envelope,
// errors that carry a status, and cookies.

export class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: Record<string, unknown>,
  ) {
    super(message);
  }
}

let seq = 0;
export function requestId(): string {
  seq = (seq + 1) % 1e9;
  return `req_ff_${Date.now().toString(36)}${seq.toString(36)}`;
}

export function json(body: unknown, status = 200, headers: HeadersInit = {}): Response {
  const h = new Headers(headers);
  h.set('content-type', 'application/json');
  h.set('cache-control', 'no-store');
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: h });
}

export function problem(e: HttpError | { status: number; code: string; message: string; details?: Record<string, unknown> }, id = requestId()): Response {
  return json({ error: { code: e.code, message: e.message, request_id: id, details: e.details } }, e.status, { 'x-request-id': id });
}

/** Writes the console asks for that no API can do yet. */
export function notYet(what: string): HttpError {
  return new HttpError(501, 'NOT_AVAILABLE', `${what} isn’t available yet. The Lumni API has nothing for it.`);
}

// ─── cookies ───────────────────────────────────────────

export function cookies(req: Request): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (req.headers.get('cookie') ?? '').split(';')) {
    const i = part.indexOf('=');
    if (i < 0) continue;
    const k = part.slice(0, i).trim();
    if (!k) continue;
    try {
      out[k] = decodeURIComponent(part.slice(i + 1).trim());
    } catch {
      out[k] = part.slice(i + 1).trim();
    }
  }
  return out;
}

export interface CookieOpts {
  maxAge?: number;
  secure?: boolean;
  /** Readable from page scripts. Never for anything that signs you in. */
  readable?: boolean;
  /** Lax only where a cross-site redirect has to carry it (an OAuth return). */
  sameSite?: 'Strict' | 'Lax';
}

export function setCookie(name: string, value: string, o: CookieOpts = {}): string {
  const parts = [`${name}=${encodeURIComponent(value)}`, 'Path=/', `SameSite=${o.sameSite ?? 'Strict'}`];
  if (!o.readable) parts.push('HttpOnly');
  if (o.secure) parts.push('Secure');
  if (o.maxAge !== undefined) parts.push(`Max-Age=${Math.max(0, Math.floor(o.maxAge))}`);
  if (o.maxAge === 0) parts.push('Expires=Thu, 01 Jan 1970 00:00:00 GMT');
  return parts.join('; ');
}

export function clearCookie(name: string, secure = false): string {
  return setCookie(name, '', { maxAge: 0, secure });
}

/** The value and lifetime of one cookie in a Set-Cookie list. */
export function readSetCookie(list: string[], name: string): { value: string; maxAge: number | null } | null {
  for (const line of list) {
    const [pair, ...attrs] = line.split(';');
    const i = pair!.indexOf('=');
    if (i < 0 || pair!.slice(0, i).trim() !== name) continue;
    let maxAge: number | null = null;
    for (const a of attrs) {
      const [k, v] = a.split('=');
      if (k?.trim().toLowerCase() === 'max-age' && v) maxAge = Number(v.trim());
    }
    return { value: decodeURIComponent(pair!.slice(i + 1).trim()), maxAge };
  }
  return null;
}
