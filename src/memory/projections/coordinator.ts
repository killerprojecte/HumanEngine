import type Database from 'better-sqlite3'
import type { MemoryRecord } from '../domain/contracts.ts'
import { ProjectionOutbox } from '../storage/projection-outbox.ts'
import { MemoryProjectionProcessor } from './processor.ts'

/** Optional derived state only. The authoritative memory store remains the read boundary. */
export class MemoryProjections {
  private readonly outbox: ProjectionOutbox
  private readonly processor: MemoryProjectionProcessor
  constructor(
    db: Database.Database,
    private readonly enabled: () => boolean,
    readEligible: (id: string) => MemoryRecord | undefined,
  ) {
    this.outbox = new ProjectionOutbox(db)
    this.processor = new MemoryProjectionProcessor(db, this.outbox, readEligible)
  }
  committed(assertionId: string, version: number, deletionEpoch: number) {
    if (!this.enabled()) return
    for (const kind of ['lexical', 'person'] as const)
      this.outbox.enqueue({ assertionId, version, deletionEpoch, kind })
  }
  remove(id: string) {
    this.processor.remove(id)
  }
  flush() {
    if (!this.enabled()) return
    return this.processor.drain(64)
  }
}
