// `npx lumniverse demo`: the console's screens on a made-up day, served from this
// package. Every screen builds its world in the page, so nothing else runs:
// no API, no account. Your own workspace: sign up at app.lumniverse.com.

import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { extname, join, normalize, sep } from 'node:path';

const TYPES = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.json': 'application/json' };
const CONFIG = JSON.stringify({ mode: 'mock', realAvailable: false, workspace: null, autoSignIn: false, adminKey: false, auth: 'key', apiUrl: null, demo: true });

function open(url) {
  const [cmd, args] = process.platform === 'darwin' ? ['open', [url]] : process.platform === 'win32' ? ['cmd', ['/c', 'start', '', url]] : ['xdg-open', [url]];
  try { spawn(cmd, args, { stdio: 'ignore', detached: true }).on('error', () => {}).unref(); } catch {}
}

export function demoHandler(dir) {
  return async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const path = decodeURIComponent(url.pathname);
    if (path === '/console/config.json') return void res.writeHead(200, { 'content-type': 'application/json', 'cache-control': 'no-store' }).end(CONFIG);
    if (path.startsWith('/v1/') || path.startsWith('/console/')) {
      return void res.writeHead(503, { 'content-type': 'application/json' }).end(JSON.stringify({ error: { code: 'DEMO', message: 'This is the demo: there’s no workspace behind it. Sign up at app.lumniverse.com for your own.' } }));
    }
    if (path === '/' || path === '/index.html') return void res.writeHead(302, { location: '/today' }).end();
    // /today → today.html; /lib/shell.js as is. Nothing outside the folder.
    const file = normalize(join(dir, /\.[a-z]+$/.test(path) ? path : `${path}.html`));
    if (!file.startsWith(dir + sep)) return void res.writeHead(403).end();
    try {
      if (!(await stat(file)).isFile()) throw new Error('not a file');
      res.writeHead(200, { 'content-type': TYPES[extname(file)] ?? 'application/octet-stream', 'cache-control': 'no-cache' }).end(await readFile(file));
    } catch {
      res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' }).end('Not found');
    }
  };
}

export async function serveDemo(dir, args = []) {
  const p = args.indexOf('--port');
  const port = p >= 0 ? Number(args[p + 1]) : 4173;
  const server = createServer(demoHandler(normalize(dir)));
  await new Promise((ok, fail) => { server.once('error', fail); server.listen(port, '127.0.0.1', ok); });
  const url = `http://127.0.0.1:${server.address().port}/today?data=mock`;
  console.log(`\n  Lumni demo: ${url}\n  A made-up day at three sizes; switch them in the top bar. Ctrl-C to stop.\n  Your own workspace: https://app.lumniverse.com\n`);
  if (!args.includes('--no-open')) open(url);
  for (const s of ['SIGINT', 'SIGTERM']) process.on(s, () => server.close(() => process.exit(0)));
  return server;
}
