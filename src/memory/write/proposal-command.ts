import type { KnowledgeService } from '../knowledge/service.ts'
import { prepareKnowledgeAssertion } from '../knowledge/admission.ts'
import { randomUUID } from 'node:crypto'
import type { Store } from '../../store.ts'
import type { CommitmentService } from '../commitments/service.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  MemoryRecord,
  ProposalResult,
} from '../domain/contracts.ts'
import {
  fingerprint,
  hash,
  historicalFingerprint,
  uniqueSources,
  validateMemory,
} from '../domain/record-values.ts'
import type { AssertionAuthority } from '../read/assertion-authority.ts'
import type { RelationCandidates } from '../relations/candidates.ts'
import {
  readAssertionRow as read,
  type AssertionRepository,
} from '../storage/assertion-repository.ts'
import { ReconciliationPolicy } from './reconciliation-policy.ts'
import type { MemoryRevisionCoordinator } from './revision-coordinator.ts'
import { SourceAdmission } from './source-admission.ts'
export class ProposalCommand {
  private readonly admission: SourceAdmission
  private readonly reconciliation: ReconciliationPolicy
  constructor(
    private readonly store: Pick<Store, 'db' | 'recalled' | 'scene' | 'sourceEvents'>,
    private readonly repository: AssertionRepository,
    private readonly authority: AssertionAuthority,
    private readonly revisions: MemoryRevisionCoordinator,
    private readonly relations: RelationCandidates,
    private readonly commitments: CommitmentService,
    private readonly knowledge: KnowledgeService,
    private readonly lifecycle: {
      refreshSupport(): string[]
      dependsOn(id: string, wanted: string): boolean
    },
  ) {
    this.admission = new SourceAdmission(store, repository, authority)
    this.reconciliation = new ReconciliationPolicy(store, repository)
  }
  propose(ctx: MemoryContext, candidate: MemoryCandidate): ProposalResult {
    return this.revisions.transaction(() => {
      let assertionContext = ctx
      let assertionCandidate = candidate
      if (candidate.knowledge?.length) {
        const prepared = prepareKnowledgeAssertion(ctx, candidate)
        if (!prepared.ok) return { status: 'rejected' as const, reason: prepared.reason }
        assertionContext = prepared.context
        assertionCandidate = prepared.candidate
      }
      if (candidate.commitment) {
        if (candidate.epistemicKind !== 'explicit')
          return { status: 'rejected' as const, reason: 'commitment_explicit_declaration_required' }
        const source = candidate.sources.length === 1 ? candidate.sources[0] : undefined
        const evidence =
          source &&
          ctx.sourceEvidence?.find(
            (item) =>
              item.sceneId === source.sceneId &&
              item.messageId === source.messageId &&
              item.senderId === source.senderId &&
              item.available &&
              !item.isBot,
          )
        if (
          !source ||
          !evidence?.sourceText?.trim() ||
          evidence.sourceText.length > 2000 ||
          candidate.subject?.kind !== 'self' ||
          candidate.subject.sourceMessageId !== source.messageId
        )
          return { status: 'rejected' as const, reason: 'commitment_literal_self_source_required' }
        if (candidate.relations?.length || candidate.supersedesId || candidate.mergeIntoId)
          return { status: 'rejected' as const, reason: 'commitment_mixed_write_denied' }
        const declaration = hash(
          JSON.stringify([
            source.sceneId,
            source.messageId,
            source.senderId,
            evidence.eventRef ?? null,
            candidate.commitment,
          ]),
        )
        // Persist a typed marker only: neither raw chat nor model-expanded plan text belongs in this backing.
        assertionCandidate = {
          ...candidate,
          content: `Model-extracted plan declaration (${candidate.commitment.kind}); reference ${declaration}. Not independently verified.`,
          kind: 'claim',
          epistemicKind: 'explicit',
          occurredAt: undefined,
          validFrom: undefined,
          validUntil: undefined,
        }
        assertionContext = {
          ...ctx,
          provenance: 'source-only',
          dependencyMemoryIds: [],
          dependencyParentReferences: [],
          internalDeclaration: true,
        }
      }
      const result = this.proposeAssertion(assertionContext, assertionCandidate)
      if (
        result.status !== 'saved' ||
        (!candidate.relations?.length && !candidate.commitment && !candidate.knowledge?.length)
      )
        return result
      this.revisions.checkpoint()
      const state = this.revisions.history.state(result.memory.id)
      const ref = {
        id: result.memory.id,
        version: state.version,
        deletionEpoch: state.deletionEpoch,
      }
      const trusted = { ...ctx, provenance: ctx.provenance ?? ('source-only' as const) }
      const relations = candidate.relations?.length
        ? this.relations.admit(trusted, ref, candidate.relations)
        : undefined
      const commitment = candidate.commitment
        ? this.commitments.admit(
            { ...trusted, exposedRefs: ctx.exposedCommitments },
            ref,
            candidate.commitment,
          )
        : undefined
      const knowledge = candidate.knowledge?.length
        ? this.knowledge.admit(
            {
              ...trusted,
              exposedEntities: ctx.exposedKnowledgeEntities,
              exposedEdges: ctx.exposedKnowledgeEdges,
              exposedAssertions: ctx.dependencyParentReferences,
            },
            ref,
            candidate.knowledge,
          )
        : undefined
      return {
        ...result,
        ...(knowledge ? { knowledge } : {}),
        ...(relations ? { relations } : {}),
        ...(commitment ? { commitment } : {}),
      }
    })()
  }

