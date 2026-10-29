import type Database from 'better-sqlite3'
export type BudgetDatabase = Pick<Database.Database, 'prepare' | 'exec' | 'transaction'>
export function migrateBudget(db: BudgetDatabase): void {
  db.transaction(() => {
    db.exec(`
    CREATE TABLE IF NOT EXISTS budget_attempts (
      id TEXT PRIMARY KEY, payload_hash TEXT NOT NULL, payload TEXT NOT NULL, policy TEXT NOT NULL,
      boot_id TEXT NOT NULL, state TEXT NOT NULL, window_start TEXT NOT NULL,
      lane TEXT NOT NULL, turn_id TEXT, reserved_nano INTEGER NOT NULL,
      known_nano INTEGER NOT NULL, held_nano INTEGER NOT NULL,
      created_at TEXT NOT NULL, started_at TEXT, settled_at TEXT,
      usage TEXT, receipt_hash TEXT, overrun INTEGER NOT NULL DEFAULT 0,
      unbounded_exposure INTEGER NOT NULL DEFAULT 0
    );
    CREATE INDEX IF NOT EXISTS budget_window_idx ON budget_attempts(window_start,lane);
    CREATE INDEX IF NOT EXISTS budget_turn_idx ON budget_attempts(turn_id);
    CREATE TABLE IF NOT EXISTS budget_reconciliations (
      evidence_id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL,
      receipt_hash TEXT NOT NULL, reconciled_at TEXT NOT NULL
    );
  `)
    const columns = db.prepare('PRAGMA table_info(budget_attempts)').all() as Array<{
      name: string
    }>
    if (!columns.some((column) => column.name === 'unbounded_exposure'))
      db.exec(
        'ALTER TABLE budget_attempts ADD COLUMN unbounded_exposure INTEGER NOT NULL DEFAULT 0',
      )
  })()
}
