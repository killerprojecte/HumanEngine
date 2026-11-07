import type Database from 'better-sqlite3'
import type { MemoryRecord } from '../domain/contracts.ts'
import { AssertionHistory, type AssertionSupportSnapshot } from '../storage/assertion-history.ts'

/** Commit the final record and proof together, never an intermediate write before
 * support alternatives have been updated. No model or network work belongs here.
 */
export class MemoryRevisionCoordinator {
  readonly history: AssertionHistory
  private depth = 0
  private dirty = new Set<string>()
  private eligibilityBefore = new Map<string, MemoryRecord | undefined>()
  private eligibilityAfter = new Map<string, Pick<MemoryRecord, 'status' | 'lineageStatus'>>()

  constructor(
    private readonly db: Database.Database,
    private readonly read: (id: string) => MemoryRecord | undefined,
    private readonly supports: (id: string) => AssertionSupportSnapshot[],
    private readonly retain: (record: MemoryRecord) => boolean,
    private readonly committed?: (id: string, version: number, epoch: number) => void,
    private readonly afterCommit?: () => void,
    private readonly beforeCommitEligibility?: (
      changes: readonly { before: MemoryRecord; after: MemoryRecord }[],
    ) => void,
  ) {
    this.history = new AssertionHistory(db)
  }

  transaction<T>(operation: () => T): () => T {
    const transaction = this.db.transaction(() => {
      const outer = this.depth === 0
      const previous = new Set(this.dirty)
      const previousEligibility = new Map(this.eligibilityBefore)
      const previousEligibilityAfter = new Map(this.eligibilityAfter)
      this.depth++
      try {
        const result = operation()
        if (outer) {
          this.flush()
          this.flushEligibility()
        }
        return result
      } catch (error) {
        this.dirty = previous
        this.eligibilityBefore = previousEligibility
        this.eligibilityAfter = previousEligibilityAfter
        throw error
      } finally {
        this.depth--
      }
    })
    return () => {
      const outer = this.depth === 0
      const result = transaction()
      if (outer) this.afterCommit?.()
      return result
    }
  }

  beforeWrite(id: string): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    if (this.dirty.has(id) && this.eligibilityBefore.has(id)) return
    const previous = this.read(id)
    if (!this.eligibilityBefore.has(id)) this.eligibilityBefore.set(id, previous)
    if (this.dirty.has(id)) return
    if (
      previous &&
      this.retain(previous) &&
      this.history.state(id).version === 0 &&
      this.history.state(id).deletionEpoch === 0
    )
      this.history.importLegacy(previous, this.supports(id))
    this.dirty.add(id)
  }

  /** The repository has already serialized this row. Retain only immutable permission
   * fields, not a caller-owned record or an authorization result across writes. */
  afterWrite(record: MemoryRecord): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    this.eligibilityAfter.set(record.id, {
      status: record.status,
      lineageStatus: record.lineageStatus,
    })
  }

  touch(id: string): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    this.dirty.add(id)
  }

  checkpoint(): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    this.flush()
  }

  private flushEligibility(): void {
    const changes: { before: MemoryRecord; after: MemoryRecord }[] = []
    for (const [id, before] of this.eligibilityBefore) {
      // Admission is not a regrant. A backup need not contain assertions born later.
      // Deletion already has its separate irreversible authority obligation.
      const final = this.eligibilityAfter.get(id)
      if (
        !before ||
        !final ||
        ((before.status === 'invalidated') === (final.status === 'invalidated') &&
          (before.lineageStatus === 'quarantined') === (final.lineageStatus === 'quarantined'))
      )
        continue
      const after = this.read(id)
      if (after) changes.push({ before, after })
    }
    this.beforeCommitEligibility?.(changes)
    this.eligibilityBefore.clear()
    this.eligibilityAfter.clear()
  }

  private flush(): void {
    for (const id of this.dirty) {
      const record = this.read(id)
      if (!record || !this.retain(record)) continue
      const state = this.history.state(id)
      if (state.deleted) throw new Error('assertion_deleted_or_stale_epoch')
      const supports = this.supports(id)
      const previous = this.history
        .revisions(
          id,
          record.scope.kind === 'scene' ? `scene:${record.scope.sceneId}` : 'shared:public',
        )
        .at(-1)
      if (
        previous &&
        JSON.stringify(previous.record) === JSON.stringify(record) &&
        JSON.stringify(previous.supports) === JSON.stringify(supports)
      )
        continue
      const appended = this.history.append({
        record,
        supports,
        expectedVersion: state.version,
        expectedDeletionEpoch: state.deletionEpoch,
        idempotencyKey: `commit:${state.deletionEpoch}:${state.version + 1}`,
        recordedAt: new Date().toISOString(),
        validFrom: record.validFrom ? new Date(record.validFrom).toISOString() : null,
        validTo: record.validUntil ? new Date(record.validUntil).toISOString() : null,
        eventKind: record.origin === 'manual' ? 'admin_assertion' : 'learned_assertion',
      })
      this.committed?.(id, appended.version, state.deletionEpoch)
    }
    this.dirty.clear()
  }
}