  private proposeAssertion(ctx: MemoryContext, candidate: MemoryCandidate): ProposalResult {
    if (ctx.simulation) return { status: 'rejected', reason: 'simulation_does_not_learn' }
    return this.revisions.transaction((): ProposalResult => {
      const admission = this.admission.prepare(ctx, candidate)
      if (admission.status === 'rejected') return admission
      candidate = admission.candidate
      const { scope, proof, attribution, parents } = admission
      const reconciliation = this.reconciliation.inspect(
        ctx,
        candidate,
        scope,
        attribution,
        parents,
      )
      if (reconciliation.status === 'rejected') return reconciliation
      const { fp, existingRow, superseded } = reconciliation
      const applyCorrection = () => {
        if (!superseded) return
        this.repository.write({ ...superseded, status: 'superseded' })
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(superseded.id)
      }
      const existing = existingRow ? read(existingRow) : undefined
      if (
        existing &&
        (parents.includes(existing.id) ||
          parents.some((id) => this.lifecycle.dependsOn(id, existing.id)))
      )
        return { status: 'rejected', reason: 'cyclic_memory_dependencies' }
      const history =
        existing && superseded ? this.correctionHistory(existing.id, superseded) : undefined
      const reverting =
        existing?.status === 'superseded' &&
        !existing.locked &&
        existing.origin === 'learned' &&
        existing.sources.length > 0 &&
        !existing.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)) &&
        superseded !== undefined &&
        history !== undefined &&
        candidate.sources.every(
          (source) =>
            !history.some((record) =>
              record.sources.some(
                (old) => old.sceneId === source.sceneId && old.messageId === source.messageId,
              ),
            ),
        )
      if (existingRow && !reverting) {
        const existing = read(existingRow)
        if (
          existing.locked ||
          existing.origin === 'manual' ||
          existing.status === 'superseded' ||
          existing.status === 'invalidated' ||
          existing.lineageStatus === 'quarantined'
        )
          return { status: 'rejected', reason: 'manual_or_invalid_record' }
        if (existing.supersedesId) {
          const history = this.correctionHistory(existing.id, existing)
          if (
            !history ||
            candidate.sources.some(
              (source) =>
                !existing.sources.some(
                  (current) =>
                    current.sceneId === source.sceneId && current.messageId === source.messageId,
                ) &&
                history
                  .slice(1)
                  .some((record) =>
                    record.sources.some(
                      (old) => old.sceneId === source.sceneId && old.messageId === source.messageId,
                    ),
                  ),
            )
          )
            return { status: 'rejected', reason: 'invalid_source_evidence' }
        }
        // Both exact and semantic duplicates preserve the target identity, content, and metadata.
        const sources = uniqueSources([...existing.sources, ...candidate.sources])
        const independent = new Set(sources.map((source) => source.senderId)).size
        const next = {
          ...existing,
          sources,
          confidence: Math.min(
            0.85,
            existing.confidence +
              (independent > new Set(existing.sources.map((source) => source.senderId)).size
                ? 0.05
                : 0),
          ),
        }
        validateMemory(next)
        applyCorrection()
        this.repository.write(next)
        // Semantic duplicates preserve existing text, so only exact-content
        // corroboration can establish an alternative proof of that text.
        if (ctx.provenance && fp === fingerprint(existing.scope, existing.content)) {
          const supports = this.repository.supports(existing.id)
          supports.push(proof)
          this.repository.saveSupports(existing.id, supports)
          next.lineageStatus = 'verified'
          this.repository.write(next)
        }
        if (superseded) this.lifecycle.refreshSupport()
        return { status: 'saved', memory: this.authority.sanitize(next) }
      }
      const memory: MemoryRecord = {
        id: randomUUID(),
        scope,
        content: candidate.content.trim(),
        kind: candidate.kind,
        sources: uniqueSources(candidate.sources),
        importance: candidate.importance ?? 'medium',
        confidence: Math.min(candidate.confidence ?? 0.5, 0.7),
        learnedAt: new Date().toISOString(),
        status: 'active',
        locked: false,
        origin: 'learned',
        ...(ctx.internalDeclaration
          ? {
              internalUse:
                ctx.internalDeclaration === 'knowledge_declaration'
                  ? ('knowledge_declaration' as const)
                  : ('commitment_declaration' as const),
            }
          : {}),
        lineageStatus: ctx.provenance ? 'verified' : 'unverified',
        ...(candidate.validFrom ? { validFrom: candidate.validFrom } : {}),
        ...(candidate.epistemicKind ? { epistemicKind: candidate.epistemicKind } : {}),
        ...(candidate.occurredAt ? { occurredAt: candidate.occurredAt } : {}),
        ...(candidate.validUntil ? { validUntil: candidate.validUntil } : {}),
        ...(candidate.supersedesId ? { supersedesId: candidate.supersedesId } : {}),
      }
      validateMemory(memory)
      // A return to an older preference is a new statement, not a resurrection:
      // retain the old version and its sources without creating a supersession cycle.
      if (reverting)
        this.store.db
          .prepare('UPDATE memories SET fingerprint=? WHERE id=?')
          .run(historicalFingerprint(existing!), existing!.id)
      applyCorrection()
      this.repository.write(memory)
      if (ctx.provenance)
        this.repository.saveSupports(memory.id, [{ ...proof, sources: memory.sources }])
      if (superseded) this.lifecycle.refreshSupport()
      return { status: 'saved', memory: this.authority.sanitize(memory) }
    })()
  }

  private correctionHistory(id: string, current: MemoryRecord): MemoryRecord[] | undefined {
    const visited = new Set<string>()
    const history: MemoryRecord[] = []
    let found = false
    let previous: MemoryRecord | undefined = current
    while (previous && !visited.has(previous.id)) {
      history.push(previous)
      if (previous.id === id) found = true
      visited.add(previous.id)
      if (!previous.supersedesId) break
      previous = this.repository.get(previous.supersedesId)
      if (!previous) return undefined
    }
    return found && previous && !previous.supersedesId ? history : undefined
  }
}
