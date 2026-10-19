#!/usr/bin/env node
// Installs the console's pinned dependencies straight out of the local npm cache,
// for machines (or sandboxes) that can't reach the registry. It only reads
// ~/.npm/_cacache: each tarball is found through its cacache index entry, then
// unpacked into node_modules/<name>. Anywhere with network, use `bun install`
// or `npm install` instead.
//
//   node scripts/seed-node-modules.mjs

import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const cache = process.env.NPM_CACHE ?? join(homedir(), '.npm/_cacache');

// Everything `package.json` pins, plus their transitive runtime dependencies.
const PACKAGES = {
  typescript: '5.9.3',
};

function tarballKey(name, version) {
  const base = name.startsWith('@') ? name.split('/')[1] : name;
  return `make-fetch-happen:request-cache:https://registry.npmjs.org/${name}/-/${base}-${version}.tgz`;
}

// cacache stores index entries in a bucket named by the sha256 of the key.
function lookup(key) {
  const hash = createHash('sha256').update(key).digest('hex');
  const bucket = join(cache, 'index-v5', hash.slice(0, 2), hash.slice(2, 4), hash.slice(4));
  if (!existsSync(bucket)) return null;
  let found = null;
  for (const line of readFileSync(bucket, 'utf8').split('\n')) {
    const tab = line.indexOf('\t');
    if (tab < 0) continue;
    const entry = JSON.parse(line.slice(tab + 1));
    if (entry.key === key && entry.integrity) found = entry;
  }
  return found;
}

function contentPath(integrity) {
  const sri = integrity.split(/\s+/).find((s) => s.startsWith('sha512-')) ?? integrity.split(/\s+/)[0];
  const [algo, b64] = sri.split('-');
  const hex = Buffer.from(b64, 'base64').toString('hex');
  return join(cache, 'content-v2', algo, hex.slice(0, 2), hex.slice(2, 4), hex.slice(4));
}

let missing = 0;
for (const [name, version] of Object.entries(PACKAGES)) {
  const entry = lookup(tarballKey(name, version));
  const file = entry && contentPath(entry.integrity);
  if (!file || !existsSync(file)) {
    console.error(`missing  ${name}@${version} is not in ${cache}`);
    missing++;
    continue;
  }
  const dest = join(root, 'node_modules', name);
  rmSync(dest, { recursive: true, force: true });
  mkdirSync(dest, { recursive: true });
  execFileSync('tar', ['-xzf', file, '-C', dest, '--strip-components=1']);
  console.log(`seeded   ${name}@${version}`);
}

// Give tsc a stable bin path, the way a package manager would.
const bin = join(root, 'node_modules/.bin');
mkdirSync(bin, { recursive: true });
rmSync(join(bin, 'tsc'), { force: true });
execFileSync('ln', ['-s', '../typescript/bin/tsc', join(bin, 'tsc')]);

if (missing) {
  console.error(`\n${missing} package(s) missing. Run \`bun install\` or \`npm install\` with network access.`);
  process.exit(1);
}
