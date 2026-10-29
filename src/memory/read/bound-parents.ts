import type { MemoryRecord, MemoryScope, MemorySource } from '../domain/contracts.ts'
import type { ProofParentReference, SupportProof } from '../domain/proofs.ts'
import { evaluateProofParents, type ProofParentState } from './proof-parents.ts'
import { ProofTraversalLimit, type ProvenanceBudget } from './provenance.ts'

export interface BoundParentPorts {
  current(
    scope: MemoryScope,
    id: string,
    cache: Map<string, boolean>,
    budget: ProvenanceBudget,
  ): { record: MemoryRecord; state: ProofParentState } | undefined
  revision(
    reference: ProofParentReference,
    current: MemoryRecord,
  ): { record: MemoryRecord; supports: SupportProof[] } | undefined
  allows(child: MemoryScope, parent: MemoryScope): boolean
  recalled(source: MemorySource): boolean
}
export const createParentTraversal = () => ({
  remaining: 512,
  memo: new Map<string, boolean>(),
  provenance: new Map<string, boolean>(),
  provenanceBudget: { remaining: 4096 },
})
/** Scope/version-aware read only. A limit is explicit, never evidence of invalid provenance. */
export function inspectBoundParents(
  scope: MemoryScope,
  proof: SupportProof,
  mode: 'current' | 'history',
  ports: BoundParentPorts,
  path = new Set<string>(),
  traversal = createParentTraversal(),
): boolean {
  if (path.size >= 64 || --traversal.remaining < 0) throw new ProofTraversalLimit()
  return evaluateProofParents(proof, mode, {
    current: (id) => {
      if (--traversal.remaining < 0) throw new ProofTraversalLimit()
      return ports.current(scope, id, traversal.provenance, traversal.provenanceBudget)?.state
    },
    revision: (reference) => {
      if (--traversal.remaining < 0) throw new ProofTraversalLimit()
      const key = JSON.stringify([reference.id, reference.version, reference.deletionEpoch])
      if (path.has(key)) return false
      const parent = ports.current(
        scope,
        reference.id,
        traversal.provenance,
        traversal.provenanceBudget,
      )
      if (!parent) return false
      const memoKey = JSON.stringify([scope, key])
      if (traversal.memo.get(memoKey) === true) return true
      const revision = ports.revision(reference, parent.record)
      if (
        !revision ||
        !ports.allows(scope, revision.record.scope) ||
        revision.record.lineageStatus === 'quarantined' ||
        revision.record.status === 'invalidated'
      )
        return false
      if (
        revision.record.sources.some((source) => {
          if (--traversal.remaining < 0) throw new ProofTraversalLimit()
          return ports.recalled(source)
        })
      )
        return false
      const next = new Set(path).add(key)
      const available =
        revision.record.lineageStatus !== 'verified' ||
        revision.supports.some((support) => {
          if (--traversal.remaining < 0) throw new ProofTraversalLimit()
          return (
            !support.invalidatedBy &&
            support.sources.length > 0 &&
            support.sources.every((source) => {
              if (--traversal.remaining < 0) throw new ProofTraversalLimit()
              return !ports.recalled(source)
            }) &&
            inspectBoundParents(revision.record.scope, support, 'history', ports, next, traversal)
          )
        })
      if (available) traversal.memo.set(memoKey, true)
      return available
    },
  }).available
}
