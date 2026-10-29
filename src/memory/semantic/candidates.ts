import { randomUUID } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import type Database from 'better-sqlite3'
import { canonicalProofKey } from '../domain/proofs.ts'
import type { RelationProof } from '../domain/relations.ts'
import {
  SemanticCandidateSchema,
  SemanticAnchorDependencySchema,
  SemanticAnchorQualificationSchema,
  type SemanticAnchorQualification,
  type SemanticAnchorDependency,
  SemanticProposalSchema,
  SemanticSelectionSchema,
  semanticFingerprint,
  semanticHash,
  semanticDefinitionHash,
  type SemanticQualification,
  type SemanticTrustedEpisode,
  type SemanticAuthority,
  type SemanticContext,
  type SemanticSelection,
  type SemanticCandidate,
  type SemanticView,
  type SemanticMutation,
  type SemanticParent,
} from './contracts.ts'
import { SemanticStorage } from './storage.ts'
import { SemanticLifecycle } from './lifecycle.ts'
export const semanticProofIntersection = (stored: RelationProof[], live: RelationProof[]) =>
  stored.filter((p) =>
    live.some(
      (q) =>
        p.proofId === q.proofId &&
        canonicalProofKey(p) === canonicalProofKey(q) &&
        isDeepStrictEqual(p, q),
    ),
  )
