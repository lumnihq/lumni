// The demo world, at three volumes. Real mode reads the same shape from
// GET /v1/console/world (server/console/views/world.ts).
//
// A realistic world for the screens, at three volumes. Everything is derived
// from a few rates and weights, so the numbers agree across screens and stay
// believable from a small team to a very large one.
//
//   const w = World('mid');   // 'small' | 'mid' | 'large' | 'real'
//
// Rates are per action. Patterns split each kind of step-in into the few
// reasons that explain most of it: at volume, people read reasons, not rows.

(() => {
  // Deterministic random, so every load shows the same world.
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

  const SCALES = {
    small: { label: '2.2k', long: 'Acme, about 2.2k actions a day', org: 'Acme', actions: 2236, ais: 10, apps: 6, things: 240, people: 3, seed: 7 },
    mid: { label: '48k', long: 'Northwind, about 48k actions a day', org: 'Northwind', actions: 48213, ais: 64, apps: 14, things: 8420, people: 12, seed: 11 },
    large: { label: '2.1M', long: 'Globex, about 2.1M actions a day', org: 'Globex', actions: 2140380, ais: 412, apps: 31, things: 312400, people: 46, seed: 23 },
  };

  // Share of actions that end up in each kind of step-in.
  const RATES = {
    recheck: 0.021, // made it look again
    wait: 0.014, // made it wait its turn
    stop: 0.0092, // stopped a repeat or a clash
    ask: 0.00045, // asked a person
    bad: 0.0006, // said done, but the app shows nothing
    blind: 0.036, // no connector, so Lumni can't check
  };
  // Big organisations write more rules that decide on their own.
  const ASK_DAMP = { small: 1, mid: 0.8, large: 0.09 };

  const TEAMS = [
    { id: 'support', name: 'Support', w: 0.31, ai: 'Su' },
    { id: 'billing', name: 'Billing', w: 0.22, ai: 'Bi' },
    { id: 'sales', name: 'Sales', w: 0.16, ai: 'Sa' },
    { id: 'ops', name: 'Ops', w: 0.12, ai: 'Op' },
    { id: 'finance', name: 'Finance', w: 0.08, ai: 'Fi' },
    { id: 'pricing', name: 'Pricing', w: 0.06, ai: 'Pr' },
    { id: 'other', name: 'Everyone else', w: 0.05, ai: '+' },
  ];

  const APPS = [
    { id: 'stripe', name: 'Stripe', w: 0.34, mark: 'S', color: '#635bff' },
    { id: 'salesforce', name: 'Salesforce', w: 0.21, mark: 'Sf', color: '#00a1e0' },
    { id: 'zendesk', name: 'Zendesk', w: 0.17, mark: 'Z', color: '#03363d' },
    { id: 'gmail', name: 'Gmail', w: 0.09, mark: 'G', color: '#ea4335', blind: true },
    { id: 'quickbooks', name: 'QuickBooks', w: 0.06, mark: 'Q', color: '#2ca01c' },
    { id: 'vercel', name: 'Vercel', w: 0.04, mark: 'V', color: '#0d0e14' },
    { id: 'more', name: 'More apps', w: 0.09, mark: '+', color: '#a3a7b3' },
  ];

  // Why each kind happened. Weights split the kind's total.
  const PATTERNS = {
    stop: [
      { id: 'p-refund2', t: 'Support AI tried refunds a second time after Stripe timed out', team: 'support', app: 'stripe', w: 0.46, money: 64, story: 'omar', spike: 11.6 },
      { id: 'p-invoice2', t: 'Billing AI sent invoices that had already gone out', team: 'billing', app: 'stripe', w: 0.21, money: 410, story: 'omar' },
      { id: 'p-ticket2', t: 'Two AIs answered the same ticket', team: 'support', app: 'zendesk', w: 0.18, story: 'omar' },
      { id: 'p-cancelled', t: 'Renewals for customers who had cancelled', team: 'billing', app: 'stripe', w: 0.09, money: 90, story: 'omar' },
      { id: 'p-quote2', t: 'Sales AI rewrote quotes it had already sent', team: 'sales', app: 'gmail', w: 0.06, story: 'omar' },
    ],
    recheck: [
      { id: 'p-oldplan', t: 'Pricing AI read a plan that had just changed', team: 'pricing', app: 'stripe', w: 0.44, story: 'sam' },
      { id: 'p-oldaddr', t: 'Account updates from a copy older than 2 minutes', team: 'ops', app: 'salesforce', w: 0.33, story: 'sam' },
      { id: 'p-oldprice', t: 'Quotes priced before today’s price change', team: 'sales', app: 'salesforce', w: 0.23, story: 'sam' },
    ],
    wait: [
      { id: 'p-deploy', t: 'Ops AI waited for deploys to finish', team: 'ops', app: 'vercel', w: 0.38, story: 'sam' },
      { id: 'p-book', t: 'Finance AI waited for refunds to land before booking', team: 'finance', app: 'quickbooks', w: 0.34, story: 'sam' },
      { id: 'p-acct', t: 'AIs took turns on the same customer account', team: 'sales', app: 'salesforce', w: 0.28, story: 'sam' },
    ],
    ask: [
      { id: 'g-bigrefund', t: 'Refunds over $500', team: 'support', app: 'stripe', w: 0.52, amount: [520, 2400], rule: 'Refunds over $500 ask a person' },
      { id: 'g-downgrade', t: 'Enterprise plan downgrades', team: 'sales', app: 'salesforce', w: 0.27, amount: [1200, 18000], rule: 'Enterprise changes ask a person' },
      { id: 'g-email', t: 'Billing email changes after a person edited it', team: 'sales', app: 'salesforce', w: 0.21, rule: 'A person’s edit wins until you choose' },
    ],
    bad: [
      { id: 'p-norefund', t: 'Refunds Stripe never shows', team: 'billing', app: 'stripe', w: 0.62, money: 48, story: 'priya' },
      { id: 'p-noclose', t: 'Tickets marked closed that are still open', team: 'support', app: 'zendesk', w: 0.38, story: 'priya' },
    ],
  };

  // How busy each hour is, from midnight.
  const HOURS = [0.9, 0.7, 0.6, 0.6, 0.8, 1.4, 2.6, 4.2, 6.1, 7.4, 7.9, 8.2, 7.6, 7.8, 8.1, 7.9, 7.2, 6.0, 4.6, 3.6, 2.8, 2.2, 1.7, 1.2];
  const HSUM = HOURS.reduce((a, b) => a + b, 0);
  const NOW_H = 14 + 13 / 60;

  function soFar(total) {
    // Share of the day's work done by 2:13 PM.
    let s = 0;
    for (let h = 0; h < 24; h++) s += HOURS[h] * Math.max(0, Math.min(1, NOW_H - h));
    return Math.round((total * s) / HSUM);
  }

  function build(key) {
    const sc = SCALES[key];
    const r = rng(sc.seed);
    const total = soFar(sc.actions);
    const kinds = {};
    for (const k in RATES) {
      let rate = RATES[k] * (k === 'ask' ? ASK_DAMP[key] : 1);
      if (k === 'blind' && key === 'small') rate = 0.081;
      kinds[k] = Math.max(k === 'ask' ? 1 : 1, Math.round(total * rate * (0.92 + r() * 0.16)));
    }
    if (key === 'small') Object.assign(kinds, { stop: 9, recheck: 6, wait: 5, ask: 2, bad: 1 });

    const patterns = {};
    for (const k in PATTERNS) {
      let left = kinds[k];
      patterns[k] = PATTERNS[k].map((p, i, all) => {
        const n = i === all.length - 1 ? left : Math.max(0, Math.round(kinds[k] * p.w));
        left -= n;
        return { ...p, kind: k, n, kept: p.money ? n * p.money : 0 };
      }).filter((p) => p.n > 0);
    }
    const kept = patterns.stop.reduce((s, p) => s + p.kept, 0);

    // Hourly volume so far, for sparklines and the day strip.
    const hourly = HOURS.map((v, h) => Math.round((sc.actions * v) / HSUM * Math.max(0, Math.min(1, NOW_H - h))));
    const stepHourly = hourly.map((v, h) => Math.round(v * 0.046 * (h === 11 ? 3.4 : 0.8 + r() * 0.4)));

    // The waiting decisions, grouped, with real-looking members.
    const groups = patterns.ask.map((g) => {
      const members = [];
      const nm = ['Omar', 'Priya', 'Lee', 'Dana', 'Kai', 'Ruth', 'Zoe', 'Ben', 'Nina', 'Theo', 'Iris', 'Hugo', 'Mia', 'Ravi', 'June', 'Finn', 'Cleo', 'Ada', 'Eli', 'Noah'];
      const co = ['Initech', 'Umbra', 'Hooli', 'Vandelay', 'Tyrell', 'Stark & Sons', 'Wayne Supply', 'Hale & Co', 'Globex', 'Northwind'];
      for (let i = 0; i < g.n; i++) {
        const a = g.amount ? Math.round((g.amount[0] + r() * (g.amount[1] - g.amount[0]) * 0.35) / 10) * 10 : null;
        members.push({
          id: `${g.id}-${i}`,
          who: g.id === 'g-bigrefund' ? nm[(i * 7) % nm.length] + ' ' + String.fromCharCode(65 + ((i * 3) % 26)) + '.' : co[(i * 3) % co.length],
          amount: a,
          mins: Math.round(2 + r() * 40),
        });
      }
      // One outlier, so the screen has something to catch.
      if (g.amount && members.length > 3) members[Math.floor(members.length / 2)].amount = g.amount[1] * 3.1;
      return { ...g, members };
    });

    const team = (id) => TEAMS.find((t) => t.id === id);
    const app = (id) => APPS.find((a) => a.id === id);
    const base = {
      key, scale: sc, total, kinds, patterns, kept, hourly, stepHourly, groups, team, app,
      teams: TEAMS.map((t) => ({ ...t, n: Math.round(total * t.w) })),
      apps: APPS.slice(0, key === 'small' ? 6 : 7).map((a) => ({ ...a, n: Math.round(total * a.w) })),
      steps: kinds.stop + kinds.recheck + kinds.wait + kinds.ask + kinds.bad,
      proven: 1 - kinds.blind / total - kinds.bad / total,
      spend: Math.round(total * 0.092),
      pace: Math.round(sc.actions * 0.092),
      working: Math.round(sc.ais * 0.62),
    };
    // The demo's clock: Thursday, Sep 25, 2:13 PM, the same moment on every load.
    const dayStart = Date.UTC(2026, 8, 25);
    const clock = {
      now: dayStart + NOW_H * 3600e3, nowH: NOW_H, dayStart, zone: 'UTC', dayLabel: 'Thursday, Sep 25', timeLabel: '2:13 PM',
      mode: 'ENFORCE', partial: false,
      // Every rule here protects, so nothing is only watched.
      would: { stop: 0, wait: 0, recheck: 0, ask: 0 },
    };
    // The last eight days, for comparisons: a steady week with a quieter weekend.
    const SHAPE = [0.93, 0.97, 1.02, 0.71, 0.66, 1.0, 0.98, 1];
    const week = {
      days: SHAPE.map((f, i) => {
        const full = i === 7 ? base.total : Math.round(sc.actions * f);
        return { date: new Date(dayStart - (7 - i) * 86400e3).toISOString().slice(0, 10), total: full, steps: Math.round(full * (base.steps / base.total)), kept: Math.round(base.kept * (full / base.total)), spend: Math.round(full * 0.092), verified: Math.round(full * base.proven) };
      }),
    };
    const vsLastWeek = { total: Math.round(base.total / 1.08), steps: Math.round(base.steps / 1.04), kept: Math.round(base.kept / 1.12), spend: Math.round(base.spend / 1.06), proven: base.proven - 0.009 };
    return { ...base, ...clock, source: 'demo', paused: false, week, vsLastWeek, ...extras(base, r) };
  }

  // ─── named AIs, money, the incident, contended things ──

  // The named AIs behind each team. At volume each is a fleet of copies.
  const ROSTER = [
    { id: 'refund', name: 'Refund agent', team: 'support', w: 0.17, cost: 0.08, app: 'stripe', op: 'stripe.refund' },
    { id: 'triage', name: 'Ticket triage', team: 'support', w: 0.14, cost: 0.05, app: 'zendesk', op: 'zendesk.reply' },
    { id: 'invoice', name: 'Invoice sender', team: 'billing', w: 0.12, cost: 0.04, app: 'stripe', op: 'stripe.send_invoice' },
    { id: 'dunning', name: 'Dunning agent', team: 'billing', w: 0.10, cost: 0.05, app: 'stripe', op: 'stripe.retry_payment' },
    { id: 'quote', name: 'Quote writer', team: 'sales', w: 0.09, cost: 0.19, app: 'gmail', op: 'gmail.send' },
    { id: 'router', name: 'Lead router', team: 'sales', w: 0.07, cost: 0.06, app: 'salesforce', op: 'salesforce.assign_lead' },
    { id: 'deploy', name: 'Deploy bot', team: 'ops', w: 0.06, cost: 0.14, app: 'vercel', op: 'vercel.deploy' },
    { id: 'accounts', name: 'Account updater', team: 'ops', w: 0.06, cost: 0.07, app: 'salesforce', op: 'salesforce.update_account' },
    { id: 'ledger', name: 'Ledger booker', team: 'finance', w: 0.05, cost: 0.06, app: 'quickbooks', op: 'quickbooks.book' },
    { id: 'recon', name: 'Reconciler', team: 'finance', w: 0.03, cost: 0.11, app: 'quickbooks', op: 'quickbooks.reconcile' },
    { id: 'pricer', name: 'Price setter', team: 'pricing', w: 0.06, cost: 0.12, app: 'stripe', op: 'salesforce.update_price' },
    { id: 'misc', name: 'Everyone else', team: 'other', w: 0.05, cost: 0.10, app: 'more', op: 'other.action' },
  ];
  // Autonomy levels, from the contract's enforcement modes.
  const LEVELS = ['Watch', 'Warn', 'Enforce', 'Autopilot'];

  function extras(w, r) {
    const { total, kinds, scale: sc } = w;
    // Fleet: per named AI, its record today. Rates vary so there's a story.
    const shapes = {
      refund: { repeat: 0.031, stale: 0.006, bad: 0.0012, streak: 0.4, level: 2, loop: 0 },
      triage: { repeat: 0.009, stale: 0.004, bad: 0.0005, streak: 0.9, level: 2, loop: 0 },
      invoice: { repeat: 0.019, stale: 0.003, bad: 0.0003, streak: 0.6, level: 2, loop: 0 },
      dunning: { repeat: 0.002, stale: 0.002, bad: 0.0001, streak: 9.8, level: 2, loop: 0, promote: true },
      quote: { repeat: 0.011, stale: 0.021, bad: 0.0004, streak: 0.3, level: 1, loop: 0 },
      router: { repeat: 0.001, stale: 0.003, bad: 0.0, streak: 14.2, level: 3, loop: 0 },
      deploy: { repeat: 0.003, stale: 0.004, bad: 0.0, streak: 2.1, level: 2, loop: 1 },
      accounts: { repeat: 0.004, stale: 0.038, bad: 0.0002, streak: 0.2, level: 1, loop: 0, demote: true },
      ledger: { repeat: 0.001, stale: 0.002, bad: 0.0, streak: 11.6, level: 2, loop: 0, promote: true },
      recon: { repeat: 0.002, stale: 0.009, bad: 0.0006, streak: 1.2, level: 0, loop: 0 },
      pricer: { repeat: 0.002, stale: 0.044, bad: 0.0001, streak: 0.5, level: 1, loop: 0 },
      misc: { repeat: 0.006, stale: 0.01, bad: 0.0005, streak: 1, level: 1, loop: 0 },
    };
    const copies = (x) => Math.max(1, Math.round(sc.ais * x.w));
    const fleet = ROSTER.map((x) => {
      const s = shapes[x.id];
      const n = Math.round(total * x.w);
      const spend = n * x.cost * (w.spend / Math.max(1, total * 0.092)) * 0.92;
      const repeats = Math.round(n * s.repeat);
      const stale = Math.round(n * s.stale);
      const bad = Math.round(n * s.bad);
      const proven = Math.max(0, n - repeats - bad - Math.round(n * (x.app === 'gmail' ? 0.9 : 0.02)));
      return {
        ...x, copies: copies(x), n, spend, repeats, stale, bad, proven,
        perProven: spend / Math.max(1, proven),
        // Clean actions in a row, so promotions have evidence.
        streak: Math.round(s.streak * Math.max(40, n * 0.18)),
        level: s.level, levelName: LEVELS[s.level], promote: !!s.promote, demote: !!s.demote, loop: s.loop,
        // How old its reads are, on average, in seconds.
        lag: Math.round(4 + s.stale * 900 + r() * 6),
      };
    });

    // Money: where the AI budget went, and what Lumni cost and saved.
    const unit = w.spend / Math.max(1, total);
    const waste = {
      repeats: { t: 'Repeats Lumni stopped before they ran', n: kinds.stop, usd: kinds.stop * unit * 1.4, avoided: true },
      stale: { t: 'Work redone after reading old info', n: kinds.recheck, usd: kinds.recheck * unit * 3, avoided: false },
      loops: { t: 'Retry loops Lumni cut short', n: Math.round(total * 0.003), usd: total * 0.003 * unit * 14, avoided: true },
      superseded: { t: 'Work thrown away because a newer task replaced it', n: Math.round(total * 0.012), usd: total * 0.012 * unit * 2.5, avoided: false },
      failed: { t: 'Actions that never happened', n: kinds.bad, usd: kinds.bad * unit * 2, avoided: false },
    };
    const lumniCost = Math.max(sc.actions === 2 ? 0 : 15, total * 0.02);
    const avoidedSpend = waste.repeats.usd + waste.loops.usd;
    const hoursSaved = Math.round(kinds.stop * 0.12 + kinds.bad * 0.5 + kinds.ask * 0.05 + kinds.wait * 0.02);
    const money = {
      unit, waste, lumniCost, avoidedSpend, hoursSaved,
      wasted: Object.values(waste).filter((x) => !x.avoided).reduce((s, x) => s + x.usd, 0),
      roi: (w.kept + avoidedSpend + hoursSaved * 85) / Math.max(1, lumniCost),
      limit: Math.round(w.pace * 0.9 / 50) * 50 || null,
      perProven: w.spend / Math.max(1, total * w.proven),
    };

    // The 11:40 Stripe timeout, minute by minute. Counts scale with volume.
    const spike = (w.patterns.stop.find((p) => p.spike) ?? { n: kinds.stop }).n;
    const burst = Math.max(1, Math.round(spike * 0.42));
    const incident = {
      title: 'Stripe timed out for 11 minutes',
      start: '11:36 AM', end: '11:47 AM', burst,
      kept: burst * 64,
      customers: Math.max(1, Math.round(burst * 0.93)),
      ais: Math.max(1, Math.round(copies(ROSTER[0]) * 0.8)),
      events: [
        { t: '11:36:04', s: 0, kind: 'app', text: 'Stripe slows down. Refund calls take 9 seconds instead of 400 ms.' },
        { t: '11:37:40', s: 96, kind: 'app', text: 'First refund call times out. Refund agent never hears back.' },
        { t: '11:37:52', s: 108, kind: 'stop', text: 'Refund agent tries the same refund again. Lumni sees it already went out and stops it.' },
        { t: '11:38:30', s: 146, kind: 'stop', text: `Retries spread across ${fmtN(Math.max(1, Math.round(copies(ROSTER[0]) * 0.8)))} copies of Refund agent. Lumni stops each one.` },
        { t: '11:39:10', s: 186, kind: 'wait', text: 'Lumni slows refunds to 8 at a time so Stripe can catch up.' },
        { t: '11:41:00', s: 296, kind: 'ask', text: 'Refunds over $500 are held for a person while Stripe is unsure.' },
        { t: '11:47:20', s: 676, kind: 'app', text: 'Stripe recovers. Calls back to 380 ms.' },
        { t: '11:49:05', s: 781, kind: 'proven', text: 'Lumni reads back every refund from the window. Each customer has exactly one.' },
        { t: '11:52:00', s: 956, kind: 'proven', text: 'Refunds back to full speed. Incident closed, nobody paged.' },
      ],
    };

    // The most fought-over things, for coordination views.
    const THINGS = ['Refunds queue', 'Checkout site', 'Acme’s account', 'Sam’s subscription', 'March invoices', 'Enterprise price list', 'Initech’s account', 'Payout schedule', 'Support inbox', 'Pricing page'];
    const things = THINGS.slice(0, w.key === 'real' ? 1 : w.key === 'small' ? 5 : 10).map((name, i) => {
      const heat = Math.pow(0.72, i);
      return {
        id: 't' + i, name,
        app: ['stripe', 'vercel', 'salesforce', 'stripe', 'stripe', 'salesforce', 'salesforce', 'quickbooks', 'zendesk', 'vercel'][i],
        touches: Math.max(2, Math.round((total / Math.max(1, sc.things)) * 60 * heat * (0.8 + r() * 0.4))),
        waits: Math.max(0, Math.round(kinds.wait * 0.09 * heat)),
        holders: Math.max(1, Math.round(Math.min(sc.ais, 3 + sc.ais * 0.05) * heat)),
      };
    });

    // Shared context: facts many AIs read, and how fresh each copy is.
    const FACTS = [
      { id: 'f1', thing: 'Sam’s subscription', field: 'plan', v: 14, value: 'Pro, $540 a year', was: 'Starter, $240 a year', changedBy: 'Pricing AI', ago: 38 },
      { id: 'f2', thing: 'Acme’s account', field: 'billing email', v: 9, value: 'billing@acme.com', was: 'ap@acme.com', changedBy: 'Jess', human: true, ago: 184 },
      { id: 'f3', thing: 'Enterprise price list', field: 'seat price', v: 31, value: '$42 a seat', was: '$39 a seat', changedBy: 'Price setter', ago: 12 },
      { id: 'f4', thing: 'Checkout site', field: 'release', v: 208, value: 'update 2.4', was: 'update 2.3', changedBy: 'Deploy bot', ago: 66 },
      { id: 'f5', thing: 'Omar’s order #4471', field: 'refund', v: 3, value: 'refunded $60', was: 'paid', changedBy: 'Refund agent', ago: 131 },
      { id: 'f6', thing: 'Initech’s account', field: 'owner', v: 22, value: 'Dana Ruiz', was: 'Lee Sato', changedBy: 'Lead router', ago: 7 },
    ];
    const facts = FACTS.slice(0, w.key === 'real' ? 1 : w.key === 'small' ? 4 : 6).map((f, i) => {
      const readers = Math.max(1, Math.round(Math.min(sc.ais, 2 + sc.ais * 0.06) * Math.pow(0.8, i)));
      const stale = Math.round(readers * [0.38, 0.12, 0.55, 0.08, 0.0, 0.62][i]);
      return { ...f, readers, stale, reads: Math.max(readers, Math.round((total / 600) * Math.pow(0.7, i))), caught: Math.round(kinds.recheck * [0.31, 0.05, 0.22, 0.04, 0.0, 0.11][i]) };
    });

    return { fleet, money, incident, things, facts, LEVELS };
  }


  /** 1,283, 48.2k, 2.14M. */
  function fmtN(n) {
    n = Math.round(n);
    if (n < 10000) return n.toLocaleString('en-US');
    if (n < 1e6) return (n / 1000).toFixed(n < 1e5 ? 1 : 0).replace(/\.0$/, '') + 'k';
    return (n / 1e6).toFixed(2).replace(/0$/, '') + 'M';
  }
  /** $64, $18.4k, $1.2M. */
  function fmtMoney(n) {
    return '$' + fmtN(n);
  }
  function pct(x, digits = 1) {
    return (x * 100).toFixed(digits).replace(/\.0$/, '') + '%';
  }

  const cache = {};
  /** The demo world at a volume. Real data never comes from here. */
  window.World = (key) => {
    if (!SCALES[key]) throw new Error(`No demo world called ${key}`);
    return (cache[key] ??= build(key));
  };
  window.World.SCALES = SCALES;
  window.World.HOURS = HOURS;
  Object.assign(window, { fmtN, fmtMoney, pct });
})();
