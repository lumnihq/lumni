// /v1/console/frame with real data: what each tab says under its name, the
// mode chip, and who’s signed in. Nothing here is a demo, so the demo’s menu
// items stay hidden, and controls with no API behind them are marked.

import type { Caps, FrameView } from '../../../src/domain/types';
import { ms } from '../../upstream';
import type { Ctx } from '../load';
import { appIdOf, isBad, stepKind } from '../model';
import { checkers, proofIn, rules } from './shared';

/** What real data can do today. Each one switches on when the API has it. */
export const REAL_CAPS: Caps = {
  pause: false,
  spendLimit: false,
  notify: false,
  handled: false,
  rulePreview: false,
  playground: false,
  customerMap: false,
  connect: false,
  appRetry: false,
};

export function openMismatches<T extends { id: string; kind: string; status: string }>(incidents: T[]): T[] {
  return incidents.filter((i) => i.id.startsWith('outcome_') && i.kind.startsWith('OUTCOME_') && i.status === 'OPEN');
}

export async function frameView(ctx: Ctx, me: { initials: string; email: string }): Promise<FrameView> {
  const [day, approvals, incidents, rs, chk, spend] = await Promise.all([
    ctx.today(),
    ctx.approvals(),
    ctx.incidents(),
    rules(ctx),
    checkers(ctx),
    ctx.finops(ctx.zone.dayStart(ctx.now), ctx.now).catch(() => null),
  ]);
  const pending = approvals.filter((a) => a.status === 'PENDING');
  const bad = openMismatches(incidents);
  const expiries = pending.flatMap((a) => {
    const t = ms(a.expires_at);
    return t === null ? [] : [t];
  });
  const proofOf = proofIn(chk);

  let stepins = 0;
  let badN = 0;
  let checked = 0;
  const used = new Set<string>();
  for (const a of day.acts) {
    if (stepKind(a)) stepins += 1;
    if (isBad(a)) badN += 1;
    if (proofOf(a).kind === 'ok') checked += 1;
    used.add(appIdOf(a));
  }
  const working = new Set(day.acts.filter((a) => a.status === 'ACTIVE' || a.status === 'RESERVED').map((a) => a.actor ?? a.id)).size;
  const protecting = rs.list.filter((r) => r.mode === 'ENFORCE').length;
  const watching = rs.list.length - protecting;
  const appsChecked = [...used].filter((id) => chk.byApp.get(id)?.enabled).length;

  return {
    demo: false,
    caps: REAL_CAPS,
    needs: pending.length + bad.length,
    protecting,
    watching,
    paused: false,
    clockPaused: false,
    org: ctx.env.workspace ?? 'Your workspace',
    me: me.initials,
    email: me.email,
    now: ctx.now,
    notes: [],
    tabs: {
      today: { stepins },
      live: { working, paused: false },
      // Outcomes Lumni is checking again after a retry.
      map: { fixing: day.acts.filter((a) => a.outcome?.status === 'PENDING' && (a.outcome.attempts ?? 0) === 0 && a.outcome.updatedAt !== null && a.outcome.updatedAt > a.at + 60_000).length },
      receipts: { bad: badN, checked },
      needs: { count: pending.length + bad.length, expiresAt: expiries.length ? Math.min(...expiries) : null },
      rules: { protecting, watching },
      spend: { spent: (spend?.total_cost_micros_usd ?? 0) / 1e6, limit: null },
      apps: { checked: appsChecked, used: used.size },
      playground: { handled: 0, total: 0 },
    },
  };
}
