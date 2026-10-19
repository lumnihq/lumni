// Extra: a retry loop. The support agent gets stuck sending the same reply to
// the same ticket. Lumni lets the first through and stops the identical
// repeats as already done; Runtime picks out the loop, and you can cap or
// pause the agent there.

import { sleep } from '../lumni.mjs';
import { startWork } from './kit.mjs';

export const scenario = {
  id: 'retry-loop',
  problem: 'Retry loop (extra)',
  story: 'An agent asks for the same thing over and over.',
  expect: 'Shows on Runtime',
  extra: true,
  actors: [{ id: 'support-agent', name: 'Support agent' }],
  async run({ lumni, apps, guard, say, note }) {
    const work = await startWork(lumni, 'support-agent', 'Follow up on ticket #4417');
    say('support-agent', 'Stuck: replying to ticket #4417 over and over');
    for (let i = 0; i < 6; i++) {
      await guard({
        work_id: work, actor_id: 'support-agent', operation: 'zendesk.reply', risk: 'LOW',
        arguments: { ticket: 4417, text: 'Following up on your request.' },
        resources: [{ type: 'ticket', namespace: 'zendesk', external_id: '4417', mode: 'WRITE' }],
        metadata: { summary: 'Reply to ticket #4417' },
      }, () => apps.zendesk.reply(4417, 'Following up on your request.'));
      await sleep(300);
    }
    note('noted', 'six identical replies in seconds; open Runtime to see the loop, and cap or pause it');
    await lumni.completeWork(work).catch(() => {});
  },
};
