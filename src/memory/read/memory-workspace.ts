import { createHash } from 'node:crypto'
import type { Store } from '../../store.ts'
import type { MemoryContext } from '../domain/contracts.ts'
import type { CommitmentHead, CommitmentRevision, CommitmentView } from '../domain/commitments.ts'
import type { ExperienceAssertionSnapshot } from '../domain/experience-view.ts'
import type { CommitmentService } from '../commitments/service.ts'
import type { WorkspaceAuthority } from './workspace-authority.ts'
import { projectExperienceView } from '../projections/experience-view.ts'
import { WorkingSetMutationHooks } from '../working-set/mutation-hooks.ts'
import { WorkingSetService } from '../working-set/service.ts'
import type {
  WorkingSetReference,
  WorkingSetSelection,
  WorkingSetSnapshot,
  WorkingSetPinPolicy,
} from '../working-set/contracts.ts'
import { recordWorkingSetPin } from '../recovery/working-set.ts'
import { requireWorkingSetReader } from '../recovery/ledger.ts'

const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const time = (value?: string) =>
  value && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null
const planRef = (view: CommitmentView) => ({
  kind: 'commitment' as const,
  ...view.ref,
  mutationVersion: view.mutationVersion,
})
const assertionRef = (value: ExperienceAssertionSnapshot) => ({
  kind: 'assertion' as const,
  ...value.ref,
})
const sceneContext = (ctx: MemoryContext) => ({ ...ctx, provenance: 'contextual' as const })

