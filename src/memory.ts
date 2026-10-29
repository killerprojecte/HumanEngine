import { SemanticPrivacy } from './memory/lifecycle/semantic-privacy.ts'
import { createSemanticService, semanticPolicyRevision } from './memory/semantic/facade.ts'
import { semanticAssertion } from './memory/semantic/authority.ts'
import { SemanticNotifications } from './memory/semantic/notifications.ts'
import { SemanticEpisodeAnchors } from './memory/semantic/episode-anchors.ts'
import { SemanticNeighborhood } from './memory/semantic/neighborhood.ts'
import type { SemanticService } from './memory/semantic/candidates.ts'
import type { SemanticInput } from './memory/semantic/input.ts'
import type { SemanticProposal } from './memory/semantic/contracts.ts'
import type { SemanticContext, SemanticRef } from './memory/semantic/contracts.ts'
import { KnowledgePrivacy } from './memory/lifecycle/knowledge-privacy.ts'
import { createKnowledgeService } from './memory/knowledge/facade.ts'
import type { KnowledgeService } from './memory/knowledge/service.ts'
import { createIdentityFacade, type IdentityFacade } from './memory/identities/facade.ts'
import { MemoryWorkspace } from './memory/read/memory-workspace.ts'
import { isDeepStrictEqual } from 'node:util'
import { CommitmentService } from './memory/commitments/service.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  MemoryKind,
  MemoryRecord,
  MemoryScope,
  MemorySource,
  ProposalResult,
  SourceEvidence,
  VectorSpace,
} from './memory/domain/contracts.ts'
import {
  parentBindingState,
  type ProofParentReference,
  type SupportProof,
} from './memory/domain/proofs.ts'
import { hash, scopeKey, uniqueSources } from './memory/domain/record-values.ts'
import { AssertionLifecycle } from './memory/lifecycle/assertion-lifecycle.ts'
import { CommitmentPrivacy } from './memory/lifecycle/commitment-privacy.ts'
import { ProofInvalidation } from './memory/lifecycle/proof-invalidation.ts'
import { MemoryProjections } from './memory/projections/coordinator.ts'
import { projectPeople, projectPerson } from './memory/projections/people.ts'
import { AssertionAuthority } from './memory/read/assertion-authority.ts'
import { WorkspaceAuthority } from './memory/read/workspace-authority.ts'
import {
  queryAssertionHistory,
  type HistoricalAssertionQuery,
} from './memory/read/assertion-history-query.ts'
import { createParentTraversal } from './memory/read/bound-parents.ts'
import { domainSourceCurrent } from './memory/read/domain-source-events.ts'
import { memorySearchTerms } from './memory/read/lexical.ts'
import { recordCommitmentMutation } from './memory/recovery/commitments.ts'
import { RelationCandidates } from './memory/relations/candidates.ts'
import {
  AssertionRepository,
  readAssertionRow as read,
  type AssertionRow as Row,
} from './memory/storage/assertion-repository.ts'
import { initializeMemoryStorage } from './memory/storage/migrations.ts'
import { type MemoryCommandGuard } from './memory/write/guarded-command.ts'
import { ManualMemoryCommands } from './memory/write/manual-commands.ts'
import { ProposalCommand } from './memory/write/proposal-command.ts'
import { MemoryRevisionCoordinator } from './memory/write/revision-coordinator.ts'
import {
  earliestObservation,
  resolveSubjectAttribution,
} from './memory/write/subject-attribution.ts'
import type { Store } from './store.ts'

export type {
  MemoryCandidate,
  MemoryContext,
  MemoryKind,
  MemoryRecord,
  MemoryScope,
  MemorySource,
  ProposalResult,
  SourceEvidence,
  VectorSpace,
} from './memory/domain/contracts.ts'
type Support = SupportProof

