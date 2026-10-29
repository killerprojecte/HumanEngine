import { isDeepStrictEqual } from 'node:util'
import type { Store } from '../../store.ts'
import type { CommitmentService } from '../commitments/service.ts'
import type { CommitmentContext, CommitmentEvidence, CommitmentRef } from '../domain/commitments.ts'
import type { AssertionAuthority } from '../read/assertion-authority.ts'
import type { AssertionRepository } from '../storage/assertion-repository.ts'
import type { MemoryRevisionCoordinator } from '../write/revision-coordinator.ts'
/** Exact pre-mutation refs only; this is not a latest-version authorization fallback. */
export class CommitmentPrivacy {
  private commitmentRequalifications?: ReadonlyMap<string, CommitmentRef>
  constructor(
    private readonly store: Pick<Store, 'recalled'>,
    private readonly repository: AssertionRepository,
    private readonly authority: AssertionAuthority,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly commitments: CommitmentService,
  ) {}
  surviving(ctx: CommitmentContext, evidence: CommitmentEvidence) {
    const before = this.commitmentRequalifications?.get(evidence.backing.id)
    const bound = evidence.qualificationRef ?? evidence.backing
    if (!before || !isDeepStrictEqual(before, bound)) return undefined
    const state = this.revisions.history.state(evidence.backing.id)
    if (
      state.deleted ||
      state.version < 1 ||
      (state.version === before.version && state.deletionEpoch === before.deletionEpoch)
    )
      return undefined
    const ref = {
      id: evidence.backing.id,
      version: state.version,
      deletionEpoch: state.deletionEpoch,
    }
    const view = this.authority.relationAuthority(ctx, ref)
    return view ? { ref, view } : undefined
  }
  /** Privacy maintenance is independent of feature/learning flags and never starts models. */
  capture() {
    const refs = new Map<string, CommitmentRef>()
    for (const record of this.repository.all()) {
      if (record.scope.kind !== 'scene') continue
      const state = this.revisions.history.state(record.id)
      if (state.deleted || state.version < 1) continue
      const ref = { id: record.id, version: state.version, deletionEpoch: state.deletionEpoch }
      if (
        this.authority.relationAuthority(
          { sceneId: record.scope.sceneId, simulation: false, provenance: 'source-only' },
          ref,
        )
      )
        refs.set(record.id, ref)
    }
    return refs
  }
  refresh(before: ReadonlyMap<string, CommitmentRef> = new Map()) {
    this.revisions.checkpoint()
    const scenes = new Set(this.commitments.storage.allHeads().map((head) => head.sceneId))
    for (const sceneId of scenes) {
      const ctx: CommitmentContext = { sceneId, simulation: false, provenance: 'source-only' }
      this.commitments.pruneUnavailableProof(ctx, (evidence, proof) => {
        const record = this.repository.get(evidence.backing.id)
        return (
          !!record &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === sceneId &&
          this.authority.supported(record) &&
          !proof.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)) &&
          this.authority.proofParentsAvailable(record.scope, proof, 'history')
        )
      })
      this.commitmentRequalifications = before
      try {
        const transitions = [...before.values()].flatMap((from) => {
          const state = this.revisions.history.state(from.id)
          const to = { id: from.id, version: state.version, deletionEpoch: state.deletionEpoch }
          return !state.deleted && !isDeepStrictEqual(from, to) ? [{ from, to }] : []
        })
        this.commitments.requalify(ctx, transitions)
      } finally {
        this.commitmentRequalifications = undefined
      }
    }
  }
}
