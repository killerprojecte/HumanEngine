import { ModelReadPolicy } from '../read/model-read-policy.ts'
import type { RuntimeMessage } from '../../runtime.ts'
import type { Router } from '../../models.ts'
import { ContextProjection } from '../../runtime/context-projection.ts'
import { buildLearningCandidateSchema } from './contracts.ts'
import {
  relationExtractionInstructions,
  commitmentExtractionInstructions,
  knowledgeExtractionInstructions,
} from './subject-input.ts'
import { extractSources } from './extraction.ts'
import { repairSources } from './repair.ts'
import { reconcileSources } from './reconciliation.ts'
import { persistCandidates } from './persistence.ts'
import type {
  LearningPorts,
  CommitProgress,
  BatchStore,
  SourceStageContext,
} from './batch-contracts.ts'

/** Compose one batch inside the caller's existing ALS flow/priority/budget. Never retry here. */
export async function runLearningBatch(input: {
  sceneId: string
  messages: RuntimeMessage[]
  signal: AbortSignal
  store: BatchStore
  ports: LearningPorts
  isDisposed: () => boolean
  progress: CommitProgress
}): Promise<boolean> {
  const { sceneId, messages, signal, store, ports, isDisposed, progress } = input
  const candidateSchema = buildLearningCandidateSchema(
    store.settings.memoryGraphEnabled,
    store.settings.memoryCommitmentsEnabled,
    store.settings.memoryKnowledgeEnabled,
    true,
  )
  const relationInstructions =
    relationExtractionInstructions(store.settings.memoryGraphEnabled) +
    commitmentExtractionInstructions(store.settings.memoryCommitmentsEnabled) +
    (store.settings.memoryKnowledgeEnabled ? knowledgeExtractionInstructions(true) : '')
  const readPolicy = new ModelReadPolicy(store)
  const readTicket = readPolicy.personalizationAllowed(sceneId)
    ? readPolicy.capture(sceneId)
    : undefined
  const assertRead = () => {
    if (readTicket) readPolicy.assert(readTicket)
  }
  const projection = new ContextProjection(sceneId)
  const baseRouter = ports.router(assertRead)
  const router = {
    complete: async (...args: Parameters<Router['complete']>) => {
      assertRead()
      const response = await baseRouter.complete(...args)
      assertRead()
      return {
        ...response,
        toolCalls: response.toolCalls.map((call) => {
          const arguments_ = { ...call.arguments }
          // Extraction never has reconciliation authority (as below).
          if (['learning.propose', 'learning.review_empty'].includes(call.name)) {
            delete arguments_.mergeIntoId
            delete arguments_.supersedesId
          }
          try {
            return { ...call, arguments: projection.restoreArguments(arguments_) }
          } catch (error) {
            if (
              !(error instanceof Error) ||
              !['context_reference_unknown', 'context_reference_cross_scene'].includes(
                error.message,
              )
            )
              throw error
            store.log('warn', 'learning', error.message, { sceneId })
            // Retain call position and budget, rejecting this candidate only.
            return { ...call, arguments: { invalidReference: true } }
          }
        }),
      }
    },
  }
  const related = readTicket
    ? await ports.searchMemory(
        { sceneId, simulation: false },
        messages
          .flatMap((message) =>
            message.parts.filter((part) => part.type === 'text').map((part) => part.text),
          )
          .join(' ')
          .slice(0, 4000),
        signal,
      )
    : []
  assertRead()
  if (signal.aborted) return false

  const context: SourceStageContext & { store: BatchStore } = {
    sceneId,
    messages,
    signal,
    store,
    projection,
    router,
    candidateSchema,
    relationInstructions,
  }
  const extraction = await extractSources(context)
  await repairSources(context, extraction, {
    unsupportedSourcePronouns: (...args) => ports.unsupportedSourcePronouns(...args),
  })
  assertRead()
  if (readTicket)
    await reconcileSources(context, extraction.calls, related, {
      memory: ports.memory,
      unsupportedSourcePronouns: (...args) => ports.unsupportedSourcePronouns(...args),
    })
  else store.log('info', 'learning', 'learning_reconciliation_read_disabled', { sceneId })
  assertRead()
  if (signal.aborted) return false
  const persisted = await persistCandidates(
    { sceneId, messages, signal, candidateSchema, store },
    extraction.calls,
    {
      proposeSource: async (...args) => {
        assertRead()
        const result = await ports.proposeSource(...args)
        assertRead()
        return result
      },
      proposeHabit: (...args) => {
        assertRead()
        return ports.proposeHabit(...args)
      },
    },
    isDisposed,
    progress,
  )
  return persisted && !signal.aborted
}
