import { isDeepStrictEqual } from 'node:util'
import type { CommitmentView } from '../domain/commitments.ts'
import type { ExperienceRefreshPort, ExperienceView } from '../domain/experience-view.ts'
import { verifyExperienceView } from '../projections/experience-view.ts'
import type { WorkingSetViewItem } from '../working-set/contracts.ts'
import type { WorkingSetService } from '../working-set/service.ts'
import type { MemoryWorkspace } from './memory-workspace.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export type WorkingSetPage = ReturnType<MemoryWorkspace['workingSet']>
export type WorkspaceStateEvidence =
  | { stateKind: 'working_set'; page: WorkingSetPage }
  | { stateKind: 'experience_view'; page: ExperienceView }
export type WorkspaceExposureInput =
  | { stateKind: 'working_set'; personId?: string; includeCold?: boolean }
  | { stateKind: 'experience_view'; personId?: string }
export interface WorkspaceExposurePort {
  workspace: Pick<MemoryWorkspace, 'workingSet' | 'episodes'> & {
    access: Pick<WorkingSetService, 'view'>
  }
  authority: ExperienceRefreshPort
  enabled(ctx: ModelMemoryContext, kind: WorkspaceStateEvidence['stateKind']): boolean
  ready(ctx: ModelMemoryContext): boolean
  /** State delivery is not ordinary memory parent exposure or a learning permission. */
  onDelivered?(ctx: ModelMemoryContext, values: readonly WorkspaceStateEvidence[]): void
}
const context = (ctx: ModelMemoryContext) => ({
  sceneId: ctx.scene.id,
  simulation: ctx.mode === 'simulation',
})
const planSources = (view: CommitmentView) =>
  [...view.evidence, ...view.requiredProposalGroups.flat()].flatMap((evidence) =>
    evidence.proofs.flatMap((proof) => proof.sources.map((source) => source.messageId)),
  )
const refKey = (ref: { id: string; version: number; deletionEpoch: number }) =>
  JSON.stringify([ref.id, ref.version, ref.deletionEpoch])
const policyWitness = (value: WorkingSetViewItem) => ({
  ref: value.ref,
  dependencyKey: value.dependencyKey,
  pinned: value.pinned,
  pinIntent: value.pinIntent,
  pinRevision: value.pinRevision,
  pinStatus: value.pinStatus,
  policyCoverage: value.policyCoverage,
  protection: value.protection,
})

