import { isDeepStrictEqual } from 'node:util'
import type {
  CommitmentContext,
  CommitmentExposure as ExposedCommitmentRef,
  CommitmentRef,
  CommitmentView,
} from '../domain/commitments.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export interface CommitmentStateEvidence {
  stateKind: 'commitment'
  view: CommitmentView
}
export interface CommitmentExposurePort {
  commitments: {
    list(
      ctx: CommitmentContext,
      options: { personId?: string; lifecycle?: 'open' | 'terminal' | 'all'; limit: number },
    ): { items: CommitmentView[]; coverage: 'complete' | 'limited' }
    current(ctx: CommitmentContext, ref: CommitmentRef): CommitmentView | undefined
  }
  /** Owner combines current feature/answer permissions. Never a captured configuration snapshot. */
  enabled(ctx: ModelMemoryContext): boolean
  ready(ctx: ModelMemoryContext): boolean
  onDelivered?(ctx: ModelMemoryContext, values: readonly CommitmentStateEvidence[]): void
}
const context = (ctx: ModelMemoryContext): CommitmentContext => ({
  sceneId: ctx.scene.id,
  simulation: ctx.mode === 'simulation',
  provenance: 'contextual',
})

/** Whole, qualified commitment state receipts; these are not ordinary MemoryRecord parents. */
export class CommitmentExposure {
  private readonly exposure: StateExposure<
    CommitmentStateEvidence,
    { personId?: string; lifecycle?: 'open' | 'terminal' | 'all' }
  >
  constructor(private readonly port: CommitmentExposurePort) {
    this.exposure = new StateExposure({
      query: (ctx, input) => {
        if (!port.enabled(ctx) || !port.ready(ctx)) return []
        const result = port.commitments.list(context(ctx), {
          lifecycle: 'open',
          ...input,
          limit: 12,
        })
        if (result.coverage !== 'complete' || result.items.length > 12)
          throw new Error('commitment_query_limited')
        return result.items.map((view) => ({ stateKind: 'commitment' as const, view }))
      },
      valid: (ctx, values) =>
        port.enabled(ctx) &&
        port.ready(ctx) &&
        values.every(
          (item) =>
            item.stateKind === 'commitment' &&
            item.view.sceneId === ctx.scene.id &&
            isDeepStrictEqual(port.commitments.current(context(ctx), item.view.ref), item.view),
        ),
      sourceIds: (values) => [
        ...new Set(
          values.flatMap((item) =>
            [...item.view.evidence, ...item.view.requiredProposalGroups.flat()].flatMap(
              (evidence) =>
                evidence.proofs.flatMap((proof) => proof.sources.map((source) => source.messageId)),
            ),
          ),
        ),
      ],
      onDelivered: (ctx, values) => port.onDelivered?.(ctx, values),
    })
  }
  query(
    ctx: ModelMemoryContext,
    input: { personId?: string; lifecycle?: 'open' | 'terminal' | 'all' } = {},
  ): readonly CommitmentStateEvidence[] {
    return this.exposure.query(ctx, input)
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]): readonly CommitmentStateEvidence[] {
    return this.exposure.deliver(ctx, values)
  }
  readDelivered(ctx: ModelMemoryContext): readonly CommitmentStateEvidence[] {
    return this.exposure.readDelivered(ctx)
  }
  readExposedRefs(ctx: ModelMemoryContext): readonly ExposedCommitmentRef[] {
    const refs = new Map<string, ExposedCommitmentRef>()
    for (const { view } of this.exposure.readDelivered(ctx)) {
      const ref = Object.freeze({ ...view.ref, mutationVersion: view.mutationVersion })
      refs.set(JSON.stringify([ref.id, ref.version, ref.deletionEpoch, ref.mutationVersion]), ref)
    }
    return Object.freeze([...refs.values()])
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    return this.exposure.captureGuard(ctx)
  }
}
