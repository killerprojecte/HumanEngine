import { SourceEventRefSchema } from './events.ts'
import type { MemorySource, SubjectWitness, SourceObservation } from './contracts.ts'

/** Captured by trusted code at first exposure, never supplied by a model. */
export interface ProofParentReference {
  id: string
  version: number
  deletionEpoch: number
}
export interface SupportProof {
  sources: MemorySource[]
  parents: string[]
  parentReferences?: ProofParentReference[]
  subjectWitness?: SubjectWitness
  sourceObservations?: SourceObservation[]
  invalidatedBy?: string
}
export type ParentBindingState = 'bound' | 'legacy_unknown' | 'invalid'

export function parentBindingState(proof: SupportProof): ParentBindingState {
  if (
    !proof ||
    !Array.isArray(proof.parents) ||
    proof.parents.some((id) => typeof id !== 'string' || !id) ||
    new Set(proof.parents).size !== proof.parents.length
  )
    return 'invalid'
  if (proof.parentReferences === undefined) return proof.parents.length ? 'legacy_unknown' : 'bound'
  const refs = proof.parentReferences
  if (
    !Array.isArray(refs) ||
    refs.some((ref) => !ref || typeof ref !== 'object') ||
    refs.length !== proof.parents.length ||
    new Set(refs.map((ref) => ref.id)).size !== refs.length ||
    refs.some(
      (ref) =>
        !proof.parents.includes(ref.id) ||
        !Number.isSafeInteger(ref.version) ||
        ref.version < 1 ||
        !Number.isSafeInteger(ref.deletionEpoch) ||
        ref.deletionEpoch < 0,
    )
  )
    return 'invalid'
  return 'bound'
}

/** Validate only trusted new writes; legacy rows are read without invented versions. */
export function validateNewProof(
  proof: SupportProof,
  provenance: 'source-only' | 'contextual',
): void {
  if (
    proof.sources.some(
      (source) =>
        source.eventRef !== undefined && !SourceEventRefSchema.safeParse(source.eventRef).success,
    )
  )
    throw new Error('memory_source_event_binding_invalid')
  if (parentBindingState(proof) !== 'bound') throw new Error('memory_parent_binding_required')
  if (provenance === 'source-only' && (proof.parents.length || proof.parentReferences?.length))
    throw new Error('memory_source_only_parents_forbidden')
}

/** Proof identity retains revision bindings and AND-group boundaries. */
const compareTuple = (a: unknown[], b: unknown[]) => {
  const left = JSON.stringify(a),
    right = JSON.stringify(b)
  return left < right ? -1 : left > right ? 1 : 0
}

export function canonicalProofKey(proof: SupportProof): string {
  return JSON.stringify([
    proof.sources
      .map((source) =>
        source.eventRef === undefined
          ? [source.sceneId, source.messageId, source.senderId]
          : [
              source.sceneId,
              source.messageId,
              source.senderId,
              [source.eventRef.eventId, source.eventRef.revision, source.eventRef.deletionEpoch],
            ],
      )
      .sort(compareTuple),
    [...proof.parents].sort(),
    proof.parentReferences === undefined
      ? null
      : proof.parentReferences
          .map((ref) => [ref.id, ref.version, ref.deletionEpoch])
          .sort(compareTuple),
    proof.invalidatedBy ?? null,
    proof.subjectWitness
      ? [
          proof.subjectWitness.kind,
          proof.subjectWitness.personId ?? null,
          proof.subjectWitness.sourceMessageId ?? null,
          proof.subjectWitness.mentionPartIndex ?? null,
        ]
      : null,
  ])
}
