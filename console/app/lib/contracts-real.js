// Outcome contracts on your workspace's real data: /contracts and
// /contract-list draft, bind, preview and run them through the Go API
// (/v1/contracts), which the console's server passes straight through.
//
//   ContractsReal.fromApi(x)          // an API contract as the pages hold it
//   ContractsReal.toApi(c)            // and back, with only the fields the API takes
//   ContractsReal.missing(rule)       // the bindings a rule still needs, per field
//   ContractsReal.client(Shell.api)   // every /v1/contracts call the pages make
//
// Nothing here touches the DOM, so the tests can read all of it. Numbers the
// API doesn't have stay null (a dash on the page); nothing is borrowed from
// the demo. Needs contracts.js first.

(() => {
  const C = globalThis.Contracts;

  // ─── bindings ───────────────────────────────────────
  //
  // What each kind of rule needs before it can watch or protect, as
  // ValidateRules in internal/contracts/service.go checks it.

  const KEYED = ['once-per-key', 'amount-cap', 'order-before', 'rate-limit', 'human-wins'];
  const MONEY = ['approval-over', 'amount-cap'];
  const READS = ['amount-cap', 'human-wins'];
  const POINTERS = ['key_pointer', 'amount_pointer', 'currency_pointer', 'value_pointer', 'last_editor_pointer', 'last_edited_at_pointer'];
  // Every field of protocol.ContractBinding. The API refuses any other.
  const BINDING = ['operation', 'actor_id', 'key_pointer', 'amount_pointer', 'currency_pointer', 'currency', 'read_binding_id', 'read_arguments', 'value_pointer', 'last_editor_pointer', 'last_edited_at_pointer', 'verifier_id', 'expected', 'within_seconds', 'max_age_seconds', 'predecessor_operation'];

  /** The binding fields a kind of rule takes, in the order the editor shows them. */
  function fields(type) {
    const f = ['operation'];
    if (KEYED.includes(type)) f.push('key_pointer');
    if (type === 'order-before') f.push('predecessor_operation');
    if (MONEY.includes(type)) f.push('amount_pointer', 'currency');
    if (READS.includes(type)) f.push('read_binding_id');
    if (type === 'amount-cap') f.push('value_pointer');
    if (type === 'human-wins') f.push('last_editor_pointer', 'last_edited_at_pointer');
    if (type === 'end-state') f.push('verifier_id', 'expected');
    return f;
  }

  /** Each field, for people: a label, what it's for, and an example. */
  const LABELS = {
    operation: { label: 'Operation', why: 'The call your agent asks Lumni about', eg: 'stripe.refund' },
    key_pointer: { label: 'Counted by', why: 'Where the key is in the call’s arguments', eg: '/order_id' },
    predecessor_operation: { label: 'Waits for', why: 'The operation that has to land first', eg: 'stripe.refund' },
    amount_pointer: { label: 'Amount', why: 'Where the amount is, in minor units', eg: '/amount' },
    currency: { label: 'Currency', why: 'Amounts are never converted', eg: 'USD' },
    read_binding_id: { label: 'Reads from', why: 'A read-only tool binding Lumni calls', eg: 'stripe.balance' },
    value_pointer: { label: 'Limit value', why: 'Where the limit is in what it reads', eg: '/eligible_balance' },
    last_editor_pointer: { label: 'Last editor', why: 'Who changed it last, in what it reads', eg: '/last_editor' },
    last_edited_at_pointer: { label: 'Last edited at', why: 'When, as an RFC 3339 time', eg: '/last_edited_at' },
    verifier_id: { label: 'Checked by', why: 'An enabled verifier that reads the app back', eg: 'v_stripe' },
    expected: { label: 'Expected', why: 'What the verifier must see, as JSON', eg: '{"status":"canceled"}' },
  };

  const str = (v) => (typeof v === 'string' ? v.trim() : '');
  const int = (v) => { const n = Number(v); return Number.isFinite(n) && n === Math.trunc(n) ? n : null; };
  const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);

  /** A binding with only what the API takes, and nothing empty. */
  function cleanBinding(b) {
    const out = { operation: str(b?.operation) };
    for (const k of BINDING) {
      if (k === 'operation' || b?.[k] == null) continue;
      const v = b[k];
      if (k === 'expected') { if (isObj(v) && Object.keys(v).length) out.expected = v; continue; }
      if (k === 'read_arguments') { if (isObj(v) && Object.keys(v).length) out.read_arguments = v; continue; }
      if (k === 'within_seconds' || k === 'max_age_seconds') { const n = int(v); if (n) out[k] = n; continue; }
      if (str(v)) out[k] = str(v);
    }
    return out;
  }

  /** A rule's parts as the API keeps them: the console's slots, the sentence, and the numbers the checks read. */
  function apiSlots(r) {
    const s = { ...(r.slots ?? {}) };
    if (r.text) s.sentence = r.text;
    if (r.type === 'approval-over' && Number.isFinite(Number(s.amount))) s.amount_minor = Math.round(Number(s.amount) * 100);
    return s;
  }

  /**
   * One rule as the API takes it: id, type, slots, binding. Nothing else. A
   * verifier's deadline follows the words ("within 15 minutes"), so an edit
   * to them can't leave a stale one behind.
   */
  function apiRule(r, i = 0) {
    const b = cleanBinding(r.binding);
    const within = Number(r.slots?.within);
    if (b.verifier_id && within > 0) b.within_seconds = Math.min(86400, Math.max(1, Math.round(within * 60)));
    return { id: r.id ?? `r${i + 1}`, type: r.type, slots: apiSlots(r), binding: b };
  }

  /** A contract as POST/PATCH /v1/contracts takes it. */
  function toApi(c) {
    const out = { title: c.title, text: c.text, rules: c.rules.filter((r) => r.type).map(apiRule) };
    if (c.revision) out.revision = c.revision;
    return out;
  }

  /**
   * What a rule still needs before it can watch or protect, field by field,
   * in the editor's order: [{ field, why }]. Empty means the API will take it.
   */
  function missing(r) {
    const b = r.binding ?? {};
    const s = apiSlots(r);
    const out = [];
    const need = (field, why) => { if (!out.some((x) => x.field === field)) out.push({ field, why }); };
    const op = str(b.operation);
    if (!op || op === '*') need('operation', 'Name the operation, like stripe.refund');
    for (const p of POINTERS) if (str(b[p]) && !str(b[p]).startsWith('/')) need(p, 'Start it with /, like /order_id');
    if (KEYED.includes(r.type) && !str(b.key_pointer)) need('key_pointer', 'Say where the key is, like /order_id');
    if (MONEY.includes(r.type)) {
      if (!str(b.amount_pointer)) need('amount_pointer', 'Say where the amount is, like /amount');
      if (!str(b.currency)) need('currency', 'Pick the currency the amount is in');
    }
    if (r.type === 'approval-over') { const n = int(s.amount_minor); if (n == null || n < 0) need('slot:amount', 'Set the amount a person approves above'); }
    if (r.type === 'amount-cap') {
      if (!str(b.read_binding_id)) need('read_binding_id', 'Pick where Lumni reads the limit');
      if (!str(b.value_pointer)) need('value_pointer', 'Say where the limit is in what it reads');
    }
    if (r.type === 'rate-limit') {
      const n = int(s.n);
      if (n == null || n < 1) need('slot:n', 'Set how many are allowed');
      if (!['minute', 'hour', 'day', 'week'].includes(String(s.window))) need('slot:window', 'Pick a minute, hour, day or week');
    }
    if (r.type === 'order-before' && !str(b.predecessor_operation)) need('predecessor_operation', 'Name the operation that goes first');
    if (r.type === 'human-wins') {
      const n = int(s.minutes);
      if (n == null || n < 1) need('slot:minutes', 'Set how many minutes a person’s edit holds');
      if (!str(b.read_binding_id)) need('read_binding_id', 'Pick where Lumni reads who edited it');
      if (!str(b.last_editor_pointer)) need('last_editor_pointer', 'Say where the last editor is');
      if (!str(b.last_edited_at_pointer)) need('last_edited_at_pointer', 'Say where the edit time is');
    }
    if (r.type === 'end-state' && !str(b.verifier_id)) need('verifier_id', 'Pick the verifier that reads the app back');
    if (str(b.verifier_id) && !(isObj(b.expected) && Object.keys(b.expected).length)) need('expected', 'Say what it must see, as a JSON object');
    return out;
  }

  /** Lumni's guesses for a rule's bindings, from its words and what's set up. */
  function suggest(r, { bindings = [], verifiers = [] } = {}) {
    const s = r.slots ?? {};
    const first = C.ACTIONS.find((a) => a.id === s.first);
    const readers = bindings.filter((x) => x.read_only && x.enabled !== false);
    const reader = readers.find((x) => x.app === s.app) ?? readers[0];
    const usable = verifiers.filter((v) => v.enabled);
    const verifier = usable.find((v) => v.config?.app === s.app || String(v.id).includes(s.app)) ?? usable[0];
    const out = { operation: C.operation(r), key_pointer: `/${s.key}_id` };
    if (r.type === 'order-before') out.predecessor_operation = `${s.app}.${first?.op ?? s.first}`;
    if (MONEY.includes(r.type)) Object.assign(out, { amount_pointer: '/amount', currency: 'USD' });
    if (READS.includes(r.type) && reader) out.read_binding_id = reader.id;
    if (r.type === 'amount-cap') out.value_pointer = `/${String(s.limit ?? 'limit').replace(/\s+/g, '_')}`;
    if (r.type === 'human-wins') Object.assign(out, { last_editor_pointer: '/last_editor', last_edited_at_pointer: '/last_edited_at' });
    if (r.type === 'end-state') {
      if (verifier) out.verifier_id = verifier.id;
      out.expected = { status: s.state };
    }
    for (const k of Object.keys(out)) if (!fields(r.type).includes(k)) delete out[k];
    return out;
  }

  /** Fill only the fields still empty with Lumni's guesses. */
  function fillGuesses(r, ctx) {
    const b = { ...(r.binding ?? {}) };
    for (const [k, v] of Object.entries(suggest(r, ctx))) if (b[k] == null || b[k] === '') b[k] = v;
    return b;
  }

  // ─── reading ────────────────────────────────────────

  /** The parts a kind needs, from its words: Lumni's on-device reading fills what the API leaves out (like `first` and `within`). */
  const localSlots = (text, type) => C.choose({ text, slots: {} }, type).slots;

  /**
   * The API's readings (POST /v1/contracts/classify) as the page's rules. The
   * kind, the ranking, how sure it is and the deciding words are the API's.
   */
  function fromReadings(readings) {
    return (readings ?? []).map((x, i) => {
      const kind = x.type ?? x.ranked?.[0]?.type ?? 'once-per-key';
      const slots = { ...localSlots(x.text, kind), ...(x.slots ?? {}) };
      const guessed = [...new Set((x.guessed ?? []).map((g) => (g === 'amount_minor' ? 'amount' : g)).filter((g) => g !== 'execution_binding'))];
      return {
        id: `r${i + 1}`, text: x.text, ranked: x.ranked ?? [], state: x.state, confidence: x.ranked?.[0]?.p ?? 0,
        type: x.state === 'sure' ? (x.type ?? kind) : null, slots, guessed, cues: x.cues ?? [], source: x.source ?? 'deterministic',
      };
    });
  }

  /** An API contract as the pages hold it. Rules keep their words and binding; anything else stays as the API said. */
  function fromApi(x) {
    const rules = (x.rules ?? []).map((r, i) => {
      const { sentence, ...rest } = r.slots ?? {};
      const text = str(sentence) || (x.rules.length === 1 ? x.text : `Rule ${i + 1}`);
      const slots = { ...localSlots(text, r.type), ...rest };
      if (rest.amount == null && int(rest.amount_minor) != null) slots.amount = rest.amount_minor / 100;
      return { id: r.id, text, type: r.type, state: 'sure', ranked: [], guessed: [], cues: [], slots, binding: { ...(r.binding ?? {}) } };
    });
    const forced = Object.fromEntries(rules.map((r) => [r.text, r.type]));
    const over = Object.fromEntries(rules.map((r) => { const { amount_minor: _m, ...s } = r.slots; return [r.text, s]; }));
    const ready = rules.every((r) => !missing(r).length);
    return {
      id: x.id, title: x.title, text: x.text, rules, forced, over,
      mode: x.mode ?? 'draft', was: x.was || undefined, revision: x.revision ?? 1, effective: x.effective_mode ?? null,
      created: x.created_at ?? null, updated: x.updated_at ?? null,
      hook: { tool: 'claude', state: 'none' },
      done: { describe: true, connect: ready && x.mode !== 'draft', preview: false },
    };
  }

  /** Keep each rule's binding when its words and kind come back the same after an edit. */
  function carryBindings(rules, before) {
    return rules.map((r) => {
      const was = before?.find((b) => b.text === r.text && b.type === r.type) ?? before?.find((b) => b.id === r.id && b.type === r.type);
      return { ...r, binding: { ...(was?.binding ?? r.binding ?? {}) } };
    });
  }

  // ─── what the API says back ─────────────────────────

  const micros = (n) => (n == null ? null : n / 1e6);
  const num = (n) => (typeof n === 'number' && Number.isFinite(n) ? n : null);

  /** GET /v1/contracts/{id}/week. Unmeasured savings stay null. */
  function week(w) {
    if (!w) return null;
    return {
      seen: num(w.seen), verified: num(w.verified), gaps: num(w.gaps), wouldStop: num(w.would_stop), stopped: num(w.stopped),
      saved: micros(w.saved_micros_usd), days: (w.days ?? []).map((d) => num(d.seen) ?? 0), dates: (w.days ?? []).map((d) => d.date),
    };
  }

  /** POST /v1/contracts/preview. */
  function preview(p) {
    if (!p) return null;
    const days = Object.entries(p.days ?? {}).sort(([a], [b]) => a.localeCompare(b));
    return {
      seen: num(p.seen), wouldStop: num(p.would_stop), verified: num(p.verified), gaps: num(p.gaps), saved: micros(p.saved_micros_usd),
      unavailable: num(p.unavailable_evidence) ?? 0, truncated: !!p.truncated, examples: p.examples ?? [], results: p.results ?? [],
      days: days.map(([, n]) => n), from: p.from ?? null, to: p.to ?? null,
    };
  }

  /** One item of GET /v1/contracts/{id}/runs. */
  function run(x) {
    const rules = x.rules ?? [];
    const broke = rules.filter((r) => r.decision && r.decision !== 'ALLOW');
    return {
      id: x.action_id, agent: x.agent ?? x.actor_id ?? null, operation: x.operation ?? null, at: x.at ?? null,
      decision: x.decision ?? null, outcome: x.outcome ?? null, receipt: x.receipt_id ?? null,
      broke: broke.length > 0, reason: broke[0]?.reason ?? null, mode: rules[0]?.mode ?? null,
      verified: rules.length ? rules.every((r) => r.verification_status === 'VERIFIED') : null,
    };
  }

  /** GET /v1/contracts/{id}/needs, per field where it says which. */
  function needs(n) {
    const out = { ready: !!n?.ready, apps: n?.apps ?? [], general: [], reads: new Map(), verifiers: new Set() };
    for (const m of n?.missing ?? []) {
      let x;
      if ((x = /^read binding\/grant: (.+)$/.exec(m))) out.reads.set(x[1], m);
      else if ((x = /^verifier: (.+)$/.exec(m))) out.verifiers.add(x[1]);
      else out.general.push(m);
    }
    return out;
  }

  /** Say an API refusal the way the page would. */
  function says(m) {
    const t = String(m ?? '');
    if (/^read binding\/grant: /.test(t)) return `Lumni can’t read through ${t.slice(20)} yet: it needs a read grant`;
    if (/^verifier: /.test(t)) return `${t.slice(10)} isn’t an enabled verifier`;
    return t ? `${t[0].toUpperCase()}${t.slice(1)}` : t;
  }

  // ─── the API ────────────────────────────────────────

  /** Every call the contract pages make. `api` is Shell.api, or the tests' stand-in. */
  function client(api) {
    const enc = encodeURIComponent;
    const tz = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'; } catch { return 'UTC'; } };
    return {
      list: async () => (await api.get('/v1/contracts?limit=200')).items.map(fromApi),
      get: async (id) => fromApi(await api.get(`/v1/contracts/${enc(id)}`)),
      create: async (c) => fromApi(await api.post('/v1/contracts', toApi(c))),
      save: async (c) => fromApi(await api.patch(`/v1/contracts/${enc(c.id)}`, toApi(c))),
      mode: async (c, mode) => fromApi(await api.put(`/v1/contracts/${enc(c.id)}/mode`, { mode, revision: c.revision })),
      remove: (c) => api.del(`/v1/contracts/${enc(c.id)}`),
      classify: (text) => api.post('/v1/contracts/classify', { text }),
      needs: async (c) => needs(await api.get(`/v1/contracts/${enc(c.id)}/needs`)),
      hooks: async (c) => (await api.get(`/v1/contracts/${enc(c.id)}/hook-status`)).items ?? [],
      week: async (c) => week(await api.get(`/v1/contracts/${enc(c.id)}/week?tz=${enc(tz())}`)),
      runs: async (c, limit = 5) => ((await api.get(`/v1/contracts/${enc(c.id)}/runs?limit=${limit}`)).items ?? []).map(run),
      preview: async (c) => preview(await api.post('/v1/contracts/preview', { rules: c.rules.filter((r) => r.type).map(apiRule), tz: tz() })),
      tests: (c, lang, action) => api.get(`/v1/contracts/${enc(c.id)}/tests?lang=${enc(lang)}&action=${enc(action)}`),
      verifiers: async () => (await api.get('/v1/verifiers?limit=200')).items ?? [],
      bindings: async () => (await api.get('/v1/tool-bindings?limit=200')).items ?? [],
    };
  }

  /** A number, or a dash when the API doesn't know it. */
  const dash = (n, f = (x) => Math.round(x).toLocaleString('en-US')) => (n == null ? '—' : f(n));

  globalThis.ContractsReal = { fields, LABELS, cleanBinding, apiRule, toApi, missing, suggest, fillGuesses, fromReadings, fromApi, carryBindings, week, preview, run, needs, says, client, dash };
})();
