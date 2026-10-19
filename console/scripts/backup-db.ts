// A consistent copy of the accounts database, safe while the console runs:
// SQLite's VACUUM INTO writes a snapshot, where copying the file could catch
// it mid-write. Used by the install's ./lumni backup.
//
//   bun scripts/backup-db.ts /tmp/console-backup.db

import { Database } from 'bun:sqlite';
import { rmSync } from 'node:fs';

const out = process.argv[2];
if (!out) {
  console.error('usage: bun scripts/backup-db.ts <file>');
  process.exit(2);
}
rmSync(out, { force: true });
const db = new Database(process.env.LUMNI_CONSOLE_DB ?? 'data/console.db', { readonly: true });
db.exec(`VACUUM INTO '${out.replaceAll("'", "''")}'`);
db.close();
