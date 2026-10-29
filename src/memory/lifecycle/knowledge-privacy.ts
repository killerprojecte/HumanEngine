import type { KnowledgeService } from '../knowledge/service.ts'
import type { RelationProof } from '../domain/relations.ts'
import type { KnowledgeRef } from '../knowledge/contracts.ts'
import type { MemoryRevisionCoordinator } from '../write/revision-coordinator.ts'
/** Only exact pre-recall authorized heads may carry surviving original proofs forward. */
export class KnowledgePrivacy {
  constructor(
    private readonly knowledge: KnowledgeService,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly proofAvailable: (
      sceneId: string,
      backing: KnowledgeRef,
      proof: RelationProof,
    ) => boolean,
  ) {}
  refresh(before: ReadonlyMap<string, KnowledgeRef>) {
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
    for (const sceneId of new Set(this.knowledge.storage.rows().map((edge) => edge.sceneId))) {
      const ctx = { sceneId, simulation: false, provenance: 'source-only' as const }
      // Privacy provenance, not current applicability. Unknown traversal errors propagate.
      this.knowledge.pruneUnavailableProof(ctx, (backing, proof) =>
        this.proofAvailable(sceneId, backing, proof),
      )
      this.knowledge.requalify(ctx, changes)
    }
  }
}