/** Application adapter: authorized batch reads and deterministic metadata only. */
export class MemoryWorkspace {
  private dirtyListener?: () => void
  readonly access: WorkingSetService
  readonly mutations: WorkingSetMutationHooks
  constructor(
    private readonly store: Pick<Store, 'db' | 'settings' | 'assertRecoveryReady' | 'sourceEvents'>,
    private readonly authority: WorkspaceAuthority,
    private readonly commitments: Pick<CommitmentService, 'list' | 'current' | 'storage'>,
    private readonly assertionExists: (sceneId: string, ref: WorkingSetReference) => boolean,
    private readonly mutationPorts?: {
      sourceAssertions(
        sceneId: string,
        messageId: string,
      ): readonly { id: string; version: number; deletionEpoch: number }[]
    },
  ) {
    this.access = new WorkingSetService(store.db, {
      assertReady: () => store.assertRecoveryReady(),
      enabled: () => store.settings.memoryWorkingSetEnabled || store.settings.memoryEpisodesEnabled,
      beforeWrite: () => requireWorkingSetReader(store.db),
      beforePinWrite: (policy) => recordWorkingSetPin(store.db, policy),
      resolveCurrent: (ctx, refs) => this.resolve(ctx, refs),
      policy: {
        maxBatch: 40,
        coldAfterMs: store.settings.memoryColdAfterDays * 86400_000,
        reviewAfterMs: store.settings.memoryReviewAfterDays * 86400_000,
      },
    })
    this.mutations = new WorkingSetMutationHooks(store.db, {
      assertReady: () => store.assertRecoveryReady(),
      enabled: () => store.settings.memoryWorkingSetEnabled || store.settings.memoryEpisodesEnabled,
      beforeWrite: () => requireWorkingSetReader(store.db),
      storage: this.access.storage,
      resolveCommitted: (ctx, refs) => this.resolve(ctx, refs),
      invalidate: (scene, refs) => this.access.invalidate(scene, refs),
      terminalInvalidate: (scene, refs) => this.access.terminalInvalidate(scene, refs),
      affectedAssertionScenes: (id, after, limit) => {
        if (!this.access.storage.assertSupported()) return []
        return (
          store.db
            .prepare(
              `SELECT scene_id FROM (
          SELECT scene_id FROM memory_working_set_pins WHERE kind='assertion' AND entity_id=?
          UNION SELECT scene_id FROM memory_working_set_items WHERE kind='assertion' AND entity_id=?
          UNION SELECT scene_id FROM memory_working_set_jobs WHERE kind='assertion' AND entity_id=?
          UNION SELECT json_extract(value,'$.sceneId') scene_id FROM memory_working_set_receipts
            WHERE json_extract(value,'$.selection.ref.kind')='assertion' AND json_extract(value,'$.selection.ref.id')=?
        ) WHERE (? IS NULL OR scene_id>?) ORDER BY scene_id COLLATE BINARY LIMIT ?`,
            )
            .all(id, id, id, id, after ?? null, after ?? null, limit) as { scene_id: string }[]
        ).map((row) => row.scene_id)
      },
    })
  }
  onDirty(listener: () => void) {
    this.dirtyListener = listener
    return () => {
      if (this.dirtyListener === listener) this.dirtyListener = undefined
    }
  }
  maintenanceBatch(): boolean {
    this.store.assertRecoveryReady()
    if (
      (!this.store.settings.memoryWorkingSetEnabled &&
        !this.store.settings.memoryEpisodesEnabled) ||
      !this.access.storage.exists()
    )
      return false
    const row = this.store.db
      .prepare(
        "SELECT scene_id FROM memory_working_set_jobs WHERE json_extract(value,'$.lease') IS NULL OR json_extract(value,'$.lease.expiresAt')<=? ORDER BY rowid LIMIT 1",
      )
      .get(Date.now()) as { scene_id: string } | undefined
    if (!row) return !!this.store.db.prepare('SELECT 1 FROM memory_working_set_jobs LIMIT 1').get()
    this.access.drain({ sceneId: row.scene_id, simulation: false }, 40)
    return !!this.store.db.prepare('SELECT 1 FROM memory_working_set_jobs LIMIT 1').get()
  }
  onAssertionCommitted(sceneId: string, id: string, version: number, deletionEpoch: number) {
    const result = this.mutations.committed({ sceneId, simulation: false }, [
      { kind: 'assertion', id, version, deletionEpoch },
    ])
    if (result.queued) this.dirtyListener?.()
    return result
  }
  onPlanCommitted(
    head: CommitmentHead,
    revisions: readonly CommitmentRevision[],
    priorRevisions: readonly CommitmentRevision[] = [],
  ) {
    if (!this.store.db.inTransaction) throw new Error('working_set_mutation_transaction_required')
    this.store.assertRecoveryReady()
    const ctx = { sceneId: head.sceneId, simulation: false }
    if (head.deleted)
      this.access.terminalInvalidate(head.sceneId, [{ kind: 'commitment', id: head.id }])
    else this.access.invalidate(head.sceneId, [{ kind: 'commitment', id: head.id }])
    const refs: WorkingSetReference[] = head.deleted
      ? []
      : [...new Set([head.activeVersion ?? head.latestVersion, head.latestVersion])].map(
          (version) => ({
            kind: 'commitment',
            id: head.id,
            version,
            deletionEpoch: head.deletionEpoch,
            mutationVersion: head.mutationVersion,
          }),
        )
    // Different terms revisions share an entity key; enqueue each independently.
    for (const ref of refs) this.mutations.committed(ctx, [ref])
    const assertions = new Map<string, WorkingSetReference>()
    for (const revision of [...priorRevisions, ...revisions])
      for (const e of [...revision.evidence, ...revision.requiredProposalGroups.flat()]) {
        const ref = e.qualificationRef ?? e.backing
        assertions.set(ref.id, { kind: 'assertion', ...ref })
      }
    const values = [...assertions.values()]
    for (let i = 0; i < values.length; i += 40)
      this.mutations.sourceEdited(ctx, values.slice(i, i + 40))
    this.dirtyListener?.()
  }
  onSourceEdited(sceneId: string, messageId: string) {
    if (!this.store.db.inTransaction) throw new Error('working_set_mutation_transaction_required')
    this.store.assertRecoveryReady()
    if (!this.mutationPorts) throw new Error('workspace_mutation_source_port_missing')
    const refs = this.mutationPorts
      .sourceAssertions(sceneId, messageId)
      .map((ref) => ({ kind: 'assertion' as const, ...ref }))
    for (let i = 0; i < refs.length; i += 40)
      this.mutations.sourceEdited({ sceneId, simulation: false }, refs.slice(i, i + 40))
    if (refs.length) this.dirtyListener?.()
  }
  onAssertionUnavailable(id: string) {
    return this.mutations.invalidateAssertion(id)
  }
  terminalAssertionDeleted(id: string) {
    return this.mutations.terminalAssertionDeleted(id)
  }
  resolvePinTarget(policy: WorkingSetPinPolicy): 'current' | 'stale' | 'missing' {
    this.store.assertRecoveryReady()
    const { ref, dependencyKey } = policy.selection
    const exists =
      ref.kind === 'assertion'
        ? this.assertionExists(policy.sceneId, ref)
        : (() => {
            const head = this.commitments.storage.head(ref.id)
            return (
              !!head &&
              !head.deleted &&
              head.sceneId === policy.sceneId &&
              head.latestVersion >= ref.version &&
              head.deletionEpoch >= ref.deletionEpoch &&
              head.mutationVersion >= ref.mutationVersion
            )
          })()
    if (!exists) return 'missing'
    const current = this.resolve({ sceneId: policy.sceneId, simulation: false }, [ref])[0]
    return current?.dependencyKey === dependencyKey ? 'current' : 'stale'
  }
  pinPolicies(ctx: MemoryContext) {
    return this.access.listPinPolicies(ctx, { limit: 200 })
  }
  configure() {
    this.access.configurePolicy({
      coldAfterMs: this.store.settings.memoryColdAfterDays * 86400_000,
      reviewAfterMs: this.store.settings.memoryReviewAfterDays * 86400_000,
    })
    if (!this.store.settings.memoryWorkingSetEnabled && !this.store.settings.memoryEpisodesEnabled)
      this.access.cancelMaintenance()
  }
  private plans(ctx: MemoryContext, personId?: string, lifecycle: 'open' | 'all' = 'all') {
    if (ctx.simulation || !this.store.settings.memoryCommitmentsEnabled)
      return { items: [] as CommitmentView[], coverage: 'complete' as const }
    return this.commitments.list(sceneContext(ctx), { personId, lifecycle, limit: 12 })
  }
  private planKey(view: CommitmentView) {
    const heads = [...view.evidence, ...view.requiredProposalGroups.flat()].flatMap((evidence) =>
      evidence.proofs.flatMap((proof) =>
        proof.sources.map(
          (source) => this.store.sourceEvents.registered(source.sceneId, source.messageId) ?? null,
        ),
      ),
    )
    return hash({ view, heads })
  }
  resolve(ctx: MemoryContext, refs: readonly WorkingSetReference[]): WorkingSetSnapshot[] {
    this.store.assertRecoveryReady()
    if (ctx.simulation) return []
    const result = this.authority.resolveCurrentBatch(ctx.sceneId, {
      assertions: refs.filter((ref) => ref.kind === 'assertion'),
      commitments: refs.filter(
        (ref): ref is Extract<WorkingSetReference, { kind: 'commitment' }> =>
          ref.kind === 'commitment',
      ),
    })
    const planHeads = this.commitments.storage.heads(ctx.sceneId)
    const openItems = this.store.settings.memoryCommitmentsEnabled
      ? planHeads
          .slice(0, 200)
          .filter((head) => !head.deleted)
          .flatMap((head) =>
            [...new Set([head.activeVersion ?? head.latestVersion, head.latestVersion])].flatMap(
              (version) => {
                const view = this.commitments.current(sceneContext(ctx), {
                  id: head.id,
                  version,
                  deletionEpoch: head.deletionEpoch,
                })
                return view && ['pending', 'needs_review'].includes(view.lifecycle) ? [view] : []
              },
            ),
          )
      : []
    const open = {
      items: openItems,
      coverage: planHeads.length > 200 ? ('limited' as const) : ('complete' as const),
    }
    const protectionUnknown =
      open.coverage === 'limited' ||
      (!this.store.settings.memoryCommitmentsEnabled &&
        this.commitments.storage.heads(ctx.sceneId).length > 0)
    const protections = new Map<string, 'open_commitment' | 'needs_review_commitment'>()
    const planDependencies = new Map<string, unknown[]>()
    for (const plan of open.items) {
      const protection =
        plan.lifecycle === 'needs_review' ? 'needs_review_commitment' : 'open_commitment'
      for (const evidence of [...plan.evidence, ...plan.requiredProposalGroups.flat()]) {
        const ref = evidence.qualificationRef ?? evidence.backing
        const key = JSON.stringify([ref.id, ref.version, ref.deletionEpoch])
        planDependencies.set(key, [
          ...(planDependencies.get(key) ?? []),
          {
            ref: plan.ref,
            mutationVersion: plan.mutationVersion,
            lifecycle: plan.lifecycle,
            qualification: this.planKey(plan),
          },
        ])
        if (protections.get(key) !== 'needs_review_commitment') protections.set(key, protection)
      }
    }
    return [
      ...result.assertions.map((item) => ({
        ref: assertionRef(item),
        dependencyKey: hash({
          authority: item.qualificationKey,
          plans:
            planDependencies.get(
              JSON.stringify([item.ref.id, item.ref.version, item.ref.deletionEpoch]),
            ) ?? [],
          protectionUnknown,
        }),
        sceneId: ctx.sceneId,
        ...(item.record.scope.kind === 'scene' && item.record.scope.personId
          ? { personId: item.record.scope.personId }
          : {}),
        recordedAt: time(item.record.learnedAt),
        epistemicKind: item.record.epistemicKind,
        protection:
          protections.get(
            JSON.stringify([item.ref.id, item.ref.version, item.ref.deletionEpoch]),
          ) ?? (protectionUnknown ? ('unresolved_commitment_coverage' as const) : null),
      })),
      ...result.commitments.map((view) => ({
        ref: planRef(view),
        dependencyKey: this.planKey(view),
        sceneId: ctx.sceneId,
        recordedAt: time(view.recordedAt),
        protection:
          view.lifecycle === 'needs_review'
            ? ('needs_review_commitment' as const)
            : view.lifecycle === 'pending'
              ? ('open_commitment' as const)
              : null,
      })),
    ]
  }
  private updateMetadata(ctx: MemoryContext, selections: readonly WorkingSetSelection[]) {
    if (!selections.length || ctx.simulation) return
    this.access.enqueue(
      ctx,
      selections.map(({ ref, dependencyKey }) => ({ ref, dependencyKey })),
    )
    this.access.drain(ctx, 40)
  }
  workingSet(ctx: MemoryContext, input: { personId?: string; includeCold?: boolean } = {}) {
    this.store.assertRecoveryReady()
    if (ctx.simulation || !this.store.settings.memoryWorkingSetEnabled)
      return { enabled: false, items: [], coverage: 'disabled' as const, omittedCold: 0 }
    const assertions = this.authority.candidates(ctx, { personId: input.personId, limit: 28 })
    const plans = this.store.settings.memoryCommitmentsAnswerEnabled
      ? this.plans(ctx, input.personId, 'open')
      : { items: [] as CommitmentView[], coverage: 'complete' as const }
    const snapshots = this.resolve(ctx, [
      ...assertions.items.map(assertionRef),
      ...plans.items.map(planRef),
    ])
    this.updateMetadata(ctx, snapshots)
    const metadata = this.access.view(
      ctx,
      snapshots.map(({ ref, dependencyKey }) => ({ ref, dependencyKey })),
      { includeCold: input.includeCold === true },
    )
    const byId = new Map(
      metadata.items.map((item) => [JSON.stringify([item.ref.kind, item.ref.id]), item]),
    )
    const entries = [
      ...assertions.items.flatMap((item) => {
        const accessibility = byId.get(JSON.stringify(['assertion', item.ref.id]))
        return accessibility ? [{ kind: 'assertion' as const, snapshot: item, accessibility }] : []
      }),
      ...plans.items.flatMap((view) => {
        const accessibility = byId.get(JSON.stringify(['commitment', view.ref.id]))
        return accessibility ? [{ kind: 'commitment' as const, view, accessibility }] : []
      }),
    ]
    entries.sort(
      (a, b) =>
        Number(!!b.accessibility.protection || b.accessibility.pinIntent) -
        Number(!!a.accessibility.protection || a.accessibility.pinIntent),
    )
    return {
      enabled: true,
      items: entries.slice(0, 12),
      coverage:
        assertions.coverage === 'limited' ||
        plans.coverage === 'limited' ||
        metadata.coverage === 'limited' ||
        entries.length > 12
          ? ('limited' as const)
          : ('complete' as const),
      omittedCold: metadata.omittedCold,
      omittedEntries: Math.max(0, entries.length - 12),
      interpretation: 'accessibility_not_truth_or_absence' as const,
    }
  }
  episodes(ctx: MemoryContext, input: { personId?: string } = {}) {
    this.store.assertRecoveryReady()
    if (ctx.simulation || !this.store.settings.memoryEpisodesEnabled)
      return projectExperienceView({
        sceneId: ctx.sceneId,
        simulation: true,
        assertions: [],
        commitments: [],
        upstreamCoverage: { assertions: 'complete', commitments: 'complete' },
      })
    const assertions = this.authority.candidates(ctx, {
      personId: input.personId,
      experienceOnly: true,
      limit: 28,
    })
    const plans = this.store.settings.memoryCommitmentsAnswerEnabled
      ? this.plans(ctx, input.personId)
      : { items: [] as CommitmentView[], coverage: 'complete' as const }
    const snapshots = this.resolve(ctx, [
      ...assertions.items.map(assertionRef),
      ...plans.items.map(planRef),
    ])
    this.updateMetadata(ctx, snapshots)
    return projectExperienceView({
      sceneId: ctx.sceneId,
      simulation: false,
      assertions: assertions.items,
      commitments: plans.items,
      upstreamCoverage: { assertions: assertions.coverage, commitments: plans.coverage },
    })
  }
}
