// The slice of Bun's API this project touches. `bun-types` isn't in the offline
// cache, so these few declarations stand in for it; on a machine with network,
// `@types/bun` can replace this file.

declare module 'bun:test' {
  type Fn = () => void | Promise<void>;
  export function describe(name: string, fn: () => void): void;
  export function test(name: string, fn: Fn): void;
  export function beforeEach(fn: Fn): void;
  export function afterEach(fn: Fn): void;
  interface Matchers {
    not: Matchers;
    toBe(expected: unknown): void;
    toEqual(expected: unknown): void;
    toMatchObject(expected: object): void;
    toBeGreaterThan(n: number): void;
    toBeGreaterThanOrEqual(n: number): void;
    toBeLessThan(n: number): void;
    toBeLessThanOrEqual(n: number): void;
    toBeCloseTo(n: number, digits?: number): void;
    toContain(item: unknown): void;
    toHaveLength(n: number): void;
    toBeDefined(): void;
    toBeUndefined(): void;
    toBeNull(): void;
    toBeTruthy(): void;
    toMatch(re: RegExp | string): void;
    toThrow(msg?: string | RegExp): void;
    rejects: { toThrow(msg?: string | RegExp): Promise<void> };
  }
  export function expect(actual: unknown): Matchers;
}

declare module '*.html' {
  const html: unknown;
  export default html;
}

interface BunServer {
  url: URL;
  port: number;
  /** Seconds a request may sit idle; 0 keeps it open, for streams. */
  timeout(req: Request, seconds: number): void;
  stop(force?: boolean): void;
}

interface BunSQL {
  (strings: TemplateStringsArray, ...values: unknown[]): Promise<Record<string, unknown>[]> & { simple(): Promise<unknown> };
  unsafe(query: string, values?: unknown[]): Promise<Record<string, unknown>[]>;
  begin<T>(fn: (tx: BunSQL) => Promise<T>): Promise<T>;
  array(values: unknown[], type?: string): unknown;
  close(): Promise<void>;
}

declare module 'bun:sqlite' {
  export interface Statement<Row = Record<string, unknown>> {
    get(...params: unknown[]): Row | null;
    all(...params: unknown[]): Row[];
    run(...params: unknown[]): { changes: number; lastInsertRowid: number | bigint };
  }
  export class Database {
    constructor(filename?: string, options?: { create?: boolean; readonly?: boolean; strict?: boolean });
    query<Row = Record<string, unknown>>(sql: string): Statement<Row>;
    prepare<Row = Record<string, unknown>>(sql: string): Statement<Row>;
    run(sql: string, ...params: unknown[]): void;
    exec(sql: string): void;
    transaction<A extends unknown[], R>(fn: (...args: A) => R): (...args: A) => R;
    close(): void;
  }
}

declare const Bun: {
  password: {
    hash(password: string, options?: { algorithm?: 'argon2id' | 'bcrypt' }): Promise<string>;
    verify(password: string, hash: string): Promise<boolean>;
  };
  serve(options: {
    port?: number;
    hostname?: string;
    idleTimeout?: number;
    development?: boolean | { hmr?: boolean; console?: boolean };
    routes?: Record<string, unknown>;
    fetch?(req: Request, server: BunServer): Response | Promise<Response>;
  }): BunServer;
  file(path: string): Blob & { exists(): Promise<boolean>; json(): Promise<unknown> };
  write(path: string, data: string): Promise<number>;
  argv: string[];
  env: Record<string, string | undefined>;
  sleep(ms: number): Promise<void>;
  hash(data: string): number | bigint;
  SQL: new (url: string, options?: Record<string, unknown>) => BunSQL;
  version: string;
};

declare const process: {
  exit(code?: number): never;
  argv: string[];
  env: Record<string, string | undefined>;
  stdout: { write(s: string): boolean };
  on(event: string, fn: (...args: unknown[]) => void): void;
};

interface Headers {
  getSetCookie(): string[];
}

interface ImportMeta {
  dir: string;
}

// The few Node built-ins the tests touch (the onboarding command's tests).
declare module 'node:fs' {
  export function existsSync(path: string): boolean;
  export function mkdirSync(path: string, opts?: { recursive?: boolean }): void;
  export function mkdtempSync(prefix: string): string;
  export function readFileSync(path: string, enc: 'utf8'): string;
  export function rmSync(path: string, opts?: { recursive?: boolean; force?: boolean }): void;
  export function writeFileSync(path: string, data: string): void;
}
declare module 'node:os' {
  export function tmpdir(): string;
}
declare module 'node:path' {
  export function join(...parts: string[]): string;
}
declare module 'node:crypto' {
  export function createHash(alg: string): { update(data: string): { digest(enc: 'hex'): string } };
  export function randomBytes(n: number): Uint8Array;
  export function randomUUID(): string;
}
