import type { z } from '@hono/zod-openapi'
import type { Store } from '../../store.ts'
import type { RuntimeMessage } from '../../runtime.ts'
import type { Router, ToolCall } from '../../models.ts'
import type { MemoryService } from '../../memory.ts'
import type { HabitService } from '../../habits.ts'
import type { ContextProjection } from '../../runtime/context-projection.ts'
import type { CandidateSchema, buildLearningCandidateSchema } from './contracts.ts'
export interface LearningPorts {
  memory: Pick<MemoryService, 'reconciliationTargets' | 'adminList'>
  foregroundBusy(): boolean
  router(admit?: () => void): Pick<Router, 'complete'>
  searchMemory(
    ctx: { sceneId: string; simulation: boolean },
    query: string,
    signal?: AbortSignal,
  ): Promise<ReturnType<MemoryService['search']>>
  proposeSource(
    sceneId: string,
    input: unknown,
    evidence: RuntimeMessage[],
    signal?: AbortSignal,
  ): Promise<ReturnType<MemoryService['propose']>>
  proposeHabit(
    sceneId: string,
    simulation: boolean,
    input: unknown,
    evidence: RuntimeMessage[],
  ): ReturnType<HabitService['propose']>
  unsupportedSourcePronouns(
    sceneId: string,
    value: z.infer<typeof CandidateSchema>,
    evidence: RuntimeMessage[],
  ): boolean
}

/** In-memory batch context only. No stage owns admission, retries or contextual learning. */
export interface SourceStageContext {
  sceneId: string
  messages: RuntimeMessage[]
  signal: AbortSignal
  store: Pick<Store, 'settings' | 'log' | 'recalled'>
  projection: ContextProjection
  router: Pick<Router, 'complete'>
  candidateSchema: ReturnType<typeof buildLearningCandidateSchema>
  relationInstructions: string
}
export interface ExtractionResult {
  calls: ToolCall[]
  extraPassUsed: boolean
}
/** Intent, not savedCount, is the conservative no-replay barrier. Mutated before any write await. */
export interface CommitProgress {
  applying: boolean
  writePending: boolean
  totals: {
    candidateCount: number
    savedCount: number
    rejectedCount: number
    ignoredCount: number
  }
}
export type BatchStore = SourceStageContext['store'] & Pick<Store, 'scene'>
