// A stand-in for Lumni that decides the way the Go kernel does
// (internal/coordination: kernel.go, durable.go, controls.go), for tests.
// It checks a limit (WAIT), work superseded or cancelled (REPLAN), a
// dependency not finished (WAIT), an effect already done (DEDUPLICATE), the
// same effect under way (WAIT), a stale read (REPLAN), another writer holding
// the thing (WAIT), and a rule asking for a person. In watch-only mode every one of
// them is a WOULD_* decision, limits included (kernel.go: a control's own
// ENFORCE mode doesn't override the workspace's).

const WRITE = new Set(['WRITE', 'CREATE', 'DELETE']);

export function standIn(mode) {
  const calls = [];
  const work = new Map();            // id -> { actor_id, status }
  const deps = [];                   // { source, target }
  const committed = new Set();       // idempotency and effect keys whose effect happened
  const activeEffect = new Map();    // effect key -> action id
  const held = new Map();            // resource -> action id
  const version = new Map();         // resource -> current version
  const limits = new Map();          // id -> limit
  const running = new Map();         // action id -> operation (admitted, no result yet)
  const actions = new Map();         // action id -> check body
  const approvals = new Map();
  const asked = new Map();
  const rules = [];
  const verifiers = new Set();
  const evidence = [];
  const costs = [];
  const ref = (r) => `${r.namespace ?? ''}:${r.type}:${r.external_id}`;

  // The kernel's order (kernel.go): limits, holds and dependencies, duplicates,
  // stale reads, conflicts, then policy.
  // internal/resource/identity.go: a write with no effect key is identified by
  // its operation, resources and whole arguments, so the same change twice is
  // the same effect, whenever it comes.
  const keyOf = (b) => {
    if (b.effect_key) return b.effect_key;
    if (!(b.resources ?? []).some((r) => WRITE.has(r.mode))) return null;
    return `derived:${b.operation}|${(b.resources ?? []).map(ref).sort().join(',')}|${JSON.stringify(b.arguments ?? {})}`;
  };
  const decide = (b) => {
    b = { ...b, effect_key: keyOf(b) };
    for (const l of limits.values()) {
      const n = [...running.entries()].filter(([id, op]) => op === l.scope.operation && id !== b.action_id).length;
      if (l.scope.operation === b.operation && l.max_in_flight != null && n >= l.max_in_flight) return { decision: 'WAIT', reason_code: 'CONCURRENCY_LIMIT', retry_after_ms: 300 };
    }
    const w = work.get(b.work_id);
    if (w && (w.status === 'SUPERSEDED' || w.status === 'CANCELLED')) return { decision: 'REPLAN', reason_code: `WORK_${w.status}` };
    if (deps.some((d) => d.source === b.work_id && work.get(d.target)?.status !== 'COMPLETED')) return { decision: 'WAIT', reason_code: 'DEPENDENCY_INCOMPLETE', retry_after_ms: 300 };
    if ((b.idempotency_key && committed.has(`i:${b.idempotency_key}`)) || (b.effect_key && committed.has(`e:${b.effect_key}`))) return { decision: 'DEDUPLICATE', reason_code: 'EXACT_EFFECT_ALREADY_COMMITTED' };
    if (b.effect_key && activeEffect.has(b.effect_key) && activeEffect.get(b.effect_key) !== b.action_id) return { decision: 'WAIT', reason_code: 'EXACT_DUPLICATE_ACTIVE', retry_after_ms: 300 };
    for (const r of b.resources ?? []) {
      if (r.observed_version && version.has(ref(r)) && version.get(ref(r)) !== r.observed_version) return { decision: 'REPLAN', reason_code: 'STALE_OBSERVATION' };
    }
    for (const r of b.resources ?? []) {
      const h = held.get(ref(r));
      if (WRITE.has(r.mode) && h && h !== b.action_id) return { decision: 'WAIT', reason_code: 'LEASE_BUSY', retry_after_ms: 300 };
    }
    const amount = b.arguments?.amount ?? 0;
    if (rules.some((x) => x.operation === b.operation && amount > x.numeric_threshold)) {
      const ap = asked.get(b.action_id);
      if (!ap) {
        const id = `ap_${asked.size + 1}`;
        asked.set(b.action_id, id);
        approvals.set(id, 1);
        return { decision: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_REQUIRED', approval_id: id };
      }
      if (approvals.get(ap) > 0) return { decision: 'REQUIRE_APPROVAL', reason_code: 'APPROVAL_PENDING', approval_id: ap };
    }
    return { decision: 'ALLOW', reason_code: 'NO_CONFLICT' };
  };

  const admit = (b) => {
    for (const r of b.resources ?? []) if (WRITE.has(r.mode)) held.set(ref(r), b.action_id);
    const key = keyOf(b);
    if (key) activeEffect.set(key, b.action_id);
    running.set(b.action_id, b.operation);
  };

  const fetchFn = async (url, init = {}) => {
    const path = url.replace('http://lumni.test', '');
    const body = init.body ? JSON.parse(init.body) : undefined;
    const auth = init.headers?.Authorization;
    calls.push({ method: init.method, path, body, auth });
    const ok = (data, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } });
    const bad = (status, code, message) => ok({ error: { code, message } }, status);
    if (auth === 'Bearer bad') return bad(401, 'UNAUTHENTICATED', 'unknown key');
    const isAdmin = auth === 'Bearer admin';
    const m = (re) => re.exec(path);
    let x;

    if (path === '/v1/work' && init.method === 'POST') {
      if (!work.has(body.id)) work.set(body.id, { actor_id: body.actor_id, status: 'RUNNING' });
      return ok({ id: body.id, ...work.get(body.id), goal: body.goal }, 201);
    }
    if ((x = m(/^\/v1\/work\/(.+)\/(complete|supersede|fail|cancel)$/))) {
      const w = work.get(decodeURIComponent(x[1]));
      if (w) w.status = { complete: 'COMPLETED', supersede: 'SUPERSEDED', fail: 'FAILED', cancel: 'CANCELLED' }[x[2]];
      return ok({ status: w?.status });
    }
    if (path === '/v1/dependencies') { deps.push({ source: body.source_id, target: body.target_id }); return ok({ created: true }, 201); }
    if (path === '/v1/config/policy-rules') { rules.push(body); return ok({ id: 'pr_1' }, 201); }
    if ((x = m(/^\/v1\/config\/limits\/(.+)$/))) {
      if (!isAdmin) return bad(403, 'SCOPE_REQUIRED', 'Required scope: admin');
      limits.set(x[1], body);
      return ok({ id: x[1], ...body });
    }
    if (path === '/v1/verifiers') {
      if (!isAdmin) return bad(403, 'SCOPE_REQUIRED', 'Required scope: admin');
      verifiers.add(body.id);
      return ok(body, 201);
    }
    if ((x = m(/^\/v1\/outcomes\/(.+)\/evidence$/))) {
      if (!isAdmin) return bad(403, 'SCOPE_REQUIRED', 'Required scope: verification');
      const action = actions.get(decodeURIComponent(x[1]).replace(/^out_/, ''));
      if (!action?.verification) return bad(400, 'INVALID', 'an authoritative verifier is required to record evidence');
      evidence.push({ action: action.action_id, ...body });
      return ok({ recorded: true }, 202);
    }
    if (path === '/v1/costs') { costs.push(body); return ok({ id: body.id }, 201); }
    if (path === '/v1/resources/resolve') return ok({ id: ref(body) }, 201);
    if ((x = m(/^\/v1\/resources\/(.+)\/version$/))) { version.set(decodeURIComponent(x[1]), body.version); return ok({ updated: true }); }
    if ((x = m(/^\/v1\/approvals\/(.+)$/))) {
      const left = approvals.get(x[1]) ?? 0;
      approvals.set(x[1], left - 1);
      return ok({ id: x[1], status: left > 0 ? 'PENDING' : 'APPROVED' });
    }
    if (path === '/v1/actions/check') {
      if (body.verification && !verifiers.has(body.verification.verifier_id)) return bad(400, 'INVALID', 'verifier unavailable');
      actions.set(body.action_id, body);
      const d = decide(body);
      const goes = d.decision === 'ALLOW' || mode === 'SHADOW';
      if (goes) admit(body);
      const shown = mode === 'SHADOW' && d.decision !== 'ALLOW' ? { ...d, decision: `WOULD_${d.decision}` } : d;
      return ok({ action_id: body.action_id, ...shown, lease_token: goes ? `lt_${body.action_id}` : undefined });
    }
    if ((x = m(/^\/v1\/actions\/(.+)\/result$/))) {
      const id = decodeURIComponent(x[1]);
      const a = actions.get(id);
      for (const [k, v] of held) if (v === id) held.delete(k);
      running.delete(id);
      const key = a ? keyOf(a) : null;
      if (key && activeEffect.get(key) === id) activeEffect.delete(key);
      if (body.status === 'SUCCEEDED') {
        if (a?.idempotency_key) committed.add(`i:${a.idempotency_key}`);
        if (key) committed.add(`e:${key}`);
      }
      return ok({ status: body.status });
    }
    return ok({ items: [] }, init.method === 'POST' ? 201 : 200);
  };
  return { calls, fetchFn, evidence, costs, work };
}

