// Serves the console. Pages are plain HTML and JS in app/, one file per screen,
// at clean paths (/flow, /needs-you); the shared shell and the demo world are
// in app/lib. The same server answers /v1 for real data (see server/): it signs
// in with the admin key from console/.env, builds /v1/console/world and the
// other views from the Lumni API, and passes the rest of /v1 through. With
// demo data the page builds its world itself and never asks.
//
//   bun serve.ts            http://localhost:5200
//   bun serve.ts --prod     after `bun run build`, from dist/

import { parseDotEnv, readEnv } from './server/env';
import { appHandler, SECURITY } from './server/app';
import { createBff } from './server/index';

const file = Bun.file(`${import.meta.dir}/.env`);
const dotenv = (await file.exists()) ? parseDotEnv(await file.text()) : {};
const env = readEnv({ ...dotenv, ...Bun.env });

const port = Number(Bun.env.PORT ?? dotenv.PORT ?? 5200);
const hostname = Bun.env.HOST ?? 'localhost';
const prod = Bun.argv.includes('--prod');
const root = `${import.meta.dir}/${prod ? 'dist' : 'app'}`;
const bff = createBff({ ...env, hosts: [...new Set([`localhost:${port}`, `127.0.0.1:${port}`, ...env.hosts])] });

const app = appHandler(root, prod ? 'long' : 'no-cache', { publicApi: env.publicApi, publicConsole: Bun.env.LUMNI_CONSOLE_PUBLIC_URL ?? dotenv.LUMNI_CONSOLE_PUBLIC_URL });

const server = Bun.serve({
  port,
  hostname,
  idleTimeout: 60,
  async fetch(req, srv) {
    const api = await bff.handle(req, () => srv.timeout(req, 0));
    if (api) return api;
    const u = new URL(req.url);
    const a = await app(u.pathname, u.origin);
    if (a) return a;
    return new Response('Not found', { status: 404, headers: { 'content-type': 'text/plain; charset=utf-8', ...SECURITY } });
  },
});

console.log(`Lumni console on ${server.url}${prod ? ' (dist)' : ''}, ${env.mode === 'real' ? `real data from ${env.api}` : 'demo data'} by default`);
