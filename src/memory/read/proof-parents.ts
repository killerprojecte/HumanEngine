import {
  parentBindingState,
  type ProofParentReference,
  type SupportProof,
} from '../domain/proofs.ts'

export interface ProofParentState {
  version: number
  deletionEpoch: number
  deleted: boolean
}
export interface ProofParentPorts {
  /** Return only after CURRENT scope/person and source authorization succeeds. */
  current(id: string): ProofParentState | undefined
  /** Validate exact retained revision, its sources and recursively its own proof. */
  revision(reference: ProofParentReference): boolean
}
export type ProofParentReason =
  | 'bound'
  | 'legacy_unknown'
  | 'invalid_binding'
  | 'invalidated'
  | 'parent_unavailable'
  | 'parent_epoch_changed'
  | 'parent_version_changed'
  | 'parent_revision_unavailable'
export interface ProofParentResult {
  available: boolean
  reason: ProofParentReason
  parentId?: string
}

/** Read applicability only. A version change is NOT a privacy deletion instruction. */
export function evaluateProofParents(
  proof: SupportProof,
  mode: 'current' | 'history',
  ports: ProofParentPorts,
): ProofParentResult {
  if (proof.invalidatedBy) return { available: false, reason: 'invalidated' }
  const binding = parentBindingState(proof)
  if (binding === 'invalid') return { available: false, reason: 'invalid_binding' }
  for (const id of proof.parents) {
    const state = ports.current(id)
    if (!state || state.deleted)
      return { available: false, reason: 'parent_unavailable', parentId: id }
    if (binding === 'legacy_unknown') continue
    const ref = proof.parentReferences!.find((ref) => ref.id === id)!
    if (state.deletionEpoch !== ref.deletionEpoch)
      return { available: false, reason: 'parent_epoch_changed', parentId: id }
    if (mode === 'current' && state.version !== ref.version)
      return { available: false, reason: 'parent_version_changed', parentId: id }
    if (!ports.revision(ref))
      return { available: false, reason: 'parent_revision_unavailable', parentId: id }
  }
  return { available: true, reason: binding === 'legacy_unknown' ? 'legacy_unknown' : 'bound' }
}
