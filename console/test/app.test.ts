// The console's pages and assets: clean paths, the right types, and nothing
// outside app/ reachable.

import { describe, expect, test } from 'bun:test';
import { PAGES, appHandler } from '../server/app';

const app = appHandler(`${import.meta.dir}/../app`);

describe('pages and assets', () => {
  test('every screen answers at its clean path, and at .html', async () => {
    for (const p of PAGES) {
      for (const path of [`/${p}`, `/${p}.html`]) {
        const r = await app(path);
        expect(r?.status).toBe(200);
        expect(r?.headers.get('content-type')).toContain('text/html');
      }
    }
  });

  test('the root goes to Today', async () => {
    const r = await app('/');
    expect(r?.status).toBe(302);
    expect(r?.headers.get('location')).toBe('/today');
  });

  test('the shared lib is served with its type', async () => {
    expect((await app('/lib/shell.js'))?.headers.get('content-type')).toContain('javascript');
    expect((await app('/lib/base.css'))?.headers.get('content-type')).toContain('css');
  });

  test('nothing outside app/ is reachable', async () => {
    for (const path of ['/../serve.ts', '/lib/../../.env', '/.env', '/lib/.hidden.js', '/server/index.ts', '/%2e%2e/serve.ts', '/nope']) {
      expect(await app(path)).toBeNull();
    }
  });

  test('every page boots the shell and names itself in the nav', async () => {
    for (const p of PAGES.filter((x) => x !== 'sign-in' && x !== 'privacy' && x !== 'terms')) {
      const html = await (await app(`/${p}`))!.text();
      expect(html).toContain('/lib/shell.js');
      expect(html).toMatch(/(Shell|Concept)\.boot\(\{\s*nav:/);
    }
  });
});

describe('the coding-agent guide', () => {
  test('is served at /init/guide.md with this console’s and the API’s addresses', async () => {
    const guide = appHandler(`${import.meta.dir}/../app`, 'no-cache', { publicApi: 'https://api.acme.dev', publicConsole: 'https://console.acme.dev' });
    const res = (await guide('/init/guide.md', 'https://ignored'))!;
    expect(res.headers.get('content-type')).toContain('text/markdown');
    const text = await res.text();
    expect(text).toContain('curl -fsSL https://console.acme.dev/init');
    expect(text).toContain('https://api.acme.dev/v1/mcp');
    expect(text).not.toContain('__LUMNI_');
  });
});
