// Where accounts live: one SQLite file next to the console (Bun's own
// driver, no dependencies). People, the workspaces they belong to and their
// roles, invites, password resets and sign-in sessions.
//
// Secrets (each workspace's admin key, each member's own API key) are sealed
// with AES-GCM under LUMNI_CONSOLE_SECRET before they're written; tokens
// people hold (sessions, invites, resets) are stored only as SHA-256 hashes.

import { Database } from 'bun:sqlite';
import { createHash, randomBytes, randomUUID } from 'node:crypto';

export type Role = 'owner' | 'admin' | 'member' | 'viewer';
export const ROLES: Role[] = ['owner', 'admin', 'member', 'viewer'];

export interface User { id: string; email: string; name: string; password_hash: string | null; created_at: number; last_seen: number | null }
export interface Workspace { id: string; name: string; tenant_id: string; admin_key_sealed: string; created_at: number }
export interface Member { workspace_id: string; user_id: string; role: Role; key_id: string | null; key_sealed: string | null; created_at: number }
export interface Invite { id: string; workspace_id: string; email: string | null; role: Role; token_hash: string; invited_by: string; created_at: number; expires_at: number; accepted_at: number | null; accepted_by: string | null }
export interface SessionRow { id_hash: string; user_id: string; workspace_id: string | null; created_at: number; expires_at: number; last_seen: number }

const SCHEMA = `
create table if not exists users (
  id text primary key, email text not null unique collate nocase, name text not null,
  password_hash text, created_at integer not null, last_seen integer);
create table if not exists identities (
  provider text not null, subject text not null, user_id text not null references users(id) on delete cascade,
  primary key (provider, subject));
create table if not exists workspaces (
  id text primary key, name text not null, tenant_id text not null unique,
  admin_key_sealed text not null, created_at integer not null);
create table if not exists members (
  workspace_id text not null references workspaces(id) on delete cascade,
  user_id text not null references users(id) on delete cascade,
  role text not null check (role in ('owner','admin','member','viewer')),
  key_id text, key_sealed text, created_at integer not null,
  primary key (workspace_id, user_id));
create table if not exists invites (
  id text primary key, workspace_id text not null references workspaces(id) on delete cascade,
  email text collate nocase, role text not null, token_hash text not null unique, invited_by text not null,
  created_at integer not null, expires_at integer not null, accepted_at integer, accepted_by text);
create table if not exists sessions (
  id_hash text primary key, user_id text not null references users(id) on delete cascade,
  workspace_id text, created_at integer not null, expires_at integer not null, last_seen integer not null);
create table if not exists resets (
  token_hash text primary key, user_id text not null references users(id) on delete cascade,
  created_by text, expires_at integer not null, used_at integer);
create index if not exists members_user on members(user_id);
create index if not exists sessions_user on sessions(user_id);
`;

export const hashToken = (t: string) => createHash('sha256').update(t).digest('hex');
export const newToken = () => Array.from(randomBytes(32), (b) => b.toString(16).padStart(2, '0')).join('');
export const newId = (prefix: string) => `${prefix}_${randomUUID().replace(/-/g, '').slice(0, 20)}`;

export class Store {
  readonly db: Database;

  constructor(path: string) {
    this.db = new Database(path, { create: true });
    this.db.exec('pragma journal_mode = wal; pragma foreign_keys = on; pragma busy_timeout = 3000;');
    this.db.exec(SCHEMA);
  }

  // ─── people ──────────────────────────────────────────
  userCount(): number {
    return (this.db.query<{ n: number }>('select count(*) n from users').get()?.n) ?? 0;
  }
  userById(id: string): User | null {
    return this.db.query<User>('select * from users where id = ?').get(id);
  }
  userByEmail(email: string): User | null {
    return this.db.query<User>('select * from users where email = ?').get(email.trim());
  }
  createUser(email: string, name: string, passwordHash: string | null, now: number): User {
    const u: User = { id: newId('usr'), email: email.trim(), name: name.trim(), password_hash: passwordHash, created_at: now, last_seen: null };
    this.db.query('insert into users (id, email, name, password_hash, created_at) values (?, ?, ?, ?, ?)').run(u.id, u.email, u.name, u.password_hash, u.created_at);
    return u;
  }
  updateUser(id: string, fields: { name?: string; password_hash?: string; last_seen?: number }): void {
    if (fields.name !== undefined) this.db.query('update users set name = ? where id = ?').run(fields.name, id);
    if (fields.password_hash !== undefined) this.db.query('update users set password_hash = ? where id = ?').run(fields.password_hash, id);
    if (fields.last_seen !== undefined) this.db.query('update users set last_seen = ? where id = ?').run(fields.last_seen, id);
  }
  userByIdentity(provider: string, subject: string): User | null {
    return this.db.query<User>('select u.* from identities i join users u on u.id = i.user_id where i.provider = ? and i.subject = ?').get(provider, subject);
  }
  linkIdentity(provider: string, subject: string, userId: string): void {
    this.db.query('insert or ignore into identities (provider, subject, user_id) values (?, ?, ?)').run(provider, subject, userId);
  }

