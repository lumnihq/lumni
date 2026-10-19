// lumni init: what it finds, what it writes, and what it never touches.

import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
// @ts-expect-error: plain JS module.
import * as init from '../scripts/lumni-init.mjs';

let dir = '';
let home = '';
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'lumni-init-'));
  home = mkdtempSync(join(tmpdir(), 'lumni-home-'));
});
afterEach(() => {
  rmSync(dir, { recursive: true, force: true });
  rmSync(home, { recursive: true, force: true });
});

/** A fake Lumni that records calls. */
function fakeLumni(opts: { badKey?: boolean } = {}) {
  const calls: { method: string; path: string; body?: Record<string, unknown> }[] = [];
  const fetchFn = async (url: string, init: { method: string; body?: string; headers: Record<string, string> }) => {
    const path = url.replace('http://lumni.test', '');
    calls.push({ method: init.method, path, body: init.body ? JSON.parse(init.body) : undefined });
    if (opts.badKey) return new Response(JSON.stringify({ error: { code: 'UNAUTHENTICATED', message: 'no' } }), { status: 401 });
    if (path === '/v1/actions/check') return Response.json({ action_id: 'act_1', decision: 'ALLOW', lease_token: 'lt_1' });
    return Response.json({ items: [] });
  };
  return { calls, fetchFn };
}

const run = async (argv: string[], f = fakeLumni()) => {
  const out: string[] = [];
  const code = await init.main([...argv, '--dir', dir, '--url', 'http://lumni.test'], {}, { fetchFn: f.fetchFn, home, log: (s: string) => out.push(s) });
  return { code, out: out.join('\n'), calls: f.calls };
};

describe('finding the project', () => {
  test('a Python LangGraph project', () => {
    writeFileSync(join(dir, 'pyproject.toml'), '[project]\ndependencies = ["langgraph>=0.2", "openai"]\n');
    const d = init.detect(dir, home);
    expect(d.languages).toEqual(['python']);
    expect(d.libraries).toEqual(['LangGraph', 'OpenAI']);
  });

  test('a TypeScript project with the Vercel AI SDK', () => {
    writeFileSync(join(dir, 'package.json'), JSON.stringify({ dependencies: { ai: '4', '@anthropic-ai/sdk': '1' }, devDependencies: { typescript: '5' } }));
    const d = init.detect(dir, home);
    expect(d.languages).toEqual(['typescript']);
    expect(d.libraries.sort()).toEqual(['Anthropic', 'Vercel AI SDK']);
  });

  test('MCP servers in a project and in the home directory', () => {
    mkdirSync(join(dir, '.cursor'));
    writeFileSync(join(dir, '.cursor', 'mcp.json'), JSON.stringify({ mcpServers: { billing: { command: 'node', args: ['billing.js'] }, web: { url: 'https://x' } } }));
    mkdirSync(join(home, '.cursor'));
    writeFileSync(join(home, '.cursor', 'mcp.json'), JSON.stringify({ mcpServers: { stripe: { command: '/usr/local/bin/gateway' } } }));
    const d = init.detect(dir, home);
    expect(d.mcp).toHaveLength(2);
    expect(d.mcp[0].servers).toEqual([{ name: 'billing', stdio: true, wrapped: false }, { name: 'web', stdio: false, wrapped: false }]);
    expect(d.mcp[1].servers[0].wrapped).toBe(true);
  });
});

describe('writing', () => {
  test('.env gets the URL and key, and nothing already there is changed', () => {
    const p = init.planEnv('OPENAI_API_KEY=sk-1\nLUMNI_URL=http://mine', 'http://lumni.test', 'lumni_k');
    expect(p.added).toEqual(['LUMNI_API_KEY']);
    expect(p.text).toContain('OPENAI_API_KEY=sk-1\nLUMNI_URL=http://mine');
    expect(p.text).toContain('LUMNI_API_KEY=lumni_k');
    expect(init.planEnv(p.text, 'x', 'y').added).toEqual([]);
  });

  test('.env is git-ignored once', () => {
    expect(init.planGitignore('node_modules\n')).toBe('node_modules\n.env\n');
    expect(init.planGitignore('.env\n')).toBeNull();
  });

  test('stdio MCP servers are wrapped with the gateway and get the key in their env', () => {
    const text = JSON.stringify({ mcpServers: { billing: { command: 'node', args: ['b.js'], env: { TOKEN: 't' } }, web: { url: 'https://x' }, done: { command: '/x/gateway' } } });
    const p = init.planMcp(text, 'mcpServers', '/opt/lumni/gateway', { url: 'http://lumni.test', key: 'lumni_k', actor: 'shop' });
    const cfg = JSON.parse(p.text);
    expect(p.wrapped).toEqual(['billing']);
    expect(cfg.mcpServers.billing).toEqual({
      command: '/opt/lumni/gateway',
      args: ['--transport', 'stdio', '--command', 'node', '--', 'b.js'],
      env: { TOKEN: 't', LUMNI_URL: 'http://lumni.test', LUMNI_API_KEY: 'lumni_k', LUMNI_GATEWAY_ACTOR: 'shop-billing' },
    });
    expect(cfg.mcpServers.web).toEqual({ url: 'https://x' });
    expect(p.skipped.map((s: { name: string }) => s.name)).toEqual(['web', 'done']);
  });
});

