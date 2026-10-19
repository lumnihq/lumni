// `npx lumniverse try`: sample agents act out the twelve problems Lumni solves,
// in your own workspace, so Today, Flow and the rest fill with real-looking
// work in a minute. You approve it in the browser; the run's key (it plays
// the checker and sets a limit, so it can do anything) is revoked at the end.

import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

export async function runTry(assets, consoleUrl, args = [], { fetchFn = fetch, log = console.log, sleep, open, browser } = {}) {
  const init = await import(pathToFileURL(join(assets, 'lumni-init.mjs')).href);
  const fleet = await import(pathToFileURL(join(assets, 'fleet', 'run.mjs')).href);

  log('\n  Sample agents for your Lumni workspace\n');
  const login = await init.deviceLogin(consoleUrl, 'lumni-try', { fetchFn, sleep, open, browser, client: 'lumni try', ask: 'Approve the sample agents in your browser' });
  const api = String(login.api_url ?? '').replace(/\/+$/, '');
  if (!api) throw new Error('The console didn’t say where its API is. Update the console, then run this again.');
  const key = await init.exchange(consoleUrl, login.code, 'lumni-try', fetchFn, api);
  log(`  ✓ Approved: running in ${key.workspace ?? 'your workspace'}\n`);

  let code = 1;
  try {
    const dir = mkdtempSync(join(tmpdir(), 'lumni-try-'));
    code = await fleet.main(['--admin-key', key.api_key, ...args], { LUMNI_URL: api, LUMNI_API_KEY: key.api_key }, { fetchFn, log, dir, sleep });
  } finally {
    const revoked = key.key_id
      ? await fetchFn(`${api}/v1/api-keys/${encodeURIComponent(key.key_id)}`, { method: 'DELETE', headers: { authorization: `Bearer ${key.api_key}` } }).then((r) => r.ok, () => false)
      : false;
    log(revoked ? '\n  ✓ Revoked the run’s key' : `\n  ! Couldn’t revoke the run’s key${key.key_id ? ` (${key.key_id})` : ''}. Revoke it in the console: Settings → API keys.`);
  }
  log(`\n  See what Lumni caught: ${consoleUrl}/today`);
  log(`
  That was sample agents. Next, your real one:

    In your agent's folder:   npx lumniverse init
    Or let your coding agent: npx skills add lumnihq/lumni
                              then ask it to "add Lumni to the refund step"

  Works with LangGraph, OpenAI Agents, Temporal, MCP, or plain
  TypeScript, Python and Go. Step by step: ${consoleUrl}/start
`);
  return code;
}
