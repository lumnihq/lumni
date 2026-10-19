// /v1/console/apps with real data: the apps your AIs worked in today, and
// whether Lumni checks each one. An app is checked when it has a checker
// (a verifier) switched on. Switching one back on works; adding an app Lumni
// has no checker for needs a connector the API doesn’t have yet.

import type { AppLane, AppRow, AppState, AppsView, ProofV, Who } from '../../../src/domain/types';
import { fmt, plural, time } from '../../../src/domain/format';
import type { GoVerifier } from '../go';
import type { Ctx } from '../load';
import { appIdOf, appName, didOf, isBad, type Act } from '../model';
import { and } from '../words';
import { checkers, people, proofIn } from './shared';

interface Day {
  id: string;
  name: string;
  verifier: GoVerifier | null;
  state: AppState;
  acts: { a: Act; p: ProofV['kind'] }[];
}

export async function appsView(ctx: Ctx): Promise<AppsView> {
  const z = ctx.zone;
  const now = z.hourOf(ctx.now);
  const [day, chk, who] = await Promise.all([ctx.today(), checkers(ctx), people(ctx)]);
  const proofOf = proofIn(chk);

  const by = new Map<string, Day>();
  const get = (id: string) => {
    let d = by.get(id);
    if (!d) {
      const v = chk.byApp.get(id) ?? null;
      d = { id, name: appName(id), verifier: v, state: v?.enabled ? 'connected' : 'off', acts: [] };
      by.set(id, d);
    }
    return d;
  };
  for (const v of chk.byApp.keys()) get(v);
  for (const a of day.acts) get(appIdOf(a)).acts.push({ a, p: proofOf(a).kind });
  const all = [...by.values()].sort((a, b) => b.acts.length - a.acts.length || a.name.localeCompare(b.name));

  const count = Math.min(96, Math.floor(now * 4) + 1);
  const onRibbon = (d: Day) => d.acts.length > 0 || d.state !== 'connected';
  const lanes: AppLane[] = all.filter(onRibbon).map((d) => {
    const quarters = Array.from({ length: count }, () => ({ seen: 0, blind: 0 }));
    const bad: AppLane['bad'] = [];
    for (const { a, p } of d.acts) {
      const q = Math.min(count - 1, Math.floor(z.hourOf(a.at) * 4));
      if (p === 'none') quarters[q]!.blind += 1;
      else quarters[q]!.seen += 1;
      if (isBad(a)) bad.push({ id: a.id, h: z.hourOf(a.at), at: a.at, text: `“${didOf(a)}”` });
    }
    return { id: d.id, name: d.name, state: d.state, count: d.acts.length, quarters, bad };
  });
  const peak = Math.max(1, ...lanes.flatMap((l) => l.quarters.map((q) => q.seen + q.blind)));

  return {
    head: head(all.filter(onRibbon), lanes),
    now,
    lanes,
    peak,
    apps: all.map((d) => row(d, who)),
    catalog: [],
    tally: {
      checked: all.filter((d) => d.state === 'connected').length,
      off: all.filter((d) => d.state !== 'connected').length,
      bad: lanes.reduce((a, l) => a + l.bad.length, 0),
    },
  };
}

function row(d: Day, who: (id: string | null) => Who): AppRow {
  const n = d.acts.length;
  const blind = d.acts.filter((x) => x.p === 'none').length;
  const checked = d.acts.filter((x) => x.p === 'ok' || x.p === 'bad').length;
  const bad = d.acts.filter((x) => isBad(x.a));
  const units = (v: number) => `${fmt(v)} ${plural(v, 'action')}`;
  const byAi = new Map<string, number>();
  for (const { a } of d.acts) if (a.actor) byAi.set(a.actor, (byAi.get(a.actor) ?? 0) + 1);
  const ais = [...byAi].sort((a, b) => b[1] - a[1]).map(([id]) => id);

  let total: AppRow['total'];
  let note: AppRow['note'];
  if (d.state !== 'connected') {
    const unseen = `/receipts?app=${encodeURIComponent(d.id)}&filter=none`;
    total = n ? { n, text: units(n) } : { n: null, text: 'Nothing yet today' };
    note = d.verifier
      ? { kind: 'none', text: blind ? `${fmt(blind)} not checked` : 'Its checker is off', to: blind ? unseen : undefined }
      : { kind: 'none', text: 'No checker yet', to: blind ? unseen : undefined };
  } else if (!n) {
    total = { n: null, text: 'Nothing yet today' };
    note = { kind: 'quiet', text: 'Ready to check' };
  } else {
    total = { n: checked, text: `${fmt(checked)} checked` };
    note = bad.length
      ? { kind: 'bad', text: `${bad.length} didn’t happen`, to: bad.length === 1 ? `/receipts/${bad[0]!.a.id}` : `/receipts?app=${encodeURIComponent(d.id)}&filter=bad` }
      : blind
        ? { kind: 'none', text: `${fmt(blind)} it can’t check`, to: `/receipts?app=${encodeURIComponent(d.id)}&filter=none` }
        : { kind: 'ok', text: 'All match' };
  }
  return {
    id: d.id,
    verifier: d.verifier?.id ?? '',
    name: d.name,
    desc: d.verifier ? `Checked by ${d.verifier.name}` : 'Lumni has no checker for it yet',
    state: d.state,
    added: false,
    canConnect: !!d.verifier,
    usedBy: ais.slice(0, 4).map(who),
    count: n,
    total,
    note,
    receiptsTo: `/receipts?app=${encodeURIComponent(d.id)}`,
  };
}

function head(used: Day[], lanes: AppLane[]): AppsView['head'] {
  if (!used.length) return { title: 'Your AIs haven’t used an app yet today', sub: 'Each app they work in shows up here, with whether Lumni can check what they did.' };
  const off = used.filter((d) => d.state !== 'connected');
  const on = used.filter((d) => d.state === 'connected');
  const others = on.length ? ` The other ${on.length} ${plural(on.length, 'app is', 'apps are')} checked.` : '';
  if (off.length) {
    const blind = off.reduce((a, d) => a + d.acts.filter((x) => x.p === 'none').length, 0);
    const title = off.length > 3 ? `${off.length} apps aren’t checked` : `${and(off.map((d) => d.name))} ${off.length > 1 ? 'aren’t' : 'isn’t'} checked`;
    const what = blind ? `Your AIs did ${fmt(blind)} ${plural(blind, 'thing')} there today that Lumni can’t check.` : 'Lumni won’t check what your AIs do there until it has a checker that’s on.';
    return { title, sub: `${what}${others}` };
  }
  const total = used.reduce((a, d) => a + d.acts.length, 0);
  const bad = lanes.flatMap((l) => l.bad.map((b) => ({ ...b, app: l.name })));
  const tail = !bad.length
    ? ' Everything checked so far matches.'
    : bad.length === 1
      ? ` ${bad[0]!.app} doesn’t show ${bad[0]!.text} from ${time(bad[0]!.at)}.`
      : ` ${bad.length} actions didn’t match. They’re waiting for you.`;
  return { title: `Lumni checks all ${used.length} ${plural(used.length, 'app')} your AIs use`, sub: `Your AIs took ${fmt(total)} actions in them today.${tail}` };
}
