// Compiled paths on your workspace's real data: /compiled and /compile read
// and change them through the Go API (/v1/compiled-paths, /v1/config/compiler,
// /v1/jobs), which the console's server passes straight through.
//
//   PathsReal.list(main)        // the list, today's totals, what Lumni noticed
//   PathsReal.detail(main, id)  // one path: program, replay, versions, drift
//
// expr, lines and model are DOM-free, so the tests can read them. Numbers the
// API doesn't have stay a dash; nothing is borrowed from the demo.

(() => {
  const OPS = { eq: '===', ne: '!==', lt: '<', lte: '<=', gt: '>', gte: '>=', and: '&&', or: '||', add: '+', sub: '-', mul: '*', div: '/' };

  /** One expression as a line of code. */
  function expr(e) {
    if (!e || typeof e !== 'object') return 'null';
    if (e.ref) return e.ref.replace(/^\/(input|vars)\/?/, (_, k) => (k === 'input' ? 'input.' : '')).replace(/\//g, '.').replace(/\.$/, '') || 'input';
    if (e.op) {
      const a = (e.args ?? []).map(expr);
      if (OPS[e.op]) return a.length === 2 ? `(${a[0]} ${OPS[e.op]} ${a[1]})` : `${e.op}(${a.join(', ')})`;
      if (e.op === 'not') return `!${a[0]}`;
      return `${e.op}(${a.join(', ')})`;
    }
    if (e.object) return `{ ${Object.entries(e.object).map(([k, v]) => `${k}: ${expr(v)}`).join(', ')} }`;
    if ('literal' in e) return JSON.stringify(e.literal);
    return 'null';
  }

  /** A path-v1 program as readable lines: one or two per node, in order. */
  function lines(program) {
    const nodes = program?.nodes ?? [];
    const out = [{ kind: 'head', text: 'function path(input) {' }];
    for (const n of nodes) {
      const at = (t) => out.push({ kind: n.kind, id: n.id, text: `  ${t}` });
      const tool = `${n.binding_id}@v${n.binding_version}`;
      if (n.kind === 'let') at(`const ${n.id} = ${expr(n.value)}`);
      else if (n.kind === 'read') at(`const ${n.id} = read(${JSON.stringify(tool)}, ${expr(n.value)})`);
      else if (n.kind === 'call') at(`const ${n.id} = call(${JSON.stringify(tool)}, ${expr(n.value)})`);
      else if (n.kind === 'guard') at(`if (!${expr(n.value)}) goto ${n.otherwise}`);
      else if (n.kind === 'fallback') at(`${n.id}: return fallback(${JSON.stringify(n.reason ?? '')})`);
      else if (n.kind === 'return') at(`return ${expr(n.value)}`);
      else at(`// ${n.kind} ${n.id}`);
    }
    out.push({ kind: 'head', text: '}' });
    return out;
  }

  const micros = (n) => (n == null ? null : n / 1e6);
  /** An API path as the page needs it. Unknowns stay null. */
  function model(p) {
    const vs = [...(p.versions ?? [])].sort((a, b) => a.v - b.v);
    const st = p.stats ?? {};
    const runs = st.runs ?? 0;
    return {
      id: p.id, name: p.name, agent: p.actor_id, operation: p.operation, revision: p.revision ?? 1,
      status: p.status ?? 'watching', can: p.can ?? [], compiling: !!p.compiling,
      paused: p.paused ? { by: p.paused_by ?? 'someone', why: p.pause_reason ?? '' } : null,
      versions: vs,
      live: vs.find((v) => v.status === 'live') ?? null,
      next: vs.find((v) => v.status === 'shadow' || v.status === 'candidate') ?? null,
      retired: vs.filter((v) => v.status === 'retired').pop() ?? null,
      drift: p.drift ?? [],
      runs, served: st.served ?? 0, llm: st.llm ?? 0, fallback: st.fallback ?? 0,
      hit: runs ? (st.served ?? 0) / runs : null,
      msAfter: st.duration_ms ?? null, msBefore: st.ms_before ?? null,
      saved: micros(st.estimated_savings_micros_usd), callsSaved: st.calls_saved ?? null, timeSaved: st.time_saved_ms ?? null,
      estimated: !!st.comparison_is_estimated,
      week: (p.week ?? []).map((d) => d.served ?? 0),
      traces: p.traces ?? null,
    };
  }

  window.PathsReal = { expr, lines, model };
  if (typeof document === 'undefined' || !window.CompiledUI) return;

  // ─── the pages ──────────────────────────────────────

  const U = window.CompiledUI;
  const { I, chip, ms, num, usd, pct } = U;
  const safe = (s) => Shell.esc(s);
  const api = Shell.api;
  const dash = '—';
  const enc = encodeURIComponent;
  const tz = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const fail = (e) => Shell.toast(e?.message ?? 'Something went wrong.', 'bad');
  const ago = (t) => { const d = Math.floor((Date.now() - Date.parse(t)) / 864e5); return d <= 0 ? 'today' : d === 1 ? 'yesterday' : `${d} days ago`; };

  /** Wait for a compile or discover job, then call done. */
  async function follow(job, done) {
    for (let i = 0; i < 120; i++) {
      await new Promise((r) => setTimeout(r, i < 5 ? 1000 : 3000));
      let j;
      try { j = await api.get(`/v1/jobs/${enc(job.id)}`); } catch (e) { return fail(e); }
      if (j.status === 'COMPLETED') return done(j);
      if (j.status === 'FAILED') return Shell.toast(`It didn’t finish: ${j.error ?? 'the job failed'}.`, 'bad');
    }
    Shell.toast('Still working. Come back in a few minutes.');
  }

  // The compiler switch: off on the server, off for this workspace, or on.
  function compilerBox(cfg) {
    if (!cfg) return '';
    if (!cfg.server_configured) return `<div class="banner"><span><b>Compiling is off on this server.</b> Set <code>LUMNI_OPENROUTER_API_KEY</code> and <code>LUMNI_COMPILER_MODEL</code> for the API and worker to turn it on. Everything already compiled keeps working.</span></div>`;
    if (!cfg.enabled) return `<div class="banner"><span><b>Compiling is off for this workspace.</b> Turning it on sends recorded traces, with credentials removed, to the configured model to write code.</span>
      <button class="btn2 accent" data-compiler="on">${I.code}Turn on compiling</button></div>`;
    return '';
  }

  async function list(main) {
    main.innerHTML = '<div class="quiet-note">Loading compiled paths…</div>';
    const q = `tz=${enc(tz())}`;
    let paths, totals, sugg, cfg;
    try {
      [paths, totals, sugg, cfg] = await Promise.all([
        api.get('/v1/compiled-paths?limit=200'),
        api.get(`/v1/compiled-paths/totals?${q}`),
        api.get('/v1/compiled-paths/suggestions?limit=24'),
        api.get('/v1/config/compiler').catch(() => null),
      ]);
    } catch (e) {
      main.innerHTML = `<div class="empty"><span class="ic">${I.warn}</span><h2>Compiled paths couldn’t load</h2><p>${safe(e.message)}</p></div>`;
      return;
    }
    const xs = paths.items.map(model);
    const ss = sugg.items.map(model);
    const t = model({ stats: totals });
    const on = cfg?.server_configured && cfg?.enabled;
    const head = `<section class="head in"><div><div class="eyebrow">${safe(Shell.session?.tenant?.name ?? 'Your workspace')}</div>
      <h1>Compiled paths</h1><p>The steps your agents reason through again and again, turned into code they call instead. The LLM still takes anything off the path.</p></div>
      <div class="acts">${on ? `<button class="btn2" data-discover>${I.spark}Look for repeats</button>` : ''}</div></section>`;
    if (!xs.length && !ss.length) {
      main.innerHTML = `${compilerBox(cfg)}${head}<div class="empty"><span class="ic">${I.code}</span><h2>Nothing compiled yet</h2>
        <p>Send complete traces with <code>POST /v1/traces</code>. Once an agent repeats the same steps, Lumni suggests a path here; compile it to check code on past runs. Nothing answers until a person promotes it.</p>
        <div class="acts2">${on ? `<button class="btn2 accent" data-discover>${I.spark}Look for repeats now</button>` : ''}<a class="btn2" href="?data=mock">See it in the demo</a></div></div>`;
      return;
    }
    const hours = t.timeSaved == null ? null : t.timeSaved / 3600e3;
    main.innerHTML = `${compilerBox(cfg)}${head}
      <section class="day"><div class="day-h"><h2>Last 7 days, added up</h2><p>${num(t.runs)} runs on ${xs.length} compiled ${xs.length === 1 ? 'path' : 'paths'}</p></div>
        <div class="results" style="grid-template-columns:repeat(4,minmax(0,1fr))">
          <div><b>${num(t.served)}</b><span>answered by code</span><em>${t.runs ? pct(t.served / t.runs) : dash} of runs</em></div>
          <div><b>${t.callsSaved == null ? dash : num(t.callsSaved)}</b><span>LLM calls not made</span><em>${t.estimated ? 'estimated from recorded traces' : 'measured'}</em></div>
          <div><b>${hours == null ? dash : hours >= 10 ? num(hours) : hours.toFixed(1)}${hours == null ? '' : '<small>hours</small>'}</b><span>of agent time saved</span><em>${t.msAfter != null && t.msBefore != null ? `${ms(t.msAfter)} a run instead of ${ms(t.msBefore)}` : 'once runs are compared'}</em></div>
          <div><b>${t.saved == null ? dash : usd(t.saved)}</b><span>saved</span><em>${t.saved == null ? 'needs recorded prices' : t.estimated ? 'estimated' : 'measured'}</em></div>
        </div></section>
      ${xs.length ? `<div class="cols"><span>Path</span><span>Who answers</span><span>Typical time</span><span>Saved today</span><span>Code, 7 days</span><span></span></div>
        <div class="list">${xs.map(row).join('')}</div>` : ''}
      ${ss.length ? `<section class="noticed"><h2>${I.spark}Lumni noticed</h2><p>Seen again and again in your traces. Compile one to check it on past runs; nothing changes until a person promotes it.</p>
        <div class="cards">${ss.map(card(on)).join('')}</div></section>` : ''}`;
  }

  function row(x, i) {
    const answering = x.status === 'live' || x.status === 'drifted';
    const max = Math.max(1, ...x.week);
    let meta = `${safe(x.agent)}, ${safe(x.operation)}`;
    if (x.status === 'paused' && x.paused) meta = `Paused by ${safe(x.paused.by)}. ${safe(x.paused.why)}`;
    const hit = answering && x.hit != null
      ? `<div class="lbl"><span>${pct(x.hit)} code</span><span>${pct(1 - x.hit)} LLM</span></div><div class="split"><b style="width:${x.hit * 100}%"></b><i style="flex:1"></i></div>`
      : `<div class="lbl off"><span>${answering ? 'No runs today' : x.status === 'shadow' ? 'Comparing, not answering' : x.status === 'paused' ? 'Paused' : 'Not running yet'}</span><span>${answering ? '' : '100% LLM'}</span></div><div class="split ${x.status === 'shadow' ? 'ghost' : ''}"></div>`;
    return `<a class="row ${answering ? '' : 'off'} ${x.status === 'paused' ? 'paused' : ''}" href="/compile?path=${enc(x.id)}" style="animation-delay:${0.08 + i * 0.05}s">
      <div class="who"><b>${safe(x.name)}${chip(x.status)}</b><small>${meta}</small></div>
      <div class="hit">${hit}</div>
      <div class="t">${x.msAfter != null ? `<b>${x.msBefore != null ? `<s>${ms(x.msBefore)}</s>` : ''}<span>${ms(x.msAfter)}</span></b>` : `<b>${dash}</b>`}<small>${num(x.runs)} runs today</small></div>
      <div class="sv"><b>${x.saved == null ? dash : usd(x.saved)}</b><small>${num(x.served)} runs as code</small></div>
      <div class="spark">${x.week.map((d) => `<i class="${d ? '' : 'z'}" style="height:${Math.max(2, (d / max) * 30)}px"></i>`).join('')}</div>
      <span class="go">${I.arrow}</span></a>`;
  }

  const card = (on) => (s, i) => `<article class="nc" data-id="${safe(s.id)}" data-rev="${s.revision}" style="animation-delay:${0.1 + i * 0.06}s">
    <div class="top2"><div><b>${safe(s.name)}</b><small>${safe(s.agent)}, ${safe(s.operation)}${s.traces != null ? `, ${num(s.traces)} traces` : ''}</small></div>${chip('watching')}</div>
    <div class="foot2">${on ? `<button class="btn2 accent" data-compile>${I.code}Compile</button><span class="hint">Checked on held-out past runs first</span>` : '<span class="hint">Turn on compiling to compile this.</span>'}
      <span class="checking">Writing code and checking it on past runs…</span><span class="prog-bar"><i></i></span></div></article>`;

  async function setCompiler(on, redraw) {
    try { await api.put('/v1/config/compiler', { enabled: on, cloud_processing_consent: on }); } catch (e) { return fail(e); }
    Shell.toast(on ? 'Compiling is on for this workspace.' : 'Compiling is off.');
    redraw();
  }

  async function compile(id, rev, verb, el, redraw) {
    el?.classList.add('compiling');
    let job;
    try { job = await api.post(`/v1/compiled-paths/${enc(id)}/${verb}`, { revision: rev }); } catch (e) { el?.classList.remove('compiling'); return fail({ message: why409(e) }); }
    Shell.toast('Writing code and checking it on past runs. This takes a minute or two.');
    follow(job, () => { Shell.toast('Compiled. Review it before it runs.'); redraw(); });
  }

  // ─── one path ───────────────────────────────────────

  const VERB = { compile: 'Compile', shadow: 'Run in shadow', promote: 'Promote', rollback: 'Roll back', pause: 'Pause', resume: 'Resume', recompile: 'Recompile' };
  const ICON = () => ({ promote: I.arrow, shadow: I.play, rollback: I.undo, pause: I.pause, resume: I.play, recompile: I.redo, compile: I.code });
  const D = { confirm: null, open: false };

  async function detail(main, id) {
    const back = `<a class="back" href="/compiled">${I.back}Compiled paths</a>`;
    if (!main.innerHTML) main.innerHTML = `${back}<div class="quiet-note">Loading…</div>`;
    let p, fb;
    try {
      [p, fb] = await Promise.all([
        api.get(`/v1/compiled-paths/${enc(id)}`),
        api.get(`/v1/compiled-paths/${enc(id)}/fallbacks?tz=${enc(tz())}`).catch(() => ({ items: [] })),
      ]);
    } catch (e) {
      main.innerHTML = `${back}<div class="empty"><h2>${e.status === 404 ? `No path called ${safe(id)}` : 'This path couldn’t load'}</h2><p>${e.status === 404 ? 'It may have been removed. Every compiled path is on the list.' : safe(e.message)}</p></div>`;
      return;
    }
    const x = model(p);
    const shown = x.next ?? x.live ?? x.versions.at(-1) ?? null;
    let rp = null;
    if (shown) rp = await api.get(`/v1/compiled-paths/${enc(id)}/versions/${shown.v}/replay`).catch(() => null);
    const redraw = () => detail(main, id);
    main.innerHTML = `${back}
      <section class="head in"><div><div class="eyebrow">${safe(x.agent)}, ${safe(x.operation)}, ${num(x.runs)} runs today</div>
        <h1>${safe(x.name)}${chip(x.status)}</h1><p>${summary(x)}</p></div>
        <div class="acts">${actions(x)}</div></section>
      ${D.confirm === 'promote' && x.next ? `<div class="confirm">${I.arrow}<span>Promote <b>v${x.next.v}</b>? It will answer runs on this path from now on.${rp?.rate != null ? ` It agreed with your agent on <b>${pct(rp.rate, 1)}</b> of ${num(rp.agree + rp.disagree)} past runs it could answer.` : ''}${x.live ? ` v${x.live.v} stays one click away.` : ''}</span>
        <button class="btn2" data-cancel>Not yet</button><button class="btn2 accent" data-yes>Promote v${x.next.v}</button></div>` : ''}
      ${D.confirm === 'pause' ? `<div class="confirm">${I.pause}<span>Pause it? Every run goes to the LLM until you resume.</span>
        <input id="why" placeholder="Why (optional)" maxlength="400" style="flex:1;height:34px;padding:0 10px;border-radius:9px;box-shadow:inset 0 0 0 1px var(--line2)" />
        <button class="btn2" data-cancel>Not yet</button><button class="btn2 accent" data-yes>Pause</button></div>` : ''}
      <section class="strip">${strip(x)}</section>
      <section class="blk"><h2>The program</h2><p>${shown ? `v${shown.v}, interpreted by Lumni, not run as JavaScript. Reads and checks come before anything that changes your apps.` : 'Not written yet.'}</p>
        ${shown ? `<div class="codec"><div class="body"><div class="blkc">${lines(shown.program).map((l, i) => `<div class="ln2"><span>${i + 1}</span><span style="font:12.5px/1.75 var(--mono);white-space:pre">${safe(l.text)}</span></div>`).join('')}</div></div></div>
          <p class="quiet-note">Hash <code>${safe(String(shown.program_hash ?? '').slice(0, 16))}</code></p>` : ''}</section>
      <section class="blk"><h2>Checked on past runs</h2><p>${rp ? `v${rp.version} on held-out runs and shadow traffic, compared with what your agent decided.${rp.complete ? '' : ' Still checking.'}` : 'Nothing checked yet.'}</p>
        ${rp ? `<div class="card">${replay(rp)}</div>` : ''}</section>
      <section class="blk"><h2>Versions</h2><p>Lumni writes and checks each version. A person decides which one answers.</p><div class="card">${versions(x)}</div></section>
      <section class="blk"><h2>Today</h2><p>Every run off the path still goes to the LLM.</p>
        <div class="today2"><div class="card"><h3>Why runs went to the LLM</h3>${fallbacks(fb.items ?? [])}</div><div class="card"><h3>What changed</h3>${drifts(x)}</div></div></section>`;
    main.onclick = async (e) => {
      const t = e.target;
      let b;
      if ((b = t.closest('[data-do]'))) return go(x, b.dataset.do, redraw);
      if (t.closest('[data-cancel]')) { D.confirm = null; return redraw(); }
      if (t.closest('[data-yes]')) { const v = D.confirm; const why = main.querySelector('#why')?.value ?? ''; D.confirm = null; return go(x, v, redraw, true, why); }
      if (t.closest('[data-dis]')) { D.open = !D.open; return main.querySelector('.dis')?.classList.toggle('open', D.open); }
      if ((b = t.closest('[data-label]'))) {
        const [cid, right] = b.dataset.label.split(':');
        try { await api.put(`/v1/compiled-paths/${enc(id)}/disagreements/${enc(cid)}/label`, { right }); } catch (err) { return fail(err); }
        Shell.toast('Labelled. It becomes a test for the next version.');
        return redraw();
      }
    };
  }

  async function go(x, verb, redraw, sure = false, why = '') {
    if ((verb === 'promote' || verb === 'pause') && !sure) { D.confirm = verb; return redraw(); }
    if (verb === 'compile' || verb === 'recompile') return compile(x.id, x.revision, verb, null, redraw);
    let p;
    try { p = await api.post(`/v1/compiled-paths/${enc(x.id)}/${verb}`, verb === 'pause' ? { revision: x.revision, why } : { revision: x.revision }); }
    catch (e) { fail({ message: why409(e) }); return redraw(); }
    const y = model(p ?? {});
    Shell.toast({
      promote: `v${y.live?.v ?? ''} is answering. The LLM still takes anything off the path.`,
      rollback: `Back on v${y.live?.v ?? ''}. The newer version is kept, marked rolled back.`,
      pause: 'Paused. Every run goes to the LLM until you resume.',
      resume: 'Answering again.',
      shadow: `v${y.next?.v ?? ''} is running beside the LLM, answering nothing.`,
    }[verb] ?? 'Done.');
    redraw();
  }

  /** The API's conflicts, in words. */
  const WHY = {
    'path revision mismatch': 'Someone else changed this path. Showing the latest.',
    'invalid lifecycle transition': 'That can’t happen from here any more. Showing the latest.',
    'held-out replay required': 'Not yet: checking on held-out past runs hasn’t finished.',
    'shadow evidence and reviewed disagreements required': 'Not yet: it needs at least one shadow run, and every disagreement labelled.',
    'binding or policy fingerprint changed': 'A tool or policy it uses has changed since it was compiled. Recompile first.',
    'read capability revoked': 'Lumni’s read access to a tool it uses was taken away.',
    'read capability required': 'It needs read access to a tool it uses.',
  };
  const why409 = (e) => (e?.status === 409 ? WHY[e.message] ?? e.message : e?.message ?? 'Something went wrong.');

  function summary(x) {
    if (x.compiling) return 'Lumni is writing code and checking it on past runs.';
    return {
      live: `Code answers ${x.hit == null ? 'runs on this path' : `${pct(x.hit)} of runs today`}; the rest go to the LLM.`,
      drifted: 'Something it reads has changed, so runs go back to the LLM. Recompile to catch up.',
      shadow: `v${x.next?.v} runs beside the LLM on live traffic and answers nothing yet. Promote it once you’re happy with how it compares.`,
      candidate: `Lumni compiled v${x.next?.v}. Review it, then run it in shadow.`,
      paused: `Paused by ${safe(x.paused?.by)}${x.paused?.why ? `: ${safe(x.paused.why)}` : '.'}`,
      watching: 'Lumni keeps seeing these steps. Compile them to check a program on past runs.',
    }[x.status] ?? '';
  }

  function actions(x) {
    if (x.compiling) return `<button class="btn2 busy" disabled>${I.code}Compiling…</button>`;
    const label = { promote: `Promote v${x.next?.v}`, shadow: `Run v${x.next?.v} in shadow`, rollback: `Roll back to v${x.retired?.v}` };
    const main = ['promote', 'shadow', 'compile', 'recompile', 'resume'];
    const icon = ICON();
    return x.can.map((k) => `<button class="btn2 ${main.includes(k) ? (k === 'recompile' ? 'warn' : 'accent') : ''}" data-do="${k}">${icon[k] ?? ''}${label[k] ?? VERB[k] ?? k}</button>`).join('');
  }

  function strip(x) {
    const tile = (l, v, s) => `<div class="tile"><div class="l">${l}</div><div class="v"><span>${v}</span></div><div class="s">${s}</div></div>`;
    return [
      tile('Runs today', num(x.runs), `${num(x.served)} by code, ${num(x.llm + x.fallback)} to the LLM`),
      tile('Typical time', x.msAfter == null ? dash : ms(x.msAfter), x.msBefore == null ? 'no recorded comparison yet' : `was ${ms(x.msBefore)}`),
      tile('LLM calls not made', x.callsSaved == null ? dash : num(x.callsSaved), x.estimated ? 'estimated from traces' : 'today'),
      tile('Saved today', x.saved == null ? dash : usd(x.saved), x.saved == null ? 'needs recorded prices' : x.estimated ? 'estimated' : 'measured'),
    ].join('');
  }

  function replay(r) {
    const bin = (k, n, l, s) => `<div class="bin ${k}"><div class="bh"><b>${num(n)}</b><span>${l}</span></div><div class="bs">${s}</div></div>`;
    const dis = (r.samples ?? []).filter((c) => c.result === 'disagree');
    return `<div class="bins">
        ${bin('a', r.agree, 'agree', r.rate == null ? 'of the runs it can answer' : `${pct(r.rate, 1)} of the runs it can answer`)}
        ${bin('d', r.disagree, 'disagree', dis.length ? 'each one is below, to label' : 'none to label')}
        ${bin('o', r.out, 'off the path', 'not the program’s to answer; they go to the LLM')}</div>
      ${dis.length ? `<div class="dis ${D.open ? 'open' : ''}"><button class="dis-t" data-dis>${D.open ? 'Hide' : 'See'} where they disagreed</button>
        <div class="more"><div>${dis.map((c) => `<div class="dr"><div><span class="k">Case</span><code>${safe(c.id)}</code></div>
          <div><span class="k">Trace</span><code>${safe(c.trace_id)}</code></div><div><span class="k">Found in</span>${safe(c.kind)}</div>
          <div class="why">${c.right ? `Labelled by ${safe(c.reviewed_by ?? 'someone')}.` : 'Not labelled yet. Promotion waits for every disagreement to be reviewed.'}</div>
          <div class="seg" role="tablist">${[['llm', 'Agent was right'], ['code', 'Program was right']].map(([k, l]) => `<button data-label="${safe(c.id)}:${k}" aria-selected="${c.right === k}">${l}</button>`).join('')}</div></div>`).join('')}</div></div></div>` : ''}`;
  }

  function versions(x) {
    const vc = (v) => `<div class="vc" data-v="${v.v}"><div class="t1"><b>v${v.v}</b>${chip(v.status)}</div><p>${safe(v.note || 'Compiled from recorded traces')}</p>
      <small>${v.promoted_by ? `Promoted by ${safe(v.promoted_by)}, ` : 'By Lumni, '}${ago(v.created_at)}</small></div>`;
    const lane = (k, title, list, sub, none) => `<div class="vl ${k}"><h4>${title}<small>${sub}</small></h4><div class="vs">${list.length ? list.map(vc).join('') : `<div class="emptyv">${none}</div>`}</div></div>`;
    const vs = x.versions;
    return `<div class="lanes">
      ${lane('retired', 'Before', vs.filter((v) => v.status === 'retired' || v.status === 'rolled-back').reverse(), 'kept, one click away', 'None yet.')}
      ${lane('live', 'Answering', vs.filter((v) => v.status === 'live'), x.paused ? 'paused' : 'runs on the program', 'Nothing yet. The LLM answers every run.')}
      ${lane('next', 'Next', vs.filter((v) => v.status === 'shadow' || v.status === 'candidate'), 'checked, not answering', 'Nothing waiting.')}</div>
      <div class="vfoot">${actions(x)}<span class="hint">${x.can.includes('promote') ? 'Promotion needs held-out replay, a shadow run and every disagreement labelled.' : x.can.includes('shadow') ? 'Shadow runs it beside the LLM on live traffic, answering nothing.' : ''}</span></div>`;
  }

  function fallbacks(items) {
    if (!items.length) return '<div class="quiet-note">No runs went to the LLM from this path today.</div>';
    const max = Math.max(1, ...items.map((r) => r.count));
    return items.map((r) => `<div class="fb"><div>${safe(String(r.reason).replace(/_/g, ' ').toLowerCase())}</div><div class="bar2"><i style="width:${(r.count / max) * 100}%"></i></div><div class="n">${num(r.count)}</div></div>`).join('');
  }

  function drifts(x) {
    if (!x.drift.length) return `<div class="dc ok"><span class="ic">${I.tick}</span><div><b>Nothing has changed</b><p>Lumni checks the tools, schemas and policies this path depends on. If one moves, runs go to the LLM until a new version is live.</p></div></div>`;
    return x.drift.map((d) => {
      const fixed = !d.open;
      return `<div class="dc ${fixed ? 'ok' : ''}"><span class="ic">${fixed ? I.tick : I.warn}</span><div><b>${safe(d.detail)}</b>
        <p>${fixed ? `Fixed${d.fixed_by ? ` by v${d.fixed_by}` : ''}.` : `Found ${ago(d.created_at)}${d.binding_id ? ` on ${safe(d.binding_id)}` : ''}.${d.fixed_by ? ` v${d.fixed_by} handles it.` : ''}`}</p>
        ${!fixed && x.can.includes('recompile') ? `<div class="da"><button class="btn2 warn" data-do="recompile">${I.redo}Recompile</button></div>` : ''}</div></div>`;
    }).join('');
  }

  /** Clicks on the list page. */
  function wireList(main) {
    const redraw = () => list(main);
    main.onclick = async (e) => {
      const t = e.target;
      let b;
      if ((b = t.closest('[data-compiler]'))) return setCompiler(b.dataset.compiler === 'on', redraw);
      if (t.closest('[data-discover]')) {
        let job;
        try { job = await api.post('/v1/compiled-paths/discover', {}); } catch (err) { return fail(err); }
        Shell.toast('Looking through recent traces for repeats…');
        return follow(job, () => { Shell.toast('Done. New suggestions are under “Lumni noticed”.'); redraw(); });
      }
      if ((b = t.closest('[data-compile]'))) { const c = b.closest('.nc'); return compile(c.dataset.id, Number(c.dataset.rev), 'compile', c, redraw); }
    };
  }

  window.PathsReal = { expr, lines, model, list: (main) => { wireList(main); return list(main); }, detail };
})();
