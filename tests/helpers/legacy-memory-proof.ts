import type { Store } from '../../src/store.ts'

/** Simulate the persisted ID-only proof format used by pre-binding releases.
 * Only compatibility fixtures use this; new-write tests always pass exact refs.
 */
export function retainLegacyProofFormat(store: Store, id: string) {
  const row = store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(id) as
    { value: string } | undefined
  const legacy = (proofs: Array<Record<string, unknown>>) =>
    proofs.map(({ parentReferences: _references, ...proof }) => proof)
  if (row)
    store.db
      .prepare('UPDATE memory_supports SET value=? WHERE memory_id=?')
      .run(JSON.stringify(legacy(JSON.parse(row.value))), id)
  const revisions = store.db
    .prepare(
      'SELECT version,value FROM memory_assertion_revisions WHERE assertion_id=? AND value IS NOT NULL',
    )
    .all(id) as Array<{ version: number; value: string }>
  for (const revision of revisions) {
    const value = JSON.parse(revision.value)
    value.supports = legacy(value.supports)
    store.db
      .prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=? AND version=?')
      .run(JSON.stringify(value), id, revision.version)
  }
}
