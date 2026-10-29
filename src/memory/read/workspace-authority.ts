import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import type { MemoryContext, MemoryRecord } from '../domain/contracts.ts'
import type { ProofParentReference, SupportProof } from '../domain/proofs.ts'
import type { CommitmentExposure, CommitmentView } from '../domain/commitments.ts'
import type {
  ExperienceAssertionSnapshot,
  ExperienceRefreshPort,
} from '../domain/experience-view.ts'

interface WorkspaceAuthorityPorts {
  ready(): void
  list(ctx: MemoryContext): MemoryRecord[]
  capture(ctx: MemoryContext, records: readonly MemoryRecord[]): readonly ProofParentReference[]
  current(
    ctx: MemoryContext,
    ref: ProofParentReference,
  ): { record: MemoryRecord; proofs: readonly SupportProof[] } | undefined
  sourceHead(sceneId: string, messageId: string): unknown
  currentPlan(ctx: MemoryContext, ref: CommitmentExposure): CommitmentView | undefined
}
/** Bounded authorized snapshots, not independent storage or a model authority bypass. */
export class WorkspaceAuthority implements ExperienceRefreshPort {
  constructor(private readonly ports: WorkspaceAuthorityPorts) {}

  private snapshot(
    ctx: MemoryContext,
    ref: ProofParentReference,
  ): ExperienceAssertionSnapshot | undefined {
    const view = this.ports.current(ctx, ref)
    if (
      !view ||
      view.record.internalUse ||
      view.record.scope.kind !== 'scene' ||
      view.record.scope.sceneId !== ctx.sceneId
    )
      return undefined
    const heads = view.proofs.flatMap((proof) =>
      proof.sources.map((source) => ({
        sceneId: source.sceneId,
        messageId: source.messageId,
        head: this.ports.sourceHead(source.sceneId, source.messageId) ?? null,
      })),
    )
    return {
      ref: { ...ref },
      record: structuredClone(view.record),
      qualificationKey: createHash('sha256')
        .update(JSON.stringify({ record: view.record, proofs: view.proofs, heads }))
        .digest('hex'),
    }
  }

  candidates(
    ctx: MemoryContext,
    options: { personId?: string; experienceOnly?: boolean; limit?: number } = {},
  ) {
    this.ports.ready()
    const limit = options.limit ?? 40
    if (!Number.isInteger(limit) || limit < 1 || limit > 40)
      throw new Error('workspace_candidate_limit')
    if (ctx.simulation)
      return { items: [] as ExperienceAssertionSnapshot[], coverage: 'complete' as const }
    const records = this.ports
      .list(ctx)
      .filter(
        (record) =>
          !record.internalUse &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === ctx.sceneId &&
          (!options.personId || record.scope.personId === options.personId) &&
          (!options.experienceOnly || record.kind === 'experience'),
      )
      .sort((a, b) => b.learnedAt.localeCompare(a.learnedAt) || a.id.localeCompare(b.id))
    const chosen = records.slice(0, limit)
    const refs = this.ports.capture(ctx, chosen)
    const byId = new Map(chosen.map((record) => [record.id, record]))
    const items = refs.flatMap((ref) => {
      const item = this.snapshot(ctx, ref)
      return item && isDeepStrictEqual(item.record, byId.get(ref.id)) ? [item] : []
    })
    return {
      items,
      coverage:
        records.length > limit || items.length !== chosen.length
          ? ('limited' as const)
          : ('complete' as const),
    }
  }

  resolveCurrentBatch(
    sceneId: string,
    references: {
      assertions: readonly ProofParentReference[]
      commitments: readonly CommitmentExposure[]
    },
  ) {
    this.ports.ready()
    if (references.assertions.length + references.commitments.length > 40)
      throw new Error('workspace_candidate_limit')
    const ctx: MemoryContext = { sceneId, simulation: false }
    const assertions = references.assertions.flatMap((ref) => {
      const item = this.snapshot(ctx, ref)
      return item ? [item] : []
    })
    const commitments = references.commitments.flatMap((ref) => {
      const view = this.ports.currentPlan(ctx, ref)
      return view && view.mutationVersion === ref.mutationVersion ? [structuredClone(view)] : []
    })
    return { assertions, commitments }
  }
}
