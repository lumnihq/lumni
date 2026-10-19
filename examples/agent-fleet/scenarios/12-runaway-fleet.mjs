// Unbounded agent fleet: one task wakes up 40 agents.
//
// A research task fans out to 40 sub-agents, each calling a paid web-research
// API at once. With a limit of 5 at a time on web research, Lumni queues the
// rest. Set it with --admin-key (it's added for you), or on Runtime or Tower
// in the console. Like every decision, it only holds agents back in Protect;
// in watch-only mode Lumni records that it would have.

import { sleep } from '../lumni.mjs';
import { startWork } from './kit.mjs';

export const CAP = { id: 'cap-web-research', scope: { operation: 'web.research' }, max_in_flight: 5, mode: 'ENFORCE' };

export const scenario = {
  id: 'runaway-fleet',
  problem: 'Unbounded agent fleet',
  story: 'One task accidentally wakes up 40 agents.',
  expect: 'WAIT (concurrency limit)',
  actors: [{ id: 'research-swarm', name: 'Research swarm' }],
  async run({ lumni, admin, apps, guard, say, spend, note }) {
    if (admin) {
      const { id, ...limit } = CAP;
      await admin.request('PUT', `/v1/config/limits/${id}`, limit);
      say('research-swarm', 'Limit set: web research, 5 at a time');
    } else {
      note('no-limit', 'no limit set: pass --admin-key, or set one on Runtime');
    }
    const work = await startWork(lumni, 'research-swarm', 'Research every competitor’s pricing page');
    say('research-swarm', 'Fanned out to 40 sub-agents');
    const topics = Array.from({ length: 40 }, (_, i) => `competitor-${String(i + 1).padStart(2, '0')}`);
    await Promise.all(topics.map(async (topic, i) => {
      await sleep(i * 10);
      await guard({
        work_id: work, actor_id: 'research-swarm', session_id: `sub-agent-${i + 1}`, operation: 'web.research', risk: 'LOW',
        arguments: { topic },
        resources: [{ type: 'page', namespace: 'web', external_id: topic, mode: 'READ' }],
        metadata: { summary: `Research ${topic}` },
      }, async (d) => {
        await spend({ actor: 'research-swarm', work, action: d.action_id, usd: 0.3, tokens: 10_000 });
        return apps.research.fetch(topic, CAP.max_in_flight, this.id);
      }, { quietWaits: i > 0 });
    }));
    say('research-swarm', `Done: ${apps.state.research.calls} calls, at most ${apps.state.research.maxAtOnce} at once`);
    await lumni.completeWork(work).catch(() => {});
  },
};
