// What the console’s own server is told: which data to show by default, where
// the Go API is, and the admin key it signs in with. The key stays here. It is
// never sent to the browser, logged, or put in a response.

export type DataMode = 'mock' | 'real';

export interface Env {
  mode: DataMode;
  /** The Go API, e.g. http://localhost:8080. */
  api: string;
  /** The admin key the server exchanges for a browser session. Empty when unset. */
  adminKey: string;
  /** What to call the workspace, since the API doesn’t say. */
  workspace: string | null;
  /** Sign in as the admin on the first visit, without a click. */
  autoSignIn: boolean;
  /** Hosts the console answers to, so another site can’t borrow the session. */
  hosts: string[];
  /** What Lumni costs per checked action, for the return on Money. Unset hides it. */
  pricePerAction: number | null;
  /** The API URL agents use, when it differs from the one this server uses (e.g. inside Docker). */
  publicApi: string;
  /** Where people reach this console, for links the onboarding command prints. Empty: the address the request came to. */
  publicConsole: string;
  /**
   * How people sign in. `key`: with the admin key on this server, or a key
   * they paste (development). `accounts`: their own accounts, with roles,
   * invites and workspaces (production; needs `secret`).
   */
  auth: 'key' | 'accounts';
  /** Seals the keys accounts hold. Required for `accounts`. */
  secret: string;
  /** The accounts database file. */
  dbPath: string;
  /** Who may create an account: people with an invite, or anyone (a new workspace each; needs `platformKey`). */
  signup: 'invite' | 'open';
  /** Lets the console create workspaces on the API, for open sign-up. */
  platformKey: string;
  github: { id: string; secret: string } | null;
  google: { id: string; secret: string } | null;
}

function bool(v: string | undefined, fallback: boolean): boolean {
  if (v === undefined || v === '') return fallback;
  return !/^(0|false|no|off)$/i.test(v.trim());
}

/** LUMNI_CONSOLE_X, falling back to the older LUMNI_FRONTFACE_X so one .env serves both consoles. */
const pick = (vars: Record<string, string | undefined>, name: string) => vars[`LUMNI_CONSOLE_${name}`] ?? vars[`LUMNI_FRONTFACE_${name}`];

export function readEnv(vars: Record<string, string | undefined>): Env {
  const mode = (pick(vars, 'DATA_MODE') ?? '').trim().toLowerCase() === 'real' ? 'real' : 'mock';
  const port = vars.PORT ?? '5200';
  const extra = (pick(vars, 'HOSTS') ?? '').split(',').map((h) => h.trim()).filter(Boolean);
  return {
    mode,
    api: (vars.LUMNI_API_URL || 'http://localhost:8080').replace(/\/+$/, ''),
    adminKey: (pick(vars, 'TEST_ADMIN_KEY') ?? pick(vars, 'ADMIN_KEY') ?? '').trim(),
    workspace: pick(vars, 'WORKSPACE_NAME')?.trim() || null,
    autoSignIn: bool(pick(vars, 'AUTO_SIGN_IN'), true),
    hosts: [`localhost:${port}`, `127.0.0.1:${port}`, ...extra],
    publicApi: (pick(vars, 'PUBLIC_API_URL') || vars.LUMNI_API_URL || 'http://localhost:8080').replace(/\/+$/, ''),
    publicConsole: (pick(vars, 'PUBLIC_URL') || '').replace(/\/+$/, ''),
    auth: (pick(vars, 'AUTH') ?? '').trim().toLowerCase() === 'accounts' ? 'accounts' : 'key',
    secret: (pick(vars, 'SECRET') ?? '').trim(),
    dbPath: (pick(vars, 'DB') ?? '').trim() || 'data/console.db',
    signup: (pick(vars, 'SIGNUP') ?? '').trim().toLowerCase() === 'open' ? 'open' : 'invite',
    platformKey: (pick(vars, 'PLATFORM_KEY') ?? '').trim(),
    github: pick(vars, 'GITHUB_CLIENT_ID') && pick(vars, 'GITHUB_CLIENT_SECRET') ? { id: pick(vars, 'GITHUB_CLIENT_ID')!.trim(), secret: pick(vars, 'GITHUB_CLIENT_SECRET')!.trim() } : null,
    google: pick(vars, 'GOOGLE_CLIENT_ID') && pick(vars, 'GOOGLE_CLIENT_SECRET') ? { id: pick(vars, 'GOOGLE_CLIENT_ID')!.trim(), secret: pick(vars, 'GOOGLE_CLIENT_SECRET')!.trim() } : null,
    pricePerAction: Number.isFinite(Number(pick(vars, 'PRICE_PER_ACTION'))) && pick(vars, 'PRICE_PER_ACTION') ? Number(pick(vars, 'PRICE_PER_ACTION')) : null,
  };
}

/** What the browser may know about the server: never the key. */
export function publicConfig(env: Env, realAvailable: boolean) {
  return {
    mode: env.mode,
    realAvailable,
    workspace: env.workspace,
    autoSignIn: env.auth === 'key' && env.autoSignIn && Boolean(env.adminKey),
    auth: env.auth,
    adminKey: Boolean(env.adminKey),
    // Where agents send checks. Not a secret; the onboarding command needs it.
    apiUrl: env.publicApi,
  };
}

/**
 * Read a .env file into a map. Only `KEY=value` lines, with optional quotes,
 * so a value can hold `=` and `#` when quoted.
 */
export function parseDotEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const m = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line);
    if (!m) continue;
    let v = m[2]!;
    const q = v[0];
    if ((q === '"' || q === "'") && v.endsWith(q) && v.length >= 2) v = v.slice(1, -1);
    else v = v.replace(/\s+#.*$/, '');
    out[m[1]!] = v;
  }
  return out;
}
