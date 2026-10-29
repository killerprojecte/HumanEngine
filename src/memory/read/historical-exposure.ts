import { isDeepStrictEqual } from 'node:util'
import type { MemoryService } from '../../memory.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'
type Input = Parameters<MemoryService['historicalQuery']>[1]
type Result = ReturnType<MemoryService['historicalQuery']>
export type HistoricalStateEvidence = Result & { stateKind: 'assertion_history'; query: Input }
/** Historical receipts are explicitly inspectable evidence, never automatic new-learning authority. */
export class HistoricalExposure {
  private readonly states: StateExposure<HistoricalStateEvidence, Input>
  constructor(memory: Pick<MemoryService, 'historicalQuery'>, enabled: () => boolean) {
    const query = (ctx: ModelMemoryContext, input: Input): HistoricalStateEvidence => ({
      ...memory.historicalQuery(
        { sceneId: ctx.scene.id, simulation: ctx.mode === 'simulation' },
        input,
      ),
      stateKind: 'assertion_history',
      query: structuredClone(input),
    })
    this.states = new StateExposure({
      query: (ctx, input) => (enabled() ? [query(ctx, input)] : []),
      valid: (ctx, values) =>
        enabled() &&
        values.every((value) => {
          try {
            return isDeepStrictEqual(query(ctx, value.query), value)
          } catch {
            return false
          }
        }),
      sourceIds: (values) =>
        values.flatMap((value) =>
          value.revisions.flatMap((revision) =>
            revision.supports.flatMap((proof) => proof.sources.map((source) => source.messageId)),
          ),
        ),
    })
  }
  query(ctx: ModelMemoryContext, input: Input) {
    return this.states.query(ctx, input)
  }
  deliver(ctx: ModelMemoryContext, value: readonly unknown[]) {
    return this.states.deliver(ctx, value)
  }
  captureGuard(ctx: ModelMemoryContext) {
    return this.states.captureGuard(ctx)
  }
}
