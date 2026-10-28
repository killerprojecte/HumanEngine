import { startFlow, type FlowDetails, type FlowWriter, type FlowStatus } from './flow.ts'
import { modelFailureReason } from './model-flow.ts'

/** Observe only work actually executed; payloads and model-generated content are never logged. */
export async function learningStep<T>(
  writer: FlowWriter,
  stage: string,
  signal: AbortSignal | undefined,
  task: () => Promise<T>,
  details: FlowDetails = {},
  summarize: (
    value: T,
  ) => FlowDetails & { outcome?: Exclude<FlowStatus, 'started' | 'cancel_requested'> } = () => ({}),
  partialEffects?: (value?: T) => FlowDetails | undefined,
): Promise<T> {
  const span = startFlow(writer, stage, details)
  span.bindSignal(signal)
  return span.run(async () => {
    try {
      const value = await task()
      const { outcome = 'completed', ...summary } = summarize(value)
      const effects = signal?.aborted ? partialEffects?.(value) : undefined
      span.finish(
        effects ? 'partial' : signal?.aborted ? 'cancelled' : outcome,
        effects ?? (signal?.aborted ? { reason: 'request_cancelled', nextStep: 'stop' } : summary),
      )
      return value
    } catch (error) {
      const effects = partialEffects?.()
      span.finish(
        effects ? 'partial' : signal?.aborted ? 'cancelled' : 'failed',
        effects ?? {
          reason: signal?.aborted ? 'request_cancelled' : modelFailureReason(error),
          nextStep: signal?.aborted ? 'stop' : 'learning_failure_policy',
        },
        error,
      )
      throw error
    }
  })
}

export function learningSkipped(
  writer: FlowWriter,
  stage: string,
  reason: string,
  nextStep: string,
) {
  startFlow(writer, stage).finish('skipped', { reason, nextStep })
}

const rejectionReasons = new Set([
  'simulation_does_not_learn',
  'learning_disabled',
  'missing_source_evidence',
  'invalid_source_evidence',
  'unsupported_personal_pronoun',
  'request_cancelled',
  'invalid_memory_dependencies',
  'invalid_merge_request',
  'manually_deleted',
  'merge_requires_exact_content',
  'merge_not_allowed',
  'correction_not_allowed',
  'correction_depends_on_superseded_memory',
  'cyclic_memory_dependencies',
  'manual_or_invalid_record',
  'incompatible_evidence_scope',
  'invalid_candidate',
  'locked_legacy_candidate',
  'locked_or_invalid_feedback',
  'locked_or_invalidated',
  'not_available',
])
export function learningResultReason(result: { status: string; reason?: unknown }) {
  if (result.status === 'saved') return 'candidate_saved'
  return typeof result.reason === 'string' && rejectionReasons.has(result.reason)
    ? result.reason
    : result.status === 'rejected'
      ? 'candidate_rejected'
      : 'candidate_unchanged'
}
