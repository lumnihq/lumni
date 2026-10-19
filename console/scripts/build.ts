// Builds dist/ for `bun serve.ts --prod`: every page and the shared lib, with
// the lib's URLs stamped by content so browsers can cache them for an hour and
// still never run an old shell against a new page.

import { PAGES } from '../server/app';

const root = `${import.meta.dir}/..`;
const LIB = ['base.css', 'shell.js', 'world-mock.js'];

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
