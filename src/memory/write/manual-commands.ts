import { randomUUID } from 'node:crypto'
import type { Store } from '../../store.ts'
import type {
  MemoryContext,
  MemoryKind,
  MemoryRecord,
  MemoryScope,
  MemorySource,
  ProposalResult,
} from '../domain/contracts.ts'
import { fingerprint, uniqueSources, validateMemory } from '../domain/record-values.ts'
import type { AssertionAuthority } from '../read/assertion-authority.ts'
import type { AssertionRepository } from '../storage/assertion-repository.ts'
import { GuardedMemoryCommands, type MemoryCommandGuard } from './guarded-command.ts'
import type { MemoryRevisionCoordinator } from './revision-coordinator.ts'
/** Manual edits and reviewed public promotion; never automatic learning admission. */
export class ManualMemoryCommands {
  private readonly commands: GuardedMemoryCommands
  constructor(
    private readonly store: Pick<Store, 'db' | 'recalled' | 'scene'>,
    private readonly repository: AssertionRepository,
    private readonly authority: AssertionAuthority,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly lifecycle: {
      refreshSupport(): string[]
      invalidateDependents(id: string, legacyOnly?: boolean): void
    },
  ) {
    this.commands = new GuardedMemoryCommands(store.db)
  }
  /** Internal engine operation only, after conservative public-technical eligibility review. */
  promotePublic(ctx: MemoryContext, id: string): ProposalResult {
    if (ctx.simulation) return { status: 'rejected', reason: 'simulation_does_not_learn' }
    return this.revisions.transaction((): ProposalResult => {
      if (!this.store.scene(ctx.sceneId)?.learningEnabled)
        return { status: 'rejected', reason: 'learning_disabled' }
      const memory = this.repository.get(id)
      if (
        !memory ||
        memory.origin !== 'learned' ||
        memory.locked ||
        memory.status !== 'active' ||
        memory.scope.kind !== 'scene' ||
        memory.scope.sceneId !== ctx.sceneId ||
        memory.scope.personId !== undefined ||
        memory.lineageStatus !== 'verified' ||
        this.repository
          .supports(id)
          .some((proof) =>
            proof.parents.some(
              (parent) => this.repository.get(parent)?.scope.kind !== 'sharedPublicKnowledge',
            ),
          ) ||
        !['fact', 'term'].includes(memory.kind) ||
        memory.validUntil !== undefined ||
        !memory.sources.length ||
        memory.sources.some(
          (source) =>
            source.sceneId !== ctx.sceneId || this.store.recalled(source.sceneId, source.messageId),
        )
      )
        return { status: 'rejected', reason: 'public_promotion_not_allowed' }
      const scope: MemoryScope = { kind: 'sharedPublicKnowledge' }
      const fp = fingerprint(scope, memory.content)
      if (this.store.db.prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'manually_deleted' }
      if (this.store.db.prepare('SELECT 1 FROM memories WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'public_memory_already_exists' }
      const promoted: MemoryRecord = { ...memory, scope }
      validateMemory(promoted)
      this.revisions.history.purge(id)
      this.repository.write(promoted)
      this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id)
      return { status: 'saved', memory: this.authority.sanitize(promoted) }
    })()
  }

  create(input: {
    scope: MemoryScope
    content: string
    kind: MemoryKind
    sources?: MemorySource[]
    importance?: MemoryRecord['importance']
    confidence?: number
    locked?: boolean
    validFrom?: string
    epistemicKind?: MemoryRecord['epistemicKind']
    occurredAt?: string
    validUntil?: string
  }): MemoryRecord {
    const memory: MemoryRecord = {
      id: randomUUID(),
      scope: input.scope,
      content: input.content.trim(),
      kind: input.kind,
      sources: uniqueSources(input.sources ?? []),
      importance: input.importance ?? 'medium',
      confidence: input.confidence ?? 1,
      learnedAt: new Date().toISOString(),
      status: 'active',
      locked: input.locked ?? false,
      origin: 'manual',
      ...(input.validFrom ? { validFrom: input.validFrom } : {}),
      ...(input.epistemicKind ? { epistemicKind: input.epistemicKind } : {}),
      ...(input.occurredAt ? { occurredAt: input.occurredAt } : {}),
      ...(input.validUntil ? { validUntil: input.validUntil } : {}),
    }
    validateMemory(memory)
    this.revisions.transaction(() => {
      if (memory.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)))
        throw new Error('recalled_source')
      this.repository.write(memory)
    })()
    return memory
  }

  update(
    id: string,
    patch: Partial<
      Pick<
        MemoryRecord,
        | 'content'
        | 'kind'
        | 'importance'
        | 'confidence'
        | 'status'
        | 'locked'
        | 'validFrom'
        | 'epistemicKind'
        | 'occurredAt'
        | 'validUntil'
      >
    >,
  ): MemoryRecord {
    return this.revisions.transaction(() => {
      const previous = this.repository.get(id)
      if (!previous) throw new Error('memory_not_found')
      this.revisions.beforeWrite(id)
      // Revalidation requires an explicit reviewed content field, even when unchanged.
      const reviewedContent = Object.hasOwn(patch, 'content')
      const lockOnly = Object.keys(patch).every((key) => key === 'locked')
      const next: MemoryRecord = {
        ...previous,
        ...patch,
        id: previous.id,
        scope: previous.scope,
        sources: previous.sources,
        origin: lockOnly ? previous.origin : 'manual',
        lineageStatus: reviewedContent ? undefined : previous.lineageStatus,
      }
      validateMemory(next)
      if (next.content !== previous.content)
        this.store.db
          .prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
          .run(fingerprint(previous.scope, previous.content), new Date().toISOString())
      if (next.content !== previous.content) this.lifecycle.invalidateDependents(id, true)
      if (reviewedContent)
        this.store.db.prepare('DELETE FROM memory_supports WHERE memory_id=?').run(id)
      this.repository.write(next)
      this.lifecycle.refreshSupport()
      if (
        next.content !== previous.content ||
        next.status === 'invalidated' ||
        next.status === 'superseded'
      )
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id)
      return next
    })()
  }
  updateGuarded(
    id: string,
    patch: Parameters<ManualMemoryCommands['update']>[1],
    guard: MemoryCommandGuard,
  ): MemoryRecord {
    return this.revisions.transaction(() =>
      this.commands.run(
        id,
        patch,
        guard,
        () => this.revisions.history.state(id),
        () => {
          this.update(id, patch)
          return this.repository.get(id)!
        },
        () => {
          const record = this.repository.get(id)
          if (!record) throw new Error('memory_not_found')
          return record
        },
        () => this.revisions.checkpoint(),
      ),
    )()
  }

  lock(id: string, locked: boolean): MemoryRecord {
    return this.update(id, { locked })
  }
}
