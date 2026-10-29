import type { SupportProof } from '../domain/proofs.ts'
import type { MemoryRecord, MemorySource } from '../domain/contracts.ts'

export interface ApplicabilityPort {
  record(id: string): MemoryRecord | undefined
  proofs(id: string): SupportProof[]
  parentBindings?(record: MemoryRecord, proof: SupportProof): boolean
  recalled(source: MemorySource): boolean
  /** Version applicability only; never a privacy invalidation/purge predicate. */
  sourceCurrent?(source: MemorySource): boolean
  supported(record: MemoryRecord): boolean
}

/** Time applicability is a read predicate, never irreversible proof invalidation.
 * Every alternative remains OR, with all required parents/sources AND within it.
 */
export function isCurrentlyApplicable(
  memory: MemoryRecord,
  port: ApplicabilityPort,
  now: number,
  path = new Set<string>(),
  cache = new Map<string, boolean>(),
): boolean {
  if (path.has(memory.id) || !['active', 'disputed'].includes(memory.status)) return false
  if (cache.has(memory.id)) return cache.get(memory.id)!
  if (
    (memory.validFrom !== undefined && Date.parse(memory.validFrom) > now) ||
    (memory.validUntil !== undefined && Date.parse(memory.validUntil) <= now)
  )
    return false
  const sourceCurrent = (source: MemorySource) =>
    !port.recalled(source) &&
    (port.sourceCurrent ? port.sourceCurrent(source) : source.eventRef === undefined)
  if (memory.lineageStatus !== 'verified') return memory.sources.every(sourceCurrent)
  const next = new Set(path).add(memory.id)
  const applicable = port.proofs(memory.id).some(
    (proof) =>
      !proof.invalidatedBy &&
      proof.sources.length > 0 &&
      (port.parentBindings?.(memory, proof) ?? true) &&
      proof.sources.every(sourceCurrent) &&
      proof.parents.every((id) => {
        const parent = port.record(id)
        return (
          parent !== undefined &&
          port.supported(parent) &&
          isCurrentlyApplicable(parent, port, now, next, cache)
        )
      }),
  )
  if (applicable) cache.set(memory.id, true)
  return applicable
}
