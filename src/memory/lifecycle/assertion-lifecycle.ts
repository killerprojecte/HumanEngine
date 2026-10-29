import type { SemanticQualification } from '../semantic/contracts.ts'
import type { SemanticPrivacy } from './semantic-privacy.ts'
import type { KnowledgePrivacy } from './knowledge-privacy.ts'
import type { Store } from '../../store.ts'
import type { CommitmentService } from '../commitments/service.ts'
import type { CommitmentRef } from '../domain/commitments.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { SupportProof as Support } from '../domain/proofs.ts'
import { fingerprint } from '../domain/record-values.ts'
import { revoke } from '../recovery/ledger.ts'
import type { RelationCandidates } from '../relations/candidates.ts'
import {
  readAssertionRow as read,
  type AssertionRepository,
  type AssertionRow as Row,
} from '../storage/assertion-repository.ts'
import type { MemoryRevisionCoordinator } from '../write/revision-coordinator.ts'
import type { CommitmentPrivacy } from './commitment-privacy.ts'
import type { ProofInvalidation } from './proof-invalidation.ts'
/** Synchronous lifecycle ordering shared by live operations and maintenance replay. */
export class AssertionLifecycle {
  constructor(
    private readonly store: Pick<Store, 'db' | 'recall' | 'recoveryQuarantined'>,
    private readonly repository: AssertionRepository,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly relations: RelationCandidates,
    private readonly commitments: CommitmentService,
    private readonly privacy: CommitmentPrivacy,
    private readonly lifecycle: ProofInvalidation<MemoryRecord, Support>,
    private readonly terminalMetadata?: (id: string) => void,
    private readonly sourceMetadata?: (sceneId: string, messageId: string) => void,
    private readonly knowledgePrivacy?: KnowledgePrivacy,
    private readonly semanticPrivacy?: SemanticPrivacy,
  ) {}
  delete(id: string): boolean {
    return this.revisions.transaction(() => {
      const memory = this.repository.get(id)
      if (!memory) return false
      const beforeCommitments = this.privacy.capture()
      let beforeSemantic: ReadonlyMap<string, readonly SemanticQualification[]> | undefined
      try {
        beforeSemantic = this.semanticPrivacy?.capture()
      } catch (error) {
        revoke(this.store.db, {
          kind: 'assertion',
          id,
          throughVersion: this.revisions.history.state(id).version,
          terminal: true,
        })
        throw error
      }
      revoke(this.store.db, {
        kind: 'assertion',
        id,
        throughVersion: this.revisions.history.state(id).version,
        terminal: true,
      })
      revoke(this.store.db, {
        kind: 'fingerprint',
        value: fingerprint(memory.scope, memory.content),
      })
      this.store.db
        .prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
        .run(fingerprint(memory.scope, memory.content), new Date().toISOString())
      if (memory.origin === 'learned' && memory.scope.kind === 'sharedPublicKnowledge') {
        const deletedAt = new Date().toISOString()
        const insert = this.store.db.prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
        for (const sceneId of new Set(memory.sources.map((source) => source.sceneId))) {
          const value = fingerprint({ kind: 'scene', sceneId }, memory.content)
          revoke(this.store.db, { kind: 'fingerprint', value })
          insert.run(value, deletedAt)
        }
      }
      this.purgeHistoryDependents(id)
      this.relations.purgeAssertion(id)
      this.commitments.purgeAssertion(id)
      this.terminalMetadata?.(id)
      this.revisions.history.erase(id)
      this.invalidateDependents(id)
      this.store.db.prepare('DELETE FROM memories WHERE id=?').run(id)
      this.refreshSupport()
      this.privacy.refresh(beforeCommitments)
      this.knowledgePrivacy?.refresh(beforeCommitments)
      this.semanticPrivacy?.refresh(beforeCommitments, beforeSemantic)
      return true
    })()
  }
  redactRecoveredHistory(id: string): void {
    if (!this.store.recoveryQuarantined) throw new Error('recovery_maintenance_required')
    this.revisions.transaction(() => {
      this.purgeHistoryDependents(id)
      this.relations.purgeAssertion(id)
      this.revisions.history.purge(id)
      if (this.repository.get(id)) this.revisions.touch(id)
      this.refreshSupport()
    })()
  }

  eraseRecoveredAssertion(id: string): void {
    if (!this.store.recoveryQuarantined) throw new Error('recovery_maintenance_required')
    this.revisions.transaction(() => {
      if (this.repository.get(id)) {
        this.delete(id)
        return
      }
      this.purgeHistoryDependents(id)
      this.relations.purgeAssertion(id)
      this.commitments.purgeAssertion(id)
      this.terminalMetadata?.(id)
      this.revisions.history.erase(id)
      this.invalidateDependents(id)
      this.refreshSupport()
    })()
  }

