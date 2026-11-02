// Builds dist/ for `bun serve.ts --prod`: every page and the shared lib, with
// the lib's URLs stamped by content so browsers can cache them for an hour and
// still never run an old shell against a new page.

import { readdirSync } from 'node:fs';
import { PAGES } from '../server/app';

const root = `${import.meta.dir}/..`;
// Every file in app/lib: a page that loads a lib file the build left out gets a 404 in production.
export const LIB = readdirSync(`${root}/app/lib`).filter((f) => /\.(js|css)$/.test(f)).sort();

const stamp: Record<string, string> = {};
for (const f of LIB) {
  const text = await Bun.file(`${root}/app/lib/${f}`).text();
  stamp[f] = Bun.hash(text).toString(36).slice(0, 8);
  await Bun.write(`${root}/dist/lib/${f}`, text);
}
for (const p of PAGES) {
  let html = await Bun.file(`${root}/app/${p}.html`).text();
  for (const f of LIB) html = html.replaceAll(`/lib/${f}"`, `/lib/${f}?v=${stamp[f]}"`);
  await Bun.write(`${root}/dist/${p}.html`, html);
}
console.log(`Built ${PAGES.length} pages and ${LIB.length} lib files into dist/`);
