// Everything else under /v1 goes straight to the Go API: approvals, outcome
// retries, the change stream. The browser’s session cookie and CSRF token go
// with it; an Authorization header never does, since the API would prefer it
// to the session.

import { HttpError, problem } from './http';
import { unreachable, type Fetch } from './upstream';

/** Request headers worth passing on. */
const ASK = ['accept', 'content-type', 'cookie', 'x-csrf-token', 'last-event-id', 'x-request-id'];
/** Response headers worth passing back. */
const ANSWER = ['content-type', 'cache-control', 'x-request-id', 'retry-after'];

export async function proxy(req: Request, base: string, fetchFn: Fetch, keepOpen?: () => void): Promise<Response> {
  const url = new URL(req.url);
  const headers = new Headers();
  for (const h of ASK) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }
  const method = req.method.toUpperCase();
  const stream = url.pathname === '/v1/graph/stream';
  // Go’s stream sends a keepalive every 15 s; the default idle timeout is shorter.
  if (stream) keepOpen?.();
  let res: Response;
  try {
    res = await fetchFn(`${base}${url.pathname}${url.search}`, {
      method,
      headers,
      body: method === 'GET' || method === 'HEAD' ? undefined : await req.arrayBuffer(),
      redirect: 'manual',
      signal: req.signal,
    });
  } catch {
    if (req.signal.aborted) return new Response(null, { status: 499 });
    return problem(unreachable(base));
  }
  const out = new Headers();
  for (const h of ANSWER) {
    const v = res.headers.get(h);
    if (v) out.set(h, v);
  }
  if (stream) {
    out.set('cache-control', 'no-cache, no-transform');
    out.set('x-accel-buffering', 'no');
  }
  if (res.status >= 300 && res.status < 400) return problem(new HttpError(502, 'BAD_UPSTREAM', 'The API tried to send this request somewhere else.'));
  return new Response(res.body, { status: res.status, headers: out });
}
