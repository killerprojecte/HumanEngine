import type Database from 'better-sqlite3'
import { isDeepStrictEqual } from 'node:util'
import { ExternalCostStorage } from '../../budget/external-cost/storage.ts'
import type { ExternalCostReceipt } from '../../budget/external-cost/contracts.ts'
import { parseExternalCostRecoveryEvent } from './external-cost-contract.ts'
import { revoke, requireOperationalReader, type Revocation } from './ledger.ts'
export function recordExternalCostReceipt(db: Database.Database, receipt: ExternalCostReceipt) {
  requireOperationalReader(db)
  revoke(db, parseExternalCostRecoveryEvent({ kind: 'external_cost_receipt', receipt }))
}
/** Administrator-declared accounting metadata, never supplier reconciliation or fabricated spend. */
export function replayExternalCosts(db: Database.Database, events: readonly Revocation[]) {
  const storage = new ExternalCostStorage(db)
  storage.assertSupported()
  const heads = new Map<string, ExternalCostReceipt>()
  for (const event of events) {
    if (event.kind !== 'external_cost_receipt') continue
    const receipt = event.receipt,
      previous = heads.get(receipt.receipt.id)
    if (previous && isDeepStrictEqual(previous, receipt)) continue
    if (receipt.revision !== (previous?.revision ?? 0) + 1)
      throw new Error('recovery_external_cost_revision_gap')
    heads.set(receipt.receipt.id, receipt)
    db.transaction(() => {
      requireOperationalReader(db)
      storage.put(receipt)
    }).immediate()
  }
  const current = storage.current()
  if (
    current.length !== heads.size ||
    current.some((r) => !isDeepStrictEqual(r, heads.get(r.receipt.id)))
  )
    throw new Error('recovery_external_cost_head_mismatch')
}
