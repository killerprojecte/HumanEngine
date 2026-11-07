import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { SupportProof } from '../domain/proofs.ts'
import {
  assertRecoveryReady,
  isRecoveryMaintenance,
  metadata,
  RecoveryAuthority,
  requireEligibilityReader,
  revoke,
  type Revocation,
} from './ledger.ts'
import {
  eligibilityDenied,
  eligibilityIncarnation,
  eligibilityMaterial,
  eligibilityState,
  type AssertionEligibilityChange,
  type AssertionEligibilityEvent,
} from './eligibility-contract.ts'

function row(db: Database.Database, id: string): MemoryRecord | undefined {
  const stored = db.prepare('SELECT value FROM memories WHERE id=?').get(id) as
    { value: string } | undefined
  return stored ? (JSON.parse(stored.value) as MemoryRecord) : undefined
}
function supports(db: Database.Database, id: string): SupportProof[] {
  const stored = db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(id) as
    { value: string } | undefined
  return stored ? (JSON.parse(stored.value) as SupportProof[]) : []
}
function change(db: Database.Database, record: MemoryRecord): AssertionEligibilityChange {
  const state = eligibilityState(record)
  return {
    id: record.id,
    incarnationHash: eligibilityIncarnation(record),
    state,
    materialHash: eligibilityDenied(state)
      ? null
      : eligibilityMaterial(record, supports(db, record.id)),
  }
}

/** One authority commit for the final explicit permission transitions of a memory transaction.
 * Existing source/deletion obligations are not evidence that this obligation was recorded. */
export function recordAssertionEligibilityChanges(
  db: Database.Database,
  records: readonly { before: MemoryRecord; after: MemoryRecord }[],
): void {
  // Offline replay must not create new authority or synthesize grant witnesses.
  if (isRecoveryMaintenance(db)) return
  const changes: AssertionEligibilityChange[] = []
  for (const { before, after } of records) {
    if (
      (before.status === 'invalidated') === (after.status === 'invalidated') &&
      (before.lineageStatus === 'quarantined') === (after.lineageStatus === 'quarantined')
    )
      continue
    if (eligibilityIncarnation(before) !== eligibilityIncarnation(after))
      throw new Error('recovery_eligibility_incarnation_mismatch')
    changes.push(change(db, after))
  }
  if (!changes.length) return
  requireEligibilityReader(db)
  // The authority validates this complete typed event once, before its durable append.
  revoke(db, {
    kind: 'assertion_eligibility',
    schemaVersion: 1,
    operationId: randomUUID(),
    mode: 'transition',
    changes,
  })
}

/** Explicit baseline primitive retained for offline tooling and recovery fixtures. */
export function recordAssertionEligibilityBaseline(db: Database.Database): void {
  baseline(db, false)
}

/** Upgrade trusts the currently configured, ready database. Run before creating the
 * Engine or admitting work; readers and offline recovery must never call this. */
export function ensureAssertionEligibilityBaseline(db: Database.Database): void {
  if (db.name === ':memory:') return
  baseline(db, true)
}

function baseline(db: Database.Database, automatic: boolean): void {
  if (db.inTransaction || isRecoveryMaintenance(db))
    throw new Error('recovery_eligibility_baseline_requires_offline')
  db.transaction(() => {
    // Serialize the census and the already-migrated check with all main writers.
    assertRecoveryReady(db)
    const gate = metadata(db)
    if (gate) {
      const authority = new RecoveryAuthority(gate.authority)
      try {
        const events = authority
          .entries()
          .filter(({ revocation }) => revocation.kind === 'assertion_eligibility')
        if (automatic) {
          if (
            events.some(
              ({ revocation }) =>
                revocation.kind === 'assertion_eligibility' && revocation.mode === 'baseline',
            )
          )
            return
        } else if (events.length) {
          throw new Error('recovery_eligibility_baseline_already_started')
        }
      } finally {
        authority.close()
      }
    }
    const exists = db
      .prepare("SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='memories'")
      .get()
    const records = exists
      ? (
          db.prepare('SELECT value FROM main.memories ORDER BY id').all() as { value: string }[]
        ).map(({ value }) => JSON.parse(value) as MemoryRecord)
      : []
    const changes = records
      .filter((record) => eligibilityDenied(eligibilityState(record)))
      .map((record) => change(db, record))
    requireEligibilityReader(db)
    revoke(db, {
      kind: 'assertion_eligibility',
      schemaVersion: 1,
      operationId: randomUUID(),
      mode: 'baseline',
      changes,
    })
  }).immediate()
}

/** Replay only each assertion's final relevant suffix obligation, at its original
 * chain position. A later source event can still withdraw an earlier matched grant.
 * An acknowledged prefix grant does not freeze all subsequent ordinary material edits. */
export function createRecoveryEligibilityPort(
  db: Database.Database,
  entries: readonly { sequence: number; revocation: Revocation }[],
  snapshotSequence: number,
) {
  const terminal = new Set<string>()
  const latest = new Map<string, { sequence: number; change: AssertionEligibilityChange }>()
  for (const { sequence, revocation } of entries) {
    if (revocation.kind === 'assertion' && revocation.terminal) terminal.add(revocation.id)
    if (revocation.kind === 'assertion_eligibility')
      for (const change of revocation.changes) latest.set(change.id, { sequence, change })
  }
  const negativeApplied = new Map<string, AssertionEligibilityChange>()
  function apply(sequence: number, event: AssertionEligibilityEvent): void {
    if (sequence <= snapshotSequence) return
    db.transaction(() => {
      requireEligibilityReader(db)
      for (const item of event.changes) {
        if (terminal.has(item.id) || latest.get(item.id)?.sequence !== sequence) continue
        const current = row(db, item.id)
        const denied = eligibilityDenied(item.state)
        // A backup may predate admission. No root means no body can become eligible;
        // neither a grant nor a withdrawal creates absent material.
        if (!current) continue
        if (eligibilityIncarnation(current) !== item.incarnationHash)
          throw new Error('recovery_eligibility_incarnation_mismatch')
        if (!denied && eligibilityMaterial(current, supports(db, item.id)) !== item.materialHash)
          throw new Error('recovery_eligibility_grant_material_missing')
        const next: MemoryRecord = { ...current }
        if (denied) {
          // Never release a stricter snapshot/lifecycle withdrawal while applying a negative.
          if (item.state.status === 'invalidated') next.status = 'invalidated'
          if (item.state.lineageStatus === 'quarantined') next.lineageStatus = 'quarantined'
          negativeApplied.set(item.id, item)
        } else {
          next.status = item.state.status
          if (item.state.lineageStatus === null) delete next.lineageStatus
          else next.lineageStatus = item.state.lineageStatus
        }
        // No new history revision, epoch, fingerprint, content, or proof is manufactured.
        db.prepare('UPDATE memories SET value=? WHERE id=?').run(JSON.stringify(next), item.id)
        if (denied) db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(item.id)
      }
    }).immediate()
  }
  function verify(): void {
    for (const [id, item] of negativeApplied) {
      const current = row(db, id)
      if (!current || terminal.has(id)) continue
      if (
        eligibilityIncarnation(current) !== item.incarnationHash ||
        (item.state.status === 'invalidated' && current.status !== 'invalidated') ||
        (item.state.lineageStatus === 'quarantined' && current.lineageStatus !== 'quarantined')
      )
        throw new Error('recovery_eligibility_withdrawal_incomplete')
    }
  }
  return { apply, verify }
}
