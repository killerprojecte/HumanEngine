import { learningStep, learningResultReason } from '../../diagnostics/learning.ts'
import { HabitProposalSchema } from './contracts.ts'
import type { ToolCall } from '../../models.ts'
import type {
  SourceStageContext,
  LearningPorts,
  CommitProgress,
  BatchStore,
} from './batch-contracts.ts'

/** The only effectful stage. Unknown/partial writes leave progress permanently non-replayable. */
export async function persistCandidates(
  context: Pick<SourceStageContext, 'sceneId' | 'messages' | 'signal' | 'candidateSchema'> & {
    store: BatchStore
  },
  calls: ToolCall[],
  ports: Pick<LearningPorts, 'proposeHabit' | 'proposeSource'>,
  isDisposed: () => boolean,
  progress: CommitProgress,
): Promise<boolean> {
  const { sceneId, messages, signal, store, candidateSchema } = context
  const { totals } = progress
  totals.candidateCount = calls.length
  // Keep habit-first processing, but malformed candidates must not discard valid siblings.
  for (const name of ['habits.propose', 'learning.propose'] as const)
    for (const call of calls) {
      if (signal.aborted || isDisposed() || !store.scene(sceneId)?.learningEnabled) return false
      if (call.name !== name) continue
      const parsed =
        name === 'habits.propose'
          ? HabitProposalSchema.safeParse(call.arguments)
          : candidateSchema.safeParse(call.arguments)
      if (!parsed.success) {
        totals.rejectedCount++
        store.log('warn', 'learning', 'learning_candidate_invalid', { sceneId })
        continue
      }
      // From this point an exception may follow a partial commit: never replay the batch.
      progress.applying = true
      progress.writePending = true
      // Unexpected storage/provider failures still reach the batch failure handler.
      const persisted = await learningStep(
        store,
        'learning.persist',
        signal,
        async () =>
          name === 'habits.propose'
            ? ports.proposeHabit(sceneId, false, parsed.data, messages)
            : ports.proposeSource(sceneId, parsed.data, messages, signal),
        { counts: { candidateCount: 1 } },
        (result) => ({
          counts: {
            savedCount: result.status === 'saved' ? 1 : 0,
            rejectedCount: result.status === 'rejected' ? 1 : 0,
          },
          outcome: result.status === 'rejected' ? 'skipped' : 'completed',
          reason: learningResultReason(result),
          nextStep: 'next_candidate',
        }),
        // Persistence may commit before indexing completes. Only this stage
        // opts into reporting effects that cancellation cannot roll back.
        (result) =>
          result && result.status !== 'saved'
            ? undefined
            : {
                reason: 'partial_commit_no_replay',
                nextStep: 'inspect_partial_learning',
                counts: {
                  savedCount: result?.status === 'saved' ? 1 : 0,
                  unconfirmedCandidateCount: result ? 0 : 1,
                },
              },
      )
      progress.writePending = false
      if (persisted.status === 'saved') totals.savedCount++
      else if (persisted.status === 'rejected') totals.rejectedCount++
      else totals.ignoredCount++
    }
  return true
}
