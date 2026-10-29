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

  constructor(
    private readonly db: Database.Database,
    private readonly read: (id: string) => MemoryRecord | undefined,
    private readonly supports: (id: string) => AssertionSupportSnapshot[],
    private readonly retain: (record: MemoryRecord) => boolean,
    private readonly committed?: (id: string, version: number, epoch: number) => void,
    private readonly afterCommit?: () => void,
  ) {
    this.history = new AssertionHistory(db)
  }

  transaction<T>(operation: () => T): () => T {
    const transaction = this.db.transaction(() => {
      const outer = this.depth === 0
      const previous = new Set(this.dirty)
      this.depth++
      try {
        const result = operation()
        if (outer) this.flush()
        return result
      } catch (error) {
        this.dirty = previous
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
    if (this.dirty.has(id)) return
    const previous = this.read(id)
    if (
      previous &&
      this.retain(previous) &&
      this.history.state(id).version === 0 &&
      this.history.state(id).deletionEpoch === 0
    )
      this.history.importLegacy(previous, this.supports(id))
    this.dirty.add(id)
  }

  touch(id: string): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    this.dirty.add(id)
  }

  checkpoint(): void {
    if (!this.db.inTransaction) throw new Error('memory_write_requires_transaction')
    this.flush()
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