describe('the command', () => {
  test('no key: stops and says where to get one, touching nothing', async () => {
    const out: string[] = [];
    const code = await init.main(['--dir', dir, '--url', 'http://lumni.test'], {}, { fetchFn: fakeLumni().fetchFn, home, log: (s: string) => out.push(s) });
    expect(code).toBe(1);
    expect(out.join('\n')).toContain('Start page');
    expect(existsSync(join(dir, '.env'))).toBe(false);
  });

  test('a bad key: stops before writing anything', async () => {
    await expect(run(['--key', 'nope'], fakeLumni({ badKey: true }))).rejects.toThrow('isn’t one Lumni knows');
    expect(existsSync(join(dir, '.env'))).toBe(false);
  });

  test('dry run: says what it would do and changes nothing', async () => {
    writeFileSync(join(dir, 'pyproject.toml'), 'dependencies = ["openai"]');
    const r = await run(['--key', 'lumni_k', '--dry-run']);
    expect(r.code).toBe(0);
    expect(r.out).toContain('Would add LUMNI_URL and LUMNI_API_KEY to .env');
    expect(r.calls).toEqual([]);
    expect(existsSync(join(dir, '.env'))).toBe(false);
  });

  test('a real run: writes .env, wraps MCP with a backup, registers the agent and says hello', async () => {
    writeFileSync(join(dir, 'package.json'), JSON.stringify({ dependencies: { openai: '4' } }));
    mkdirSync(join(dir, '.git'));
    writeFileSync(join(dir, '.mcp.json'), JSON.stringify({ mcpServers: { billing: { command: 'node', args: ['b.js'] } } }));
    const r = await run(['--key', 'lumni_k', '--actor', 'shop', '--gateway', '/opt/lumni/gateway']);
    expect(r.code).toBe(0);
    expect(readFileSync(join(dir, '.env'), 'utf8')).toContain('LUMNI_API_KEY=lumni_k');
    expect(readFileSync(join(dir, '.gitignore'), 'utf8')).toBe('.env\n');
    expect(JSON.parse(readFileSync(join(dir, '.mcp.json'), 'utf8')).mcpServers.billing.command).toBe('/opt/lumni/gateway');
    expect(JSON.parse(readFileSync(join(dir, '.mcp.json.lumni-backup'), 'utf8')).mcpServers.billing.command).toBe('node');
    expect(r.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'GET /v1/actors', 'POST /v1/actors', 'POST /v1/work', 'POST /v1/actions/check', 'POST /v1/actions/act_1/start', 'POST /v1/actions/act_1/result',
    ]);
    expect(r.calls[1]!.body).toMatchObject({ id: 'shop', type: 'AGENT', runtime: 'node' });
    expect(r.out).toContain('shouldExecute');
  });

  test('running it twice changes nothing the second time', async () => {
    await run(['--key', 'lumni_k', '--no-hello']);
    const first = readFileSync(join(dir, '.env'), 'utf8');
    const r = await run(['--key', 'lumni_k', '--no-hello']);
    expect(readFileSync(join(dir, '.env'), 'utf8')).toBe(first);
    expect(r.out).toContain('left as it is');
  });

  test('a code from the Start page is traded for the key and the API URL, then used', async () => {
    const calls: string[] = [];
    const fetchFn = async (url: string, init: { method: string; body?: string }) => {
      calls.push(`${init.method} ${url}`);
      if (url === 'https://console.acme.dev/console/onboarding/exchange') {
        expect(JSON.parse(init.body!)).toMatchObject({ code: 'LUM-7Q4K-9P2X' });
        return Response.json({ api_key: 'lumni_k_from_code', key_prefix: 'lumni_k', api_url: 'https://api.acme.dev', workspace: 'Acme', actor_hint: 'support-agent' });
      }
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      return Response.json({ items: [] });
    };
    const out: string[] = [];
    const code = await init.main(['--code', 'LUM-7Q4K-9P2X', '--console', 'https://console.acme.dev', '--dir', dir], {}, { fetchFn, home, log: (s: string) => out.push(s) });
    expect(code).toBe(0);
    expect(calls[0]).toBe('POST https://console.acme.dev/console/onboarding/exchange');
    expect(calls[1]).toBe('GET https://api.acme.dev/v1/actors');
    expect(readFileSync(join(dir, '.env'), 'utf8')).toContain('LUMNI_URL=https://api.acme.dev\nLUMNI_API_KEY=lumni_k_from_code');
    expect(out.join('\n')).toContain('Traded your code');
    expect(out.join('\n')).toContain('support-agent');
  });

  test('a code the API made is traded with the API itself, not the console', async () => {
    const calls: string[] = [];
    const code = 'LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ';
    const fetchFn = async (url: string, init: { method: string; body?: string }) => {
      calls.push(`${init.method} ${url}`);
      if (url === 'https://api.acme.dev/v1/onboarding/exchange') {
        expect(JSON.parse(init.body!)).toMatchObject({ code });
        return Response.json({ api_key: 'lm_secret', key_id: 'k1', tenant_name: 'Acme', api_url: 'https://api.acme.dev', actor_hint: 'billing-agent' });
      }
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      return Response.json({ items: [] });
    };
    const out: string[] = [];
    const rc = await init.main(['--code', code, '--url', 'https://api.acme.dev', '--dir', dir], {}, { fetchFn, home, log: (s: string) => out.push(s) });
    expect(rc).toBe(0);
    expect(calls[0]).toBe('POST https://api.acme.dev/v1/onboarding/exchange');
    expect(readFileSync(join(dir, '.env'), 'utf8')).toContain('LUMNI_API_KEY=lm_secret');
    expect(out.join('\n')).toContain('Acme');
    expect(out.join('\n')).not.toContain('lm_secret');
  });

  test('a code made without a name: the agent is named after its folder, not left blank', async () => {
    const calls: { url: string; body?: Record<string, unknown> }[] = [];
    const fetchFn = async (url: string, init: { method: string; body?: string }) => {
      calls.push({ url, body: init.body ? JSON.parse(init.body) : undefined });
      if (url.endsWith('/v1/onboarding/exchange')) return Response.json({ api_key: 'lm_k', key_id: 'k1', tenant_name: 'Local Development', api_url: 'http://lumni.test', actor_hint: '' });
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      return Response.json({ items: [] });
    };
    const rc = await init.main(['--code', 'LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ', '--url', 'http://lumni.test', '--dir', dir], {}, { fetchFn, home, log: () => {} });
    expect(rc).toBe(0);
    const reg = calls.find((c) => c.url.endsWith('/v1/actors') && c.body)!;
    expect(String(reg.body!.id)).toMatch(/^lumni-init-/);
  });

  test('no code and no key: logs in through the browser, then trades the one-time code with the API', async () => {
    const calls: string[] = [];
    let polls = 0;
    const opened: string[] = [];
    const fetchFn = async (url: string, init: { method: string; body?: string }) => {
      calls.push(`${init.method} ${url}`);
      if (url === 'https://console.acme.dev/console/device') {
        expect(JSON.parse(init.body!)).toMatchObject({ client: 'lumni init' });
        return Response.json({ device_code: 'd'.repeat(64), user_code: 'WDJB-MJHT', verification_uri: 'https://console.acme.dev/device', verification_uri_complete: 'https://console.acme.dev/device?code=WDJB-MJHT', expires_in: 600, interval: 3 });
      }
      if (url === 'https://console.acme.dev/console/device/token') {
        polls++;
        if (polls < 3) return Response.json({ error: { code: 'authorization_pending', message: 'Waiting' } }, { status: 400 });
        return Response.json({ code: 'LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ', made_by: 'api', actor_hint: 'billing-agent', api_url: 'https://api.acme.dev' });
      }
      if (url === 'https://api.acme.dev/v1/onboarding/exchange') return Response.json({ api_key: 'lm_browser', key_id: 'k', tenant_name: 'Acme', api_url: 'https://api.acme.dev', actor_hint: 'billing-agent' });
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      return Response.json({ items: [] });
    };
    const out: string[] = [];
    const rc = await init.main(['--console', 'https://console.acme.dev', '--url', 'https://api.acme.dev', '--dir', dir], {}, { fetchFn, home, log: (x: string) => out.push(x), sleep: async () => {}, open: (u: string) => opened.push(u) });
    expect(rc).toBe(0);
    expect(opened).toEqual(['https://console.acme.dev/device?code=WDJB-MJHT']);
    expect(polls).toBe(3);
    expect(out.join('\n')).toContain('WDJB-MJHT');
    expect(readFileSync(join(dir, '.env'), 'utf8')).toContain('LUMNI_API_KEY=lm_browser');
    const reg = calls.indexOf('POST https://api.acme.dev/v1/actors');
    expect(reg).toBeGreaterThan(calls.indexOf('POST https://api.acme.dev/v1/onboarding/exchange'));
  });

  test('browser login with only the console known (npx lumniverse init): the API is where the console says', async () => {
    const calls: string[] = [];
    const fetchFn = async (url: string, init: { method: string }) => {
      calls.push(`${init.method} ${url}`);
      if (url === 'https://console.acme.dev/console/device') return Response.json({ device_code: 'd'.repeat(64), user_code: 'WDJB-MJHT', verification_uri_complete: 'https://console.acme.dev/device?code=WDJB-MJHT', expires_in: 600, interval: 3 });
      if (url === 'https://console.acme.dev/console/device/token') return Response.json({ code: 'LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ', made_by: 'api', actor_hint: '', api_url: 'https://api.acme.dev' });
      if (url === 'https://api.acme.dev/v1/onboarding/exchange') return Response.json({ api_key: 'lm_browser', key_id: 'k', tenant_name: 'Acme', api_url: 'https://api.acme.dev', actor_hint: '' });
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      if (url.startsWith('https://api.acme.dev/')) return Response.json({ items: [] });
      throw new Error(`unexpected ${url}`);
    };
    const rc = await init.main(['--console', 'https://console.acme.dev', '--dir', dir], {}, { fetchFn, home, log: () => {}, sleep: async () => {}, open: () => {} });
    expect(rc).toBe(0);
    expect(calls).toContain('POST https://api.acme.dev/v1/onboarding/exchange');
    expect(calls.some((c) => c.includes('localhost'))).toBe(false);
    expect(readFileSync(join(dir, '.env'), 'utf8')).toContain('LUMNI_URL=https://api.acme.dev');
  });

  test('already connected: the key in .env is used, with no login', async () => {
    writeFileSync(join(dir, '.env'), 'LUMNI_URL=https://api.acme.dev\nLUMNI_API_KEY=lm_saved\n');
    const calls: string[] = [];
    const fetchFn = async (url: string, init: { method: string; headers?: Record<string, string> }) => {
      calls.push(`${init.method} ${url} ${init.headers?.authorization ?? ''}`);
      if (url.endsWith('/v1/actions/check')) return Response.json({ action_id: 'act_1', decision: 'ALLOW' });
      return Response.json({ items: [] });
    };
    const out: string[] = [];
    const rc = await init.main(['--console', 'https://console.acme.dev', '--dir', dir], {}, { fetchFn, home, log: (x: string) => out.push(x), sleep: async () => {}, open: () => {} });
    expect(rc).toBe(0);
    expect(calls.some((c) => c.includes('/console/device'))).toBe(false);
    expect(calls[0]).toBe('GET https://api.acme.dev/v1/actors Bearer lm_saved');
    expect(out.join('\n')).toContain('Already connected');
  });

  test('cancelled in the browser: stops, and writes nothing', async () => {
    const fetchFn = async (url: string) => {
      if (url.endsWith('/console/device')) return Response.json({ device_code: 'd', user_code: 'BCDF-GHJK', verification_uri_complete: 'https://c/device?code=BCDF-GHJK', expires_in: 600, interval: 3 });
      return Response.json({ error: { code: 'access_denied', message: 'no' } }, { status: 400 });
    };
    await expect(init.main(['--console', 'https://c', '--dir', dir], {}, { fetchFn, home, log: () => {}, sleep: async () => {}, open: () => {} })).rejects.toThrow('cancelled');
    expect(existsSync(join(dir, '.env'))).toBe(false);
  });

  test('a console without browser login says to use a code', async () => {
    const fetchFn = async () => Response.json({ error: { code: 'NOT_FOUND', message: 'no' } }, { status: 404 });
    await expect(init.main(['--console', 'https://c', '--dir', dir], {}, { fetchFn, home, log: () => {}, sleep: async () => {}, open: () => {} })).rejects.toThrow('--code');
  });

  test('which side makes a code is told by its length', () => {
    expect(init.madeByApi('LUM-7Q4K-9P2X')).toBe(false);
    expect(init.madeByApi('LUM-ABCDEFGHIJKLM-NOPQRSTUVWXYZ')).toBe(true);
  });

  test('a used or wrong code stops before anything is written', async () => {
    const fetchFn = async () => Response.json({ error: { code: 'NOT_FOUND', message: 'That code isn’t valid.' } }, { status: 404 });
    await expect(init.main(['--code', 'LUM-AAAA-AAAA', '--console', 'https://c', '--dir', dir], {}, { fetchFn, home, log: () => {} })).rejects.toThrow('isn’t valid');
    expect(existsSync(join(dir, '.env'))).toBe(false);
  });
});
