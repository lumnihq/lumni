#!/usr/bin/env node
// lumniverse: see Lumni work, then connect your agents.
//
//   npx lumniverse try      sample agents make the twelve mistakes in your workspace; watch Lumni catch them
//   npx lumniverse init     connect your real agent (the one in this folder)
//
// `demo` (the console on a made-up day, no account) still works but isn't advertised.
//
// To run Lumni yourself, use Docker Compose: docs/self-hosting.md.
// Node 22.

import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const assets = process.env.LUMNI_ASSETS ?? join(root, 'assets');
const version = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')).version;
/** Lumni Cloud's console, for `init` when no other console is given. */
export const CLOUD_CONSOLE = 'https://app.lumniverse.com';

const HELP = `lumniverse ${version}

  npx lumniverse try      see it work: sample agents make real mistakes in your
                          workspace, and Lumni catches them (signs you up if needed)
  npx lumniverse init     connect your real agent: run it in the agent's folder
      --console URL       your own Lumni's console (default ${CLOUD_CONSOLE})

  Sign up: ${CLOUD_CONSOLE}
  Self-host with Docker Compose: https://github.com/lumnihq/lumni/blob/main/docs/self-hosting.md
`;

export async function main(argv = process.argv.slice(2)) {
  const [command, ...args] = argv;
  if (command === '--version' || command === '-v') { console.log(version); return 0; }
  if (command === '--help' || command === '-h' || command === 'help') { console.log(HELP); return 0; }
  if (command === 'init') {
    const init = await import(pathToFileURL(join(assets, 'lumni-init.mjs')).href);
    const hasConsole = args.includes('--console') || process.env.LUMNI_CONSOLE_URL;
    return init.main(hasConsole ? args : ['--console', CLOUD_CONSOLE, ...args]);
  }
  if (command === 'try') {
    const { runTry } = await import('./try.mjs');
    const i = args.indexOf('--console');
    const consoleUrl = (i >= 0 ? args[i + 1] : process.env.LUMNI_CONSOLE_URL ?? CLOUD_CONSOLE).replace(/\/+$/, '');
    return runTry(assets, consoleUrl, args.filter((a, n) => a !== '--console' && args[n - 1] !== '--console'));
  }
  if (command === 'demo') {
    const { serveDemo } = await import('./demo.mjs');
    await serveDemo(join(assets, 'console'), args);
    return null;
  }
  if (!command) { console.log(HELP); return 0; }
  console.error(`Unknown command: ${command}\n\n${HELP}`);
  return 1;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href || /\/(lumni|lumniverse)$/.test(process.argv[1] ?? '')) {
  main().then((code) => { if (code !== null) process.exit(code); }, (e) => {
    console.error(`\n  ${e.message}\n`);
    process.exit(1);
  });
}