/** SQLite is authoritative; embeddings are replaceable, scope-filtered derived data. */
export class MemoryService {
  private readonly commitmentPrivacy: CommitmentPrivacy
  private readonly lifecycleCommands: AssertionLifecycle
  private readonly manual: ManualMemoryCommands
  private readonly proposals: ProposalCommand
  private readonly authority: AssertionAuthority
  private readonly repository: AssertionRepository
  private readonly unsubscribeWorkspaceSource: () => void
  readonly semantic: SemanticService
  readonly semanticNotifications: SemanticNotifications
  readonly semanticEpisodes: SemanticEpisodeAnchors
  readonly semanticNeighborhood: SemanticNeighborhood
  readonly knowledge: KnowledgeService
  readonly identities: IdentityFacade
  readonly workspace: MemoryWorkspace
  readonly workspaceAuthority: WorkspaceAuthority
  readonly commitments: CommitmentService
  readonly relations: RelationCandidates
  private readonly revisions: MemoryRevisionCoordinator
  private readonly projections: MemoryProjections
  private readonly lifecycle: ProofInvalidation<MemoryRecord, Support>
  knowledgeModelEligible(ctx: MemoryContext, refs: readonly ProofParentReference[]): boolean {
    return refs.every((ref) => {
      const view = this.authority.relationAuthority({ ...ctx, provenance: 'contextual' }, ref)
      return (
        !!view &&
        ['fact', 'claim', 'term'].includes(view.record.kind) &&
        (view.record.scope.kind !== 'scene' || !view.record.scope.personId)
      )
    })
  }
  semanticCurrent(ctx: SemanticContext, ref: SemanticRef) {
    return semanticAssertion(
      this.store,
      (context, reference) =>
        this.authority.relationAuthority({ ...context, provenance: 'contextual' }, reference),
      ctx,
      ref,
    )
  }
  beginSemanticAttempt(snapshot: SemanticInput) {
    return this.revisions.transaction(() => {
      if (!this.semanticNeighborhood.valid(snapshot)) return false
      return this.semantic.beginAttempt(
        { sceneId: snapshot.sceneId, simulation: false },
        { inputKey: snapshot.inputKey, parents: snapshot.selections },
      )
    })()
  }
  finishSemanticAttempt(snapshot: SemanticInput, status: 'finished' | 'abandoned') {
    this.revisions.transaction(() =>
      this.semantic.finishAttempt(
        { sceneId: snapshot.sceneId, simulation: false },
        snapshot.inputKey,
        status,
      ),
    )()
  }
  commitSemantic(
    snapshot: SemanticInput,
    proposals: readonly SemanticProposal[],
    guard: () => void,
  ) {
    return this.revisions.transaction(() => {
      guard()
      if (!this.semanticNeighborhood.valid(snapshot)) throw new Error('semantic_authority_changed')
      const result = this.semantic.admit(
        { sceneId: snapshot.sceneId, simulation: false },
        snapshot.selections,
        proposals,
        snapshot.episodeAnchor && snapshot.episodeMembershipKey
          ? {
              anchor: snapshot.episodeAnchor,
              membershipKey: snapshot.episodeMembershipKey,
              anchorDependencies: snapshot.anchorDependencies ?? [],
              anchorQualifications: snapshot.anchorQualifications ?? [],
            }
          : undefined,
      )
      return { created: result.created.length, rejected: result.rejected.length }
    })()
  }
  constructor(private readonly store: Store) {
    initializeMemoryStorage(store)
    this.identities = createIdentityFacade(store, this)
    this.repository = new AssertionRepository(store, {
      beforeWrite: (id) => this.revisions.beforeWrite(id),
      removeProjection: (id) => {
        this.projections.remove(id)
        this.workspace?.onAssertionUnavailable(id)
      },
    })
    this.relations = new RelationCandidates(store.db, {
      enabled: () => store.settings.memoryGraphEnabled,
      autoAdmission: () => store.settings.memoryGraphAutoAdmissionEnabled,
      learningEnabled: (ctx) => !!store.scene(ctx.sceneId)?.learningEnabled,
      current: (ctx, ref) => this.authority.relationAuthority(ctx, ref),
    })
    this.knowledge = createKnowledgeService(store, (ctx, ref) =>
      this.authority.relationAuthority(ctx, ref),
    )
    this.semanticEpisodes = new SemanticEpisodeAnchors({
      knowledge: this.knowledge,
      anchorCurrent: (ctx, ref) =>
        this.authority.relationAuthority({ ...ctx, provenance: 'contextual' }, ref),
      current: (ctx, ref) => this.semanticCurrent(ctx, ref),
      triggerRecord: (ctx, ref) =>
        this.authority.relationAuthority({ ...ctx, provenance: 'contextual' }, ref)?.record,
    })
    this.semantic = createSemanticService(
      store,
      (ctx, ref) => this.semanticCurrent(ctx, ref),
      (ctx, ref) => this.authority.relationAuthority({ ...ctx, provenance: 'contextual' }, ref),
      (ctx, anchor, parents, key) =>
        !!key && this.semanticEpisodes.valid(ctx, anchor, parents, key),
      (ctx, personId) => this.semanticNeighborhood.partitionKey(ctx, personId),
    )
    this.semanticNotifications = new SemanticNotifications({
      enabled: (sceneId) =>
        store.settings.memorySemanticEnabled && !!store.scene(sceneId)?.learningEnabled,
      policyIdentity: (sceneId) =>
        JSON.stringify([
          semanticPolicyRevision(store),
          store.scene(sceneId)?.memoryReadRevision ?? 0,
          store.scene(sceneId)?.learningEnabled,
        ]),
      current: (ctx, ref) => this.semanticCurrent(ctx, ref),
      episodes: (ctx, ref) => this.semanticEpisodes.discover(ctx, ref).groups,
      failed: () => store.log('info', 'memory', 'semantic_notification_unavailable'),
    })
    this.semanticNeighborhood = new SemanticNeighborhood({
      records: (ctx) => this.list(ctx),
      references: (ctx, records) => this.captureParentReferences(ctx, records),
      current: (ctx, ref) => this.semanticCurrent(ctx, ref),
      episodes: (ctx, ref) => this.semanticEpisodes.discover(ctx, ref).groups,
    })
    this.commitments = new CommitmentService(store.db, {
      enabled: () => store.settings.memoryCommitmentsEnabled,
      learningEnabled: (ctx) => !!store.scene(ctx.sceneId)?.learningEnabled,
      current: (ctx, ref) => this.authority.relationAuthority(ctx, ref),
      historical: (ctx, ref) => this.authority.historicalAssertion(ctx, ref),
      surviving: (ctx, evidence) => this.commitmentPrivacy.surviving(ctx, evidence),
      people: (ctx, ref) => {
        const view = this.authority.relationAuthority(ctx, ref)
        if (!view) return []
        const sources = view.proofs.flatMap((proof) => proof.sources)
        const trusted = (ctx as MemoryContext).sourceEvidence ?? []
        return [
          ...new Set(
            sources.flatMap((source) => {
              const evidence = trusted.find(
                (item) =>
                  item.sceneId === source.sceneId &&
                  item.messageId === source.messageId &&
                  item.senderId === source.senderId &&
                  item.available &&
                  !item.isBot,
              )
              return evidence
                ? [
                    source.senderId,
                    ...(evidence.mentions ?? [])
                      .filter((mention) => !mention.isBot)
                      .map((mention) => mention.userId),
                  ]
                : [source.senderId]
            }),
          ),
        ]
      },
      beforeWrite: (change) => recordCommitmentMutation(store.db, change),
      afterWrite: (head, revisions, priorRevisions) =>
        this.workspace?.onPlanCommitted(head, revisions, priorRevisions),
    })
    this.projections = new MemoryProjections(
      store.db,
      () => store.settings.memoryProjectionEnabled,
      (id) => {
        const record = this.repository.get(id)
        return record && !record.internalUse && this.authority.supported(record)
          ? record
          : undefined
      },
    )
    this.revisions = new MemoryRevisionCoordinator(
      store.db,
      (id) => this.repository.get(id),
      (id) => this.authority.historySupports(id),
      (record) =>
        record.status !== 'invalidated' &&
        record.lineageStatus !== 'quarantined' &&
        !record.sources.some((source) => store.recalled(source.sceneId, source.messageId)),
      (id, version, epoch) => {
        this.relations.assertionChanged(id)
        this.projections.committed(id, version, epoch)
        const record = this.repository.get(id)
        if (record?.scope.kind === 'scene') {
          this.workspace?.onAssertionCommitted(record.scope.sceneId, id, version, epoch)
          this.semanticNotifications?.notify(
            record.scope.sceneId,
            { id, version, deletionEpoch: epoch },
            'assertion_committed',
          )
        }
      },
      () => this.flushProjections(),
    )
    this.authority = new AssertionAuthority(store, this.repository, this.revisions)
    this.lifecycle = new ProofInvalidation({
      records: () => this.adminList(),
      proofs: (id) => this.repository.supports(id),
      historyDependents: (id) => this.revisions.history.dependents(id),
      exists: (id) => this.repository.get(id) !== undefined,
      removeProjection: (id) => {
        this.relations.purgeAssertion(id)
        this.projections.remove(id)
        this.workspace?.onAssertionUnavailable(id)
      },
      purgeHistory: (id) => {
        this.relations.purgeAssertion(id)
        this.workspace?.onAssertionUnavailable(id)
        this.revisions.history.purge(id)
      },
      touch: (id) => this.revisions.touch(id),
      saveProofs: (id, proofs) => this.repository.saveSupports(id, proofs),
      supported: (record, path, cache) => this.authority.supported(record, path, cache),
      invalidateRecord: (record) => {
        this.knowledge.purgeAssertion(record.id)
        this.semantic.lifecycle.purgeAssertion(record.id)
        this.repository.write({ ...record, status: 'invalidated' })
      },
      removeVector: (id) => {
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id)
      },
    })
    this.commitmentPrivacy = new CommitmentPrivacy(
      store,
      this.repository,
      this.authority,
      this.revisions,
      this.commitments,
    )
    this.lifecycleCommands = new AssertionLifecycle(
      store,
      this.repository,
      this.revisions,
      this.relations,
      this.commitments,
      this.commitmentPrivacy,
      this.lifecycle,
      (id) => {
        this.knowledge.purgeAssertion(id)
        this.semantic.lifecycle.purgeAssertion(id)
        this.workspace?.terminalAssertionDeleted(id)
      },
      (sceneId, messageId) => {
        this.identities.sourceRecalled(sceneId, messageId)
        this.knowledge.purgeSource(sceneId, messageId)
        this.semantic.lifecycle.purgeSource(sceneId, messageId)
      },
      new KnowledgePrivacy(this.knowledge, this.revisions, (sceneId, backing, proof) => {
        const record = this.repository.get(backing.id)
        return (
          !!record &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === sceneId &&
          this.authority.supported(record) &&
          !proof.sources.some((source) => store.recalled(source.sceneId, source.messageId)) &&
          this.authority.proofParentsAvailable(record.scope, proof, 'history')
        )
      }),
      new SemanticPrivacy(this.semantic, this.revisions, (sceneId, backing, proof) => {
        const record = this.repository.get(backing.id)
        return (
          !!record &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === sceneId &&
          this.authority.supported(record) &&
          !proof.sources.some((source) => store.recalled(source.sceneId, source.messageId)) &&
          this.authority.proofParentsAvailable(record.scope, proof, 'history')
        )
      }),
    )
    this.proposals = new ProposalCommand(
      store,
      this.repository,
      this.authority,
      this.revisions,
      this.relations,
      this.commitments,
      this.knowledge,
      {
        refreshSupport: () => this.lifecycleCommands.refreshSupport(),
        dependsOn: (id, wanted) => this.lifecycleCommands.dependsOn(id, wanted),
      },
    )
    this.manual = new ManualMemoryCommands(store, this.repository, this.authority, this.revisions, {
      refreshSupport: () => this.lifecycleCommands.refreshSupport(),
      invalidateDependents: (id, legacyOnly) =>
        this.lifecycleCommands.invalidateDependents(id, legacyOnly),
    })
    this.workspaceAuthority = new WorkspaceAuthority({
      ready: () => store.assertRecoveryReady(),
      list: (ctx) => this.list(ctx),
      capture: (ctx, records) => this.captureParentReferences(ctx, records),
      current: (ctx, ref) =>
        this.authority.relationAuthority(
          { ...ctx, provenance: ctx.provenance ?? 'contextual' },
          ref,
        ),
      sourceHead: (sceneId, messageId) => store.sourceEvents.registered(sceneId, messageId),
      currentPlan: (ctx, ref) =>
        this.commitments.current({ ...ctx, provenance: 'contextual' }, ref),
    })
    this.workspace = new MemoryWorkspace(
      store,
      this.workspaceAuthority,
      this.commitments,
      (sceneId, ref) => {
        const record = this.repository.get(ref.id)
        const state = this.revisions.history.state(ref.id)
        return (
          !!record &&
          !state.deleted &&
          state.version >= ref.version &&
          state.deletionEpoch >= ref.deletionEpoch &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === sceneId
        )
      },
      {
        sourceAssertions: (sceneId, messageId) => {
          const rows = store.db
            .prepare(
              'SELECT DISTINCT memory_id AS id FROM memory_sources WHERE scene_id=? AND message_id=?',
            )
            .all(sceneId, messageId) as { id: string }[]
          return rows
            .map(({ id }) => ({ id, ...this.revisions.history.state(id) }))
            .filter((ref) => !ref.deleted && ref.version > 0)
            .map(({ id, version, deletionEpoch }) => ({ id, version, deletionEpoch }))
        },
      },
    )
    this.unsubscribeWorkspaceSource = store.sourceEvents.subscribeCommitted((mutation) => {
      if (mutation.kind === 'new' || mutation.kind === 'edited') {
        const { sceneId, messageId } = mutation.identity
        this.workspace.onSourceEdited(sceneId, messageId)
        const rows = store.db
          .prepare(
            'SELECT DISTINCT memory_id AS id FROM memory_sources WHERE scene_id=? AND message_id=?',
          )
          .all(sceneId, messageId) as { id: string }[]
        for (const { id } of rows) {
          const state = this.revisions.history.state(id)
          if (!state.deleted && state.version > 0)
            this.semanticNotifications.notify(
              sceneId,
              { id, version: state.version, deletionEpoch: state.deletionEpoch },
              'source_qualification_changed',
            )
        }
      }
    })
    let cachedTermsJson = ''
    let cachedTerms: string[] = []
    store.db.function('humanengine_lexical', { deterministic: true }, (content, termsJson) => {
      if (termsJson !== cachedTermsJson) {
        cachedTermsJson = String(termsJson)
        cachedTerms = JSON.parse(cachedTermsJson) as string[]
      }
      const lower = String(content).toLowerCase()
      return cachedTerms.length
        ? cachedTerms.filter((term) => lower.includes(term)).length / cachedTerms.length
        : 0
    })
  }

  dispose(): void {
    this.unsubscribeWorkspaceSource()
    this.semanticNotifications.dispose()
  }

  delete(id: string): boolean {
    return this.lifecycleCommands.delete(id)
  }
  redactRecoveredHistory(id: string): void {
    this.lifecycleCommands.redactRecoveredHistory(id)
  }
  eraseRecoveredAssertion(id: string): void {
    this.lifecycleCommands.eraseRecoveredAssertion(id)
  }
  prepareRecall(sceneId: string, messageId: string): () => string[] {
    return this.lifecycleCommands.prepareRecall(sceneId, messageId)
  }
  recall(sceneId: string, messageId: string): string[] {
    return this.lifecycleCommands.recall(sceneId, messageId)
  }

  propose(ctx: MemoryContext, candidate: MemoryCandidate): ProposalResult {
    return this.proposals.propose(ctx, candidate)
  }

  list(ctx: MemoryContext, personId?: string): MemoryRecord[] {
    this.store.assertRecoveryReady()
    if (!ctx.simulation) this.lifecycleCommands.refreshSupport()
    return this.scopedRows(ctx, personId)
      .map(read)
      .filter(
        (record) => this.authority.supported(record) && this.authority.currentApplicable(record),
      )
      .map((record) => this.authority.sanitize(record))
  }
  /** Read-only views; authoritative filtering remains in list(), never in a cache. */
  people(ctx: MemoryContext) {
    return projectPeople(ctx, this.list(ctx))
  }

  personProfile(ctx: MemoryContext, personId: string) {
    return projectPerson(ctx, personId, this.list(ctx, personId))
  }

  parentBinding(id: string): 'bound' | 'legacy_unknown' | undefined {
    const proofs = this.repository.supports(id).filter((proof) => proof.parents.length > 0)
    if (!proofs.length) return undefined
    return proofs.some((proof) => parentBindingState(proof) !== 'bound')
      ? 'legacy_unknown'
      : 'bound'
  }

  /** Capture actual authorized read content and its immutable revision together. */
  captureParentReference(
    ctx: MemoryContext,
    returned: MemoryRecord,
  ): ProofParentReference | undefined {
    return this.captureParentReferences(ctx, [returned])[0]
  }

  /** One current scene read per bounded batch, not one scene scan per projected record. */
  captureParentReferences(
    ctx: MemoryContext,
    returned: readonly MemoryRecord[],
  ): ProofParentReference[] {
    if (ctx.simulation) return []
    if (returned.length > 40) throw new Error('workspace_candidate_limit')
    return this.captureReturnedReferences(ctx, returned)
  }

  /** Trusted model-delivery capture. Preserves the existing unpaginated person-read
   * contract: no silent prefix, no caller-supplied authority map. One scene scan per
   * synchronous batch; each returned body still must equal current authorized data. */
  captureExposedReferences(
    ctx: MemoryContext,
    returned: readonly MemoryRecord[],
  ): ProofParentReference[] {
    if (ctx.simulation) return []
    return this.captureReturnedReferences(ctx, returned)
  }

  private captureReturnedReferences(
    ctx: MemoryContext,
    returned: readonly MemoryRecord[],
  ): ProofParentReference[] {
    return this.revisions.transaction(() => {
      const current = new Map(this.list(ctx).map((record) => [record.id, record]))
      return returned.flatMap((record) => {
        const { score: _score, ...body } = record
        const authorized = current.get(record.id)
        if (!authorized || !isDeepStrictEqual(authorized, body)) return []
        let state = this.revisions.history.state(record.id)
        if (state.deleted) return []
        if (state.version === 0 && state.deletionEpoch === 0) {
          this.revisions.history.importLegacy(authorized, this.authority.historySupports(record.id))
          state = this.revisions.history.state(record.id)
        }
        return state.version > 0
          ? [{ id: record.id, version: state.version, deletionEpoch: state.deletionEpoch }]
          : []
      })
    })()
  }

  /** Current authorization is mandatory even for historical versions. */
  historyFor(ctx: MemoryContext, id: string) {
    this.store.assertRecoveryReady()
    if (ctx.simulation) return undefined
    this.lifecycleCommands.refreshSupport()
    const current = this.repository.get(id)
    if (
      !current ||
      current.scope.kind !== 'scene' ||
      current.scope.sceneId !== ctx.sceneId ||
      !this.authority.supported(current)
    )
      return undefined
    this.revisions.transaction(() => {
      const state = this.revisions.history.state(id)
      if (state.version === 0 && state.deletionEpoch === 0)
        this.revisions.history.importLegacy(current, this.authority.historySupports(id))
    })()
    const retained = this.revisions.history.revisions(id, scopeKey(current.scope), 201)
    const retainedCount = this.revisions.history.retainedCount(id, scopeKey(current.scope))
    const coverage =
      retainedCount > 200 || retained.length !== Math.min(retainedCount, 201)
        ? ('limited' as const)
        : ('complete' as const)
    const traversal = createParentTraversal()
    // This one bounded page may contain up to 200 independently sourced versions.
    traversal.remaining = 8192
    const revisions = retained
      .slice(-200)
      .map((revision) => ({
        ...revision,
        supports: this.authority.filterHistorySupports(
          revision.record.scope,
          revision.supports,
          traversal,
        ),
      }))
      .filter(
        (revision) =>
          revision.record.scope.kind === 'scene' &&
          revision.record.scope.sceneId === ctx.sceneId &&
          this.authority.parentScopeAllows(current.scope, revision.record.scope) &&
          !revision.record.sources.some((source) =>
            this.store.recalled(source.sceneId, source.messageId),
          ) &&
          (revision.record.lineageStatus !== 'verified' || revision.supports.length > 0),
      )
    return {
      ...this.revisions.history.state(id),
      revisions: revisions.map((revision) => ({
        ...revision,
        record: this.authority.withObservation(revision.record, revision.supports),
      })),
      coverage,
    }
  }

  /** Previously recorded assertions, not replayed message bodies or implicit current learning authority. */
  historicalQuery(
    ctx: MemoryContext,
    input: Omit<HistoricalAssertionQuery, 'anchor'> & {
      memoryId: string
      anchor?: ProofParentReference
    },
  ) {
    this.store.assertRecoveryReady()
    if (ctx.simulation || !this.store.settings.memoryHistoricalReadEnabled)
      throw new Error('memory_history_disabled')
    const available = this.historyFor(ctx, input.memoryId)
    if (!available || available.deleted || available.version < 1)
      throw new Error('memory_history_anchor_unavailable')
    const currentAnchor = {
      id: input.memoryId,
      version: available.version,
      deletionEpoch: available.deletionEpoch,
    }
    if (input.anchor && !isDeepStrictEqual(input.anchor, currentAnchor))
      throw new Error('memory_history_anchor_changed')
    const anchor = input.anchor ?? currentAnchor
    const authorize = (ref: ProofParentReference) => {
      const record = this.repository.get(ref.id)
      const state = this.revisions.history.state(ref.id)
      return record?.scope.kind === 'scene' &&
        record.scope.sceneId === ctx.sceneId &&
        !state.deleted &&
        state.version === ref.version &&
        state.deletionEpoch === ref.deletionEpoch &&
        this.authority.supported(record)
        ? scopeKey(record.scope)
        : undefined
    }
    const result = queryAssertionHistory(
      { ...input, anchor },
      {
        authorizeAnchor: authorize,
        query: (id, scope, options) => {
          const page = this.revisions.history.queryRetained(id, scope, options)
          const traversal = createParentTraversal()
          traversal.remaining = 8192
          return {
            ...page,
            revisions: page.revisions.map((revision) => {
              const supports = this.authority.filterHistorySupports(
                revision.record.scope,
                revision.supports,
                traversal,
              )
              return {
                ...revision,
                supports,
                record: this.authority.withObservation(
                  {
                    ...revision.record,
                    sources: uniqueSources(supports.flatMap((proof) => proof.sources)),
                  },
                  supports,
                ),
              }
            }),
          }
        },
        authorizeRevision: (revision) =>
          this.authority.parentScopeAllows(
            this.repository.get(input.memoryId)!.scope,
            revision.record.scope,
          ) &&
          revision.record.scope.kind === 'scene' &&
          revision.record.scope.sceneId === ctx.sceneId &&
          (revision.record.lineageStatus !== 'verified' || revision.supports.length > 0),
        qualification: (revision) =>
          revision.supports.some((proof) =>
            proof.sources.some((source) => {
              if (!source.eventRef) return false
              const scene = this.store.scene(source.sceneId)
              return (
                !scene ||
                !this.store.sourceEvents.historical(source.eventRef, {
                  sceneId: source.sceneId,
                  adapterId: scene.adapterId,
                  accountId: scene.accountId,
                  messageId: source.messageId,
                  senderId: source.senderId,
                }).eligible
              )
            }),
          )
            ? ['source_event_metadata_unavailable']
            : [],
      },
    )
    return { ...result, anchor }
  }

  /** Bounded learning-only targets. Candidate metadata is relevance, never read authority. */
  reconciliationTargets(
    ctx: Omit<MemoryContext, 'sourceEvidence'> & {
      sourceEvidence: (SourceEvidence & { origin: 'live' | 'history' | 'simulation' })[]
    },
    candidates: {
      candidateIndex: number
      content: string
      kind: MemoryKind
      personId?: string
      sourceMessageIds: string[]
      subject?: MemoryCandidate['subject']
      epistemicKind?: MemoryCandidate['epistemicKind']
    }[],
    semantic: MemoryRecord[],
  ) {
    const limits = { perCandidate: 4, perGroup: 8, total: 16, semantic: 8 }
    const selected = new Map<string, { memory: MemoryRecord; candidateIndexes: number[] }>()
    let capped = candidates.length > 8
    if (ctx.simulation || !this.store.scene(ctx.sceneId)?.learningEnabled)
      return { targets: [], coverage: 'complete' as const, limits }
    this.lifecycleCommands.refreshSupport()
    const kindGroup = (kind: MemoryKind) => (['fact', 'claim'].includes(kind) ? 'statement' : kind)
    const groups = new Map<
      string,
      { personId?: string; kind: MemoryKind; candidates: typeof candidates }
    >()
    for (const candidate of candidates.slice(0, 8)) {
      const sources = candidate.sourceMessageIds.map((id) =>
        ctx.sourceEvidence.find(
          (evidence) =>
            evidence.messageId === id &&
            evidence.sceneId === ctx.sceneId &&
            evidence.origin === 'live' &&
            evidence.available &&
            !evidence.isBot &&
            !this.store.recalled(ctx.sceneId, id),
        ),
      )
      if (!sources.length || sources.some((source) => !source)) continue
      const attribution = resolveSubjectAttribution(ctx, {
        ...candidate,
        sources: sources as SourceEvidence[],
      })
      if (!attribution.ok) continue
      const resolvedCandidate = {
        ...candidate,
        personId: attribution.personId,
        kind: attribution.kind,
        epistemicKind: attribution.epistemicKind,
      }
      const key = JSON.stringify([
        resolvedCandidate.personId ?? null,
        kindGroup(resolvedCandidate.kind),
      ])
      const group = groups.get(key) ?? {
        personId: resolvedCandidate.personId,
        kind: resolvedCandidate.kind,
        candidates: [] as typeof candidates,
      }
      group.candidates.push(resolvedCandidate)
      groups.set(key, group)
    }
    if (groups.size && semantic.length > limits.semantic) capped = true
    for (const group of groups.values()) {
      const kinds = kindGroup(group.kind) === 'statement' ? ['fact', 'claim'] : [group.kind]
      const rows = this.store.db
        .prepare(
          `SELECT * FROM memories
        WHERE scope_key=? AND person_id IS ? AND json_extract(value,'$.kind') IN (SELECT value FROM json_each(?))
          AND json_extract(value,'$.internalUse') IS NULL AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined'
          AND json_extract(value,'$.status') IN ('active','disputed')
          AND (json_extract(value,'$.validFrom') IS NULL OR julianday(json_extract(value,'$.validFrom')) <= julianday('now')) AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now'))
        ORDER BY rowid DESC LIMIT ?`,
        )
        .all(
          `scene:${ctx.sceneId}`,
          group.personId ?? null,
          JSON.stringify(kinds),
          limits.perGroup + 1,
        ) as Row[]
      if (rows.length > limits.perGroup) capped = true
      const metadata = rows
        .slice(0, limits.perGroup)
        .map(read)
        .filter(
          (record) => this.authority.supported(record) && this.authority.currentApplicable(record),
        )
      const semanticMatches = semantic.slice(0, limits.semantic).flatMap((record) => {
        const current = this.repository.get(record.id)
        if (
          !current ||
          current.internalUse ||
          !this.authority.supported(current) ||
          !this.authority.currentApplicable(current) ||
          !kinds.includes(current.kind)
        )
          return []
        if (current.scope.kind === 'scene') {
          if (current.scope.sceneId !== ctx.sceneId || current.scope.personId !== group.personId)
            return []
        } else if (group.personId) return []
        return [current]
      })
      const groupIds = new Set<string>()
      for (const candidate of group.candidates) {
        const tokens = [
          ...new Set(candidate.content.toLowerCase().match(/[\p{L}\p{N}_]+/gu) ?? []),
        ].filter((token) => token.length > 1)
        const score = (record: MemoryRecord) =>
          tokens.filter((token) => record.content.toLowerCase().includes(token)).length
        const ranked = [...metadata].sort((a, b) => score(b) - score(a))
        // Reserve metadata slots before semantic hits: missing vectors cannot erase this path.
        const choices = [
          ...new Map(
            [...ranked.slice(0, 2), ...semanticMatches, ...ranked.slice(2)].map((record) => [
              record.id,
              record,
            ]),
          ).values(),
        ]
        if (choices.length > limits.perCandidate) capped = true
        for (const memory of choices.slice(0, limits.perCandidate)) {
          if (
            (!groupIds.has(memory.id) && groupIds.size >= limits.perGroup) ||
            (!selected.has(memory.id) && selected.size >= limits.total)
          ) {
            capped = true
            continue
          }
          groupIds.add(memory.id)
          const entry = selected.get(memory.id) ?? {
            memory: this.authority.sanitize(memory),
            candidateIndexes: [],
          }
          if (!entry.candidateIndexes.includes(candidate.candidateIndex))
            entry.candidateIndexes.push(candidate.candidateIndex)
          selected.set(memory.id, entry)
        }
      }
    }
    return {
      targets: [...selected.values()],
      coverage: capped ? ('capped' as const) : ('complete' as const),
      limits,
    }
  }
  adminList(): MemoryRecord[] {
    return this.repository.all()
  }

  search(
    ctx: MemoryContext,
    query: string,
    personId?: string,
    options?: { spaceId: string; vector: number[]; limit?: number },
  ): MemoryRecord[] {
    if (!ctx.simulation) this.lifecycleCommands.refreshSupport()
    const terms = memorySearchTerms(query)
    // Validate the selected space even for an empty permitted scope, as before.
    const vector = options ? this.vector(options.spaceId, options.vector) : undefined
    const scope = this.scopeFilter(ctx, personId)
    if (!scope) return []
    const applicability = new Map<string, boolean>()
    const unsupported = this.adminList()
      .filter(
        (record) =>
          !this.authority.currentApplicable(record, new Set(), applicability) ||
          (ctx.simulation && !this.authority.supported(record)),
      )
      .map((record) => record.id)
    const lexical = terms.length ? 'humanengine_lexical(scoped.content, ?)' : '0.0'
    const semantic = options
      ? 'CASE WHEN v.embedding IS NULL THEN 0.0 ELSE max(0.0, 1.0 - vec_distance_cosine(v.embedding, ?)) END'
      : '0.0'
    const join = options
      ? 'LEFT JOIN memory_vectors v ON v.memory_id=scoped.id AND v.space_id=?'
      : ''
    const limit = Math.max(1, Math.min(options?.limit ?? 20, 100))
    const rows = this.store.db
      .prepare(
        `
      WITH scoped AS MATERIALIZED (
        SELECT rowid AS ordinal, * FROM memories
        WHERE ${scope.sql} AND id NOT IN (SELECT value FROM json_each(?)) AND json_extract(value,'$.internalUse') IS NULL AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed')
          AND (json_extract(value,'$.validFrom') IS NULL OR julianday(json_extract(value,'$.validFrom')) <= julianday('now')) AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now'))
      ), relevant AS MATERIALIZED (
        SELECT scoped.value, scoped.ordinal, max(${lexical}, ${semantic}) AS relevance
        FROM scoped ${join}
      ), ranked AS (
        SELECT value, ordinal, relevance,
          relevance * 0.8 + json_extract(value,'$.confidence') * 0.1 +
          CASE json_extract(value,'$.importance') WHEN 'low' THEN 0.02 WHEN 'medium' THEN 0.06 ELSE 0.1 END AS score
        FROM relevant
      )
      SELECT value, score FROM ranked WHERE ? = 0 OR relevance > 0
      ORDER BY score DESC, json_extract(value,'$.learnedAt') DESC, ordinal DESC LIMIT ?
    `,
      )
      .all(
        ...scope.params,
        JSON.stringify(unsupported),
        ...(terms.length ? [JSON.stringify(terms)] : []),
        ...(options ? [vector!, options.spaceId] : []),
        query.trim() ? 1 : 0,
        Math.trunc(limit),
      ) as { value: string; score: number }[]
    return rows
      .filter((row) => this.authority.supported(JSON.parse(row.value) as MemoryRecord))
      .map((row) => ({
        ...this.authority.sanitize(JSON.parse(row.value) as MemoryRecord),
        score: row.score,
      }))
  }

  promotePublic(ctx: MemoryContext, id: string): ProposalResult {
    return this.manual.promotePublic(ctx, id)
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
    return this.manual.create(input)
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
    return this.manual.update(id, patch)
  }

  revisionState(id: string) {
    return this.revisions.history.state(id)
  }

  updateGuarded(
    id: string,
    patch: Parameters<MemoryService['update']>[1],
    guard: MemoryCommandGuard,
  ): MemoryRecord {
    return this.manual.updateGuarded(id, patch, guard)
  }

  lock(id: string, locked: boolean): MemoryRecord {
    return this.manual.lock(id, locked)
  }

  indexable(): MemoryRecord[] {
    this.lifecycleCommands.refreshSupport()
    return this.adminList().filter(
      (memory) =>
        !memory.internalUse &&
        ['active', 'disputed'].includes(memory.status) &&
        (memory.origin === 'manual' || memory.lineageStatus === 'verified') &&
        this.authority.supported(memory),
    )
  }

  registerSpace(space: VectorSpace): string {
    if (
      !space.providerId ||
      !space.modelId ||
      !space.apiModelName ||
      !space.deployment ||
      !space.preprocessing ||
      !Number.isSafeInteger(space.dimensions) ||
      space.dimensions < 1 ||
      space.dimensions > 16384 ||
      typeof space.normalized !== 'boolean'
    )
      throw new Error('invalid_vector_space')
    const value = JSON.stringify({
      providerId: space.providerId,
      modelId: space.modelId,
      apiModelName: space.apiModelName,
      deployment: space.deployment,
      dimensions: space.dimensions,
      preprocessing: space.preprocessing,
      normalized: space.normalized,
      ...(space.generation ? { generation: space.generation } : {}),
      ...(space.encoderRevision ? { encoderRevision: space.encoderRevision } : {}),
    })
    const id = hash(value)
    this.store.db.prepare('INSERT OR IGNORE INTO vector_spaces VALUES(?,?)').run(id, value)
    return id
  }
  index(id: string, spaceId: string, vector: number[]): void {
    const bytes = this.vector(spaceId, vector)
    const memory = this.repository.get(id)
    if (
      !memory ||
      !['active', 'disputed'].includes(memory.status) ||
      (memory.origin === 'manual' && memory.lineageStatus === undefined
        ? !['active', 'disputed'].includes(memory.status)
        : !this.authority.supported(memory))
    ) {
      this.lifecycleCommands.refreshSupport()
      throw new Error('memory_not_indexable')
    }
    this.store.db
      .prepare(
        'INSERT INTO memory_vectors VALUES(?,?,?) ON CONFLICT(memory_id,space_id) DO UPDATE SET embedding=excluded.embedding',
      )
      .run(id, spaceId, bytes)
  }
  flushProjections() {
    try {
      return this.projections.flush()
    } catch {
      this.store.log('warn', 'memory', 'memory_projection_refresh_failed')
    }
  }

  private vector(spaceId: string, vector: number[]): Buffer {
    const row = this.store.db.prepare('SELECT value FROM vector_spaces WHERE id=?').get(spaceId) as
      { value: string } | undefined
    if (!row) throw new Error('vector_space_not_found')
    const space = JSON.parse(row.value) as VectorSpace
    if (
      vector.length !== space.dimensions ||
      vector.some((value) => !Number.isFinite(value)) ||
      vector.every((value) => value === 0)
    )
      throw new Error('invalid_vector')
    const norm = space.normalized ? Math.hypot(...vector) : 1
    const float = new Float32Array(vector.map((value) => value / norm))
    if (float.some((value) => !Number.isFinite(value))) throw new Error('invalid_vector')
    return Buffer.from(float.buffer)
  }
  private scopeFilter(
    ctx: MemoryContext,
    personId?: string,
  ): { sql: string; params: string[] } | undefined {
    this.store.assertRecoveryReady()
    if (ctx.simulation)
      return personId ? undefined : { sql: "scope_key='shared:public'", params: [] }
    if (personId)
      return { sql: 'scope_key=? AND person_id=?', params: [`scene:${ctx.sceneId}`, personId] }
    return { sql: "(scope_key=? OR scope_key='shared:public')", params: [`scene:${ctx.sceneId}`] }
  }
  private scopedRows(ctx: MemoryContext, personId?: string): Row[] {
    const scope = this.scopeFilter(ctx, personId)
    if (!scope) return []
    return this.store.db
      .prepare(
        `SELECT * FROM memories WHERE ${scope.sql} AND json_extract(value,'$.internalUse') IS NULL AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed') AND (json_extract(value,'$.validFrom') IS NULL OR julianday(json_extract(value,'$.validFrom')) <= julianday('now')) AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now')) ORDER BY rowid DESC`,
      )
      .all(...scope.params) as Row[]
  }

  subjectEvidence(ctx: MemoryContext, id: string) {
    return this.subjectEvidenceBatch(ctx, [id]).items.get(id)
  }
  subjectEvidenceBatch(ctx: MemoryContext, ids: readonly string[]) {
    const wanted = [...new Set(ids)]
    const items = new Map<string, ReturnType<MemoryService['projectSubjectEvidence']>>()
    if (ctx.simulation) return { items, coverage: 'complete' as const }
    const visible = new Map(this.list(ctx).map((record) => [record.id, record]))
    for (const id of wanted.slice(0, 200)) {
      const record = visible.get(id)
      if (record?.scope.kind === 'scene' && record.scope.sceneId === ctx.sceneId)
        items.set(id, this.projectSubjectEvidence(record))
    }
    return { items, coverage: wanted.length > 200 ? ('limited' as const) : ('complete' as const) }
  }
  private projectSubjectEvidence(record: MemoryRecord) {
    const id = record.id
    const proofs = this.authority
      .historySupports(id)
      .filter(
        (proof) =>
          this.authority.proofParentsAvailable(record.scope, proof, 'current') &&
          proof.sources.every((source) => domainSourceCurrent(this.store, source)),
      )
    return {
      observedAt: earliestObservation(proofs.flatMap((proof) => proof.sourceObservations ?? [])),
      witnesses: proofs.slice(0, 40).flatMap((proof) =>
        proof.subjectWitness
          ? [
              {
                ...proof.subjectWitness,
                sources: structuredClone(proof.sources),
                observations: structuredClone(proof.sourceObservations ?? []),
              },
            ]
          : [],
      ),
      coverage: proofs.length > 40 ? ('limited' as const) : ('complete' as const),
    }
  }
}
