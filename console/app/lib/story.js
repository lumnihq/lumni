// The demo's one story, across four screens: from what a business wants to
// AIs doing it, checked, and cheaper the next time.
//
//   1 Business intent       /contracts  say it in plain words
//   2 Executable contract   /contracts  Lumni reads it into checks
//   3 Coordinate, execute   /live       three AIs on one order
//   4 Verify the outcome    /live       Stripe and Postgres, read back
//   5 Optimize future runs  /compile    the repeated refund check, as code
//
// A page joins the story with ?story=<step>, and /live is always in it in the
// demo. The strip under the top bar says where you are, what to say, and links
// to the step before and after. Demo only: hidden in a real workspace.

(() => {
  /** What the business asks for. /contracts reads this into the contract. */
  const INTENT = 'No more than one charge is permitted per order. No more than one refund is permitted per order. Every cancelled order must leave the charge refunded in Stripe within 10 minutes.';

  const STEPS = [
    { id: 'intent', title: 'Business intent', href: '/contracts?data=mock&story=intent',
      say: 'The business says what it needs, in plain words: charge each order once, refund it at most once, and a cancelled order ends refunded.' },
    { id: 'contract', title: 'Executable contract', href: '/contracts?data=mock&story=contract',
      say: 'Lumni reads that into explicit checks: what must hold before each action, what to read back after it, and a test for every miss.' },
    { id: 'execute', title: 'Coordinate and execute', href: '/live?data=mock&scene=payment&story=execute',
      say: 'Three AIs work the same order at once. Lumni decides who runs, who waits and who looks again, even when Stripe goes quiet halfway.' },
    { id: 'verify', title: 'Verify the outcome', href: '/live?data=mock&scene=payment&shot&story=verify',
      say: 'Lumni reads Stripe and Postgres back. The outcome is what the systems say happened, not what an AI reported.' },
    { id: 'optimize', title: 'Optimize future runs', href: '/compile?data=mock&path=refund-eligibility&story=optimize',
      say: 'The refund check runs on every cancelled order, so Lumni turns it into code, checked against past runs. Anything off the path still goes to the LLM.' },
  ];

  const step = (id) => STEPS.find((s) => s.id === id) ?? null;

  const ICON = {
    tick: '<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8.5 3 3 7-7"/></svg>',
    next: '<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 3 5 5-5 5"/></svg>',
    back: '<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m10 3-5 5 5 5"/></svg>',
  };

  const CSS = `
  .story { position: relative; z-index: 5; padding: 14px 40px 0; }
  .story-in { max-width: 1360px; margin: 0 auto; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 22px; padding: 12px 14px 12px 18px; border-radius: 18px; background: var(--ink); color: #fff; box-shadow: 0 18px 40px -22px rgba(13,14,20,.6); }
  .story-t { font-size: 12px; font-weight: 650; color: rgba(255,255,255,.55); line-height: 1.3; }
  .story-t b { display: block; font-size: 14px; font-weight: 750; color: #fff; letter-spacing: -0.01em; }
  .story ol { list-style: none; display: flex; align-items: center; min-width: 0; }
  .story li { display: flex; align-items: center; flex: 1 1 auto; }
  .story li a { flex: none; }
  .story li:not(:last-child)::after { content: ''; flex: 1; height: 2px; margin: 0 8px; min-width: 12px; border-radius: 2px; background: rgba(255,255,255,.14); }
  .story li.done:not(:last-child)::after { background: var(--accent-2); }
  .story li a { display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 12px 0 5px; border-radius: 999px; font-size: 13px; font-weight: 650; color: rgba(255,255,255,.6); white-space: nowrap; transition: background .2s, color .2s; }
  .story li a:hover { color: #fff; background: rgba(255,255,255,.08); }
  .story li i { flex: none; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; font: 750 11.5px var(--sans); font-style: normal; background: rgba(255,255,255,.1); color: rgba(255,255,255,.75); }
  .story li.done i { background: var(--accent-2); color: #fff; }
  .story li.on a { background: #fff; color: var(--ink); }
  .story li.on i { background: var(--accent); color: #fff; box-shadow: 0 0 0 4px rgba(133,120,255,.3); }
  .story-go { display: flex; gap: 6px; }
  .story-go a { display: inline-flex; align-items: center; gap: 6px; height: 34px; padding: 0 12px; border-radius: 10px; font-size: 13px; font-weight: 650; color: rgba(255,255,255,.75); box-shadow: inset 0 0 0 1px rgba(255,255,255,.18); white-space: nowrap; }
  .story-go a:hover { color: #fff; box-shadow: inset 0 0 0 1px rgba(255,255,255,.4); }
  .story-go a.primary { background: var(--accent); color: #fff; box-shadow: none; }
  .story-go a.primary:hover { filter: brightness(1.1); }
  .story-say { max-width: 1360px; margin: 10px auto 0; display: flex; align-items: center; gap: 10px; font-size: 14.5px; color: var(--ink2); }
  .story-say b { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--accent-soft); color: var(--accent); font-size: 12px; font-weight: 700; }
  body[data-mode='real'] .story { display: none; }
  @media (max-width: 1500px) { .story li a span { display: none; } .story li.on a span { display: inline; } }`;

  /** The strip, as HTML. Pure, so tests can read it. */
  function html(id) {
    const at = STEPS.findIndex((s) => s.id === id);
    if (at < 0) return '';
    const prev = STEPS[at - 1];
    const next = STEPS[at + 1];
    return `<div class="story-in">
      <div class="story-t">The story<b>From intent to execution</b></div>
      <ol>${STEPS.map((s, i) => `<li class="${i < at ? 'done' : i === at ? 'on' : ''}"><a href="${s.href}" title="${s.title}"><i>${i < at ? ICON.tick : i + 1}</i><span>${s.title}</span></a></li>`).join('')}</ol>
      <div class="story-go">
        ${prev ? `<a href="${prev.href}">${ICON.back}Back</a>` : ''}
        ${next ? `<a class="primary" href="${next.href}">Next: ${next.title}${ICON.next}</a>` : `<a class="primary" href="${STEPS[0].href}">Start again</a>`}
      </div>
    </div>
    <div class="story-say"><b>Step ${at + 1} of ${STEPS.length}</b>${STEPS[at].say}</div>`;
  }

  /** Puts the strip under the top bar. `fallback` is the step when the URL names none. */
  function mount(fallback) {
    const id = new URLSearchParams(location.search).get('story') ?? fallback;
    if (!step(id)) return null;
    if (!document.getElementById('story-css')) {
      const st = document.createElement('style');
      st.id = 'story-css';
      st.textContent = CSS;
      document.head.append(st);
    }
    let el = document.querySelector('.story');
    if (!el) {
      el = document.createElement('div');
      el.className = 'story';
      document.getElementById('top')?.after(el);
    }
    el.innerHTML = html(id);
    document.body.dataset.story = id;
    return id;
  }

  globalThis.Story = { INTENT, STEPS, step, html, mount };
})();
