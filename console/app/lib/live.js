// Live execution: AIs working shared business state at the same time, and
// every decision Lumni made for them. Two scenes:
//
//   payment   Three AIs on one $2,400 order. The Order AI reserves stock, the
//             Payment AI charges the card, and the customer cancels while both
//             are busy. With the fault on, Stripe charges the card but its
//             answer never reaches the Payment AI. Lumni holds everyone off the
//             order, asks Stripe what happened, then lets the refund, the stock
//             release and the cancel run one at a time.
//   supplier  The Sales AI prices a quote on the supplier's cost, version 18.
//             With the change on, the supplier raises the cost (version 19)
//             before the quote goes out. The quote declared what it was built
//             on, so Lumni sends it back (REPLAN STALE_OBSERVATION) and the
//             corrected quote goes out instead.
//
//   const s = Live.scenario({ scene: 'supplier', fault: true });
//   const v = Live.at(s, 12.5);                // everything on screen at 12.5s
//   const r = Live.result(s);                  // how it ended up
//
// Every decision, reason code and route here is one the Lumni API really
// returns (pkg/protocol/types.go, internal/api/server.go), so the screen can
// later read the same shapes from a real run. Nothing here touches the DOM.

(() => {
  const ORDER = { id: 'ORD-1042', customer: 'Northwind Industrial', item: 'Hydraulic valve assembly', sku: 'SKU-7731', qty: 10, total: 2400 };
  const PI = 'pi_3QxL7kA2eZvKYlo2C1h8';
  const RE = 're_3QxL7kA2eZvKYlo2C9tq';
  const WORK = { order: 'wrk_fulfil_1042', payment: 'wrk_pay_1042', cancel: 'wrk_cancel_1042', release: 'wrk_release_1042' };

  const AGENTS = [
    { id: 'order', name: 'Order AI', short: 'OR', actor: 'order-agent', goal: 'Reserve and ship 10 units' },
    { id: 'payment', name: 'Payment AI', short: 'PA', actor: 'payment-agent', goal: 'Charge the customer $2,400' },
    { id: 'cancel', name: 'Cancellation AI', short: 'CA', actor: 'cancellation-agent', goal: 'Handle the cancel request' },
  ];

  const usd = (n) => `$${n.toLocaleString('en-US', { minimumFractionDigits: n % 1 ? 2 : 0 })}`;
  const ORDER_PILL = { pending: '', payment_pending: 'mid', cancelled: 'end' };
  const PAY_PILL = { none: '', captured: 'mid', refunded: 'good' };

  // Each system says how its rows read: a title, a line under it, and cells
  // that are either numbers with a label or pills. `key` marks what changed.
  const SYSTEMS = [
    { id: 'inventory', name: 'Inventory', kind: 'Postgres, test database', logo: ['#0d0e14', 'DB'],
      row: (id, r) => ({ title: id, sub: r.item, cells: [{ key: 'available', label: 'available', value: r.available }, { key: 'reserved', label: 'reserved', value: r.reserved }] }) },
    { id: 'stripe', name: 'Stripe', kind: 'Test mode', logo: ['#635bff', 'S'], empty: 'No charges or refunds yet',
      row: (id, r) => ({ title: id, sub: `${r.type}, ${usd(r.amount)}`, cells: [{ key: 'status', pill: r.type === 'Refund' ? 'refunded' : r.status, tone: r.type === 'Refund' ? 'good' : 'mid' }] }) },
    { id: 'orders', name: 'Orders', kind: 'Postgres, test database', logo: ['#3f4352', 'DB'],
      row: (id, r) => ({ title: id, sub: usd(r.total), cells: [{ key: 'status', pill: r.status, tone: ORDER_PILL[r.status] ?? '' }, { key: 'payment', pill: r.payment === 'none' ? 'unpaid' : r.payment, tone: PAY_PILL[r.payment] ?? '' }] }) },
  ];

  /** The state of the three systems before anything runs. */
  const START = () => ({
    inventory: { [ORDER.sku]: { item: ORDER.item, available: 40, reserved: 0 } },
    stripe: {},
    orders: { [ORDER.id]: { customer: ORDER.customer, total: ORDER.total, status: 'pending', payment: 'none' } },
  });

  // What each kind of decision looks like on an agent's card. `tone` picks the
  // colour: run = ink, wait = sky, hold = grey, replan = orange, done = violet.
  const CARD = {
    idle: { words: 'Not started', tone: 'idle' },
    run: { words: 'Runs', tone: 'run' },
    wait: { words: 'Waits its turn', tone: 'wait' },
    hold: { words: 'Held, outcome unknown', tone: 'hold' },
    replan: { words: 'Looks again', tone: 'replan' },
    lost: { words: 'Call timed out', tone: 'fault' },
    done: { words: 'Done and checked', tone: 'done' },
    checked: { words: 'Checked, not retried', tone: 'done' },
  };

  const check = (agent, op, resources, extra = {}) => ({
    method: 'POST',
    path: '/v1/actions/check',
    body: { work_id: WORK[agent], actor_id: AGENTS.find((a) => a.id === agent)?.actor ?? agent, operation: op, resources, ...extra },
  });
  const res = (type, id, mode) => ({ type, id: `${type}:${id}`, mode });
  const orderW = res('order', ORDER.id, 'WRITE');
  const stockW = res('inventory', ORDER.sku, 'WRITE');
  const decided = (type, reason, more = {}) => ({ decision: type, reason_code: reason, enforced: true, ...more });

  // ─── the story ──────────────────────────────────────

  /** The opening both versions share: stock reserved, the charge started. */
  function opening() {
    return [
      { t: 0, who: 'customer', kind: 'arrive', title: `${ORDER.customer} orders 10 valve assemblies for $2,400`, note: 'Three AIs pick it up at once.' },
      { t: 1, who: 'order', kind: 'run', title: 'Order AI asks to reserve 10 units', card: { ...CARD.run, step: 'Reserving 10 units' },
        api: { ...check('order', 'inventory.reserve', [stockW, res('order', ORDER.id, 'READ')]), response: decided('ALLOW', 'NO_CONFLICT') },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' } },
      { t: 1.6, who: 'payment', kind: 'run', title: 'Payment AI asks to charge $2,400', card: { ...CARD.run, step: 'Charging $2,400' },
        api: { ...check('payment', 'stripe.charge', [orderW, res('payment', ORDER.id, 'WRITE')], { effect_key: `charge:${ORDER.id}` }), response: decided('ALLOW', 'NO_CONFLICT', { lease_token: '41' }) },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, lease: { holder: 'payment', token: 41, queue: [] },
        effects: [{ system: 'orders', row: ORDER.id, set: { status: 'payment_pending' } }] },
      { t: 3.2, who: 'order', kind: 'done', title: '10 units reserved', card: { ...CARD.run, step: 'Reserved, now picking' },
        api: { method: 'POST', path: '/v1/actions/{id}/result', body: { status: 'SUCCEEDED' }, response: { status: 'SUCCEEDED' } },
        effects: [{ system: 'inventory', row: ORDER.sku, set: { available: 30, reserved: 10 } }] },
      { t: 4, who: 'order', kind: 'wait', title: 'Order AI wants to pack the order, but Payment AI has it', card: { ...CARD.wait, step: 'Pick and pack, behind Payment AI' },
        api: { ...check('order', 'order.pack', [orderW, stockW]), response: decided('WAIT', 'LEASE_BUSY', { retry_after_ms: 2000 }) },
        decision: { type: 'WAIT', reason_code: 'LEASE_BUSY' }, lease: { holder: 'payment', token: 41, queue: ['order'] } },
    ];
  }

  /** The ending both versions share, from the moment the refund may run. */
  function ending(t0, queue) {
    let t = t0;
    const at = (d) => (t = Math.round((t + d) * 10) / 10);
    return [
      { t: at(0), who: 'cancel', kind: 'run', title: 'Cancellation AI may refund now', card: { ...CARD.run, step: 'Refunding $2,400' },
        api: { ...check('cancel', 'stripe.refund', [orderW, res('payment', ORDER.id, 'WRITE')], { effect_key: `refund:${ORDER.id}` }), response: decided('ALLOW', 'NO_CONFLICT', { lease_token: '42' }) },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, lease: { holder: 'cancel', token: 42, queue: queue.filter((x) => x !== 'cancel') } },
      { t: at(1.5), who: 'stripe', kind: 'external', title: 'Stripe refunds $2,400',
        effects: [{ system: 'stripe', row: RE, set: { type: 'Refund', amount: ORDER.total, status: 'succeeded', of: PI } }] },
      { t: at(1), who: 'lumni', kind: 'verify', title: 'Lumni reads the refund back from Stripe', lane: 'Refund checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'STRIPE', status: 'VERIFIED', observed: { refund: RE, amount: ORDER.total * 100, metadata: { lumni_effect_id: `refund:${ORDER.id}` } } } },
        effects: [{ system: 'orders', row: ORDER.id, set: { payment: 'refunded' } }] },
      { t: at(0.4), who: 'cancel', kind: 'done', card: { ...CARD.run, step: 'Refunded, now cancelling' }, quiet: true, lease: { holder: null, token: 42, queue: ['order'] } },
      { t: at(0.6), who: 'order', kind: 'run', title: 'Order AI may release the stock now', card: { ...CARD.run, step: 'Releasing 10 units' },
        api: { method: 'POST', path: '/v1/actions/{id}/recheck', response: decided('ALLOW', 'NO_CONFLICT', { lease_token: '43' }) },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, lease: { holder: 'order', token: 43, queue: ['cancel'] } },
      { t: at(1.5), who: 'order', kind: 'done', title: '10 units back on the shelf',
        effects: [{ system: 'inventory', row: ORDER.sku, set: { available: 40, reserved: 0 } }] },
      { t: at(1), who: 'lumni', kind: 'verify', title: 'Lumni reads the stock back from the database', lane: 'Stock checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'POSTGRES', status: 'VERIFIED', observed: { sku: ORDER.sku, available: 40, reserved: 0 } } },
        card: { ...CARD.done, step: 'Stock released' }, cardFor: 'order', decision: { type: 'VERIFIED' }, lease: { holder: null, token: 43, queue: ['cancel'] } },
      { t: at(1), who: 'cancel', kind: 'run', title: 'Cancellation AI marks the order cancelled', card: { ...CARD.run, step: 'Cancelling the order' },
        api: { ...check('cancel', 'order.cancel', [orderW]), response: decided('ALLOW', 'NO_CONFLICT', { lease_token: '44' }) },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, lease: { holder: 'cancel', token: 44, queue: [] },
        effects: [{ system: 'orders', row: ORDER.id, set: { status: 'cancelled' } }] },
      { t: at(1), who: 'lumni', kind: 'verify', title: 'Lumni reads the order back from the database', lane: 'Order checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'POSTGRES', status: 'VERIFIED', observed: { order: ORDER.id, status: 'cancelled', payment: 'refunded' } } },
        card: { ...CARD.done, step: 'Refunded and cancelled' }, cardFor: 'cancel', decision: { type: 'VERIFIED' }, lease: { holder: null, token: 44, queue: [] } },
      { t: at(1), who: 'lumni', kind: 'resolved', title: 'Every system agrees: cancelled, refunded, stock back', note: 'One charge, one refund, nothing left half done.' },
    ];
  }

  /** Stripe charges the card but its answer is lost on the way back. */
  function withFault() {
    return [
      ...opening(),
      { t: 5.5, who: 'stripe', kind: 'external', title: 'Stripe charges the card $2,400', unseen: 'The Payment AI never got this answer', note: 'The Payment AI never hears this.',
        effects: [{ system: 'stripe', row: PI, set: { type: 'PaymentIntent', amount: ORDER.total, status: 'succeeded' } }] },
      { t: 6.5, who: 'fault', kind: 'fault', title: 'Injected fault: Stripe’s answer is dropped after the charge', mark: 'Fault injected', note: 'The call times out after 5 seconds.',
        card: { ...CARD.lost, step: 'No answer from Stripe' }, cardFor: 'payment' },
      { t: 7.2, who: 'payment', kind: 'hold', title: 'Payment AI reports it doesn’t know what happened',
        api: { method: 'POST', path: '/v1/actions/{id}/result', body: { status: 'UNKNOWN', retryable: true, result_metadata: { error: 'stripe timeout after 5s' } }, response: { status: 'UNKNOWN', effect_hold: ['order:ORD-1042', 'payment:ORD-1042'] } },
        card: { ...CARD.hold, step: 'Did the charge go through?' }, hold: true, lease: { holder: null, token: 41, queue: ['order'] } },
      { t: 8.5, who: 'payment', kind: 'hold', title: 'Payment AI tries to charge again. Lumni holds it.', prevented: true, note: 'A second $2,400 charge was never sent.',
        api: { ...check('payment', 'stripe.charge', [orderW, res('payment', ORDER.id, 'WRITE')], { effect_key: `charge:${ORDER.id}` }), response: decided('WAIT', 'ACTION_RESULT_UNKNOWN') },
        decision: { type: 'WAIT', reason_code: 'ACTION_RESULT_UNKNOWN' }, card: { ...CARD.hold, step: 'Retry held, no second charge' } },
      { t: 10, who: 'customer', kind: 'arrive', title: 'The customer asks to cancel ORD-1042', card: { ...CARD.run, step: 'Cancel request received' }, cardFor: 'cancel' },
      { t: 11, who: 'cancel', kind: 'run', title: 'Cancellation AI cancels the fulfilment work',
        api: { method: 'POST', path: `/v1/work/${WORK.order}/cancel`, response: { id: WORK.order, status: 'CANCELLED' } },
        card: { ...CARD.run, step: 'Fulfilment cancelled' } },
      { t: 12, who: 'order', kind: 'replan', title: 'Order AI is told its work was cancelled, and replans',
        api: { method: 'POST', path: '/v1/actions/{id}/recheck', response: decided('REPLAN', 'WORK_CANCELLED') },
        decision: { type: 'REPLAN', reason_code: 'WORK_CANCELLED' }, card: { ...CARD.replan, step: 'New plan: release the 10 units' }, lease: { holder: null, token: 41, queue: [] } },
      { t: 13, who: 'cancel', kind: 'hold', title: 'Cancellation AI wants to refund. Lumni holds it.', note: 'Nobody can refund a charge nobody has seen yet.',
        api: { ...check('cancel', 'stripe.refund', [orderW, res('payment', ORDER.id, 'WRITE')], { effect_key: `refund:${ORDER.id}` }), response: decided('WAIT', 'ACTION_RESULT_UNKNOWN') },
        decision: { type: 'WAIT', reason_code: 'ACTION_RESULT_UNKNOWN' }, card: { ...CARD.hold, step: 'Refund, once the charge is known' }, lease: { holder: null, token: 41, queue: ['cancel'] } },
      { t: 14, who: 'order', kind: 'hold', title: 'Order AI wants to release stock. Lumni holds it too.',
        api: { ...check('release', 'inventory.release', [stockW, orderW]), response: decided('WAIT', 'ACTION_RESULT_UNKNOWN') },
        decision: { type: 'WAIT', reason_code: 'ACTION_RESULT_UNKNOWN' }, card: { ...CARD.hold, step: 'Release, after the refund' }, lease: { holder: null, token: 41, queue: ['cancel', 'order'] } },
      { t: 15.5, who: 'lumni', kind: 'verify', title: 'Lumni asks Stripe instead of retrying: the charge went through', lane: 'Asked Stripe', clears: [`stripe/${PI}`],
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'HTTP', request: `GET https://api.stripe.com/v1/payment_intents/${PI}`, status: 'VERIFIED', observed: { id: PI, status: 'succeeded', amount: ORDER.total * 100 } } } },
      { t: 16.5, who: 'payment', kind: 'done', title: 'Payment AI finishes from Stripe’s own answer. No retry.',
        card: { ...CARD.checked, step: 'Charged once, confirmed by Stripe' }, decision: { type: 'VERIFIED' }, hold: false,
        effects: [{ system: 'orders', row: ORDER.id, set: { payment: 'captured' } }] },
      ...ending(17.5, ['cancel', 'order']),
    ];
  }

  /** No fault: the charge comes back, and the cancel simply waits its turn. */
  function withoutFault() {
    return [
      ...opening(),
      { t: 5, who: 'customer', kind: 'arrive', title: 'The customer asks to cancel ORD-1042', card: { ...CARD.run, step: 'Cancel request received' }, cardFor: 'cancel' },
      { t: 5.5, who: 'stripe', kind: 'external', title: 'Stripe charges the card $2,400',
        effects: [{ system: 'stripe', row: PI, set: { type: 'PaymentIntent', amount: ORDER.total, status: 'succeeded' } }] },
      { t: 6, who: 'cancel', kind: 'run', title: 'Cancellation AI cancels the fulfilment work',
        api: { method: 'POST', path: `/v1/work/${WORK.order}/cancel`, response: { id: WORK.order, status: 'CANCELLED' } },
        card: { ...CARD.run, step: 'Fulfilment cancelled' } },
      { t: 6.8, who: 'cancel', kind: 'wait', title: 'Cancellation AI wants to refund, but Payment AI has the order',
        api: { ...check('cancel', 'stripe.refund', [orderW, res('payment', ORDER.id, 'WRITE')], { effect_key: `refund:${ORDER.id}` }), response: decided('WAIT', 'LEASE_BUSY', { retry_after_ms: 2000 }) },
        decision: { type: 'WAIT', reason_code: 'LEASE_BUSY' }, card: { ...CARD.wait, step: 'Refund, behind Payment AI' }, lease: { holder: 'payment', token: 41, queue: ['order', 'cancel'] } },
      { t: 7.5, who: 'payment', kind: 'done', title: 'Stripe answers: charged',
        api: { method: 'POST', path: '/v1/actions/{id}/result', body: { status: 'SUCCEEDED', external_result_id: PI }, response: { status: 'SUCCEEDED' } },
        effects: [{ system: 'orders', row: ORDER.id, set: { payment: 'captured' } }] },
      { t: 8.5, who: 'lumni', kind: 'verify', title: 'Lumni reads the charge back from Stripe', lane: 'Charge checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'HTTP', status: 'VERIFIED', observed: { id: PI, status: 'succeeded', amount: ORDER.total * 100 } } },
        card: { ...CARD.done, step: 'Charged once' }, cardFor: 'payment', decision: { type: 'VERIFIED' }, lease: { holder: null, token: 41, queue: ['order', 'cancel'] } },
      { t: 9.2, who: 'order', kind: 'replan', title: 'Order AI is told its work was cancelled, and replans',
        api: { method: 'POST', path: '/v1/actions/{id}/recheck', response: decided('REPLAN', 'WORK_CANCELLED') },
        decision: { type: 'REPLAN', reason_code: 'WORK_CANCELLED' }, card: { ...CARD.replan, step: 'New plan: release the 10 units' }, lease: { holder: null, token: 41, queue: ['cancel'] } },
      { t: 9.8, who: 'order', kind: 'wait', title: 'Order AI queues the release behind the refund',
        api: { ...check('release', 'inventory.release', [stockW, orderW]), response: decided('WAIT', 'LEASE_BUSY', { retry_after_ms: 2000 }) },
        decision: { type: 'WAIT', reason_code: 'LEASE_BUSY' }, card: { ...CARD.wait, step: 'Release, after the refund' }, lease: { holder: null, token: 41, queue: ['cancel', 'order'] } },
      ...ending(10.6, ['cancel', 'order']),
    ];
  }

  /** How the order ended up, read from the systems, not from the story. */
  function paymentTally(systems, events) {
    const stripe = Object.values(systems.stripe);
    const stock = systems.inventory[ORDER.sku];
    const r = {
      order: systems.orders[ORDER.id].status,
      payment: systems.orders[ORDER.id].payment,
      available: stock.available,
      startAvailable: START().inventory[ORDER.sku].available,
      reserved: stock.reserved,
      charges: stripe.filter((x) => x.type === 'PaymentIntent' && x.status === 'succeeded').length,
      refunds: stripe.filter((x) => x.type === 'Refund' && x.status === 'succeeded').length,
      ...counts(events),
    };
    r.reconciled = r.order === 'cancelled' && r.payment === 'refunded' && r.available === r.startAvailable && r.reserved === 0 && r.charges === r.refunds;
    r.ok = r.reconciled;
    return r;
  }

  /** What the result panel says about the order scene. */
  function paymentOutcome(r, fault) {
    const restored = r.available === r.startAvailable && r.reserved === 0;
    return {
      done: 'Reconciled', doneSub: 'Order, payment and stock all agree',
      nums: [
        { label: 'Order', value: r.order === 'cancelled' ? 'Cancelled' : r.order === 'pending' ? 'Pending' : 'Paying', ok: r.order === 'cancelled' },
        { label: 'Inventory', value: restored ? 'Restored' : `${r.reserved} held`, sub: `${r.available} of ${r.startAvailable}`, ok: restored },
        { label: 'Successful charges', value: r.charges, ok: r.charges === 1 },
        { label: 'Refunds', value: r.refunds, ok: r.refunds === r.charges },
      ],
      extra: [
        ...(fault ? [{ label: 'Duplicate charges prevented', value: r.prevented, accent: true }] : []),
        { label: 'Decisions Lumni made', value: r.decisions },
        { label: 'Changes read back from the app', value: r.checks },
      ],
      without: fault
        ? 'the Payment AI retries and charges $2,400 a second time, the refund races a charge nobody has seen, and the stock stays reserved for a cancelled order.'
        : 'the refund can run before the charge lands, and the Order AI keeps packing an order the customer cancelled.',
    };
  }

  // ─── scene two: the supplier changes its price mid-quote ──────

  const QUOTE = { id: 'Q-2207', customer: 'Northwind Industrial', item: 'Hydraulic valve assembly', sku: 'SKU-7731', qty: 120, markup: 1.35, floor: 0.25 };
  const COST = { old: 80, now: 110 };
  const MSG = 'msg_7Hc2Qk91';
  const COST_ID = `supplier_cost:${QUOTE.sku}`;
  const RULE_ID = 'pricing_policy:margin-floor';
  const QWORK = { sales: 'wrk_quote_2207', procurement: 'wrk_supply_2207' };
  const SALES = [
    { id: 'sales', name: 'Sales AI', short: 'SA', actor: 'sales-agent', goal: 'Quote 120 units to Northwind' },
    { id: 'procurement', name: 'Procurement AI', short: 'PR', actor: 'procurement-agent', goal: 'Confirm supplier stock and cost' },
  ];
  const price = (cost) => Math.round(cost * QUOTE.markup * 100) / 100;
  const quoted = (cost) => Math.round(price(cost) * QUOTE.qty * 100) / 100;
  const SUPPLIER_SYSTEMS = [
    { id: 'supplier', name: 'Supplier price list', kind: 'Supplier API, test feed', logo: ['#0f766e', 'SP'],
      row: (id, r) => ({ title: id, sub: r.item, cells: [{ key: 'cost', label: 'cost', value: usd(r.cost) }, { key: 'version', label: 'version', value: `v${r.version}` }, { key: 'held', label: 'held', value: r.held }] }) },
    { id: 'quotes', name: 'Quotes', kind: 'CRM, test database', logo: ['#3f4352', 'CRM'], empty: 'No quote drafted yet',
      row: (id, r) => ({ title: id, sub: `${usd(r.unit)} a unit, on cost v${r.basis}`, cells: [{ key: 'total', label: 'total', value: usd(r.total) }, { key: 'status', pill: r.status, tone: r.status === 'sent' ? 'good' : 'mid' }] }) },
    { id: 'email', name: 'Customer email', kind: 'Outbox, test mode', logo: ['#0d0e14', '@'], empty: 'Nothing sent to the customer yet',
      row: (id, r) => ({ title: id, sub: `To ${r.to}`, cells: [{ key: 'total', label: 'quoted', value: usd(r.total) }, { key: 'to', pill: 'sent', tone: 'end' }] }) },
  ];
  const SUPPLIER_START = () => ({
    supplier: { [QUOTE.sku]: { item: QUOTE.item, cost: COST.old, version: 18, held: 0 } },
    quotes: {},
    email: {},
  });

  const asks = (agent, op, resources, extra = {}) => ({
    method: 'POST', path: '/v1/actions/check',
    body: { work_id: QWORK[agent], actor_id: SALES.find((a) => a.id === agent).actor, operation: op, resources, ...extra },
  });
  // The quote says what it was built on: the supplier's cost at a version, and
  // the margin rule at a version. That is the whole dependency primitive.
  const builtOn = (v) => [
    { type: 'quote', id: `quote:${QUOTE.id}`, mode: 'WRITE' },
    { type: 'supplier_cost', id: COST_ID, mode: 'READ', observed_version: String(v), version_source: 'supplier-feed' },
    { type: 'pricing_policy', id: RULE_ID, mode: 'READ', observed_version: '7', version_source: 'pricing-service' },
  ];
  const deps = (observed, current) => [
    { id: COST_ID, label: 'Supplier cost', observed: String(observed), current: String(current) },
    { id: RULE_ID, label: 'Margin rule', observed: '7', current: '7' },
  ];
  const observe = (agent, v) => ({
    method: 'POST', path: '/v1/observations',
    body: { work_id: QWORK[agent], resource: { type: 'supplier_cost', id: COST_ID }, version: String(v), version_source: 'supplier-feed' },
    response: { id: `obs_${agent}_${v}` },
  });
  const draft = (cost, v) => ({ unit: price(cost), total: quoted(cost), basis: v, status: 'draft' });

  function supplierOpening() {
    return [
      { t: 0, who: 'customer', kind: 'arrive', title: `${QUOTE.customer} asks for a quote: 120 valve assemblies`, note: 'Two AIs pick it up at once.' },
      { t: 1, who: 'sales', kind: 'run', title: 'Sales AI reads the supplier cost: $80 a unit, version 18', api: observe('sales', 18),
        card: { ...CARD.run, step: 'Pricing on an $80 cost' }, deps: deps(18, 18) },
      { t: 1.6, who: 'procurement', kind: 'run', title: 'Procurement AI asks the supplier to hold 120 units',
        api: { ...asks('procurement', 'supplier.hold', [{ type: 'supply', id: `supply:${QUOTE.sku}`, mode: 'WRITE' }]), response: decided('ALLOW', 'NO_CONFLICT', { lease_token: '7' }) },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, card: { ...CARD.run, step: 'Asking the supplier to hold 120 units' } },
      { t: 3, who: 'sales', kind: 'run', title: `Sales AI drafts the quote: ${usd(price(COST.old))} a unit, ${usd(quoted(COST.old))}`, note: 'A 26% margin, as the pricing rule asks.',
        card: { ...CARD.run, step: `Draft ready: ${usd(quoted(COST.old))}` },
        effects: [{ system: 'quotes', row: QUOTE.id, set: draft(COST.old, 18) }] },
      { t: 4.5, who: 'procurement', kind: 'done', title: 'The supplier holds 120 units for 9 days',
        api: { method: 'POST', path: '/v1/actions/{id}/result', body: { status: 'SUCCEEDED' }, response: { status: 'SUCCEEDED' } },
        effects: [{ system: 'supplier', row: QUOTE.sku, set: { held: 120 } }] },
      { t: 5.3, who: 'lumni', kind: 'verify', title: 'Lumni reads the hold back from the supplier', lane: 'Hold checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'HTTP', status: 'VERIFIED', observed: { sku: QUOTE.sku, held: 120 } } },
        card: { ...CARD.done, step: 'Holding 120 units' }, cardFor: 'procurement', decision: { type: 'VERIFIED' } },
    ];
  }

  /** The send that goes out, its read-back, and the end. */
  function supplierEnding(t0, cost, v) {
    let t = t0;
    const at = (d) => (t = Math.round((t + d) * 10) / 10);
    const q = draft(cost, v);
    return [
      { t: at(0), who: 'sales', kind: 'run', title: v === 18 ? 'Sales AI asks to send the quote. Lumni allows it.' : 'Sales AI asks to send again. Lumni allows it.',
        note: `Everything it was built on is current: cost v${v}, margin rule v7.`,
        api: { ...asks('sales', 'quote.send', builtOn(v)), response: decided('ALLOW', 'NO_CONFLICT') },
        decision: { type: 'ALLOW', reason_code: 'NO_CONFLICT' }, card: { ...CARD.run, step: `Sending ${usd(q.total)}` } },
      { t: at(1), who: 'email', kind: 'external', title: `The quote is emailed to ${QUOTE.customer}`,
        effects: [{ system: 'email', row: MSG, set: { to: 'buyer@northwind.test', total: q.total } }, { system: 'quotes', row: QUOTE.id, set: { status: 'sent' } }] },
      { t: at(1), who: 'lumni', kind: 'verify', title: 'Lumni reads the quote back from the CRM', lane: 'Quote checked',
        api: { method: 'GET', path: '/v1/verification-attempts/{id}', response: { verifier: 'POSTGRES', status: 'VERIFIED', observed: { quote: QUOTE.id, status: 'sent', unit_price: q.unit, total: q.total, cost_version: String(v) } } },
        card: { ...CARD.done, step: `Sent at ${usd(q.unit)} a unit` }, cardFor: 'sales', decision: { type: 'VERIFIED' } },
      { t: at(1), who: 'lumni', kind: 'resolved', title: `The customer has a quote that holds: ${usd(q.unit)} a unit, 26% margin`, note: `Built on the cost the supplier charges today, ${usd(cost)} a unit.` },
    ];
  }

  function priceChanges() {
    return [
      ...supplierOpening(),
      { t: 6.2, who: 'supplier', kind: 'change', mark: 'Price changed', title: 'The supplier raises its cost to $110 a unit', note: 'It comes with the hold confirmation.',
        unseen: 'The Sales AI’s draft still uses $80',
        effects: [{ system: 'supplier', row: QUOTE.sku, set: { cost: COST.now, version: 19 } }] },
      { t: 7, who: 'procurement', kind: 'run', title: 'Procurement AI records the new cost with Lumni: version 19',
        note: 'Nothing is broadcast. Lumni checks it against the next action built on it.',
        api: { method: 'POST', path: `/v1/resources/${COST_ID}/version`, body: { version: '19', version_source: 'supplier-feed', summary: 'cost per unit 80 to 110' }, response: { updated: true } },
        card: { ...CARD.done, step: 'Reported the new cost' }, deps: deps(18, 19) },
      { t: 8.5, who: 'sales', kind: 'replan', title: 'Sales AI asks to send the quote. Lumni sends it back.', prevented: true,
        note: 'The quote was built on cost v18. The cost is now v19. A quote below cost was never sent.',
        api: { ...asks('sales', 'quote.send', builtOn(18)), response: decided('REPLAN', 'STALE_OBSERVATION', {
          evidence: [{ type: 'RESOURCE_VERSION', data: { resource_id: COST_ID, observed_version: '18', current_version: '19', observed_source: 'supplier-feed', current_source: 'supplier-feed' } }] }) },
        decision: { type: 'REPLAN', reason_code: 'STALE_OBSERVATION' }, card: { ...CARD.replan, step: 'Repricing on a $110 cost' }, clears: [`supplier/${QUOTE.sku}`] },
      { t: 9.7, who: 'sales', kind: 'run', title: 'Sales AI reads the cost again: $110 a unit, version 19', api: observe('sales', 19),
        card: { ...CARD.run, step: 'Repricing on a $110 cost' }, deps: deps(19, 19) },
      { t: 10.7, who: 'sales', kind: 'run', title: `Sales AI reprices under the margin rule: ${usd(price(COST.now))} a unit, ${usd(quoted(COST.now))}`, note: 'Still a 26% margin.',
        card: { ...CARD.run, step: `New draft: ${usd(quoted(COST.now))}` },
        effects: [{ system: 'quotes', row: QUOTE.id, set: draft(COST.now, 19) }] },
      ...supplierEnding(11.7, COST.now, 19),
    ];
  }

  function priceHolds() {
    return [...supplierOpening(), ...supplierEnding(6.5, COST.old, 18)];
  }

  function supplierTally(systems, events) {
    const quote = systems.quotes[QUOTE.id];
    const cost = systems.supplier[QUOTE.sku];
    const sent = Object.values(systems.email);
    const r = {
      quote: quote?.status ?? 'none',
      unit: quote?.unit ?? 0,
      total: quote?.total ?? 0,
      basis: quote?.basis ?? null,
      cost: cost.cost,
      version: cost.version,
      sent: sent.length,
      belowCost: sent.filter((m) => m.total < cost.cost * QUOTE.qty).length,
      margin: quote ? (quote.unit - cost.cost) / quote.unit : 0,
      ...counts(events),
    };
    r.ok = r.sent === 1 && r.belowCost === 0 && r.basis === r.version && r.margin >= QUOTE.floor;
    return r;
  }

  function supplierOutcome(r, fault) {
    return {
      done: fault ? 'Corrected quote sent' : 'Quote sent', doneSub: 'Priced on today’s cost, inside the margin rule',
      nums: [
        { label: 'Quote sent', value: r.sent ? usd(r.total) : 'Not yet', sub: r.unit ? `${r.sent ? '' : 'draft at '}${usd(r.unit)} a unit` : '', ok: r.sent === 1 },
        { label: 'Margin', value: r.unit ? `${Math.round(r.margin * 100)}%` : 'None yet', sub: 'rule asks 25%', ok: r.margin >= QUOTE.floor },
        { label: 'Cost it was built on', value: r.basis ? `v${r.basis}` : 'None yet', sub: `now v${r.version}`, ok: r.basis === r.version },
        { label: 'Quotes sent below cost', value: r.belowCost, ok: r.belowCost === 0 },
      ],
      extra: [
        ...(fault ? [{ label: 'Out of date sends stopped', value: r.prevented, accent: true }] : []),
        { label: 'Decisions Lumni made', value: r.decisions },
        { label: 'Changes read back from the app', value: r.checks },
      ],
      without: fault
        ? `the Sales AI sends ${usd(price(COST.old))} a unit on a ${usd(COST.now)} cost: ${usd(quoted(COST.old))} for goods that now cost ${usd(COST.now * QUOTE.qty)} to buy, a ${usd(COST.now * QUOTE.qty - quoted(COST.old))} loss before shipping.`
        : 'nothing changes here: the cost held, so the first quote was right. Turn the price change on to see Lumni step in.',
    };
  }

  // ─── the scenes ─────────────────────────────────────

  const SCENES = {
    payment: {
      label: 'Payment fails halfway', title: `Order #${ORDER.id}`, customer: ORDER.customer, subject: `order:${ORDER.id}`, strip: 'lease',
      meta: [['3 AIs', 'on one order'], ['3 business systems', ''], [usd(ORDER.total), `${ORDER.qty} x ${ORDER.item}`]],
      toggle: { on: 'Fault: Stripe answer dropped', off: 'No fault', title: 'Drop Stripe’s answer after it charges the card', kind: 'fault' },
      agents: AGENTS, systems: SYSTEMS, start: START, events: (f) => (f ? withFault() : withoutFault()),
      others: { customer: ['NI', 'person'], stripe: ['S', '#635bff'] },
      tally: paymentTally, outcome: paymentOutcome,
      // Where this order sits in the story: the contract it kept, and the
      // check the Cancellation AI repeats on every order, now compiled.
      links: [
        { label: 'Kept by contract', text: 'One charge and one refund per order', href: '/contracts?data=mock&story=contract' },
        { label: 'Next time', text: 'The refund check runs as code', href: '/compile?data=mock&path=refund-eligibility&story=optimize' },
      ],
    },
    supplier: {
      label: 'Price changes mid-quote', title: `Quote #${QUOTE.id}`, customer: QUOTE.customer, subject: `quote:${QUOTE.id}`, strip: 'deps',
      meta: [['2 AIs', 'on one quote'], ['3 business systems', ''], [`${QUOTE.qty} units`, QUOTE.item]],
      toggle: { on: 'Supplier raises its price', off: 'Price holds', title: 'The supplier raises its cost before the quote goes out', kind: 'change' },
      agents: SALES, systems: SUPPLIER_SYSTEMS, start: SUPPLIER_START, events: (f) => (f ? priceChanges() : priceHolds()),
      others: { customer: ['NI', 'person'], supplier: ['SP', '#0f766e'], email: ['@', '#0d0e14'] },
      tally: supplierTally, outcome: supplierOutcome,
    },
  };

  function scenario({ scene = 'payment', fault = true } = {}) {
    const id = SCENES[scene] ? scene : 'payment';
    const d = SCENES[id];
    const events = d.events(fault).map((e, i) => ({ id: `e${i + 1}`, ...e }));
    const end = events[events.length - 1].t + 1;
    return {
      scene: id, fault, label: d.label, title: d.title, customer: d.customer, meta: d.meta, toggle: d.toggle,
      strip: d.strip, resource: d.subject, agents: d.agents, systems: d.systems, others: d.others, start: d.start(), events, end,
      tally: d.tally, outcome: d.outcome, links: d.links ?? [],
    };
  }

  const scenes = () => Object.entries(SCENES).map(([id, d]) => ({ id, label: d.label }));

  // ─── reading it at a moment ─────────────────────────

  /** Agent lanes: from each card change to the next, coloured by its tone. */
  function lanes(s) {
    const out = {};
    for (const a of s.agents) {
      const marks = s.events.filter((e) => e.card && (e.cardFor ?? e.who) === a.id);
      const bars = marks.map((e, i) => ({ from: e.t, to: marks[i + 1]?.t ?? s.end, tone: e.card.tone, words: e.card.words, step: e.card.step }));
      // One bar per stretch of the same decision, however many steps it took.
      out[a.id] = bars.reduce((acc, b) => {
        const prev = acc[acc.length - 1];
        if (prev && prev.tone === b.tone) prev.to = b.to;
        else acc.push(b);
        return acc;
      }, []);
    }
    out.lumni = s.events.filter((e) => e.who === 'lumni' && e.kind === 'verify').map((e) => ({ at: e.t, words: e.lane }));
    out.marks = s.events.filter((e) => e.mark).map((e) => ({ at: e.t, kind: e.kind, words: e.mark }));
    return out;
  }

  function at(s, t) {
    const seen = s.events.filter((e) => e.t <= t);
    const systems = JSON.parse(JSON.stringify(s.start));
    const changed = {};
    const unseen = {};
    const cards = Object.fromEntries(s.agents.map((a) => [a.id, { ...CARD.idle, step: a.goal, decision: null }]));
    let lease = { holder: null, token: null, queue: [] };
    let hold = false;
    let dep = [];
    for (const e of seen) {
      for (const f of e.effects ?? []) {
        const key = `${f.system}/${f.row}`;
        const row = (systems[f.system][f.row] ??= {});
        Object.assign(row, f.set);
        changed[key] = { at: e.t, keys: Object.keys(f.set) };
        if (e.unseen) unseen[key] = e.unseen;
      }
      for (const key of e.clears ?? []) delete unseen[key];
      const who = e.cardFor ?? e.who;
      // A new step with the same tone keeps the last decision; a new tone starts clean.
      if (e.card && cards[who]) cards[who] = { ...e.card, decision: e.decision ?? (e.card.tone === cards[who].tone ? cards[who].decision : null), at: e.t };
      else if (e.decision && cards[who]) cards[who] = { ...cards[who], decision: e.decision, at: e.t };
      if (e.lease) lease = e.lease;
      if (e.hold !== undefined) hold = e.hold;
      if (e.deps) dep = e.deps;
    }
    const last = seen[seen.length - 1];
    return { t, seen, cards, lease, hold, deps: dep, systems, changed, unseen, resolved: last?.kind === 'resolved', step: seen.length };
  }

  function counts(events) {
    return {
      prevented: events.filter((e) => e.prevented).length,
      decisions: events.filter((e) => e.decision && e.decision.type !== 'VERIFIED').length,
      checks: events.filter((e) => e.kind === 'verify').length,
    };
  }

  /** How it ended up, read from the systems, not from the story. */
  function result(s, t = s.end) {
    const v = at(s, t);
    return s.tally(v.systems, v.seen);
  }

  /** What the result panel shows: words and numbers, from `result`. */
  const outcome = (s, t = s.end) => s.outcome(result(s, t), s.fault);

  globalThis.Live = { scenario, scenes, at, lanes, result, outcome, CARD };
})();