  // ─── workspaces and members ──────────────────────────
  createWorkspace(name: string, tenantId: string, adminKeySealed: string, now: number): Workspace {
    const w: Workspace = { id: newId('ws'), name: name.trim(), tenant_id: tenantId, admin_key_sealed: adminKeySealed, created_at: now };
    this.db.query('insert into workspaces (id, name, tenant_id, admin_key_sealed, created_at) values (?, ?, ?, ?, ?)').run(w.id, w.name, w.tenant_id, w.admin_key_sealed, w.created_at);
    return w;
  }
  workspace(id: string): Workspace | null {
    return this.db.query<Workspace>('select * from workspaces where id = ?').get(id);
  }
  workspaceByTenant(tenantId: string): Workspace | null {
    return this.db.query<Workspace>('select * from workspaces where tenant_id = ?').get(tenantId);
  }
  renameWorkspace(id: string, name: string): void {
    this.db.query('update workspaces set name = ? where id = ?').run(name, id);
  }
  addMember(workspaceId: string, userId: string, role: Role, now: number): void {
    this.db.query('insert or ignore into members (workspace_id, user_id, role, created_at) values (?, ?, ?, ?)').run(workspaceId, userId, role, now);
  }
  member(workspaceId: string, userId: string): Member | null {
    return this.db.query<Member>('select * from members where workspace_id = ? and user_id = ?').get(workspaceId, userId);
  }
  membershipsOf(userId: string): (Member & { name: string; tenant_id: string })[] {
    return this.db.query<Member & { name: string; tenant_id: string }>('select m.*, w.name, w.tenant_id from members m join workspaces w on w.id = m.workspace_id where m.user_id = ? order by m.created_at').all(userId);
  }
  members(workspaceId: string): (Member & { email: string; name: string; last_seen: number | null })[] {
    return this.db.query<Member & { email: string; name: string; last_seen: number | null }>('select m.*, u.email, u.name, u.last_seen from members m join users u on u.id = m.user_id where m.workspace_id = ? order by m.created_at').all(workspaceId);
  }
  setRole(workspaceId: string, userId: string, role: Role): void {
    this.db.query('update members set role = ?, key_id = null, key_sealed = null where workspace_id = ? and user_id = ?').run(role, workspaceId, userId);
  }
  setMemberKey(workspaceId: string, userId: string, keyId: string, keySealed: string): void {
    this.db.query('update members set key_id = ?, key_sealed = ? where workspace_id = ? and user_id = ?').run(keyId, keySealed, workspaceId, userId);
  }
  /** Forget a member's key, so the next sign-in makes a new one. */
  clearMemberKey(workspaceId: string, userId: string): void {
    this.db.query('update members set key_id = null, key_sealed = null where workspace_id = ? and user_id = ?').run(workspaceId, userId);
  }
  removeMember(workspaceId: string, userId: string): void {
    this.db.query('delete from members where workspace_id = ? and user_id = ?').run(workspaceId, userId);
    this.db.query('update sessions set workspace_id = null where user_id = ? and workspace_id = ?').run(userId, workspaceId);
  }
  owners(workspaceId: string): number {
    return this.db.query<{ n: number }>("select count(*) n from members where workspace_id = ? and role = 'owner'").get(workspaceId)?.n ?? 0;
  }

