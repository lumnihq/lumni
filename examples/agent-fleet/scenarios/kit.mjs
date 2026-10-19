// What several scenarios share.

import { resourceId, shortId } from '../lumni.mjs';
import { usd } from '../apps.mjs';

/** Start a piece of work in Lumni; returns its id. */
export async function startWork(lumni, actor, goal, id = `${actor}-${shortId()}`) {
  await lumni.startWork({ id, actor_id: actor, goal, risk: 'MEDIUM' });
  return id;
}

export const PRICE_LIST = { type: 'price_list', namespace: 'salesforce', external_id: 'enterprise' };
const priceIds = new WeakMap();

/** Tell Lumni the price list's current version, as Salesforce's webhook would. */
export async function recordPriceVersion(lumni, read, by, actionId) {
  if (!priceIds.has(lumni)) priceIds.set(lumni, await resourceId(lumni, PRICE_LIST));
  await lumni.updateResourceVersion(priceIds.get(lumni), {
    version: read.version, version_source: 'salesforce', source: 'salesforce',
    summary: `Enterprise seat price ${usd(read.seatPrice)}`,
    ...(by ? { actor_id: by } : {}), ...(actionId ? { action_id: actionId } : {}),
  });
}

/** A write to the price list, based on what the writer last read. */
export const priceListWrite = (read) => ({ ...PRICE_LIST, mode: 'WRITE', observed_version: read.version, version_source: 'salesforce' });

/**
 * A checker that reads the app afterwards and tells Lumni what really happened
 * (the role Stripe's webhook or a reconciler plays). Needs a key with the
 * `verification` scope; the admin key has it.
 */
export async function ensureVerifier(admin, id, name) {
  await admin.request('POST', '/v1/verifiers', { id, name, kind: 'CUSTOM', version: '1', config: { source: 'agent-fleet' }, enabled: true });
}
export function evidence(admin, actionId, status, observed, reason) {
  return admin.request('POST', `/v1/outcomes/${encodeURIComponent(`out_${actionId}`)}/evidence`, { id: `ev_${actionId}`, status, observed, reason });
}
