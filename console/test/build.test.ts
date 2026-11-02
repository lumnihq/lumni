// The production build: every lib file a page loads is in dist/.

import { describe, expect, test } from 'bun:test';
import { PAGES } from '../server/app';

const root = `${import.meta.dir}/..`;

describe('the production build', () => {
  test('ships every lib file any page loads', async () => {
    await import('../scripts/build');
    for (const p of PAGES) {
      const html = await Bun.file(`${root}/dist/${p}.html`).text();
      for (const [, f] of html.matchAll(/src="\/lib\/([\w-]+\.js)[?"]/g)) {
        expect(await Bun.file(`${root}/dist/lib/${f}`).exists()).toBe(true);
      }
      if (html.includes("/lib/shell.js")) expect(html).toMatch(/\/lib\/shell\.js\?v=/);
    }
    // The contract pages are stamped like the rest, so a new page never runs an old lib.
    expect(await Bun.file(`${root}/dist/contracts.html`).text()).toMatch(/\/lib\/contracts\.js\?v=\w+"/);
  });
});