class SemanticRejection extends Error {}
export class SemanticService {
  readonly storage: SemanticStorage
  readonly lifecycle: SemanticLifecycle
  constructor(
    db: Database.Database,
    private readonly authority: SemanticAuthority,
    private readonly options: {
      now?: () => number
      id?: () => string
      beforeWrite?: (mutation: SemanticMutation) => void
    } = {},
  ) {
    this.storage = new SemanticStorage(db, () => authority.assertReady(), options.beforeWrite)
    this.lifecycle = new SemanticLifecycle(this.storage, authority)
  }
  private now() {
    return (this.options.now ?? Date.now)()
  }
  private resolve(ctx: SemanticContext, selection: SemanticSelection) {
    const view = this.authority.current(ctx, selection.ref)
    if (
      !view ||
      view.record.id !== selection.ref.id ||
      view.record.scope.kind !== 'scene' ||
      view.record.scope.sceneId !== ctx.sceneId ||
      view.derived ||
      view.record.internalUse ||
      view.dependencyKey !== selection.dependencyKey ||
      view.rootCoverage !== 'complete' ||
      !view.roots.length ||
      view.roots.length > 128 ||
      view.roots.some((r) => !/^[a-f0-9]{64}$/.test(r)) ||
      !view.proofs.length
    )
      return
    return view
  }
  admit(
    ctx: SemanticContext,
    delivered: readonly SemanticSelection[],
    inputs: readonly unknown[],
    trustedEpisode?: SemanticTrustedEpisode,
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation || !this.authority.enabled(ctx) || !this.authority.learningEnabled(ctx))
      return {
        created: [] as SemanticCandidate[],
        rejected: [{ index: 0, reason: 'semantic_disabled' }],
      }
    const pending: SemanticCandidate[] = []
    let index = 0
    try {
      if (delivered.length < 2 || delivered.length > 12 || inputs.length < 1 || inputs.length > 3)
        throw new SemanticRejection('semantic_input_limit')
      const selections = delivered.map((v) => {
        const parsed = SemanticSelectionSchema.safeParse(v)
        if (!parsed.success) throw new SemanticRejection('semantic_invalid_candidate')
        return parsed.data
      })
      if (new Set(selections.map((s) => s.ref.id)).size !== selections.length)
        throw new SemanticRejection('semantic_duplicate_parent')
      const views = selections.map((s) => this.resolve(ctx, s))
      if (views.some((v) => !v)) throw new SemanticRejection('semantic_parent_unavailable')
      const people = new Set(
        views.map((v) =>
          v!.record.scope.kind === 'scene' ? (v!.record.scope.personId ?? null) : null,
        ),
      )
      const personId = people.size === 1 ? [...people][0]! : null
      const parents: SemanticParent[] = selections.map((s, i) => ({
        ...s,
        fingerprint: semanticFingerprint(views[i]!.record),
        proofs: structuredClone(views[i]!.proofs),
        roots: [...new Set(views[i]!.roots)].sort(),
      }))
      let partition: string | undefined
      let partitionRead = false
      for (const raw of inputs) {
        const parsed = SemanticProposalSchema.safeParse(raw)
        if (!parsed.success) throw new SemanticRejection('semantic_invalid_candidate')
        const proposal = parsed.data
        if (people.size !== 1 && proposal.kind !== 'episode_candidate')
          throw new SemanticRejection('semantic_subject_partition')
        if (proposal.selectedParentIds.some((id) => !parents.some((p) => p.ref.id === id)))
          throw new SemanticRejection('semantic_parent_not_exposed')
        if (
          proposal.episodeAnchor &&
          (!trustedEpisode ||
            !isDeepStrictEqual(trustedEpisode.anchor, proposal.episodeAnchor) ||
            !/^[a-f0-9]{64}$/.test(trustedEpisode.membershipKey))
        )
          throw new SemanticRejection('semantic_episode_membership_required')
        if (
          proposal.episodeAnchor &&
          !this.authority.episodeAnchor(
            ctx,
            proposal.episodeAnchor,
            parents.map((p) => p.ref),
            trustedEpisode!.membershipKey,
          )
        )
          throw new SemanticRejection('semantic_episode_unverified')
        const anchorQualifications: SemanticAnchorQualification[] = []
        const anchorDependencies: SemanticAnchorDependency[] = []
        if (proposal.episodeAnchor) {
          const qualifications = trustedEpisode!.anchorQualifications
          if (
            !qualifications?.length ||
            qualifications.length > 40 ||
            new Set(qualifications.map((q) => q.edgeId)).size !== qualifications.length
          )
            throw new SemanticRejection('semantic_anchor_qualifications_required')
          for (const q of qualifications) {
            const parsed = SemanticAnchorQualificationSchema.safeParse(q)
            if (!parsed.success)
              throw new SemanticRejection('semantic_anchor_qualifications_invalid')
            anchorQualifications.push(parsed.data)
          }
          if (
            !trustedEpisode!.anchorDependencies?.length ||
            trustedEpisode!.anchorDependencies.length > 128
          )
            throw new SemanticRejection('semantic_anchor_dependencies_required')
          for (const raw of trustedEpisode!.anchorDependencies) {
            const parsed = SemanticAnchorDependencySchema.safeParse(raw)
            if (!parsed.success) throw new SemanticRejection('semantic_anchor_dependencies_invalid')
            const dep = parsed.data,
              live = this.authority.anchorCurrent(ctx, dep.ref)
            if (
              !live ||
              live.record.id !== dep.ref.id ||
              live.record.scope.kind !== 'scene' ||
              live.record.scope.sceneId !== ctx.sceneId ||
              semanticFingerprint(live.record) !== dep.fingerprint ||
              semanticProofIntersection(dep.proofs, live.proofs).length !== dep.proofs.length
            )
              throw new SemanticRejection('semantic_anchor_unavailable')
            anchorDependencies.push(structuredClone(dep))
          }
        }
        let partitionKey: string | null = null
        if (proposal.kind !== 'episode_candidate') {
          if (!partitionRead) {
            partition = this.authority.partitionKey(ctx, personId)
            partitionRead = true
          }
          if (!partition || !/^[a-f0-9]{64}$/.test(partition))
            throw new SemanticRejection('semantic_partition_unavailable')
          partitionKey = partition
        }
        const inputKey = semanticHash([
          ctx.sceneId,
          parents,
          proposal.kind,
          proposal.selectedParentIds,
          proposal.episodeAnchor ?? null,
          proposal.episodeAnchor ? trustedEpisode!.membershipKey : null,
          partitionKey,
          anchorDependencies,
          anchorQualifications,
          1,
        ])
        if (this.storage.seen(inputKey)) {
          index++
          continue
        }
        if (pending.some((p) => p.inputKey === inputKey))
          throw new SemanticRejection('semantic_duplicate_proposal')
        const auto =
          proposal.kind === 'summary_candidate' &&
          !proposal.hypothesis &&
          this.authority.autoSummary(ctx) &&
          views.every(
            (v) =>
              ['fact', 'claim', 'term'].includes(v!.record.kind) &&
              ['explicit', 'observed'].includes(v!.record.epistemicKind ?? '') &&
              v!.record.status === 'active',
          )
        const now = this.now()
        const candidate: SemanticCandidate = {
          schemaVersion: 1,
          id: (this.options.id ?? randomUUID)(),
          sceneId: ctx.sceneId,
          personId,
          partitionKey,
          anchorDependencies,
          anchorQualifications,
          version: 1,
          inputKey,
          kind: proposal.kind,
          text: proposal.text,
          selectedParentIds: proposal.selectedParentIds,
          parents,
          episodeAnchor: proposal.episodeAnchor,
          ...(proposal.episodeAnchor
            ? { episodeMembershipKey: trustedEpisode!.membershipKey }
            : {}),
          hypothesis: proposal.hypothesis,
          status: auto ? 'admitted' : 'candidate',
          admission: auto ? 'structural_low_risk_summary' : 'qualified_candidate',
          createdAt: new Date(now).toISOString(),
          applicableUntil: proposal.hypothesis ? new Date(now + 7 * 86400_000).toISOString() : null,
        }
        SemanticCandidateSchema.parse(candidate)
        pending.push(candidate)
        index++
      }
      if (pending.length > this.storage.availableSlots(ctx.sceneId))
        throw new SemanticRejection('semantic_capacity')
    } catch (error) {
      if (!(error instanceof SemanticRejection)) throw error
      return {
        created: [] as SemanticCandidate[],
        rejected: [
          {
            index,
            reason:
              error instanceof Error && /^semantic_[a-z_]+$/.test(error.message)
                ? error.message
                : 'semantic_invalid_candidate',
          },
        ],
      }
    }
    for (const candidate of pending) this.storage.write(candidate, undefined, 'admit')
    return {
      created: structuredClone(pending),
      rejected: [] as { index: number; reason: string }[],
    }
  }
  current(ctx: SemanticContext, id: string): SemanticView | undefined {
    return this.project(ctx, id, new Map())
  }
  private project(
    ctx: SemanticContext,
    id: string,
    partitions: Map<string | null, string | undefined>,
    permitRejected = false,
    maintenance = false,
  ): SemanticView | undefined {
    this.authority.assertReady()
    if (ctx.simulation || (!maintenance && !this.authority.enabled(ctx))) return
    const candidate = this.storage.get(id)
    if (
      !candidate ||
      candidate.sceneId !== ctx.sceneId ||
      (candidate.status === 'rejected' && !permitRejected)
    )
      return
    if (candidate.partitionKey !== null) {
      if (!partitions.has(candidate.personId))
        partitions.set(candidate.personId, this.authority.partitionKey(ctx, candidate.personId))
      if (partitions.get(candidate.personId) !== candidate.partitionKey) return
    }
    const anchorDependencies: SemanticAnchorDependency[] = []
    for (const dep of candidate.anchorDependencies) {
      const live = this.authority.anchorCurrent(ctx, dep.ref)
      if (
        !live ||
        live.record.id !== dep.ref.id ||
        live.record.scope.kind !== 'scene' ||
        live.record.scope.sceneId !== ctx.sceneId ||
        semanticFingerprint(live.record) !== dep.fingerprint
      )
        return
      const proofs = semanticProofIntersection(dep.proofs, live.proofs)
      if (!proofs.length) return
      anchorDependencies.push({ ...dep, proofs })
    }
    const declarations: SemanticView['declarations'] = []
    const projectedParents: SemanticParent[] = []
    for (const parent of candidate.parents) {
      const view = this.resolve(ctx, parent)
      if (
        !view ||
        semanticFingerprint(view.record) !== parent.fingerprint ||
        !semanticProofIntersection(parent.proofs, view.proofs).length
      )
        return
      projectedParents.push({
        ...parent,
        proofs: semanticProofIntersection(parent.proofs, view.proofs),
        roots: parent.roots.filter((root) => view.roots.includes(root)),
      })
      declarations.push({ ref: parent.ref, record: structuredClone(view.record) })
    }
    if (
      candidate.episodeAnchor &&
      !this.authority.episodeAnchor(
        ctx,
        candidate.episodeAnchor,
        candidate.parents.map((p) => p.ref),
        candidate.episodeMembershipKey!,
      )
    )
      return
    return {
      candidate: structuredClone({ ...candidate, parents: projectedParents, anchorDependencies }),
      declarations,
      roots: [...new Set(projectedParents.flatMap((p) => p.roots))].sort(),
      applicability:
        candidate.applicableUntil && this.now() >= Date.parse(candidate.applicableUntil)
          ? 'review_needed'
          : 'current',
      interpretation:
        candidate.kind === 'summary_candidate'
          ? 'attributed_summary_not_verified_truth'
          : candidate.kind === 'duplicate_candidate'
            ? 'possible_duplicate_not_merged'
            : 'grouped_declarations_not_observed_event',
    }
  }
  /** Trusted root-lifecycle capture, before proof pruning, including while the feature is OFF. */
  captureQualification(ctx: SemanticContext): SemanticQualification[] {
    this.authority.assertReady()
    if (ctx.simulation) return []
    const partitions = new Map<string | null, string | undefined>()
    return this.storage.rows(ctx.sceneId).flatMap((candidate) => {
      if (!this.project(ctx, candidate.id, partitions, false, true)) return []
      return [
        {
          id: candidate.id,
          inputKey: candidate.inputKey,
          definitionHash: semanticDefinitionHash(candidate),
          partitionKey: candidate.partitionKey,
          parents: candidate.parents.map((p) => ({
            ref: structuredClone(p.ref),
            dependencyKey: p.dependencyKey,
          })),
        },
      ]
    })
  }
  list(ctx: SemanticContext, limit = 12) {
    this.authority.assertReady()
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 40)
      throw new Error('semantic_read_limit')
    if (ctx.simulation || !this.authority.enabled(ctx))
      return { items: [] as SemanticView[], coverage: 'complete' as const }
    const partitions = new Map<string | null, string | undefined>()
    const all = this.storage.rows(ctx.sceneId).flatMap((row) => {
      const view = this.project(ctx, row.id, partitions)
      return view ? [view] : []
    })
    return {
      items: all.slice(0, limit),
      coverage: all.length > limit ? ('limited' as const) : ('complete' as const),
    }
  }
  /** Trusted administration: rejected entries still require fresh evidence; stale rows reveal no body. */
  adminList(ctx: SemanticContext, limit = 12) {
    this.authority.assertReady()
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 40)
      throw new Error('semantic_read_limit')
    const unavailable: {
      id: string
      version: number
      status: SemanticCandidate['status']
      reason: 'qualification_unavailable'
    }[] = []
    const items: SemanticView[] = []
    if (ctx.simulation || !this.authority.enabled(ctx))
      return { items, unavailable, coverage: 'complete' as const }
    const rows = this.storage.rows(ctx.sceneId),
      partitions = new Map<string | null, string | undefined>()
    for (const row of rows.slice(0, limit)) {
      const view = this.project(ctx, row.id, partitions, true)
      if (view) items.push(view)
      else
        unavailable.push({
          id: row.id,
          version: row.version,
          status: row.status,
          reason: 'qualification_unavailable',
        })
    }
    return {
      items,
      unavailable,
      coverage: rows.length > limit ? ('limited' as const) : ('complete' as const),
    }
  }
  beginAttempt(
    ctx: SemanticContext,
    input: { inputKey: string; parents: readonly SemanticSelection[] },
  ): boolean {
    this.storage.requireTransaction()
    if (ctx.simulation || !this.authority.enabled(ctx) || !this.authority.learningEnabled(ctx))
      return false
    if (
      !/^[a-f0-9]{64}$/.test(input.inputKey) ||
      input.parents.length < 2 ||
      input.parents.length > 12
    )
      throw new Error('semantic_attempt_invalid')
    const rows = this.storage.attempts()
    if (rows.some((row) => row.inputKey === input.inputKey) || rows.length >= 4096) return false
    const parents = input.parents.map((p) => SemanticSelectionSchema.parse(p))
    if (new Set(parents.map((p) => p.ref.id)).size !== parents.length)
      throw new Error('semantic_duplicate_parent')
    const views = parents.map((p) => this.resolve(ctx, p))
    if (views.some((v) => !v)) return false
    this.storage.writeAttempt(
      {
        schemaVersion: 1,
        sceneId: ctx.sceneId,
        inputKey: input.inputKey,
        status: 'started',
        version: 1,
        parents,
        roots: [...new Set(views.flatMap((v) => v!.roots))].sort(),
      },
      undefined,
    )
    return true
  }
  finishAttempt(ctx: SemanticContext, inputKey: string, status: 'finished' | 'abandoned'): void {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    if (status !== 'finished' && status !== 'abandoned') throw new Error('semantic_attempt_invalid')
    const previous = this.storage
      .attempts()
      .find((row) => row.inputKey === inputKey && row.sceneId === ctx.sceneId)
    if (!previous) throw new Error('semantic_attempt_missing')
    if (previous.status !== 'started') return
    this.storage.writeAttempt({ ...previous, status, version: previous.version + 1 }, previous)
  }
  review(
    ctx: SemanticContext,
    id: string,
    expectedVersion: number,
    status: 'admitted' | 'rejected',
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation || !this.authority.enabled(ctx)) throw new Error('semantic_disabled')
    const current = this.storage.get(id)
    if (!current || current.sceneId !== ctx.sceneId || current.version !== expectedVersion)
      throw new Error('semantic_version_conflict')
    if (status === 'admitted' && !this.project(ctx, id, new Map(), true))
      throw new Error('semantic_parent_unavailable')
    if (status !== 'admitted' && status !== 'rejected') throw new Error('semantic_review_invalid')
    const next = {
      ...current,
      version: current.version + 1,
      status,
      admission: 'administrator_review' as const,
    }
    this.storage.write(next, current, 'review')
    return next
  }
}
