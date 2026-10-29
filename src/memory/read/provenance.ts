import type { MemoryRecord, MemorySource } from '../domain/contracts.ts'
import { parentBindingState, type SupportProof } from '../domain/proofs.ts'

export interface ProvenanceBudget {
  remaining: number
}
export class ProofTraversalLimit extends Error {
  constructor() {
    super('memory_proof_budget_exhausted')
    this.name = 'ProofTraversalLimit'
  }
}
export interface ProvenancePorts {
  record(id: string): MemoryRecord | undefined
  proofs(id: string): SupportProof[]
  recalled(source: MemorySource): boolean
  parentAllowed(child: MemoryRecord, parent: MemoryRecord, proof: SupportProof): boolean
}
/** Provenance only. Version/time drift is not a privacy invalidation.
 * Budget exhaustion throws so lifecycle transactions roll back rather than purge unknown evidence.
 */
export function hasProvenance(
  memory: MemoryRecord,
  ports: ProvenancePorts,
  path = new Set<string>(),
  cache = new Map<string, boolean>(),
  budget: ProvenanceBudget = { remaining: 4096 },
): boolean {
  if (--budget.remaining < 0 || path.size >= 64) throw new ProofTraversalLimit()
  if (
    !['active', 'disputed', 'superseded'].includes(memory.status) ||
    memory.lineageStatus === 'quarantined' ||
    path.has(memory.id)
  )
    return false
  if (cache.get(memory.id) === true) return true
  if (memory.lineageStatus !== 'verified') return true
  const next = new Set(path).add(memory.id)
  const available = ports.proofs(memory.id).some((proof) => {
    if (--budget.remaining < 0) throw new ProofTraversalLimit()
    return (
      !proof.invalidatedBy &&
      proof.sources.length > 0 &&
      parentBindingState(proof) !== 'invalid' &&
      proof.sources.every((source) => {
        if (--budget.remaining < 0) throw new ProofTraversalLimit()
        return !ports.recalled(source)
      }) &&
      proof.parents.every((id) => {
        if (--budget.remaining < 0) throw new ProofTraversalLimit()
        const parent = ports.record(id)
        return (
          !!parent &&
          ports.parentAllowed(memory, parent, proof) &&
          hasProvenance(parent, ports, next, cache, budget)
        )
      })
    )
  })
  // Path/cycle and work-limit dependent failures must not poison another valid OR path.
  if (available) cache.set(memory.id, true)
  return available
}
