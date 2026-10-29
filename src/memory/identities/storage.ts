import type Database from 'better-sqlite3'
import { RegistryStateSchema, type RegistryState } from './contracts.ts'

/** Bounded registry state; no chat body, embedding, or implicit nickname identity. */
export class IdentityStorage {
  constructor(
    private readonly db: Database.Database,
    private readonly assertReady: () => void,
  ) {}
  read(): RegistryState {
    this.assertReady()
    if (
      !this.db
        .prepare(
          "SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_identity_registry'",
        )
        .get()
    )
      return { format: 1, revision: 0, edges: [], aliases: [], operations: [] }
    const row = this.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get() as
      { value: string } | undefined
    if (!row || typeof row.value !== 'string') throw new Error('identity_registry_corrupt')
    try {
      return RegistryStateSchema.parse(JSON.parse(row.value))
    } catch {
      throw new Error('identity_registry_corrupt')
    }
  }
  write(state: RegistryState): void {
    this.assertReady()
    if (!this.db.inTransaction) throw new Error('identity_transaction_required')
    const safe = RegistryStateSchema.parse(state)
    this.db.exec(
      'CREATE TABLE IF NOT EXISTS memory_identity_registry (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)',
    )
    this.db
      .prepare(
        'INSERT INTO memory_identity_registry(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
      )
      .run(JSON.stringify(safe))
  }
}
