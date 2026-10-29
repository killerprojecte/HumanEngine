import type { TrustedSourceChange, SourceEventResult } from '../domain/events.ts'
import type { SourceEventStore } from '../storage/event-revisions.ts'

/** A trusted adapter boundary, not a model tool. CAS bases must come from the producer. */
export class SourceEventIngest {
  constructor(private readonly store: SourceEventStore) {}
  observe(change: TrustedSourceChange, context: { simulation?: boolean } = {}): SourceEventResult {
    if (context.simulation) return { status: 'simulation', bodyCoverage: 'unavailable' }
    return this.store.observe(change)
  }
}
