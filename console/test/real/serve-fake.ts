// The tests’ fake Lumni API as a server, for working on the real-data screens
// without Docker. It listens where lumnid would (:8080), takes the admin key
// from console/.env, and holds a busy day in memory: approvals, retries and
// rule changes made in the console change it until it stops.
//
//   bun test/real/serve-fake.ts            then open the console with real data
//
// Stop it before starting the real API, since both want the same port.

import { parseDotEnv } from '../../server/env';
import { localZoneName } from '../../src/domain/format';
import { richDay } from './rich';
import { fakeFetch } from './fake-go';

const file = Bun.file(`${import.meta.dir}/../../.env`);
const dotenv = (await file.exists()) ? parseDotEnv(await file.text()) : {};
const key = (Bun.env.LUMNI_CONSOLE_ADMIN_KEY ?? dotenv.LUMNI_CONSOLE_ADMIN_KEY ?? Bun.env.LUMNI_FRONTFACE_TEST_ADMIN_KEY ?? dotenv.LUMNI_FRONTFACE_TEST_ADMIN_KEY ?? 'test_admin_key').trim();
const port = Number(Bun.env.FAKE_API_PORT ?? 8080);

const w = richDay(Number(Bun.env.FAKE_API_ACTIONS ?? 900), { key, perSnapshot: 400 }, localZoneName());
const answer = fakeFetch(w);

const server = Bun.serve({
  port,
  hostname: '127.0.0.1',
  async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === '/v1/health/ready') return Response.json({ status: 'ready' });
    const headers: Record<string, string> = {};
    req.headers.forEach((v, k) => {
      headers[k] = v;
    });
    const body = req.method === 'GET' || req.method === 'HEAD' ? undefined : await req.text();
    return answer(`http://fake${url.pathname}${url.search}`, { method: req.method, headers, body });
  },
});

console.log(`Fake Lumni API on ${server.url} with ${w.actions.length} actions today`);
