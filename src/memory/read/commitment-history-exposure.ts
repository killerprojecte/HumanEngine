import { isDeepStrictEqual } from 'node:util'
import type {
  CommitmentContext,
  CommitmentExposure as ExposedRef,
  CommitmentRef,
  CommitmentView,
} from '../domain/commitments.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export interface CommitmentHistoryResult {
  revisions: CommitmentView[]
  coverage: 'limited'
  interpretation: 'previously_recorded_not_current_truth'
}
export interface CommitmentHistoryEvidence extends CommitmentHistoryResult {
  stateKind: 'commitment_history'
  anchor: ExposedRef
  sourceBodyCoverage: 'unavailable'
  historyWindow: { maxRevisions: 20; omittedRevisions: number; throughVersion: number }
}
export interface CommitmentHistoryExposurePort {
  commitments: {
    history(ctx: CommitmentContext, ref: CommitmentRef): CommitmentHistoryResult | undefined
    current(ctx: CommitmentContext, ref: CommitmentRef): CommitmentView | undefined
  }
  readCurrentExposedRefs(ctx: ModelMemoryContext): readonly ExposedRef[]
  enabled(ctx: ModelMemoryContext): boolean
  ready(ctx: ModelMemoryContext): boolean
}
interface PreparedHistory {
  exposure: StateExposure<CommitmentHistoryEvidence, undefined>
  values: readonly CommitmentHistoryEvidence[]
}
const sameRef = (a: CommitmentRef, b: CommitmentRef) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
const context = (ctx: ModelMemoryContext, anchor: ExposedRef): CommitmentContext => ({
  sceneId: ctx.scene.id,
  simulation: ctx.mode === 'simulation',
  provenance: 'contextual',
  exposedRefs: [anchor],
})

/** Historical snapshots do not register current commitment/learning references. */
export class CommitmentHistoryExposure {
  private readonly prepared = new WeakMap<ModelMemoryContext, PreparedHistory[]>()
  constructor(private readonly port: CommitmentHistoryExposurePort) {}
  private snapshot(
    ctx: ModelMemoryContext,
    anchor: ExposedRef,
  ): CommitmentHistoryEvidence | undefined {
    const history = this.port.commitments.history(context(ctx, anchor), anchor)
    if (!history) return undefined
    if (
      history.coverage !== 'limited' ||
      history.interpretation !== 'previously_recorded_not_current_truth'
    )
      throw new Error('commitment_history_qualification_invalid')
    const revisions = history.revisions.filter(
      (view) => view.ref.id === anchor.id && view.ref.version <= anchor.version,
    )
    if (
      revisions.some(
        (view) => view.sceneId !== ctx.scene.id || view.ref.deletionEpoch !== anchor.deletionEpoch,
      )
    )
      throw new Error('commitment_history_scope_invalid')
    const ordered = [...revisions].sort((a, b) => a.ref.version - b.ref.version)
    return {
      stateKind: 'commitment_history',
      anchor: structuredClone(anchor),
      revisions: ordered.slice(-20),
      coverage: 'limited',
      interpretation: 'previously_recorded_not_current_truth',
      sourceBodyCoverage: 'unavailable',
      historyWindow: {
        maxRevisions: 20,
        omittedRevisions: Math.max(0, ordered.length - 20),
        throughVersion: anchor.version,
      },
    }
  }
  query(ctx: ModelMemoryContext, ref: CommitmentRef): readonly CommitmentHistoryEvidence[] {
    if (
      ctx.signal.aborted ||
      ctx.mode !== 'real' ||
      !this.port.enabled(ctx) ||
      !this.port.ready(ctx)
    )
      return Object.freeze([])
    const exposed = this.port.readCurrentExposedRefs(ctx).filter((item) => sameRef(item, ref))
    if (exposed.length !== 1) return Object.freeze([])
    const anchor = structuredClone(exposed[0]!)
    const initial = this.port.commitments.current(context(ctx, anchor), anchor)
    if (
      !initial ||
      initial.sceneId !== ctx.scene.id ||
      initial.mutationVersion !== anchor.mutationVersion ||
      !sameRef(initial.ref, anchor)
    )
      return Object.freeze([])
    const current = structuredClone(initial)
    const first = this.snapshot(ctx, anchor)
    if (!first) return Object.freeze([])
    // Each closure retains only its original authority, independent of a later turn signal.
    const exposure = new StateExposure<CommitmentHistoryEvidence, undefined>({
      query: () => [first],
      valid: (c, values) =>
        this.port.enabled(c) &&
        this.port.ready(c) &&
        isDeepStrictEqual(this.port.commitments.current(context(c, anchor), anchor), current) &&
        values.length === 1 &&
        isDeepStrictEqual(this.snapshot(c, anchor), values[0]),
      sourceIds: (values) => [
        ...new Set(
          values.flatMap((item) =>
            item.revisions.flatMap((view) =>
              [...view.evidence, ...view.requiredProposalGroups.flat()].flatMap((evidence) =>
                evidence.proofs.flatMap((proof) => proof.sources.map((source) => source.messageId)),
              ),
            ),
          ),
        ),
      ],
    })
    const values = exposure.query(ctx, undefined)
    if (values.length)
      this.prepared.set(ctx, [...(this.prepared.get(ctx) ?? []), { exposure, values }])
    return values
  }
  deliver(
    ctx: ModelMemoryContext,
    values: readonly unknown[],
  ): readonly CommitmentHistoryEvidence[] {
    if (!values.length) return Object.freeze([])
    const prepared = this.prepared.get(ctx)?.find((item) => item.values[0] === values[0])
    return prepared?.exposure.deliver(ctx, values) ?? Object.freeze([])
  }
  readDelivered(ctx: ModelMemoryContext): readonly CommitmentHistoryEvidence[] {
    return Object.freeze(
      (this.prepared.get(ctx) ?? []).flatMap((item) => item.exposure.readDelivered(ctx)),
    )
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const guards = (this.prepared.get(ctx) ?? []).flatMap((item) => {
      const guard = item.exposure.captureGuard(ctx)
      return guard ? [guard] : []
    })
    return guards.length ? () => guards.every((guard) => guard()) : undefined
  }
}
