// /v1/console/world and /v1/console/journey/:id against a fake Go API with a
// full day: the real world has the demo world's shape, its numbers add up, and
// nothing is filled in that the records don't say.

import { describe, expect, test } from 'bun:test';
import { readEnv } from '../../server/env';
import { createBff } from '../../server/index';
import { busyDay } from './day';
import { fakeFetch, type FakeWorld } from './fake-go';
import { richDay } from './rich';

// Late afternoon, so the morning burst is in the day.
const NOW = Date.UTC(2026, 9, 14, 16, 20);

function setUp(w: FakeWorld, vars: Record<string, string> = {}) {
  const env = readEnv({ LUMNI_CONSOLE_DATA_MODE: 'real', LUMNI_API_URL: 'http://go.test', LUMNI_CONSOLE_ADMIN_KEY: w.key, PORT: '5200', ...vars });
  const bff = createBff(env, fakeFetch(w));
  const jar = new Map<string, string>();
  const send = async (path: string) => {
    const headers: Record<string, string> = { host: 'localhost:5200', 'x-lumni-tz': 'UTC' };
    if (jar.size) headers.cookie = [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
    const res = (await bff.handle(new Request(`http://localhost:5200${path}`, { headers })))!;
    for (const line of res.headers.getSetCookie()) {
      const [k, v] = line.split(';')[0]!.split('=');
      jar.set(k!, v!);
    }
    const text = await res.text();
    return { status: res.status, data: text ? JSON.parse(text) : undefined };
  };
  return { send };
}

// The fake reads the real clock for "today"; pin it.
const realNow = Date.now;
function at<T>(t: number, fn: () => Promise<T>): Promise<T> {
  Date.now = () => t;
  return fn().finally(() => {
    Date.now = realNow;
  });
}

async function realWorld(w: FakeWorld, vars: Record<string, string> = {}) {
  return at(NOW, async () => {
    const { send } = setUp(w, vars);
    await send('/v1/auth/session');
    const r = await send('/v1/console/world');
    expect(r.status).toBe(200);
    return r.data;
  });
}

describe('the real world', () => {
  test('has every field the screens read', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    for (const k of ['key', 'now', 'nowH', 'dayLabel', 'timeLabel', 'scale', 'total', 'kinds', 'would', 'steps', 'kept', 'hourly', 'stepHourly', 'proven', 'spend', 'pace', 'working', 'teams', 'apps', 'patterns', 'groups', 'fleet', 'money', 'incident', 'things', 'facts', 'LEVELS']) {
      expect(w[k]).toBeDefined();
    }
    expect(w.key).toBe('real');
    expect(w.hourly).toHaveLength(24);
  });

  test('every AI has its main operation, and the long tail isn’t a thing', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    for (const f of w.fleet as { name: string; op: string | null }[]) expect(f.op).toBeTruthy();
    expect((w.things as { id: string }[]).some((t) => t.id === 'other')).toBe(false);
    for (const f of w.fleet as { runaway: { operation: string | null } | null }[]) if (f.runaway) expect(f.runaway.operation).toBeTruthy();
  });

  test('new agents can be asked for by when they were made', async () => {
    const day = richDay(20, {}, 'UTC', NOW);
    const fetchFn = fakeFetch(day);
    const auth = { authorization: `Bearer ${day.key}`, 'content-type': 'application/json' };
    const before = new Date().toISOString();
    await fetchFn('http://go.test/v1/actors', { method: 'POST', headers: auth, body: JSON.stringify({ id: 'new-agent', type: 'AGENT' }) });
    const all = await (await fetchFn('http://go.test/v1/actors', { headers: auth })).json();
    const fresh = await (await fetchFn(`http://go.test/v1/actors?from=${encodeURIComponent(before)}`, { headers: auth })).json();
    expect(all.items.length).toBeGreaterThan(1);
    expect(fresh.items.map((a: { id: string }) => a.id)).toEqual(['new-agent']);
  });

  test('the change stream says what happened after the last event the browser saw', async () => {
    const day = richDay(20, {}, 'UTC', NOW);
    const fetchFn = fakeFetch(day);
    const auth = { authorization: `Bearer ${day.key}`, 'content-type': 'application/json' };
    const first = await (await fetchFn('http://go.test/v1/graph/stream?types=action.checked', { headers: auth })).text();
    const head = /id: (\d+)/.exec(first)![1]!;
    await fetchFn('http://go.test/v1/actors', { method: 'POST', headers: auth, body: JSON.stringify({ id: 'bot', type: 'AGENT' }) });
    await fetchFn('http://go.test/v1/actions/check', { method: 'POST', headers: auth, body: JSON.stringify({ action_id: 'a_new', actor_id: 'bot', work_id: 'w', operation: 'x.y' }) });
    const next = await (await fetchFn('http://go.test/v1/graph/stream?types=action.checked', { headers: { ...auth, 'last-event-id': head } })).text();
    expect(next).toContain('"event_type":"action.checked"');
    expect(next).toContain('"entity_id":"a_new"');
    expect(next).not.toContain('actor.created');
  });

  test('pulse: exact totals, the newest decisions told, and only what came after the cursor', async () => {
    const day = richDay(300, {}, 'UTC', NOW);
    const { send } = setUp(day);
    await at(NOW, async () => {
      await send('/v1/auth/session');
      const first = await send('/v1/console/pulse');
      expect(first.status).toBe(200);
      expect(first.data.totals.total).toBe(day.actions.filter((a) => a.at >= Date.UTC(2026, 9, 14) && a.at <= NOW).length);
      expect(first.data.events.length).toBeGreaterThan(0);
      expect(first.data.events.length).toBeLessThanOrEqual(8);
      for (const e of first.data.events) {
        expect(['allow', 'stop', 'wait', 'recheck', 'ask', 'bad']).toContain(e.kind);
        expect(e.who.name).toBeTruthy();
        expect(e.at).toBeGreaterThan(NOW - 10 * 60_000);
      }
      const later = await send(`/v1/console/pulse?after=${first.data.cursor}`);
      expect(later.data.events).toEqual([]);
      expect(later.data.more).toBe(0);
    });
  });

  test('counts add up', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    const hours = (w.hourly as number[]).reduce((a, b) => a + b, 0);
    expect(hours).toBe(w.total);
    expect(w.steps).toBe(w.kinds.stop + w.kinds.wait + w.kinds.recheck + w.kinds.ask + w.kinds.bad);
    expect(w.kinds.stop).toBeGreaterThan(40);
    expect(w.kinds.recheck).toBeGreaterThan(0);
    expect(w.kinds.wait).toBeGreaterThan(0);
    expect(w.kinds.bad).toBeGreaterThan(0);
    expect(w.would.ask).toBeGreaterThan(0);
    const teamTotal = (w.teams as { n: number }[]).reduce((s, t) => s + t.n, 0);
    expect(teamTotal).toBe(w.total);
  });

  test('step-ins are grouped by reason, with a real action to replay', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    const top = w.patterns.stop[0];
    expect(top.t).toContain('Refund agent');
    expect(top.n).toBeGreaterThan(40);
    expect(top.kept).toBeGreaterThan(0);
    expect(top.story).toMatch(/^a_/);
    // The burst at 11:36 makes the 11 AM hour stand out.
    expect(Math.floor(top.spike)).toBe(11);
  });

  test('waiting decisions are grouped by the rule that asked', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    const groups = w.groups as { t: string; n: number; amount: number[] | null; members: { amount: number; mins: number }[] }[];
    expect(groups).toHaveLength(2);
    const refunds = groups.find((g) => /Refund/i.test(g.t))!;
    expect(refunds.n).toBe(4);
    expect(refunds.amount).toEqual([540, 7440]);
    expect(refunds.members.map((m) => m.mins).sort((a, b) => a - b)).toEqual([6, 12, 24, 31]);
  });

  test('the fleet is each AI, with its level from the enforcement settings', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    const fleet = w.fleet as { name: string; levelName: string; loop: number; copies: number; lag: number | null }[];
    expect(fleet.map((f) => f.name)).toContain('Price setter');
    expect(fleet.find((f) => f.name === 'Price setter')!.levelName).toBe('Warn');
    expect(fleet.find((f) => f.name === 'Refund agent')!.levelName).toBe('Enforce');
    expect(fleet.find((f) => f.name === 'Deploy bot')!.loop).toBe(1);
    expect(fleet.find((f) => f.name === 'Ticket triage')!.copies).toBe(2);
    // Read age comes from the freshness analytics: real seconds, not made up.
    expect(fleet.find((f) => f.name === 'Price setter')!.lag).toBeGreaterThan(0);
  });

  test('apps say whether Lumni can check them', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    const gmail = (w.apps as { id: string; blind: boolean }[]).find((a) => a.id === 'gmail')!;
    expect(gmail.blind).toBe(true);
    expect((w.apps as { id: string; blind: boolean }[]).find((a) => a.id === 'stripe')!.blind).toBe(false);
  });

  test('the morning burst reads as an incident, built from its own actions', async () => {
    const w = await realWorld(richDay(900, {}, 'UTC', NOW));
    expect(w.incident).not.toBeNull();
    expect(w.incident.start).toMatch(/^11:3\d AM$/);
    expect(w.incident.burst).toBeGreaterThan(30);
    expect(w.incident.events.length).toBeGreaterThan(2);
  });

  test('the return uses what Lumni’s billing says it cost', async () => {
    const w = await realWorld(richDay(300, {}, 'UTC', NOW));
    // The fake bills the team plan: 20,000 micro-USD per checked action.
    expect(w.money.pricePerAction).toBeCloseTo(0.02, 6);
    expect(w.money.lumniCost).toBeGreaterThan(0);
    expect(w.money.roi).toBeGreaterThan(0);
  });

  test('a quiet workspace is an honest, nearly empty world', async () => {
    const w = await at(NOW, async () => realWorld(busyDay(0)));
    expect(w.total).toBe(1);
    expect(w.incident).toBeNull();
    expect(w.patterns.stop).toEqual([]);
  });
});

describe('a real journey', () => {
  test('a stopped repeat stops at the second check', async () => {
    await at(NOW, async () => {
      const { send } = setUp(richDay(900, {}, 'UTC', NOW));
      await send('/v1/auth/session');
      const r = await send('/v1/console/journey/a_burst_3');
      expect(r.status).toBe(200);
      const j = r.data;
      expect(j.end).toBe('stop');
      expect(j.stages.map((s: { id: string; hit: boolean }) => [s.id, s.hit])).toEqual([['fresh', false], ['done', true], ['other', false], ['rule', false]]);
      expect(j.kept).toBeGreaterThan(0);
      expect(j.who.name).toBe('Refund agent');
    });
  });

  test('an unknown action is a 404', async () => {
    await at(NOW, async () => {
      const { send } = setUp(richDay(50, {}, 'UTC', NOW));
      await send('/v1/auth/session');
      expect((await send('/v1/console/journey/nope')).status).toBe(404);
    });
  });
});
