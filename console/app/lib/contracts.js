// Outcome contracts: a rule in plain words, read into checks before the action,
// proof after it, the evidence to keep, and a test for every miss.
//
//   const read = Contracts.classify('No more than one refund per order.');
//   read[0].ranked  // [{ type: 'once-per-key', p: 0.97 }, ...]
//   read[0].state   // 'sure' | 'ask' | 'pick'
//
// The reading here is on-device and keyword-weighted, for the demo. Its answer
// has the shape of a JEV `choice` question (internal/semantic/types.go: one
// choice, a probability per option, a confidence), so JEV can take its place
// once contracts reach the API. Nothing here touches the DOM.

(() => {
  // ─── words Lumni knows ──────────────────────────────

  const APPS = [
    { id: 'stripe', name: 'Stripe', re: /\bstripe\b/i },
    { id: 'shopify', name: 'Shopify', re: /\bshopify\b/i },
    { id: 'salesforce', name: 'Salesforce', re: /\bsalesforce\b|\bcrm\b/i },
    { id: 'zendesk', name: 'Zendesk', re: /\bzendesk\b/i },
    { id: 'gmail', name: 'Gmail', re: /\bgmail\b/i },
    { id: 'quickbooks', name: 'QuickBooks', re: /\bquickbooks\b|\bledger\b/i },
    { id: 'vercel', name: 'Vercel', re: /\bvercel\b/i },
    { id: 'hubspot', name: 'HubSpot', re: /\bhubspot\b/i },
  ];
  const appName = (id) => APPS.find((a) => a.id === id)?.name ?? id;

  // An action: how it's written, the operation it runs as, where it usually
  // lives, what it's usually counted by, and the state that proves it.
  const ACTIONS = [
    { id: 'refund', re: /\brefund(s|ed|ing)?\b/i, op: 'refund', app: 'stripe', key: 'order', object: 'refund', state: 'succeeded', plural: 'refunds' },
    { id: 'cancellation', re: /\bcancel(s|led|led|ling|lation|lations)?\b/i, op: 'cancel_subscription', app: 'stripe', key: 'subscription', object: 'subscription', state: 'canceled', plural: 'cancellations' },
    { id: 'charge', re: /\bcharg(e|es|ed|ing)\b/i, op: 'charge', app: 'stripe', key: 'customer', object: 'charge', state: 'succeeded', plural: 'charges' },
    { id: 'invoice', re: /\binvoic(e|es|ed|ing)\b/i, op: 'send_invoice', app: 'stripe', key: 'invoice', object: 'invoice', state: 'paid', plural: 'invoices' },
    { id: 'renewal', re: /\brenew(s|al|als|ed)?\b/i, op: 'renew', app: 'stripe', key: 'subscription', object: 'subscription', state: 'active', plural: 'renewals' },
    { id: 'email', re: /\b(e-?mail(s|ed)?|send(s|ing)?|message(s)?)\b/i, op: 'send', app: 'gmail', key: 'recipient', object: 'message', state: 'sent', plural: 'emails' },
    { id: 'ticket close', re: /\bclos(e|es|ed|ing)\b.*\bticket|\bticket(s)?\b/i, op: 'close_ticket', app: 'zendesk', key: 'ticket', object: 'ticket', state: 'closed', plural: 'ticket closes' },
    { id: 'deploy', re: /\bdeploy(s|ed|ing|ment|ments)?\b/i, op: 'deploy', app: 'vercel', key: 'project', object: 'deployment', state: 'ready', plural: 'deploys' },
    { id: 'delete', re: /\bdelet(e|es|ed|ing)\b/i, op: 'delete', app: 'salesforce', key: 'record', object: 'record', state: 'deleted', plural: 'deletes' },
    { id: 'record update', re: /\b(record|account|lead|contact)s?\b|\boverwrit/i, op: 'update_account', app: 'salesforce', key: 'record', object: 'record', state: 'updated', plural: 'record updates' },
  ];
  const actionOf = (id) => ACTIONS.find((a) => a.id === id) ?? ACTIONS[0];

  const KEYS = ['order', 'customer', 'subscription', 'invoice', 'ticket', 'recipient', 'record', 'project'];
  const STATES = ['canceled', 'inactive', 'paid', 'closed', 'succeeded', 'active', 'archived', 'deleted'];

  const money = (n) => `$${Number(n).toLocaleString('en-US', { maximumFractionDigits: 2 })}`;
  const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);

  // ─── the kinds of contract ──────────────────────────
  //
  // Each kind: the words that suggest it (weighted), the parts it needs, its
  // clauses as templates over those parts, and whether Lumni can run it today.
  // Lanes: before = checked when the AI asks, after = read back from the app,
  // evidence = what the receipt keeps.

  const TYPES = [
    {
      id: 'once-per-key', label: 'Once per thing', icon: '1', live: true,
      blurb: 'Never do the same action twice for one order, ticket or customer.',
      example: 'No more than one refund is permitted per order.',
      ask: 'Stop the second one before it happens',
      cues: [
        [/\b(no more than|only|at most|never more than) (one|1|a single)\b/i, 3],
        [/\b(only once|at most once|more than once|never twice|twice|a second|second time|again|duplicate[sd]?|double)\b/i, 3],
        [/\b(one|a single) \w+ (per|for each|for every)\b/i, 2],
        [/\bper (order|customer|ticket|invoice|subscription|account|recipient)\b|\bsame (order|customer|ticket|invoice|subscription)\b/i, 1.5],
      ],
      parts: ['action', 'key', 'app'],
      short: (s) => `One ${s.action} per ${s.key}`,
      clauses: [
        { lane: 'before', t: 'Stop a second {action} for the same {key}', where: 'admission', live: true },
        { lane: 'after', t: 'Check the first {action} really landed in {app}', where: 'verify', live: true },
        { lane: 'evidence', t: 'Keep the {app} record as the receipt', where: 'receipt', live: true },
      ],
    },
    {
      id: 'end-state', label: 'Leaves the right state', icon: '✓', live: true,
      blurb: 'After the action, read the app back and check the state it left.',
      example: 'Every successful cancellation must leave the subscription inactive in Stripe.',
      ask: 'Let it through, then check the result in the app',
      cues: [
        [/\bmust (leave|end|be|stay|remain|show|result)\b/i, 2],
        [/\b(inactive|canceled|cancelled|closed|paid|archived|deactivated)\b/i, 2.5],
        [/\b(every|each) successful\b|\bafter (each|every|a|the)\b|\bactually\b|\breally\b/i, 1],
        [/\b(check|verify|confirm|prove|make sure)\b/i, 2],
        [/\bin (stripe|shopify|salesforce|zendesk|quickbooks|hubspot)\b/i, 0.8],
      ],
      parts: ['action', 'object', 'state', 'app'],
      short: (s) => `${cap(s.object)} ${s.state} after ${s.action}`,
      clauses: [
        { lane: 'before', t: 'Hold a {action} when the {object} is already {state}', where: 'admission', live: true },
        { lane: 'after', t: 'After each {action}, the {object} must be {state} in {app}', where: 'verify', live: true },
        { lane: 'evidence', t: 'Record the {object} state Lumni read back', where: 'receipt', live: true },
      ],
    },
    {
      id: 'approval-over', label: 'A person approves', icon: '?', live: true,
      blurb: 'Ask a person first when the amount is big.',
      example: 'Ask a person before any refund over $500.',
      ask: 'Ask a person first',
      cues: [
        [/\b(ask|approv\w*|sign[- ]?off|a person|a human|manager|review(ed)?)\b/i, 3],
        [/\b(over|above|more than|greater than|exceeds?|bigger than)\s*\$?\d/i, 1.5],
      ],
      parts: ['action', 'amount', 'app'],
      short: (s) => `A person approves ${s.action}s over ${money(s.amount)}`,
      clauses: [
        { lane: 'before', t: 'Ask a person before any {action} over {amount}', where: 'admission', live: true },
        { lane: 'evidence', t: 'Record who approved it, and when', where: 'receipt', live: true },
      ],
    },
    {
      id: 'amount-cap', label: 'Stays within a limit', icon: '≤', live: false,
      blurb: 'Never more than what is owed, eligible or allowed.',
      example: 'Refund amount must never exceed the eligible balance.',
      ask: 'Cap the amount',
      cues: [
        [/(≤|<=|=<|\bnot exceed|\bnever exceed|\bexceeds?\b|\bwithin\b|\bcap(ped)?\b|\blimit(ed)?\b)/i, 2.5],
        [/\b(eligible|remaining|available|original)\b|\bbalance\b|\bowed\b|\bpaid amount\b/i, 3],
        [/\bamount\b|\btotal\b/i, 1.2],
      ],
      parts: ['action', 'limit', 'key', 'app'],
      short: (s) => `${cap(s.action)} within the ${s.limit}`,
      clauses: [
        { lane: 'before', t: 'A {action} is never more than the {limit}', where: 'admission', live: false },
        { lane: 'after', t: 'The {app} total for the {key} stays within it', where: 'verify', live: true },
        { lane: 'evidence', t: 'Record the {limit} Lumni compared against', where: 'receipt', live: true },
      ],
    },
    {
      id: 'order-before', label: 'One thing first', icon: '↧', live: false,
      blurb: 'Hold one action until another has landed.',
      example: 'Don’t close a ticket before its refund lands.',
      ask: 'Wait for the other action first',
      cues: [
        [/\b(before|only after|until|unless)\b/i, 2.5],
        [/\b(lands?|landed|settles?|settled|clears?|cleared|finishes|finished|done|completes?)\b/i, 1.5],
      ],
      parts: ['action', 'first', 'key', 'app'],
      short: (s) => `${cap(s.action)} only after the ${s.first}`,
      clauses: [
        { lane: 'before', t: 'Hold a {action} until the {first} for the same {key} has landed', where: 'admission', live: false },
        { lane: 'after', t: 'Check the {first} in {app} before letting it go', where: 'verify', live: true },
      ],
    },
    {
      id: 'rate-limit', label: 'Not too often', icon: '≈', live: false,
      blurb: 'At most so many in a window.',
      example: 'No more than 3 emails per customer a day.',
      ask: 'Limit how often',
      cues: [
        [/\b(per|an|a|every|each) (minute|hour|day|week)\b|\bdaily\b|\bhourly\b/i, 3],
        [/\b(more than|at most|no more than|max(imum)?) \d+\b/i, 1.5],
      ],
      parts: ['action', 'n', 'key', 'window'],
      short: (s) => `At most ${s.n} ${s.action}s a ${s.window}`,
      clauses: [
        { lane: 'before', t: 'At most {n} {action}s per {key} each {window}', where: 'admission', live: false },
        { lane: 'evidence', t: 'Count each one on the receipt', where: 'receipt', live: true },
      ],
    },
    {
      id: 'human-wins', label: 'People win', icon: '◐', live: false,
      blurb: 'An AI never overwrites what a person just changed.',
      example: 'Never overwrite a record a person edited in the last 10 minutes.',
      ask: 'Leave it to the person',
      cues: [
        [/\b(overwrit\w*|clobber\w*|undo\w*)\b/i, 3],
        [/\b(a person|a human|someone|manual(ly)?|by hand)\b.*\b(edit\w*|chang\w*|touch\w*|updat\w*)\b/i, 2],
      ],
      parts: ['key', 'minutes', 'app'],
      short: (s) => `People win on a ${s.key} for ${s.minutes} min`,
      clauses: [
        { lane: 'before', t: 'No AI changes a {key} a person edited in the last {minutes} minutes', where: 'admission', live: false },
        { lane: 'evidence', t: 'Record who edited it last', where: 'receipt', live: true },
      ],
    },
  ];
  const typeOf = (id) => TYPES.find((t) => t.id === id);

  // What each part can be, for the little menus.
  const OPTIONS = {
    action: ACTIONS.map((a) => a.id),
    app: APPS.map((a) => a.id),
    key: KEYS,
    state: STATES,
    object: ['subscription', 'refund', 'invoice', 'order', 'ticket', 'charge', 'record'],
    amount: [50, 100, 250, 500, 1000, 5000],
    limit: ['eligible balance', 'amount paid', 'order total', 'daily budget'],
    first: ['refund', 'charge', 'invoice', 'cancellation', 'deploy'],
    n: [1, 2, 3, 5, 10, 25],
    window: ['minute', 'hour', 'day', 'week'],
    minutes: [5, 10, 30, 60],
    within: [1, 5, 15, 60],
  };

  /** A part, as words. */
  function say(slot, v) {
    if (slot === 'app') return appName(v);
    if (slot === 'amount') return money(v);
    if (slot === 'within') return v >= 60 ? `${v / 60} ${v === 60 ? 'hour' : 'hours'}` : `${v} ${v === 1 ? 'minute' : 'minutes'}`;
    return String(v);
  }

  // ─── reading ────────────────────────────────────────

  /** Split a message into rules: sentences, or clauses joined by ";". */
  function split(text) {
    return String(text ?? '')
      .split(/(?<=[.!?])\s+|;\s*|\n+/)
      .map((s) => s.trim().replace(/[.!?]+$/, ''))
      .filter((s) => /[a-z]/i.test(s));
  }

  /** The parts of a rule, from its words, with the guessed ones marked. */
  function slotsOf(text, typeId) {
    const s = {};
    const guessed = new Set();
    // The action: the first one named, or for "before", the one it holds.
    const found = ACTIONS.map((a) => ({ a, i: text.search(a.re) })).filter((x) => x.i >= 0).sort((x, y) => x.i - y.i);
    // "Send" alone is weak: "send an invoice" is about the invoice.
    const weakSend = found.length > 1 && !/\be-?mails?\b/i.test(text);
    if (weakSend) found.splice(0, found.length, ...found.filter((x) => x.a.id !== 'email'));
    let act = found[0]?.a;
    if (typeId === 'order-before') {
      const cut = text.search(/\b(before|only after|until|unless)\b/i);
      const head = found.filter((x) => x.i < cut);
      const tail = found.filter((x) => x.i > cut);
      // "Don't close a ticket before its refund lands": hold the close, wait for the refund.
      act = (head.find((x) => x.a.id === 'ticket close') ?? head[0])?.a ?? act;
      const first = tail.find((x) => x.a !== act)?.a;
      s.first = first?.id ?? 'refund';
      if (!first) guessed.add('first');
    }
    if (typeId === 'end-state' && found.some((x) => x.a.id === 'cancellation')) act = actionOf('cancellation');
    if (!act) {
      act = ACTIONS[0];
      guessed.add('action');
    }
    s.action = act.id;

    const app = APPS.find((a) => a.re.test(text));
    s.app = app?.id ?? act.app;
    if (!app) guessed.add('app');

    const key = text.match(/\b(?:per|for each|for every|same|for the same|each|for (?:an?|the)|its)\s+(order|customer|ticket|invoice|subscription|account|recipient|record|project)s?\b/i);
    s.key = key ? key[1].toLowerCase().replace('account', 'customer') : act.key;
    if (!key) guessed.add('key');

    const st = text.match(/\b(inactive|canceled|cancelled|closed|paid|archived|deleted|active|succeeded|deactivated)\b/i);
    s.state = st ? st[1].toLowerCase().replace('cancelled', 'canceled').replace('deactivated', 'inactive') : act.state;
    if (!st) guessed.add('state');
    const obj = text.match(/\b(subscription|refund|invoice|order|ticket|charge|record)\b(?=[^.]*\b(must|should|is|stays?|remains?|be)\b)/i);
    s.object = obj && obj[1].toLowerCase() !== act.id ? obj[1].toLowerCase() : act.object;

    const amt = text.match(/\$\s?([\d,]+(?:\.\d+)?)\s*(k)?/i) ?? text.match(/\b([\d,]{2,})\s*(?:dollars|usd)\b/i);
    if (amt) s.amount = Number(amt[1].replace(/,/g, '')) * (amt[2] ? 1000 : 1);
    else {
      s.amount = 100;
      guessed.add('amount');
    }

    const lim = text.match(/\b(eligible balance|remaining balance|amount paid|order total|original (?:charge|amount)|daily budget|balance)\b/i);
    s.limit = lim ? lim[1].toLowerCase().replace(/^balance$/, 'eligible balance').replace('remaining balance', 'eligible balance').replace(/original (charge|amount)/, 'amount paid') : 'eligible balance';
    if (!lim) guessed.add('limit');

    const words = { one: 1, two: 2, three: 3, four: 4, five: 5, ten: 10 };
    const n = text.match(/\b(\d+|one|two|three|four|five|ten)\s+(?:\w+\s+)?(?:times|per|a|an|each|every)\b/i) ?? text.match(/\b(?:more than|at most|max(?:imum)?|no more than)\s+(\d+)/i);
    s.n = n ? (words[n[1].toLowerCase()] ?? Number(n[1])) : 3;
    if (!n) guessed.add('n');

    const win = text.match(/\b(minute|hour|day|week)\b|\b(daily|hourly|weekly)\b/i);
    s.window = win ? (win[1] ?? { daily: 'day', hourly: 'hour', weekly: 'week' }[win[2].toLowerCase()]).toLowerCase() : 'day';
    if (!win) guessed.add('window');

    const within = text.match(/\bwithin\s+(\d+)\s*(seconds?|secs?|minutes?|mins?|hours?|hrs?)\b/i);
    s.within = within ? Number(within[1]) * (/^h/i.test(within[2]) ? 60 : /^s/i.test(within[2]) ? 1 / 60 : 1) : 5;
    if (!within) guessed.add('within');

    const mins = text.match(/\b(\d+)\s*(minutes?|mins?|hours?|hrs?)\b/i);
    s.minutes = mins ? Number(mins[1]) * (/^h/i.test(mins[2]) ? 60 : 1) : 10;
    if (!mins) guessed.add('minutes');

    return { slots: s, guessed: [...guessed] };
  }

  /** Score one rule against every kind; the words that decided are kept. */
  function score(text) {
    return TYPES.map((t) => {
      let sc = 0;
      const cues = [];
      for (const [re, wt] of t.cues) {
        const m = text.match(re);
        if (!m) continue;
        sc += wt;
        cues.push({ start: m.index, end: m.index + m[0].length, type: t.id });
      }
      return { type: t.id, score: sc, cues };
    });
  }

  /**
   * Read a message into rules. Each rule: its words, every kind ranked by
   * probability, how sure the reading is, its parts and the deciding words.
   *   state 'sure' — one kind clearly; 'ask' — two kinds close; 'pick' — none.
   */
  function classify(text) {
    return split(text).map((clause, i) => {
      const scored = score(clause);
      const top = Math.max(...scored.map((x) => x.score));
      // A softmax over the scores, with a "none of these" option at 1.6, so
      // weak readings stay unsure.
      const NONE = 1.6;
      const ex = scored.map((x) => Math.exp(x.score));
      const sum = ex.reduce((a, b) => a + b, 0) + Math.exp(NONE) * TYPES.length * 0.15;
      const ranked = scored.map((x, j) => ({ type: x.type, p: ex[j] / sum })).sort((a, b) => b.p - a.p);
      const [a, b] = ranked;
      const state = top < 2 ? 'pick' : a.p >= 0.6 && a.p - b.p >= 0.3 ? 'sure' : 'ask';
      const typeId = state === 'pick' ? null : a.type;
      const { slots, guessed } = slotsOf(clause, typeId ?? 'once-per-key');
      return {
        id: `r${i + 1}`,
        text: clause,
        ranked,
        state,
        confidence: a.p,
        type: state === 'sure' ? a.type : null,
        slots,
        guessed,
        cues: scored.flatMap((x) => x.cues).sort((x, y) => x.start - y.start),
      };
    });
  }

  /** Fill the parts a kind needs when someone picks it by hand. */
  function choose(rule, typeId) {
    const { slots, guessed } = slotsOf(rule.text, typeId);
    return { ...rule, type: typeId, state: 'sure', slots: { ...slots, ...pick(rule.slots, guessed) }, guessed };
  }
  const pick = (slots, guessed) => Object.fromEntries(Object.entries(slots ?? {}).filter(([k]) => !guessed.includes(k)));

  /** A rule's clauses, as text runs and parts: [{lane, where, live, runs:[string | {slot, value}]}]. */
  function clauses(rule) {
    const t = typeOf(rule.type);
    if (!t) return [];
    return t.clauses.map((c) => ({
      lane: c.lane, where: c.where, live: c.live,
      runs: c.t.split(/(\{\w+\})/).filter(Boolean).map((r) => {
        const m = r.match(/^\{(\w+)\}$/);
        return m ? { slot: m[1], value: rule.slots[m[1]] } : r;
      }),
    }));
  }

  /** One line for a rule: "One refund per order". */
  const short = (rule) => (typeOf(rule.type)?.short(rule.slots) ?? rule.text);

  /** The operation a rule's action runs as, e.g. stripe.refund. */
  const operation = (rule) => `${rule.slots.app}.${actionOf(rule.slots.action).op}`;

  /** A title for a set of rules: "Refund outcome contract". */
  function title(rules) {
    const acts = [...new Set(rules.filter((r) => r.type).map((r) => r.slots.action))];
    if (!acts.length) return 'New outcome contract';
    if (acts.length === 1) return `${cap(acts[0])} outcome contract`;
    return `${cap(acts[0])} and ${acts[1]} contract`;
  }

  // ─── what a contract needs ──────────────────────────

  /**
   * The apps a contract reads, and the field agents must send. `connected` is
   * the set of app ids the workspace has a checker for.
   */
  function needs(rules, connected) {
    const out = [];
    const seen = new Set();
    for (const r of rules) {
      if (!r.type) continue;
      const apps = [r.slots.app];
      for (const id of apps) {
        if (seen.has(id)) continue;
        seen.add(id);
        const act = actionOf(r.slots.action);
        out.push({ kind: 'app', id, name: appName(id), why: `Reads each ${act.object} back`, ok: connected.has(id) });
      }
    }
    const keyed = rules.find((r) => r.type === 'once-per-key' || r.type === 'rate-limit' || r.type === 'order-before');
    if (keyed) {
      const act = actionOf(keyed.slots.action);
      out.push({ kind: 'field', id: `field:${keyed.slots.key}`, name: `${keyed.slots.key}_id`, why: `Your agents send it with each ${act.id}`, ok: true });
    }
    return out;
  }

  // ─── what it would have done last week (demo) ───────

  function hash(s) {
    let h = 2166136261;
    for (const c of String(s)) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
    return h >>> 0;
  }
  function rng(seed) {
    let s = seed >>> 0;
    return () => {
      s = (s + 0x6d2b79f5) >>> 0;
      let t = s;
      t = Math.imul(t ^ (t >>> 15), t | 1);
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  // The demo world already says why Lumni stepped in today. A contract about
  // the same thing reuses that count, so it agrees with Fixes and Today.
  const ANCHOR = { refund: 'p-refund2', invoice: 'p-invoice2', renewal: 'p-cancelled', cancellation: 'p-cancelled', 'ticket close': 'p-ticket2', email: 'p-quote2' };
  const RATE = { 'once-per-key': 0.028, 'end-state': 0.006, 'approval-over': 0.04, 'amount-cap': 0.011, 'order-before': 0.018, 'rate-limit': 0.009, 'human-wins': 0.013 };

  /**
   * Replay a contract on the demo's last 7 days. Deterministic per contract
   * and volume. Never call this for a real workspace.
   */
  function replay(rules, w) {
    const live = rules.filter((r) => r.type);
    const r = rng(hash(live.map((x) => `${x.type}:${JSON.stringify(x.slots)}`).join('|') + w.key));
    const main = live[0];
    if (!main) return null;
    const act = actionOf(main.slots.action);
    const op = `${main.slots.app}.${act.op}`;
    const agent = w.fleet?.find((f) => f.op === op) ?? w.fleet?.find((f) => f.app === main.slots.app) ?? w.fleet?.[0];
    const appW = (w.apps?.find((a) => a.id === main.slots.app)?.n ?? w.total * 0.1) / Math.max(1, w.total);
    const daily = Math.max(24, Math.round((agent?.n ?? w.total * appW * 0.3) * (0.85 + r() * 0.3)));
    const seen = daily * 7;
    let stop = 0;
    let perUsd = main.slots.amount && main.type !== 'once-per-key' ? main.slots.amount * 0.6 : 0;
    for (const x of live) {
      const anchor = w.patterns?.stop?.find((p) => p.id === ANCHOR[x.slots.action]);
      const today = anchor && (x.type === 'once-per-key' || x.type === 'end-state') ? anchor.n : Math.round(daily * RATE[x.type] * (0.6 + r() * 0.8));
      stop += Math.max(1, Math.round(today * (6.2 + r() * 1.4)));
      if (anchor?.money && !perUsd) perUsd = anchor.money;
    }
    if (!perUsd) perUsd = 40 + Math.round(r() * 80);
    const gaps = Math.round(seen * (0.002 + r() * 0.004));
    const days = Array.from({ length: 7 }, () => r() * 0.8 + 0.6);
    const tot = days.reduce((a, b) => a + b, 0);
    const perDay = days.map((d) => Math.round((d / tot) * stop));
    const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
    const examples = Array.from({ length: 3 }, (_, i) => {
      const id = Math.floor(1000 + r() * 8999);
      const h = 8 + Math.floor(r() * 10);
      const m = Math.floor(r() * 60);
      const amount = Math.round((perUsd * (0.5 + r())) * 100) / 100;
      return {
        action: `act_${(hash(`${id}${i}`) % 0xfffff).toString(36)}${(hash(`${i}${id}`) % 0xfff).toString(36)}`,
        agent: agent?.name ?? 'Your agent',
        key: `${main.slots.key.slice(0, 3)}_${id}`,
        amount,
        day: DAYS[Math.floor(r() * 7)],
        at: `${((h + 11) % 12) + 1}:${String(m).padStart(2, '0')} ${h < 12 ? 'AM' : 'PM'}`,
        first: `${act.object.slice(0, 2)}_${(hash(`${id}a`) % 0xffffff).toString(36)}`,
        second: `${act.object.slice(0, 2)}_${(hash(`${id}b`) % 0xffffff).toString(36)}`,
        evidence: `sha256:${(hash(`${id}e`)).toString(16).padStart(8, '0')}${(hash(`${id}f`)).toString(16).padStart(8, '0')}`,
        story: 'omar',
      };
    });
    return {
      op, agent: agent?.name ?? 'Your agent', seen, wouldStop: stop, verified: seen - gaps, gaps,
      saved: Math.round(stop * perUsd), days: perDay, examples, plural: act.plural, ms: 300 + Math.round(r() * 500),
    };
  }

  // ─── what you type after ────────────────────────────

  /**
   * A message typed while a contract is open: a change to it, a request, or a
   * new rule. Returns { kind, value }.
   */
  function followUp(text, rules) {
    const t = String(text ?? '').trim();
    const l = t.toLowerCase();
    if (/^(rename|call it|name it)\b/.test(l)) return { kind: 'rename', value: t.replace(/^(rename( it)?( to)?|call it|name it)\s*/i, '').replace(/^["“]|["”]$/g, '') };
    if (/\b(test|fixture|regression|spec)\b/.test(l)) return { kind: 'test', value: /\b(py|python|pytest)\b/.test(l) ? 'py' : 'ts' };
    if (/\b(show|example|why|which one|receipt|evidence)\b/.test(l) && t.split(/\s+/).length <= 8) return { kind: 'show' };
    if (/\b(protect|enforce|block|turn it on|go live)\b/.test(l) && t.split(/\s+/).length <= 6) return { kind: 'mode', value: 'protect' };
    if (/\b(watch|shadow|dry run|just watch)\b/.test(l) && t.split(/\s+/).length <= 6) return { kind: 'mode', value: 'watch' };
    const short = t.split(/\s+/).length <= 7 && /^(make|set|change|use|also|only|instead|and|but|now|switch|per|over|for)\b/i.test(t);
    if (short) {
      const amt = t.match(/\$\s?([\d,]+(?:\.\d+)?)\s*(k)?/i) ?? t.match(/\b(\d{2,})\b/);
      if (amt && rules.some((r) => r.type === 'approval-over')) return { kind: 'slot', slot: 'amount', value: Number(amt[1].replace(/,/g, '')) * (amt[2] ? 1000 : 1) };
      const n = t.match(/\b(\d+)\b/);
      if (n && rules.some((r) => r.type === 'rate-limit')) return { kind: 'slot', slot: 'n', value: Number(n[1]) };
      if (n && rules.some((r) => r.type === 'human-wins')) return { kind: 'slot', slot: 'minutes', value: Number(n[1]) };
      const app = APPS.find((a) => a.re.test(t));
      if (app) return { kind: 'slot', slot: 'app', value: app.id };
      const key = t.match(/\b(order|customer|subscription|invoice|ticket|recipient|record)\b/i);
      if (key) return { kind: 'slot', slot: 'key', value: key[1].toLowerCase() };
      const st = t.match(/\b(inactive|canceled|cancelled|closed|paid|archived)\b/i);
      if (st) return { kind: 'slot', slot: 'state', value: st[1].toLowerCase().replace('cancelled', 'canceled') };
    }
    return { kind: 'new' };
  }

  // ─── a test for every miss ──────────────────────────

  const camel = (s) => s.replace(/[^a-z0-9]+(.)/gi, (_, c) => c.toUpperCase());
  const snake = (s) => s.replace(/[^a-z0-9]+/gi, '_').toLowerCase();

  /** What a test asserts for one rule, given the example that broke it. */
  function expectation(rule, ex) {
    const s = rule.slots;
    const act = actionOf(s.action);
    const keyF = `${s.key}_id`;
    switch (rule.type) {
      case 'once-per-key': return {
        name: `refuses a second ${s.action} for ${s.key} ${ex.key}`,
        ts: `expect(world.${s.app}.${act.plural.replace(/\s/g, '_')}({ ${keyF}: '${ex.key}' })).toHaveLength(1);`,
        py: `assert len(world.${s.app}.${snake(act.plural)}(${keyF}="${ex.key}")) == 1`,
      };
      case 'end-state': {
        const id = `${s.object.slice(0, 3)}_${String(ex.key).split('_').pop()}`;
        return {
          name: `leaves the ${s.object} ${s.state} after a ${s.action}`,
          ts: `expect(world.${s.app}.${s.object}('${id}').status).toBe('${s.state}');`,
          py: `assert world.${s.app}.${s.object}("${id}").status == "${s.state}"`,
        };
      }
      case 'approval-over': return {
        name: `asks a person before a ${s.action} over ${money(s.amount)}`,
        ts: `expect(world.lumni.decisions.at(-1).type).toBe('REQUIRE_APPROVAL');`,
        py: `assert world.lumni.decisions[-1].type == "REQUIRE_APPROVAL"`,
      };
      case 'amount-cap': return {
        name: `keeps the ${s.action} within the ${s.limit}`,
        ts: `expect(world.${s.app}.total({ ${keyF}: '${ex.key}' })).toBeLessThanOrEqual(world.${camel(s.limit)}('${ex.key}'));`,
        py: `assert world.${s.app}.total(${keyF}="${ex.key}") <= world.${snake(s.limit)}("${ex.key}")`,
      };
      case 'order-before': return {
        name: `waits for the ${s.first} before the ${s.action}`,
        ts: `expect(world.order()).toEqual(['${s.first}', '${s.action}']);`,
        py: `assert world.order() == ["${s.first}", "${s.action}"]`,
      };
      case 'rate-limit': return {
        name: `sends at most ${s.n} ${act.plural} per ${s.key} a ${s.window}`,
        ts: `expect(world.count('${s.action}', { ${keyF}: '${ex.key}', per: '${s.window}' })).toBeLessThanOrEqual(${s.n});`,
        py: `assert world.count("${s.action}", ${keyF}="${ex.key}", per="${s.window}") <= ${s.n}`,
      };
      default: return {
        name: `leaves a ${s.key} a person just edited alone`,
        ts: `expect(world.${s.app}.${s.key}('${ex.key}').lastEditedBy).toBe('person');`,
        py: `assert world.${s.app}.${s.key}("${ex.key}").last_edited_by == "person"`,
      };
    }
  }

  /** A regression test file from a contract and the miss it would have stopped. */
  function testFor(c, lang, ex) {
    const rules = c.rules.filter((r) => r.type);
    const agent = ex?.agent ?? 'Your agent';
    const fn = camel(agent.toLowerCase());
    const quote = c.text.length > 90 ? `${c.text.slice(0, 88)}…` : c.text;
    const its = rules.map((r) => expectation(r, ex));
    if (lang === 'py') {
      return [
        `# Generated by Lumni from “${quote}”`,
        `# Replays ${ex.action} (${ex.day} ${ex.at}) so this miss can’t come back.`,
        `from lumni.fixtures import replay`,
        `from agents import ${snake(agent)}`,
        ``,
        ...its.flatMap((x) => [
          `def test_${snake(x.name)}():`,
          `    world = replay("${ex.action}")`,
          `    ${snake(agent)}.run(world.task)`,
          `    ${x.py}`,
          ``,
        ]),
      ].join('\n').trimEnd();
    }
    return [
      `// Generated by Lumni from “${quote}”`,
      `// Replays ${ex.action} (${ex.day} ${ex.at}) so this miss can’t come back.`,
      `import { describe, it, expect } from 'vitest';`,
      `import { replay } from '@lumni/fixtures';`,
      `import { ${fn} } from '../src/agents';`,
      ``,
      `describe('${c.title}', () => {`,
      ...its.flatMap((x, i) => [
        ...(i ? [''] : []),
        `  it('${x.name}', async () => {`,
        `    const world = await replay('${ex.action}');`,
        `    await ${fn}.run(world.task);`,
        `    ${x.ts}`,
        `  });`,
      ]),
      `});`,
    ].join('\n');
  }

  /** A prompt for a coding agent: add the test, make it pass. */
  function agentPrompt(c, lang, ex) {
    return [
      `Add this regression test from Lumni and make it pass.`,
      ``,
      `Contract: ${c.text}`,
      `It caught: ${ex.agent} on ${ex.key}, ${ex.day} ${ex.at} (${ex.action}).`,
      `Fix the agent, not the test. Before each action, call lumni.checkAction() with the ${c.rules.find((r) => r.type)?.slots.key ?? 'resource'}_id, and only act when shouldExecute(decision) is true.`,
      ``,
      '```' + (lang === 'py' ? 'python' : 'ts'),
      testFor(c, lang, ex),
      '```',
    ].join('\n');
  }

  // ─── a contract, as When, Before, Verify, Match, Within ──

  // Templates over a rule's parts. "Before" is checked when the agent asks
  // (admission); "Verify" is read back from the app; "Match" is what must line
  // up between the agent's claim and the app; "Within" is how long it may take.
  const SPEC = {
    'once-per-key': { when: 'Before each {action}', before: 'No other {action} for the same {key}', verify: 'Exactly one {action} succeeded in {app}', match: (s) => [`${s.key}_id`, 'amount', 'currency'], within: true },
    'end-state': { when: 'Your agent reports a {action} as done', verify: 'The {object} is {state} in {app}', match: (s) => [`${s.object}_id`, 'customer'], within: true },
    'approval-over': { when: 'Before each {action} over {amount}', before: 'A person approves it first', verify: 'The {action} matches what was approved', match: () => ['amount', 'approver'] },
    'amount-cap': { when: 'Before each {action}', before: 'The amount is at most the {limit} in {app}', verify: 'Total {action}s for the {key} stay within it', match: (s) => [`${s.key}_id`, 'amount', 'currency'], within: true },
    'order-before': { when: 'Before each {action}', before: 'The {first} for the same {key} has landed in {app}', match: (s) => [`${s.key}_id`, `${s.first}_id`] },
    'rate-limit': { when: 'Before each {action}', before: 'At most {n} per {key} each {window}', match: (s) => [`${s.key}_id`] },
    'human-wins': { when: 'Before each {action}', before: 'No person edited the {key} in the last {minutes} minutes', match: (s) => [`${s.key}_id`, 'last editor'] },
  };
  const runsOf = (t, slots) => t.split(/(\{\w+\})/).filter(Boolean).map((r) => {
    const m = r.match(/^\{(\w+)\}$/);
    return m ? { slot: m[1], value: slots[m[1]] } : r;
  });

  /** A rule as rows: [{k: 'when'|'before'|'verify'|'match'|'within', runs?, fields?}]. */
  function spec(rule) {
    const t = SPEC[rule.type];
    if (!t) return [];
    const s = rule.slots;
    const rows = [{ k: 'when', runs: runsOf(t.when, s) }];
    if (t.before) rows.push({ k: 'before', runs: runsOf(t.before, s) });
    if (t.verify) rows.push({ k: 'verify', runs: runsOf(t.verify, s) });
    rows.push({ k: 'match', fields: t.match(s) });
    if (t.within) rows.push({ k: 'within', runs: runsOf('{within} of the agent’s claim', s) });
    return rows;
  }

  /** What Lumni understood, in one sentence. */
  function sentence(rule) {
    const s = rule.slots;
    const app = appName(s.app);
    const w = say('within', s.within);
    switch (rule.type) {
      case 'once-per-key': return `Before each ${s.action}, Lumni stops a second one for the same ${s.key}, then checks ${app} shows exactly one within ${w}.`;
      case 'end-state': return `When your agent reports a ${s.action} as done, Lumni checks ${app} shows the ${s.object} ${s.state} within ${w}.`;
      case 'approval-over': return `Before any ${s.action} over ${money(s.amount)}, Lumni asks a person, then checks the ${s.action} matches what they approved.`;
      case 'amount-cap': return `Before each ${s.action}, Lumni reads the ${s.limit} from ${app} and stops anything over it.`;
      case 'order-before': return `Before each ${s.action}, Lumni waits until the ${s.first} for the same ${s.key} has landed in ${app}.`;
      case 'rate-limit': return `Lumni lets at most ${s.n} ${actionOf(s.action).plural} through per ${s.key} each ${s.window}.`;
      case 'human-wins': return `Lumni keeps your agents off any ${s.key} a person edited in the last ${s.minutes} minutes.`;
      default: return '';
    }
  }

  /** Words worth adding while someone types: the parts Lumni had to guess. */
  function hints(text, rules) {
    const out = [];
    const r = rules.find((x) => x.type);
    if (!r) return out;
    const g = new Set(r.guessed ?? []);
    if (g.has('key') && ['once-per-key', 'amount-cap', 'rate-limit', 'order-before'].includes(r.type)) out.push({ slot: 'key', add: ` per ${r.slots.key}`, label: `per ${r.slots.key}` });
    if (g.has('app') && !APPS.some((a) => a.re.test(text))) out.push({ slot: 'app', add: ` in ${appName(r.slots.app)}`, label: `in ${appName(r.slots.app)}` });
    if (g.has('amount') && r.type === 'approval-over') out.push({ slot: 'amount', add: ' over $500', label: 'over $500' });
    if (g.has('within') && SPEC[r.type]?.within) out.push({ slot: 'within', add: ' within 5 minutes', label: 'within 5 minutes' });
    return out.slice(0, 3);
  }

  /** The hook an agent needs: ask before (check), tell after (report). */
  function hookNeeds(rules) {
    const ts = rules.filter((r) => r.type).map((r) => SPEC[r.type]);
    return { check: ts.some((t) => t.before), report: ts.some((t) => t.verify) };
  }

  /** A task for a coding agent that adds Lumni to the agent's flow. */
  function agentTask(c, agent, tool) {
    const r = c.rules.find((x) => x.type);
    const s = r.slots;
    const act = actionOf(s.action);
    const need = hookNeeds(c.rules);
    return [
      `Add Lumni to ${agent}’s ${s.action} flow.`,
      ...(need.check ? [`Before each ${s.action}, call lumni.checkAction() with operation "${operation(r)}" and effect_key "${s.action.replace(/\s/g, '_')}:<${s.key}_id>". Only go on when shouldExecute(decision) is true.`] : []),
      ...(need.report ? [`After it finishes, call lumni.reportResult() with the ${appName(s.app)} ${act.object} id.`] : []),
      `Don’t change how the ${s.action} itself runs.`,
      `Run it once against test data and paste the receipt link.`,
    ].join('\n');
  }

  /**
   * The few lines the hook adds, as [kept, line] pairs. A result is reported
   * against the action Lumni checked, so reporting needs the check too; lines
   * the agent already has (`checks`) are kept, not added.
   */
  function hookLines(c, checks = false) {
    const r = c.rules.find((x) => x.type);
    const s = r.slots;
    const act = actionOf(s.action);
    const need = hookNeeds(c.rules);
    // The app call the agent already makes, as it would read in its code.
    const STRIPE = { refund: 'stripe.refunds.create(…)', cancellation: 'stripe.subscriptions.cancel(…)', charge: 'stripe.paymentIntents.create(…)', invoice: 'stripe.invoices.sendInvoice(…)', renewal: 'stripe.subscriptions.resume(…)' };
    const call = (s.app === 'stripe' && STRIPE[s.action]) || { gmail: 'gmail.users.messages.send(…)', vercel: 'vercel.deployments.create(…)' }[s.app] || `${s.app}.${act.op.replace(/_(\w)/g, (_, ch) => ch.toUpperCase())}(…)`;
    const keepCheck = checks || !need.check && !need.report;
    return [
      [keepCheck, `const d = await lumni.checkAction({ operation: '${operation(r)}', effect_key: \`${s.action.replace(/\s/g, '_')}:\${${s.key}.id}\` })`],
      [keepCheck, 'if (shouldExecute(d)) {'],
      [true, `  const out = await ${call}`],
      ...(need.report ? [[false, "  await lumni.reportResult(d.action_id, { status: 'SUCCEEDED', output: { id: out.id } })"]] : []),
      [keepCheck, '}'],
    ];
  }

  /** Exactly what rules read, per app: each record, and the fields on it. */
  function reads(rules) {
    const out = new Map();
    for (const r of rules.filter((x) => x.type)) {
      const s = r.slots;
      const object = r.type === 'end-state' ? s.object : r.type === 'order-before' ? actionOf(s.first).object : actionOf(s.action).object;
      const x = out.get(s.app) ?? { app: s.app, name: appName(s.app), objects: new Map() };
      const fields = x.objects.get(`${object}s`) ?? new Set();
      for (const f of SPEC[r.type]?.match(s) ?? []) if (f !== 'approver') fields.add(f);
      fields.add('status');
      x.objects.set(`${object}s`, fields);
      out.set(s.app, x);
    }
    return [...out.values()];
  }

  /** What Lumni never does in each app. */
  const NEVER = {
    stripe: ['Create charges, refunds or payouts', 'Change or cancel subscriptions', 'See card or bank numbers'],
    shopify: ['Create orders or refunds', 'Change products, prices or stock'],
    zendesk: ['Reply to, assign or close tickets'],
    salesforce: ['Create, edit or delete records'],
    gmail: ['Send, delete or label email'],
    quickbooks: ['Create or edit invoices and payments'],
    vercel: ['Start, cancel or promote deployments'],
  };

  globalThis.Contracts = { reads, NEVER, hash, spec, sentence, hints, hookNeeds, agentTask, hookLines, TYPES, APPS, ACTIONS, OPTIONS, classify, choose, clauses, short, say, title, operation, needs, replay, followUp, testFor, agentPrompt, typeOf, appName, money };
})();
