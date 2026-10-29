import type { Store } from '../../store.ts'
import type { MemoryCandidate, MemoryContext, MemoryScope } from '../domain/contracts.ts'
import type { SupportProof as Support } from '../domain/proofs.ts'
import { validateNewProof } from '../domain/proofs.ts'
import { uniqueSources } from '../domain/record-values.ts'
import type { AssertionAuthority } from '../read/assertion-authority.ts'
import { domainSourceCurrent } from '../read/domain-source-events.ts'
import type { AssertionRepository } from '../storage/assertion-repository.ts'
import { resolveSubjectAttribution } from './subject-attribution.ts'
/** Revalidate trusted sources and exact parents inside the command transaction. */
export class SourceAdmission {
  constructor(
    private readonly store: Pick<Store, 'recalled' | 'scene' | 'sourceEvents'>,
    private readonly repository: AssertionRepository,
    private readonly authority: AssertionAuthority,
  ) {}
  prepare(ctx: MemoryContext, candidate: MemoryCandidate) {
    if (!this.store.scene(ctx.sceneId)?.learningEnabled)
      return { status: 'rejected' as const, reason: 'learning_disabled' }
    if (!candidate.sources?.length)
      return { status: 'rejected' as const, reason: 'missing_source_evidence' }
    if (
      candidate.sources.some(
        (source) =>
          source.sceneId !== ctx.sceneId ||
          this.store.recalled(source.sceneId, source.messageId) ||
          !ctx.sourceEvidence?.some(
            (evidence) =>
              evidence.sceneId === source.sceneId &&
              evidence.messageId === source.messageId &&
              evidence.senderId === source.senderId &&
              evidence.available &&
              !evidence.isBot,
          ),
      )
    )
      return { status: 'rejected' as const, reason: 'invalid_source_evidence' }
    candidate = {
      ...candidate,
      sources: candidate.sources.map((source) => {
        const evidence = ctx.sourceEvidence!.find(
          (value) =>
            value.sceneId === source.sceneId &&
            value.messageId === source.messageId &&
            value.senderId === source.senderId,
        )!
        return {
          sceneId: source.sceneId,
          messageId: source.messageId,
          senderId: source.senderId,
          ...(evidence.eventRef ? { eventRef: structuredClone(evidence.eventRef) } : {}),
        }
      }),
    }
    if (candidate.sources.some((source) => !domainSourceCurrent(this.store, source)))
      return { status: 'rejected' as const, reason: 'source_event_changed' }
    const attribution = resolveSubjectAttribution(ctx, candidate)
    if (!attribution.ok) return { status: 'rejected' as const, reason: attribution.reason }
    candidate = {
      ...candidate,
      personId: attribution.personId,
      kind: attribution.kind,
      epistemicKind: attribution.epistemicKind,
    }
    const scope: MemoryScope = {
      kind: 'scene',
      sceneId: ctx.sceneId,
      ...(candidate.personId ? { personId: candidate.personId } : {}),
    }
    const parents = [...(ctx.dependencyMemoryIds ?? [])]
    const proof: Support = {
      sources: uniqueSources(candidate.sources),
      parents,
      parentReferences: structuredClone(ctx.dependencyParentReferences ?? []),
      ...(attribution.witness ? { subjectWitness: attribution.witness } : {}),
      sourceObservations: attribution.sourceObservations,
    }
    try {
      validateNewProof(proof, ctx.provenance ?? 'contextual')
    } catch {
      return { status: 'rejected' as const, reason: 'invalid_memory_dependencies' }
    }
    if (
      !ctx.provenance ||
      (ctx.provenance === 'contextual' && !ctx.dependencyMemoryIds) ||
      (ctx.provenance === 'source-only' && parents.length) ||
      parents.some((id) => {
        const parent = this.repository.get(id)
        return (
          !parent ||
          !this.authority.validParent(scope, id) ||
          !this.authority.currentApplicable(parent)
        )
      }) ||
      !this.authority.proofParentsAvailable(scope, proof, 'current')
    )
      return { status: 'rejected' as const, reason: 'invalid_memory_dependencies' }
    return { status: 'accepted' as const, candidate, scope, proof, attribution, parents }
  }
}
