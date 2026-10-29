import { isDeepStrictEqual } from 'node:util'
import type {
  CommitmentContext,
  CommitmentExposure,
  CommitmentRef,
  CommitmentView,
} from '../domain/commitments.ts'
import type { TemporalHistoryResult } from '../commitments/temporal-read.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export interface CommitmentTimelineEvidence extends TemporalHistoryResult {
  stateKind: 'commitment_timeline'
  anchor: CommitmentExposure
  asOfRecorded?: string
}
interface Port {
  commitments: {
    current(ctx: CommitmentContext, ref: CommitmentRef): CommitmentView | undefined
    temporalHistory(
      ctx: CommitmentContext,
      anchor: CommitmentExposure,
      query: { asOfRecorded?: string; limit?: number },
    ): TemporalHistoryResult | undefined
  }
  readCurrentExposedRefs(ctx: ModelMemoryContext): readonly CommitmentExposure[]
  enabled(ctx: ModelMemoryContext): boolean
  ready(ctx: ModelMemoryContext): boolean
}
const same = (a: CommitmentRef, b: CommitmentRef) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
const context = (c: ModelMemoryContext, a: CommitmentExposure): CommitmentContext => ({
  sceneId: c.scene.id,
  simulation: c.mode === 'simulation',
  provenance: 'contextual',
  exposedRefs: [a],
})
/** Forward-only declaration-state history; never supplies new learning or current plan authority. */
export class CommitmentTimelineExposure {
  private readonly prepared = new WeakMap<
    ModelMemoryContext,
    {
      exposure: StateExposure<CommitmentTimelineEvidence, undefined>
      values: readonly CommitmentTimelineEvidence[]
    }[]
  >()
  constructor(private readonly port: Port) {}
  query(
    ctx: ModelMemoryContext,
    ref: CommitmentRef,
    query: { asOfRecorded?: string; limit?: number } = {},
  ): readonly CommitmentTimelineEvidence[] {
    if (
      ctx.signal.aborted ||
      ctx.mode !== 'real' ||
      !this.port.enabled(ctx) ||
      !this.port.ready(ctx)
    )
      return Object.freeze([])
    const refs = this.port.readCurrentExposedRefs(ctx).filter((a) => same(a, ref))
    if (refs.length !== 1) return Object.freeze([])
    const anchor = structuredClone(refs[0]!),
      options = structuredClone(query)
    const current = this.port.commitments.current(context(ctx, anchor), anchor)
    if (
      !current ||
      current.mutationVersion !== anchor.mutationVersion ||
      !same(current.ref, anchor)
    )
      return Object.freeze([])
    const initial = structuredClone(current)
    const snapshot = (c: ModelMemoryContext): CommitmentTimelineEvidence | undefined => {
      const result = this.port.commitments.temporalHistory(context(c, anchor), anchor, options)
      if (!result) return undefined
      if (
        result.interpretation !== 'previously_recorded_declaration_state_not_current_truth' ||
        result.coverage !== 'limited' ||
        result.window.throughMutationVersion !== anchor.mutationVersion
      )
        throw Error('commitment_timeline_qualification_invalid')
      return {
        ...structuredClone(result),
        stateKind: 'commitment_timeline',
        anchor: structuredClone(anchor),
        ...(options.asOfRecorded ? { asOfRecorded: options.asOfRecorded } : {}),
      }
    }
    const first = snapshot(ctx)
    if (!first) return Object.freeze([])
    const exposure = new StateExposure<CommitmentTimelineEvidence, undefined>({
      query: () => [first],
      valid: (c, values) =>
        this.port.enabled(c) &&
        this.port.ready(c) &&
        isDeepStrictEqual(this.port.commitments.current(context(c, anchor), anchor), initial) &&
        values.length === 1 &&
        isDeepStrictEqual(snapshot(c), values[0]),
      sourceIds: (values) => [
        ...new Set(
          values.flatMap((v) =>
            v.recordedStates.flatMap((r) =>
              r.states.flatMap((s) =>
                [...s.evidence, ...s.requiredProposalGroups.flat()].flatMap((e) =>
                  e.proofs.flatMap((p) => p.sources.map((x) => x.messageId)),
                ),
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
  ): readonly CommitmentTimelineEvidence[] {
    if (!values.length) return Object.freeze([])
    return (
      this.prepared
        .get(ctx)
        ?.find((p) => p.values[0] === values[0])
        ?.exposure.deliver(ctx, values) ?? Object.freeze([])
    )
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const guards = (this.prepared.get(ctx) ?? []).flatMap((p) => {
      const g = p.exposure.captureGuard(ctx)
      return g ? [g] : []
    })
    return guards.length ? () => guards.every((g) => g()) : undefined
  }
}
