import type { RelationProof } from '../domain/relations.ts'
import { digest } from './qualification.ts'
import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import {
  KnowledgeProposalSchema,
  KnowledgeRefSchema,
  type KnowledgeAuthority,
  type KnowledgeContext,
  type KnowledgeRef,
  type KnowledgeProposal,
  type KnowledgeEdge,
  type KnowledgeEdgeView,
  type KnowledgeEndpoint,
  type KnowledgeDependency,
  type KnowledgeReadDependency,
  type KnowledgeSpan,
} from './contracts.ts'
import { conflictKey, validateKnowledgePredicate, knowledgeAutomaticPolicy } from './registry.ts'
import { KnowledgeStorage, KnowledgeEdgeSchema } from './storage.ts'
import { knowledgeBackingFingerprint, sameRef, sameSource } from './qualification.ts'
import { KnowledgeReader } from './read.ts'
import { KnowledgeLifecycle } from './lifecycle.ts'
export { knowledgeBackingFingerprint } from './qualification.ts'
export class KnowledgeService {
  readonly storage: KnowledgeStorage
  private readonly reader: KnowledgeReader
  private readonly lifecycle: KnowledgeLifecycle
  constructor(
    db: Database.Database,
    private readonly authority: KnowledgeAuthority,
    private readonly options: {
      now?: () => number
      id?: () => string
      beforeWrite?: () => void
    } = {},
  ) {
    this.storage = new KnowledgeStorage(db, () => authority.assertReady(), options.beforeWrite)
    this.reader = new KnowledgeReader(this.storage, authority, () => this.now())
    this.lifecycle = new KnowledgeLifecycle(this.storage, (ctx, ref) =>
      this.reader.current(ctx, ref),
    )
  }
  private now() {
    return this.options.now?.() ?? Date.now()
  }
  private current(ctx: KnowledgeContext, ref: KnowledgeRef) {
    return this.reader.current(ctx, ref)
  }
  private project(...args: Parameters<KnowledgeReader['project']>) {
    return this.reader.project(...args)
  }
  query(...args: Parameters<KnowledgeReader['query']>) {
    return this.reader.query(...args)
  }
  discover(...args: Parameters<KnowledgeReader['discover']>) {
    return this.reader.discover(...args)
  }
  requalify(...args: Parameters<KnowledgeLifecycle['requalify']>) {
    return this.lifecycle.requalify(...args)
  }
  pruneUnavailableProof(...args: Parameters<KnowledgeLifecycle['pruneUnavailableProof']>) {
    return this.lifecycle.pruneUnavailableProof(...args)
  }
  purgeSource(...args: Parameters<KnowledgeLifecycle['purgeSource']>) {
    return this.lifecycle.purgeSource(...args)
  }
  purgeAssertion(...args: Parameters<KnowledgeLifecycle['purgeAssertion']>) {
    return this.lifecycle.purgeAssertion(...args)
  }
  private endpoint(
    ctx: KnowledgeContext,
    backing: KnowledgeRef,
    edgeId: string,
    input: KnowledgeProposal['subject'] | KnowledgeProposal['object'],
    dependencies: KnowledgeDependency[],
  ): KnowledgeEndpoint {
    if (input.kind === 'literal') return structuredClone(input)
    if (input.kind === 'self') {
      const view = this.current(ctx, backing)
      if (
        !view ||
        view.record.scope.kind !== 'scene' ||
        view.record.scope.personId !== input.personId ||
        !view.proofs.some(
          (p) =>
            p.subjectWitness?.kind === 'self' &&
            p.subjectWitness.personId === input.personId &&
            p.sources.some(
              (s) =>
                s.senderId === input.personId && s.messageId === p.subjectWitness!.sourceMessageId,
            ),
        )
      )
        throw new Error('knowledge_subject_unverified')
      return { kind: 'person', personId: input.personId }
    }
    if (input.kind === 'new')
      return {
        kind: 'entity',
        entity: {
          id: (this.options.id ?? randomUUID)(),
          sceneId: ctx.sceneId,
          kind: input.entityKind,
          title: input.title,
          backing,
          originEdgeId: edgeId,
        },
      }
    if (ctx.provenance !== 'contextual') throw new Error('knowledge_existing_source_only')
    const entity = this.storage.entity(input.entityId, ctx.sceneId)
    if (
      !entity ||
      entity.sceneId !== ctx.sceneId ||
      !ctx.exposedEntities?.some(
        (e) => e.entityId === entity.id && sameRef(e.backingRef, entity.backing),
      )
    )
      throw new Error('knowledge_entity_not_exposed')
    const view = this.current(ctx, entity.backing),
      origin = this.storage.get(entity.originEdgeId)
    if (!view || !origin || !this.project(ctx, origin))
      throw new Error('knowledge_entity_unavailable')
    dependencies.push({
      entity: structuredClone(entity),
      backingFingerprint: knowledgeBackingFingerprint(view.record),
      proofs: structuredClone(view.proofs),
    })
    return { kind: 'entity', entity }
  }
  private spans(
    ctx: KnowledgeContext,
    p: KnowledgeProposal,
    proofs: RelationProof[],
  ): KnowledgeSpan[] {
    return p.sourceSpans.map((span) => {
      const actual = this.authority.source(ctx, span.messageId),
        part = actual?.parts[span.partIndex]
      if (
        !actual ||
        actual.source.sceneId !== ctx.sceneId ||
        !part ||
        part.type !== 'text' ||
        span.start >= span.end ||
        span.end > part.text.length ||
        !proofs.some((p) => p.sources.some((s) => sameSource(s, actual.source)))
      )
        throw new Error('knowledge_span_unavailable')
      const boundary = (n: number) =>
        n > 0 &&
        n < part.text.length &&
        /[\uD800-\uDBFF]/.test(part.text[n - 1]!) &&
        /[\uDC00-\uDFFF]/.test(part.text[n]!)
      if (boundary(span.start) || boundary(span.end))
        throw new Error('knowledge_span_surrogate_boundary')
      if (
        span.stance === 'self' &&
        p.subject.kind === 'self' &&
        actual.source.senderId !== p.subject.personId
      )
        throw new Error('knowledge_stance_sender_mismatch')
      return {
        source: structuredClone(actual.source),
        rootId: digest([ctx.sceneId, actual.source.messageId]),
        partIndex: span.partIndex,
        start: span.start,
        end: span.end,
        spanHash: digest(part.text.slice(span.start, span.end)),
        stance: span.stance,
      }
    })
  }
  admit(
    ctx: KnowledgeContext,
    backing: KnowledgeRef,
    inputs: readonly unknown[],
  ): { created: KnowledgeEdge[]; rejected: { index: number; reason: string }[] } {
    this.storage.requireTransaction()
    if (ctx.simulation || !this.authority.enabled(ctx) || !this.authority.learningEnabled(ctx))
      return { created: [], rejected: [{ index: -1, reason: 'knowledge_disabled' }] }
    if (inputs.length > 3 || !KnowledgeRefSchema.safeParse(backing).success)
      return { created: [], rejected: [{ index: -1, reason: 'knowledge_input_limit' }] }
    const readDependencies: KnowledgeReadDependency[] = []
    if (
      (ctx.exposedEdges?.length ?? 0) > 40 ||
      (ctx.exposedAssertions?.length ?? 0) > 128 ||
      (ctx.provenance === 'source-only' &&
        (ctx.exposedEdges?.length || ctx.exposedAssertions?.length))
    )
      return {
        created: [],
        rejected: [{ index: -1, reason: 'knowledge_read_dependencies_invalid' }],
      }
    const readStage = { remaining: 512, memo: new Map<string, KnowledgeEdgeView>() }
    try {
      for (const backing of ctx.exposedAssertions ?? []) {
        if (
          !KnowledgeRefSchema.safeParse(backing).success ||
          readDependencies.some((d) => !d.edgeId && d.backing.id === backing.id)
        )
          throw new Error('knowledge_read_dependency_invalid')
        const view = this.current(ctx, backing)
        if (!view) throw new Error('knowledge_read_dependency_unavailable')
        readDependencies.push({
          backing: structuredClone(backing),
          backingFingerprint: knowledgeBackingFingerprint(view.record),
          proofs: structuredClone(view.proofs),
        })
      }
      for (const snapshot of ctx.exposedEdges ?? []) {
        if (readDependencies.some((d) => d.edgeId === snapshot.id))
          throw new Error('knowledge_read_dependency_duplicate')
        const original = this.storage.get(snapshot.id)
        if (
          !original ||
          original.version !== snapshot.version ||
          !sameRef(original.backing, snapshot.backing)
        )
          throw new Error('knowledge_read_dependency_stale')
        const edge = this.project(ctx, original, new Set(), readStage)
        if (!edge) throw new Error('knowledge_read_dependency_unavailable')
        readDependencies.push({
          edgeId: edge.id,
          edgeVersion: edge.version,
          backing: edge.backing,
          backingFingerprint: edge.backingFingerprint,
          proofs: structuredClone(edge.proofs),
        })
      }
    } catch (error) {
      return {
        created: [],
        rejected: [
          {
            index: -1,
            reason:
              error instanceof Error && error.message.startsWith('knowledge_')
                ? error.message
                : 'knowledge_read_dependencies_invalid',
          },
        ],
      }
    }
    const pending: { edge: KnowledgeEdge; key: string; duplicate: boolean }[] = []
    for (const [index, input] of inputs.entries())
      try {
        const proposal = KnowledgeProposalSchema.parse(input),
          view = this.current(ctx, backing)
        if (!view) throw new Error('knowledge_backing_unavailable')
        const id = (this.options.id ?? randomUUID)(),
          dependencies: KnowledgeDependency[] = []
        const subject = this.endpoint(ctx, backing, id, proposal.subject, dependencies),
          object = this.endpoint(ctx, backing, id, proposal.object, dependencies)
        validateKnowledgePredicate(proposal.predicate, subject, object, proposal.property)
        const spans = this.spans(ctx, proposal, view.proofs)
        if (
          spans.some((s) => s.stance === 'quoted' || s.stance === 'reported') &&
          (view.record.kind !== 'claim' ||
            !['reported', 'inferred', 'hypothesis'].includes(view.record.epistemicKind ?? ''))
        )
          throw new Error('knowledge_stance_backing_conflict')
        if (proposal.subject.kind !== 'self' && spans.some((s) => s.stance === 'self'))
          throw new Error('knowledge_self_stance_subject')
        const proofs = view.proofs.filter((p) =>
          p.sources.some((s) => spans.some((span) => sameSource(s, span.source))),
        )
        if (!proofs.length) throw new Error('knowledge_proof_unavailable')
        const key = digest([ctx.sceneId, backing, proposal, readDependencies]),
          previous = this.storage.byKey(key)
        if (pending.some((item) => item.key === key))
          throw new Error('knowledge_duplicate_proposal')
        if (previous) {
          const current = this.project(ctx, previous)
          if (!current) throw new Error('knowledge_duplicate_unavailable')
          pending.push({ edge: current, key, duplicate: true })
          continue
        }
        const edge: KnowledgeEdge = {
          id,
          version: 1,
          sceneId: ctx.sceneId,
          registryVersion: 1,
          predicate: proposal.predicate,
          ...(proposal.property ? { property: proposal.property } : {}),
          subject,
          object,
          polarity: proposal.polarity,
          modality: proposal.modality,
          temporalScope: proposal.temporalScope,
          conditions: proposal.conditions,
          ...(proposal.validFrom ? { validFrom: proposal.validFrom } : {}),
          ...(proposal.validUntil ? { validUntil: proposal.validUntil } : {}),
          backing,
          backingFingerprint: knowledgeBackingFingerprint(view.record),
          proofs: structuredClone(proofs),
          spans,
          dependencies,
          readDependencies: structuredClone(readDependencies),
          status: 'candidate',
          admission: 'qualified_candidate',
          admissionPolicyVersion: 1,
          createdAt: new Date(this.now()).toISOString(),
        }
        const visible = [
          ...this.storage.rows(ctx.sceneId, 201),
          ...pending.map((item) => item.edge),
        ]
        const conflict =
          visible.length > 200 ||
          visible.some((e) => conflictKey(e) === conflictKey(edge) && this.project(ctx, e))
        const policy = knowledgeAutomaticPolicy(proposal.predicate)
        const stanceEligible =
          policy === 'self_explicit'
            ? proposal.subject.kind === 'self' &&
              view.record.epistemicKind === 'explicit' &&
              spans.every((s) => s.stance === 'self')
            : proposal.subject.kind !== 'self' &&
              ['explicit', 'observed'].includes(view.record.epistemicKind ?? '') &&
              spans.every((s) => s.stance === 'asserted')
        if (
          !conflict &&
          this.authority.autoAdmission(ctx) &&
          stanceEligible &&
          proposal.polarity === 'positive' &&
          proposal.modality === 'asserted' &&
          proposal.temporalScope === 'current' &&
          !proposal.conditions.length &&
          (!proposal.validFrom || Date.parse(proposal.validFrom) <= this.now()) &&
          (!proposal.validUntil || this.now() < Date.parse(proposal.validUntil)) &&
          dependencies.length === 0 &&
          readDependencies.length === 0
        ) {
          edge.status = 'accepted'
          edge.admission =
            policy === 'self_explicit'
              ? 'self_explicit_no_visible_conflict'
              : 'direct_assertion_no_visible_conflict'
        }
        KnowledgeEdgeSchema.parse(edge)
        pending.push({ edge, key, duplicate: false })
      } catch (error) {
        return {
          created: [],
          rejected: [
            {
              index,
              reason:
                error instanceof Error && error.message.startsWith('knowledge_')
                  ? error.message
                  : 'knowledge_proposal_invalid',
            },
          ],
        }
      }
    for (const item of pending) if (!item.duplicate) this.storage.put(item.edge, item.key)
    return { created: pending.map((p) => p.edge), rejected: [] }
  }
}
