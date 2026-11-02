// Shared by /compiled and /compile: the demo's state in this browser, status
// chips, code colouring, and the few motions both screens use. Unlike
// compiler.js this one needs the DOM.

(() => {
  const C = window.Compiler;
  const q = new URLSearchParams(location.search);
  // Reduced motion and screenshots get the end state straight away.
  const calm = (matchMedia('(prefers-reduced-motion: reduce)').matches || q.has('shot')) && !q.has('motion');
  const esc = (s) => Shell.esc(s);
  if (q.has('motion')) document.documentElement.classList.add('motion');

  const storeKey = () => `lumni.compiled.v1.${Shell.key}`;
  function load() {
    try {
      const v = JSON.parse(localStorage.getItem(storeKey()) ?? 'null');
      if (v?.paths) {
        const s = C.seed();
        for (const id in s.paths) v.paths[id] ??= s.paths[id];
        return v;
      }
    } catch {}
    return C.seed();
  }
  const save = (st) => { try { localStorage.setItem(storeKey(), JSON.stringify(st)); } catch {} };
  const reset = () => { try { localStorage.removeItem(storeKey()); } catch {} };

  const LABEL = { live: 'Live', shadow: 'In shadow', candidate: 'To review', drifted: 'Drifted', paused: 'Paused', watching: 'Watching', retired: 'Retired', 'rolled-back': 'Rolled back' };
  const chip = (s, extra = '') => `<span class="cs ${s}">${LABEL[s] ?? s}${extra}</span>`;
  const ms = (n) => (n >= 1000 ? `${(n / 1000).toFixed(1)}s` : `${Math.round(n)}ms`);
  const num = (n) => (window.fmtN ? window.fmtN(n) : Math.round(n).toLocaleString('en-US'));
  const usd = (n) => (n >= 1000 ? `$${num(n)}` : n >= 10 ? `$${Math.round(n)}` : `$${n.toFixed(n < 0.1 ? 3 : 2)}`);
  const pct = (x, d = 0) => `${(x * 100).toFixed(d)}%`;

  /** Ease out over `dur` ms, calling f(0…1). Calm calls f(1) at once. */
  function tween(dur, f, done) {
    if (calm) { f(1); done?.(); return; }
    const t0 = performance.now();
    const step = (now) => {
      const k = Math.min(1, (now - t0) / dur);
      f(1 - (1 - k) ** 3);
      if (k < 1) requestAnimationFrame(step);
      else done?.();
    };
    requestAnimationFrame(step);
  }
  const countTo = (el, from, to, show, dur = 900) => el && tween(dur, (k) => { el.textContent = show(from + (to - from) * k); });

  /** Run f once, the first time el is a quarter on screen. */
  function onSeen(el, f) {
    if (!el) return;
    if (calm || !('IntersectionObserver' in window)) return f();
    const io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) { io.disconnect(); f(); } }, { threshold: 0.25 });
    io.observe(el);
  }

  /**
   * Call spawn every `every` ms while el is on the page and the tab is
   * showing. Calm never starts it. Returns a stop function.
   */
  function loop(el, every, spawn) {
    if (calm) return () => {};
    let h = setInterval(() => {
      if (!el.isConnected) return stop();
      if (!document.hidden) spawn();
    }, every);
    const stop = () => { clearInterval(h); h = null; };
    return stop;
  }

  /**
   * One dot along points [[x, y, dwell?], ...] in px, inside box. A point's
   * dwell (0…1 of the run) holds the dot there, like an LLM thinking.
   */
  function dot(box, points, { dur = 1200, cls = '' } = {}) {
    const el = document.createElement('i');
    el.className = `fd ${cls}`;
    box.append(el);
    // Each leg between points weighs 1; a dwell adds its share of the run.
    const legs = points.length - 1;
    const total = legs + points.reduce((t, p) => t + (p[2] ?? 0) * legs, 0);
    const frames = [];
    let at = 0;
    points.forEach((p, i) => {
      if (i) at += 1;
      const transform = `translate(${p[0]}px, ${p[1]}px)`;
      frames.push({ transform, offset: at / total, opacity: 1 });
      if (p[2]) { at += p[2] * legs; frames.push({ transform, offset: at / total, opacity: 1 }); }
    });
    frames[0].opacity = 0;
    frames.at(-1).opacity = 0;
    const a = el.animate(frames, { duration: dur, easing: 'linear', fill: 'both' });
    a.onfinish = () => el.remove();
    return el;
  }

  /**
   * Slide elements with data-v from where they were to where they are now.
   * Take `before` with rects(root) before a redraw, then call flip(root, before).
   */
  const rects = (root) => new Map([...root.querySelectorAll('[data-v]')].map((el) => [el.dataset.v, el.getBoundingClientRect()]));
  function flip(root, before) {
    if (calm) return;
    for (const el of root.querySelectorAll('[data-v]')) {
      const was = before.get(el.dataset.v);
      const now = el.getBoundingClientRect();
      if (!was) { el.animate([{ opacity: 0, transform: 'scale(.6)' }, { opacity: 1, transform: 'none' }], { duration: 480, easing: 'cubic-bezier(0.34, 1.56, 0.64, 1)', delay: 260 }); continue; }
      const dx = was.left - now.left, dy = was.top - now.top;
      if (Math.abs(dx) + Math.abs(dy) < 1) continue;
      el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }], { duration: 620, easing: 'cubic-bezier(0.2, 0.8, 0.2, 1)' });
    }
  }

  /** Program source to HTML: keywords, strings, the fallback, and the fields it reads. */
  function code(line, fields = [], drift = []) {
    let h = esc(line);
    const keep = [];
    // One private-use character per held piece, so later passes can't touch it.
    const hold = (s) => String.fromCharCode(0xe000 + keep.push(s) - 1);
    h = h.replace(/(&#39;[^&]*?&#39;|`[^`]*`)/g, (m) => hold(`<span class="cs-s">${m}</span>`));
    h = h.replace(/\/\/.*$/, (m) => hold(`<span class="cs-c">${m}</span>`));
    h = h.replace(/\bfallback\(/g, () => hold('<span class="cs-fb">fallback</span>('));
    for (const f of [...fields].sort((a, b) => b.length - a.length)) {
      const last = f.split('.').at(-1);
      const re = new RegExp(`(?<=[.{ ]|&#39;)(${last.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})(?=[ .,):}]|&#39;|$)`, 'g');
      h = h.replace(re, (m) => hold(`<span class="gf ${drift.includes(f) ? 'drift' : ''}" title="${drift.includes(f) ? 'This field changed in the app' : 'Read from the app’s live schema'}">${m}</span>`));
    }
    h = h.replace(/\b(export|function|const|return|if|await|new)\b/g, '<span class="cs-k">$1</span>');
    h = h.replace(/\b(\d[\d_]*)\b/g, '<span class="cs-n">$1</span>');
    return h.replace(/[\ue000-\uefff]/g, (c) => keep[c.charCodeAt(0) - 0xe000]);
  }

  const I = {
    code: '<svg width="14" height="14" viewBox="0 0 16 16"><path d="M5.5 4 2 8l3.5 4M10.5 4 14 8l-3.5 4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    spark: '<svg width="13" height="13" viewBox="0 0 16 16"><path d="M8 1.5 9.4 6.6 14.5 8 9.4 9.4 8 14.5 6.6 9.4 1.5 8 6.6 6.6z" fill="currentColor"/></svg>',
    tick: '<svg width="13" height="13" viewBox="0 0 14 14"><path d="M3 7.4 5.8 10 11 4.2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    x: '<svg width="11" height="11" viewBox="0 0 12 12"><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg>',
    arrow: '<svg width="14" height="14" viewBox="0 0 16 16"><path d="M3 8h10M9 4l4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    back: '<svg width="14" height="14" viewBox="0 0 16 16"><path d="M13 8H3M7 4 3 8l4 4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    search: '<svg width="15" height="15" viewBox="0 0 16 16"><circle cx="7" cy="7" r="4.8" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M10.6 10.6 14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    play: '<svg width="12" height="12" viewBox="0 0 12 12"><path d="M3 2v8l7-4z" fill="currentColor"/></svg>',
    pause: '<svg width="12" height="12" viewBox="0 0 12 12"><path d="M3 2h2v8H3zM7 2h2v8H7z" fill="currentColor"/></svg>',
    undo: '<svg width="13" height="13" viewBox="0 0 16 16"><path d="M4 6h6a3.5 3.5 0 0 1 0 7H6M4 6l3-3M4 6l3 3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    redo: '<svg width="13" height="13" viewBox="0 0 16 16"><path d="M13.5 8A5.5 5.5 0 1 1 11.9 4.1M13.5 2.5v3h-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    warn: '<svg width="14" height="14" viewBox="0 0 16 16"><path d="M8 2 14.5 13.5h-13z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M8 6.5v3M8 11.6v.01" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg>',
    bot: '<svg width="14" height="14" viewBox="0 0 20 20"><rect x="3.5" y="6" width="13" height="10" rx="3" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M10 3v3M7.5 11h.01M12.5 11h.01" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>',
  };

  window.CompiledUI = { calm, load, save, reset, LABEL, chip, ms, num, usd, pct, tween, countTo, onSeen, loop, dot, rects, flip, code, I };
})();
