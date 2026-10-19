#!/usr/bin/env node
// Builds llms-full.txt: llms.txt followed by the public docs it links to, in one file.
//
//   node scripts/llms-full.mjs [out]    (default: llms-full.txt at the repo root)
//
// coordination.md is left out on purpose: it is a long design document, linked from llms.txt.

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const out = resolve(process.argv[2] ?? join(root, 'llms-full.txt'));

const parts = [
  'skills/lumni/SKILL.md',
  'README.md',
  'docs/getting-started.md',
  'docs/sdk.md',
  'sdk/typescript/README.md',
  'sdk/python/README.md',
  'docs/api.md',
  'docs/integrations.md',
  'docs/self-hosting.md',
  'docs/configuration.md',
];

const body = parts
  .map((p) => `\n\n---\n\n<!-- source: https://github.com/lumnihq/lumni/blob/main/${p} -->\n\n${readFileSync(join(root, p), 'utf8').trim()}`)
  .join('');

writeFileSync(out, `${readFileSync(join(root, 'llms.txt'), 'utf8').trim()}${body}\n`);
console.log(`wrote ${out}`);