/** Whole bounded pages preserve limited coverage. Prepared/copy/partial values confer no authority. */
export class WorkspaceExposure {
  private readonly exposure: StateExposure<WorkspaceStateEvidence, WorkspaceExposureInput>
  constructor(private readonly port: WorkspaceExposurePort) {
    this.exposure = new StateExposure<WorkspaceStateEvidence, WorkspaceExposureInput>({
      query: (ctx, input) => {
        if (!port.enabled(ctx, input.stateKind) || !port.ready(ctx)) return []
        if (input.stateKind === 'working_set') {
          const page = port.workspace.workingSet(context(ctx), {
            personId: input.personId,
            includeCold: input.includeCold,
          })
          if (!page.enabled) return []
          if (
            !Array.isArray(page.items) ||
            page.items.length > 12 ||
            !['complete', 'limited'].includes(page.coverage)
          )
            throw new Error('workspace_page_invalid')
          return [{ stateKind: 'working_set' as const, page }]
        }
        const page = port.workspace.episodes(context(ctx), { personId: input.personId })
        if (
          !Array.isArray(page.items) ||
          page.items.length > 12 ||
          !['complete', 'limited'].includes(page.coverage.status)
        )
          throw new Error('workspace_page_invalid')
        return [{ stateKind: 'experience_view' as const, page }]
      },
      valid: (ctx, values) => {
        try {
          return (
            values.length === 1 &&
            values.every(
              (value) =>
                port.ready(ctx) &&
                port.enabled(ctx, value.stateKind) &&
                (value.stateKind === 'experience_view'
                  ? verifyExperienceView(ctx.scene.id, value.page, port.authority)
                  : this.validWorkingSet(ctx, value.page)),
            )
          )
        } catch {
          return false
        }
      },
      sourceIds: (values) => [
        ...new Set(
          values.flatMap((value) =>
            value.stateKind === 'working_set'
              ? value.page.items.flatMap((item) =>
                  item.kind === 'assertion'
                    ? item.snapshot.record.sources.map((source) => source.messageId)
                    : planSources(item.view),
                )
              : value.page.items.flatMap((item) =>
                  item.kind === 'reported_experience'
                    ? item.record.sources.map((source) => source.messageId)
                    : planSources(item.view),
                ),
          ),
        ),
      ],
      onDelivered: (ctx, values) => port.onDelivered?.(ctx, values),
    })
  }
  private validWorkingSet(ctx: ModelMemoryContext, page: WorkingSetPage): boolean {
    if (
      !page.enabled ||
      !Array.isArray(page.items) ||
      page.items.length > 12 ||
      !['complete', 'limited'].includes(page.coverage)
    )
      return false
    const keys = new Set(
      page.items.map((item) =>
        JSON.stringify([
          item.kind,
          item.kind === 'assertion' ? item.snapshot.ref.id : item.view.ref.id,
        ]),
      ),
    )
    if (keys.size !== page.items.length) return false
    for (const item of page.items) {
      if (item.kind === 'assertion') {
        if (
          item.snapshot.record.scope.kind !== 'scene' ||
          item.snapshot.record.scope.sceneId !== ctx.scene.id ||
          item.snapshot.record.id !== item.snapshot.ref.id ||
          item.accessibility.ref.kind !== 'assertion' ||
          refKey(item.snapshot.ref) !== refKey(item.accessibility.ref)
        )
          return false
      } else if (
        item.view.sceneId !== ctx.scene.id ||
        item.accessibility.ref.kind !== 'commitment' ||
        refKey(item.view.ref) !== refKey(item.accessibility.ref) ||
        item.view.mutationVersion !== item.accessibility.ref.mutationVersion
      )
        return false
    }
    // View performs one current exact-ref/dependency authorization batch. Access
    // counts, selected timestamps and clock-only hot/cold changes are not evidence drift.
    const current = this.port.workspace.access.view(
      context(ctx),
      page.items.map((item) => ({
        ref: item.accessibility.ref,
        dependencyKey: item.accessibility.dependencyKey,
      })),
      { includeCold: true },
    )
    if (!current.enabled || current.items.length !== page.items.length) return false
    const metadata = new Map(
      current.items.map((item) => [JSON.stringify([item.ref.kind, item.ref.id]), item]),
    )
    if (
      page.items.some(
        (item) =>
          !isDeepStrictEqual(
            policyWitness(item.accessibility),
            policyWitness(
              metadata.get(
                JSON.stringify([item.accessibility.ref.kind, item.accessibility.ref.id]),
              )!,
            ),
          ),
      )
    )
      return false
    const assertions = page.items.flatMap((item) =>
      item.kind === 'assertion' ? [item.snapshot] : [],
    )
    const plans = page.items.flatMap((item) => (item.kind === 'commitment' ? [item.view] : []))
    if (assertions.length || plans.length) {
      const refreshed = this.port.authority.resolveCurrentBatch(ctx.scene.id, {
        assertions: assertions.map((item) => item.ref),
        commitments: plans.map((view) => ({ ...view.ref, mutationVersion: view.mutationVersion })),
      })
      if (
        !refreshed ||
        refreshed.assertions.length !== assertions.length ||
        refreshed.commitments.length !== plans.length
      )
        return false
      const records = new Map(refreshed.assertions.map((item) => [refKey(item.ref), item]))
      const planViews = new Map(refreshed.commitments.map((view) => [view.ref.id, view]))
      if (
        assertions.some((item) => !isDeepStrictEqual(item, records.get(refKey(item.ref)))) ||
        plans.some((view) => !isDeepStrictEqual(view, planViews.get(view.ref.id)))
      )
        return false
    }
    return true
  }
  query(ctx: ModelMemoryContext, input: WorkspaceExposureInput): readonly WorkspaceStateEvidence[] {
    return this.exposure.query(ctx, input)
  }
  workingSet(
    ctx: ModelMemoryContext,
    input: { personId?: string; includeCold?: boolean } = {},
  ): readonly WorkspaceStateEvidence[] {
    return this.query(ctx, { stateKind: 'working_set', ...input })
  }
  episodes(
    ctx: ModelMemoryContext,
    input: { personId?: string } = {},
  ): readonly WorkspaceStateEvidence[] {
    return this.query(ctx, { stateKind: 'experience_view', ...input })
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]): readonly WorkspaceStateEvidence[] {
    return this.exposure.deliver(ctx, values)
  }
  readDelivered(ctx: ModelMemoryContext): readonly WorkspaceStateEvidence[] {
    return this.exposure.readDelivered(ctx)
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    return this.exposure.captureGuard(ctx)
  }
}
