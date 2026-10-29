import type { CommitmentExposure } from '../domain/commitments.ts'
import { currentSourceSnapshot } from '../../runtime/source-events.ts'
import { withBudgetContext } from '../../budget/native-attempt.ts'
import type { ProofParentReference } from '../domain/proofs.ts'
import { withModelPriority } from '../../scheduler.ts'
import { createHash } from 'node:crypto'
import type { Store } from '../../store.ts'
import type { RuntimeMessage, TurnContext } from '../../runtime.ts'
import type { MemoryService } from '../../memory.ts'
import { buildLearningCandidateSchema } from './contracts.ts'
import { ContextualLearningQueue } from './contextual-queue.ts'

export interface ExposedMemorySnapshot {
  id: string
  version: number
  deletionEpoch: number
  deleted: boolean
}
export interface ExposedRelationEntitySnapshot {
  entityId: string
  backingRef: ProofParentReference
}
interface ContextualProposalPorts {
  memory: Pick<MemoryService, 'list' | 'revisionState'>
  preview(sceneId: string): RuntimeMessage[]
  foregroundBusy(): boolean
  commit(
    sceneId: string,
    input: unknown,
    evidence: RuntimeMessage[],
    dependencies: ProofParentReference[],
    signal: AbortSignal,
    exposedEntities: readonly ExposedRelationEntitySnapshot[],
    exposedCommitments: readonly CommitmentExposure[],
    exposedKnowledgeEntities: readonly ExposedRelationEntitySnapshot[],
    exposedKnowledgeEdges: readonly {
      id: string
      version: number
      backing: ProofParentReference
    }[],
  ): Promise<unknown>
}
/** Trusted submission boundary. Model arguments never choose provenance or parent authority. */
export class ContextualProposals {
  readonly queue: ContextualLearningQueue
  constructor(
    private readonly store: Store,
    private readonly ports: ContextualProposalPorts,
  ) {
    this.queue = new ContextualLearningQueue({
      foregroundBusy: ports.foregroundBusy,
      onEvent: (event) =>
        store.log(
          event.status === 'failed' ? 'warn' : 'info',
          'learning',
          `contextual_learning_${event.status}`,
          {
            batchId: event.jobId,
            stage: 'learning.contextual',
            status:
              event.status === 'running'
                ? 'started'
                : event.status === 'failed'
                  ? 'failed'
                  : event.status === 'cancelled'
                    ? 'cancelled'
                    : event.status === 'discarded'
                      ? 'skipped'
                      : 'completed',
            reason: `contextual_learning_${event.status}`,
            ...(event.committed ? { counts: { committedWrites: 1 } } : {}),
          },
        ),
    })
  }
  submit(
    context: TurnContext,
    input: unknown,
    exposed: readonly ExposedMemorySnapshot[],
    exposedEntities: readonly ExposedRelationEntitySnapshot[] = [],
    trustedEntityValid?: () => boolean,
    exposedCommitments: readonly CommitmentExposure[] = [],
    trustedCommitmentValid?: () => boolean,
    trustedRead?: { identity: string; valid: () => boolean },
    exposedKnowledgeEntities: readonly ExposedRelationEntitySnapshot[] = [],
    trustedKnowledgeValid?: () => boolean,
    exposedKnowledgeEdges: readonly {
      id: string
      version: number
      backing: ProofParentReference
    }[] = [],
  ) {
    if (context.mode === 'simulation')
      return { status: 'rejected', reason: 'simulation_does_not_learn' }
    if (context.signal.aborted) return { status: 'rejected', reason: 'request_cancelled' }
    const sceneId = context.scene.id
    if (!this.store.scene(sceneId)?.learningEnabled)
      return { status: 'rejected', reason: 'learning_disabled' }
    const candidate = buildLearningCandidateSchema(
      this.store.settings.memoryGraphEnabled,
      this.store.settings.memoryCommitmentsEnabled,
      this.store.settings.memoryKnowledgeEnabled,
    ).parse(input)
    const knowledgeStates = structuredClone([...exposedKnowledgeEntities])
    const knowledgeEdges = structuredClone([...exposedKnowledgeEdges])
    if (trustedKnowledgeValid && !candidate.knowledge?.length)
      return { status: 'rejected', reason: 'knowledge_state_not_freeform_learning_authority' }
    if ((knowledgeStates.length || knowledgeEdges.length) && !trustedKnowledgeValid)
      return { status: 'rejected', reason: 'knowledge_authority_unavailable' }
    const commitmentStates = structuredClone([...exposedCommitments])
    if (
      trustedCommitmentValid &&
      (!candidate.commitment || candidate.commitment.kind === 'propose')
    )
      return { status: 'rejected', reason: 'commitment_state_not_freeform_learning_authority' }
    if (commitmentStates.length && !trustedCommitmentValid)
      return { status: 'rejected', reason: 'commitment_authority_unavailable' }
    const entityStates = structuredClone([...exposedEntities]).sort((a, b) =>
      a.entityId.localeCompare(b.entityId),
    )
    if (entityStates.length && !trustedEntityValid)
      return { status: 'rejected', reason: 'entity_authority_unavailable' }
    const sourceIds = [...new Set(candidate.sourceMessageIds)].sort()
    const evidence = structuredClone(
      (context.sourceSnapshots ?? this.ports.preview(sceneId)).filter((message) =>
        sourceIds.includes(message.id),
      ),
    )
    if (
      !context.sourceSnapshots &&
      evidence.some(
        (message) =>
          message.sourceEventRef || this.store.sourceEvents.registered(sceneId, message.id),
      )
    )
      return { status: 'rejected', reason: 'source_snapshot_unavailable' }
    if (evidence.some((message) => message.sourceEventRegistrationLimited))
      return { status: 'rejected', reason: 'source_event_registration_limited' }
    const parentStates = structuredClone([...exposed]).sort((a, b) => a.id.localeCompare(b.id))
    const dependencies = parentStates.map(({ id, version, deletionEpoch }) => ({
      id,
      version,
      deletionEpoch,
    }))
    const valid = () => {
      if (candidate.knowledge?.length && !this.store.settings.memoryKnowledgeEnabled) return false
      if (trustedKnowledgeValid) {
        try {
          if (!trustedKnowledgeValid()) return false
        } catch {
          return false
        }
      }
      if (trustedRead) {
        try {
          if (!trustedRead.valid()) return false
        } catch {
          return false
        }
      }
      if (!this.store.scene(sceneId)?.learningEnabled) return false
      if (entityStates.length) {
        if (
          !this.store.settings.memoryGraphEnabled ||
          !this.store.settings.memoryGraphAnswerEnabled
        )
          return false
      }
      if (trustedCommitmentValid) {
        try {
          if (!trustedCommitmentValid()) return false
        } catch {
          return false
        }
      }
      if (candidate.commitment && !this.store.settings.memoryCommitmentsEnabled) return false
      if (trustedEntityValid) {
        try {
          if (!trustedEntityValid()) return false
        } catch {
          return false
        }
      }
      if (
        (candidate.relations?.length || entityStates.length) &&
        !this.store.settings.memoryGraphEnabled
      )
        return false
      if (
        sourceIds.some(
          (id) =>
            !evidence.some(
              (m) =>
                m.id === id &&
                m.sceneId === sceneId &&
                m.origin === 'live' &&
                m.status === 'available' &&
                m.senderId !== 'bot' &&
                currentSourceSnapshot(this.store, m),
            ) || this.store.recalled(sceneId, id),
        )
      )
        return false
      const visible = new Set(
        this.ports.memory.list({ sceneId, simulation: false }).map((m) => m.id),
      )
      if (
        !entityStates.every(({ backingRef }) => {
          const current = this.ports.memory.revisionState(backingRef.id)
          return (
            visible.has(backingRef.id) &&
            !current.deleted &&
            current.version === backingRef.version &&
            current.deletionEpoch === backingRef.deletionEpoch
          )
        })
      )
        return false
      return parentStates.every((parent) => {
        const current = this.ports.memory.revisionState(parent.id)
        return (
          visible.has(parent.id) &&
          !current.deleted &&
          current.version === parent.version &&
          current.deletionEpoch === parent.deletionEpoch
        )
      })
    }
    const key = createHash('sha256')
      .update(
        JSON.stringify({
          candidate,
          sourceIds,
          sourceRefs: evidence.map((m) => [m.id, m.sourceEventRef ?? null]),
          parentStates,
          entityStates,
          commitmentStates,
          knowledgeStates,
          knowledgeEdges,
          readPolicyIdentity: trustedRead?.identity,
        }),
      )
      .digest('hex')
    return this.queue.enqueue({
      sceneId,
      key,
      sourceIds,
      validate: valid,
      committed: (result) => {
        if (
          !result ||
          typeof result !== 'object' ||
          !('status' in result) ||
          result.status !== 'saved' ||
          !('memory' in result)
        )
          return false
        const memory = result.memory
        return Boolean(
          memory &&
          typeof memory === 'object' &&
          'id' in memory &&
          typeof memory.id === 'string' &&
          memory.id.length,
        )
      },
      run: async (signal) => {
        if (signal.aborted || !valid()) return { status: 'rejected', reason: 'context_ineligible' }
        // commit performs its synchronous authoritative proposal before awaiting any indexing/review.
        return withBudgetContext(
          {
            turnId: `contextual:${createHash('sha256')
              .update(JSON.stringify([sceneId, key]))
              .digest('hex')}`,
            lane: 'realtime',
          },
          () =>
            withModelPriority(1, () =>
              this.ports.commit(
                sceneId,
                candidate,
                evidence,
                dependencies,
                signal,
                entityStates,
                commitmentStates,
                knowledgeStates,
                knowledgeEdges,
              ),
            ),
        )
      },
    })
  }
}
