// try-real: the scenario script sends the calls a real agent would, in order,
// and says what Lumni decided in plain words.

import { describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
// @ts-expect-error: plain JS module.
import * as tr from '../scripts/try-real.mjs';

/** A stub Lumni: answers each check by what's in it, like the Go API would in this mode. */
function stubLumni(mode: 'SHADOW' | 'ENFORCE') {
  const calls: { method: string; path: string; auth: string; body?: Record<string, unknown> }[] = [];
  const held = new Set<string>();
  const done = new Set<string>();
  const fetchFn = async (url: string, init: { method: string; body?: string; headers: Record<string, string> }) => {
    const path = url.replace('http://lumni.test', '');
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method: init.method, path, auth: init.headers.authorization!, body });
    if (path === '/v1/resources/resolve') return Response.json({ id: 'res_1', ...body }, { status: 201 });
    if (path === '/v1/actions/check') {
      const r = body.resources?.[0] ?? {};
      const key = `${r.namespace ?? ''}:${r.external_id}`;
      let d = 'ALLOW';
      if (body.idempotency_key && done.has(body.idempotency_key)) d = 'DEDUPLICATE';
      else if (held.has(key)) d = 'WAIT';
      else if (r.observed_version) d = 'REPLAN';
      else if ((body.arguments?.amount ?? 0) > 500) d = 'REQUIRE_APPROVAL';
      if (body.idempotency_key) done.add(body.idempotency_key);
      if (d === 'ALLOW' && r.type === 'deployment') held.add(key);
      const enforced = mode === 'ENFORCE';
      return Response.json({ action_id: body.action_id, decision: enforced || d === 'ALLOW' ? d : `WOULD_${d}`, lease_token: d === 'ALLOW' ? `lt_${body.action_id}` : undefined });
    }
    if (/\/result$/.test(path)) held.clear();
    return Response.json({}, { status: 201 });
  };
  return { calls, fetchFn };
}

const run = async (argv: string[], s = stubLumni('ENFORCE'), cwd = tmpdir()) => {
  const out: string[] = [];
  const code = await tr.main([...argv, '--url', 'http://lumni.test'], {}, { fetchFn: s.fetchFn, log: (x: string) => out.push(x), cwd, sleep: async () => {} });
  return { code, out: out.join('\n'), calls: s.calls };
};

describe('try-real', () => {
  test('one round: a repeat stopped, a deploy that waits, a stale read, and a refund that needs a person', async () => {
    const r = await run(['--key', 'lumni_k']);
    expect(r.code).toBe(0);
    expect(r.out).toContain('the same refund again: was stopped: it was already done');
    expect(r.out).toContain('deploy it at the same time: waited its turn');
    expect(r.out).toContain('quote from the list it read earlier: was sent back to read the latest');
    expect(r.out).toMatch(/refund \$900 on ch_\w+: is waiting for a person/);
    expect(r.calls.filter((c) => c.path === '/v1/actors')).toHaveLength(5);
    expect(r.calls.every((c) => c.auth === 'Bearer lumni_k')).toBe(true);
  });

  test('watch-only mode says what Lumni would have done', async () => {
    const r = await run(['--key', 'lumni_k'], stubLumni('SHADOW'));
    expect(r.out).toContain('would have been stopped as already done (watch-only, so it went ahead)');
  });

  test('the price list changes between the read and the write', async () => {
    const r = await run(['--key', 'lumni_k']);
    const versions = r.calls.filter((c) => c.path === '/v1/resources/res_1/version').map((c) => c.body!.version);
    expect(versions).toEqual(['v1', 'v2']);
    const quote = r.calls.find((c) => c.body?.operation === 'salesforce.update_price')!;
    expect((quote.body!.resources as { observed_version: string }[])[0]!.observed_version).toBe('v1');
  });

  test('an admin key adds the refund rule first, with the admin key', async () => {
    const r = await run(['--key', 'lumni_k', '--admin-key', 'lumni_admin']);
    expect(r.calls[0]).toMatchObject({ method: 'POST', path: '/v1/config/policy-rules', auth: 'Bearer lumni_admin', body: { operation: 'stripe.refund', numeric_threshold: 500 } });
  });

  test('rounds repeat, and versions keep moving forward', async () => {
    const r = await run(['--key', 'lumni_k', '--rounds', '3']);
    expect(r.out.match(/Round \d of 3/g)).toHaveLength(3);
    expect(r.calls.filter((c) => c.path.endsWith('/version')).map((c) => c.body!.version)).toEqual(['v1', 'v2', 'v3', 'v4', 'v5', 'v6']);
  });

  test('reads the key from .env, as lumni init left it', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'try-real-'));
    writeFileSync(join(dir, '.env'), 'LUMNI_URL=http://lumni.test\nLUMNI_API_KEY="lumni_from_env"\n');
    const r = await run([], stubLumni('ENFORCE'), dir);
    rmSync(dir, { recursive: true, force: true });
    expect(r.code).toBe(0);
    expect(r.calls[0]!.auth).toBe('Bearer lumni_from_env');
  });

  test('no key: says to run lumni init, and calls nothing', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'try-real-'));
    const r = await run([], stubLumni('ENFORCE'), dir);
    rmSync(dir, { recursive: true, force: true });
    expect(r.code).toBe(1);
    expect(r.out).toContain('lumni init');
    expect(r.calls).toEqual([]);
  });
});
