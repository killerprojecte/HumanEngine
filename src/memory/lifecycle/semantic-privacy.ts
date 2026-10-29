import type { SemanticQualification } from '../semantic/contracts.ts'
import type { SemanticService } from '../semantic/candidates.ts'
import type { RelationProof } from '../domain/relations.ts'
import type { KnowledgeRef } from '../knowledge/contracts.ts'
import type { MemoryRevisionCoordinator } from '../write/revision-coordinator.ts'
/** Only exact pre-recall authorized heads may carry surviving original proofs forward. */
export class SemanticPrivacy {
  constructor(
    private readonly semantic: SemanticService,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly proofAvailable: (
      sceneId: string,
      backing: KnowledgeRef,
      proof: RelationProof,
    ) => boolean,
  ) {}
  capture(): ReadonlyMap<string, readonly SemanticQualification[]> {
    const result = new Map<string, readonly SemanticQualification[]>()
    for (const sceneId of new Set(this.semantic.storage.rows().map((row) => row.sceneId)))
      result.set(sceneId, this.semantic.captureQualification({ sceneId, simulation: false }))
    return result
  }
  refresh(
    before: ReadonlyMap<string, KnowledgeRef>,
    qualifiedBefore: ReadonlyMap<string, readonly SemanticQualification[]> = new Map(),
  ) {
    this.revisions.checkpoint()
    const changes = [...before.values()].flatMap((old) => {
      const state = this.revisions.history.state(old.id)
      if (
        state.deleted ||
        state.version < 1 ||
        (state.version === old.version && state.deletionEpoch === old.deletionEpoch)
      )
        return []
      return [
        {
          before: old,
          after: { id: old.id, version: state.version, deletionEpoch: state.deletionEpoch },
        },
      ]
    })
    for (const sceneId of new Set(this.semantic.storage.rows().map((edge) => edge.sceneId))) {
      const ctx = { sceneId, simulation: false, provenance: 'source-only' as const }
      // Privacy provenance, not current applicability. Unknown traversal errors propagate.
      this.semantic.lifecycle.pruneUnavailableProof(ctx, (backing, proof) =>
        this.proofAvailable(sceneId, backing, proof),
      )
      this.semantic.lifecycle.requalify(ctx, changes, qualifiedBefore.get(sceneId))
    }
  }
}