  // ─── invites ─────────────────────────────────────────
  createInvite(workspaceId: string, email: string | null, role: Role, invitedBy: string, now: number, ttlMs: number): { invite: Invite; token: string } {
    const token = newToken();
    const i: Invite = { id: newId('inv'), workspace_id: workspaceId, email: email?.trim() || null, role, token_hash: hashToken(token), invited_by: invitedBy, created_at: now, expires_at: now + ttlMs, accepted_at: null, accepted_by: null };
    this.db.query('insert into invites (id, workspace_id, email, role, token_hash, invited_by, created_at, expires_at) values (?, ?, ?, ?, ?, ?, ?, ?)').run(i.id, i.workspace_id, i.email, i.role, i.token_hash, i.invited_by, i.created_at, i.expires_at);
    return { invite: i, token };
  }
  inviteByToken(token: string): Invite | null {
    return this.db.query<Invite>('select * from invites where token_hash = ?').get(hashToken(token));
  }
  pendingInvites(workspaceId: string, now: number): Invite[] {
    return this.db.query<Invite>('select * from invites where workspace_id = ? and accepted_at is null and expires_at > ? order by created_at desc').all(workspaceId, now);
  }
  acceptInvite(id: string, userId: string, now: number): void {
    this.db.query('update invites set accepted_at = ?, accepted_by = ? where id = ?').run(now, userId, id);
  }
  deleteInvite(workspaceId: string, id: string): void {
    this.db.query('delete from invites where workspace_id = ? and id = ?').run(workspaceId, id);
  }

  // ─── sessions ────────────────────────────────────────
  createSession(userId: string, workspaceId: string | null, now: number, ttlMs: number): string {
    const token = newToken();
    this.db.query('insert into sessions (id_hash, user_id, workspace_id, created_at, expires_at, last_seen) values (?, ?, ?, ?, ?, ?)').run(hashToken(token), userId, workspaceId, now, now + ttlMs, now);
    return token;
  }
  session(token: string): SessionRow | null {
    return this.db.query<SessionRow>('select * from sessions where id_hash = ?').get(hashToken(token));
  }
  touchSession(token: string, now: number, ttlMs: number): void {
    this.db.query('update sessions set last_seen = ?, expires_at = ? where id_hash = ?').run(now, now + ttlMs, hashToken(token));
  }
  setSessionWorkspace(token: string, workspaceId: string): void {
    this.db.query('update sessions set workspace_id = ? where id_hash = ?').run(workspaceId, hashToken(token));
  }
  deleteSession(token: string): void {
    this.db.query('delete from sessions where id_hash = ?').run(hashToken(token));
  }
  deleteSessionsOf(userId: string): void {
    this.db.query('delete from sessions where user_id = ?').run(userId);
  }
  sweep(now: number): void {
    this.db.query('delete from sessions where expires_at < ?').run(now);
    this.db.query('delete from resets where expires_at < ?').run(now - 86_400_000);
  }

  // ─── password resets ─────────────────────────────────
  createReset(userId: string, createdBy: string | null, now: number, ttlMs: number): string {
    const token = newToken();
    this.db.query('insert into resets (token_hash, user_id, created_by, expires_at) values (?, ?, ?, ?)').run(hashToken(token), userId, createdBy, now + ttlMs);
    return token;
  }
  useReset(token: string, now: number): string | null {
    const r = this.db.query<{ user_id: string; expires_at: number; used_at: number | null }>('select user_id, expires_at, used_at from resets where token_hash = ?').get(hashToken(token));
    if (!r || r.used_at || r.expires_at < now) return null;
    this.db.query('update resets set used_at = ? where token_hash = ?').run(now, hashToken(token));
    return r.user_id;
  }
}

// ─── sealing secrets ──────────────────────────────────

const enc = new TextEncoder();
const dec = new TextDecoder();
const b64 = (b: Uint8Array) => btoa(String.fromCharCode(...b));
const unb64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));

/** AES-256-GCM under a key derived from the console's secret. Sealed values look like v1.<iv>.<data>. */
export class Sealer {
  private key: Promise<CryptoKey>;
  constructor(secret: string) {
    if (!secret || secret.length < 16) throw new Error('LUMNI_CONSOLE_SECRET must be set (16+ characters) to keep accounts.');
    this.key = crypto.subtle.digest('SHA-256', enc.encode(`lumni-console:${secret}`)).then((raw) => crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']));
  }
  async seal(plain: string): Promise<string> {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const data = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, await this.key, enc.encode(plain)));
    return `v1.${b64(iv)}.${b64(data)}`;
  }
  async open(sealed: string): Promise<string> {
    const [v, iv, data] = sealed.split('.');
    if (v !== 'v1' || !iv || !data) throw new Error('Unreadable sealed value.');
    return dec.decode(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: unb64(iv) }, await this.key, unb64(data)));
  }
}
