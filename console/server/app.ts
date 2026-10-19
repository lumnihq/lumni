// The console's pages and assets: one HTML file per screen at a clean path
// (/flow, /needs-you), and the shared shell and demo world under /lib. Used by
// serve.ts and by the screenshot script, so both answer exactly the same way.

/** Every screen, by its path. */
export const PAGES = [
  'flow', 'today', 'day-map', 'journey', 'runtime', 'context', 'tower', 'needs-you', 'fixes', 'autonomy',
  'money', 'incident', 'without', 'wrapped', 'share', 'sign-in', 'start', 'settings', 'device', 'privacy',
];

const TYPES: Record<string, string> = {
  html: 'text/html; charset=utf-8', js: 'text/javascript; charset=utf-8', css: 'text/css; charset=utf-8',
  svg: 'image/svg+xml', png: 'image/png', ico: 'image/x-icon', json: 'application/json',
};

export const SECURITY: Record<string, string> = {
  'x-content-type-options': 'nosniff',
  'referrer-policy': 'same-origin',
  'x-frame-options': 'DENY',
};

const INIT = `${import.meta.dir}/../scripts/lumni-init.mjs`;
const GUIDE = `${import.meta.dir}/../scripts/agent-guide.md`;

/**
 * Answers page and asset paths from `root`, or null for anything else. `/init`
 * is the onboarding command's script, with the API URL agents should use.
 */
export function appHandler(root: string, cache: 'no-cache' | 'long' = 'no-cache', opts: { publicApi?: string; publicConsole?: string } = {}) {
  return async (path: string, origin = ''): Promise<Response | null> => {
    if (path === '/init' || path === '/init.mjs') {
      const text = (await Bun.file(INIT).text()).replace("'__LUMNI_URL__'", JSON.stringify(opts.publicApi ?? 'http://localhost:8080')).replace("'__LUMNI_CONSOLE__'", JSON.stringify(opts.publicConsole || origin));
      return new Response(text, { headers: { 'content-type': 'text/javascript; charset=utf-8', 'cache-control': 'no-cache', ...SECURITY } });
    }
    // What a coding agent (Claude Code, Codex, Cursor) follows to set Lumni up in a project.
    if (path === '/init/guide.md') {
      const text = (await Bun.file(GUIDE).text()).replaceAll('__LUMNI_URL__', opts.publicApi ?? 'http://localhost:8080').replaceAll('__LUMNI_CONSOLE__', opts.publicConsole || origin);
      return new Response(text, { headers: { 'content-type': 'text/markdown; charset=utf-8', 'cache-control': 'no-cache', ...SECURITY } });
    }
    if (path === '/' || path === '/index.html') return new Response(null, { status: 302, headers: { location: '/today' } });
    const name = path.replace(/^\//, '').replace(/\.html$/, '');
    if (PAGES.includes(name)) {
      return new Response(Bun.file(`${root}/${name}.html`), { headers: { 'content-type': TYPES.html!, 'cache-control': 'no-cache', ...SECURITY } });
    }
    // Only plain names under the root: no dot segments, no hidden files.
    if (!/^\/(lib\/)?[\w-]+\.(js|css|svg|png|ico|json)$/.test(path)) return null;
    const f = Bun.file(`${root}${path}`);
    if (!(await f.exists())) return null;
    const ext = path.split('.').pop()!;
    return new Response(f, { headers: { 'content-type': TYPES[ext]!, 'cache-control': cache === 'long' ? 'public, max-age=3600' : 'no-cache', ...SECURITY } });
  };
}