  /** Capture while authorized; execute synchronously inside the root recall transaction. */

  prepareRecall(sceneId: string, messageId: string): () => string[] {
    const before = this.privacy.capture()
    const beforeSemantic = this.semanticPrivacy?.capture()
    let used = false
    return () => {
      if (used) throw new Error('memory_recall_preparation_consumed')
      used = true
      return this.recallPrepared(sceneId, messageId, before, beforeSemantic)
    }
  }
  recall(sceneId: string, messageId: string): string[] {
    return this.revisions.transaction(() => {
      let execute: () => string[]
      try {
        execute = this.prepareRecall(sceneId, messageId)
      } catch (error) {
        this.store.recall(sceneId, messageId)
        throw error
      }
      return execute()
    })()
  }
  private recallPrepared(
    sceneId: string,
    messageId: string,
    beforeCommitments: ReadonlyMap<string, CommitmentRef>,
    beforeSemantic?: ReadonlyMap<string, readonly SemanticQualification[]>,
  ): string[] {
    return this.revisions.transaction(() => {
      // Root intent precedes all derivative redaction so crash recovery knows the actual operation.
      this.store.recall(sceneId, messageId)
      this.sourceMetadata?.(sceneId, messageId)
      this.commitments.purgeSource(sceneId, messageId)
      const redacted = new Set(this.revisions.history.redactSource(sceneId, messageId))
      const purged = new Set<string>()
      for (const id of redacted) {
        this.relations.purgeAssertion(id)
        this.purgeHistoryDependents(id, purged, redacted)
      }
      const rows = this.store.db
        .prepare(
          'SELECT m.* FROM memories m JOIN memory_sources s ON s.memory_id=m.id WHERE s.scene_id=? AND s.message_id=?',
        )
        .all(sceneId, messageId) as Row[]
      for (const row of rows) {
        const previous = read(row)
        this.purgeHistoryDependents(previous.id, purged, redacted)
        const sources = previous.sources.filter(
          (source) => source.sceneId !== sceneId || source.messageId !== messageId,
        )
        const next: MemoryRecord = {
          ...previous,
          sources,
          confidence: (previous.confidence * sources.length) / previous.sources.length,
          status: ['superseded', 'invalidated'].includes(previous.status)
            ? previous.status
            : sources.length
              ? 'disputed'
              : 'invalidated',
        }
        this.repository.saveSupports(
          previous.id,
          this.repository.supports(previous.id).map((proof) => {
            if (
              !proof.sources.some(
                (source) => source.sceneId === sceneId && source.messageId === messageId,
              )
            )
              return proof
            const { subjectWitness: _witness, sourceObservations: _observations, ...rest } = proof
            return rest
          }),
        )
        this.repository.write(next)
        if (!sources.length)
          this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(previous.id)
      }
      const affected = new Set(rows.map((row) => row.id))
      for (const memory of this.repository.all()) {
        if (memory.lineageStatus !== 'unverified') continue
        if (
          (memory.scope.kind === 'scene' && memory.scope.sceneId === sceneId) ||
          memory.sources.some((source) => source.sceneId === sceneId)
        ) {
          this.purgeHistoryDependents(memory.id, purged, redacted)
          this.repository.write({ ...memory, lineageStatus: 'quarantined' })
          this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(memory.id)
          affected.add(memory.id)
        }
      }
      for (const id of this.refreshSupport()) affected.add(id)
      this.privacy.refresh(beforeCommitments)
      this.knowledgePrivacy?.refresh(beforeCommitments)
      this.semanticPrivacy?.refresh(beforeCommitments, beforeSemantic)
      return [...affected]
    })()
  }

  dependsOn(id: string, wanted: string, seen = new Set<string>()): boolean {
    return this.lifecycle.dependsOn(id, wanted, seen)
  }

  /** Provenance maintenance only; temporal/version applicability stays in the read layer. */
  refreshSupport(): string[] {
    return this.revisions.transaction(() => this.lifecycle.refreshSupport())()
  }
  private purgeHistoryDependents(
    id: string,
    seen = new Set<string>(),
    alreadyPurged = new Set<string>(),
  ) {
    this.lifecycle.purgeHistoryDependents(id, seen, alreadyPurged)
  }
  invalidateDependents(id: string, legacyOnly = false) {
    this.lifecycle.invalidateDependents(
      id,
      (proof) => !legacyOnly || proof.parentReferences === undefined,
    )
  }
}
