import type { ModelMemoryContext } from '../read/model-exposure.ts'
import type { ProofParentReference } from '../domain/proofs.ts'
import type { MemoryWorkspace } from '../read/memory-workspace.ts'
import type { WorkingSetSelection } from './contracts.ts'

const object = (value: unknown): Record<string, unknown> | undefined =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
/** A retained context selection is not a model invocation, successful use, or a truth signal. */
export class WorkspaceSelectionTracker {
  private readonly delivered = new WeakMap<ModelMemoryContext, Map<string, WorkingSetSelection>>()
  constructor(
    private readonly workspace: MemoryWorkspace,
    private readonly enabled: () => boolean,
  ) {}
  capture(ctx: ModelMemoryContext, refs: readonly ProofParentReference[]) {
    if (!this.enabled() || ctx.mode !== 'real' || ctx.signal.aborted || !refs.length) return
    const map = this.delivered.get(ctx) ?? new Map<string, WorkingSetSelection>()
    for (let i = 0; i < refs.length; i += 40) {
      const snapshots = this.workspace.resolve(
        { sceneId: ctx.scene.id, simulation: false },
        refs.slice(i, i + 40).map((ref) => ({
          kind: 'assertion' as const,
          id: ref.id,
          version: ref.version,
          deletionEpoch: ref.deletionEpoch,
        })),
      )
      for (const { ref, dependencyKey } of snapshots)
        if (!map.has(ref.id)) map.set(ref.id, { ref, dependencyKey })
    }
    this.delivered.set(ctx, map)
  }
  selected(ctx: ModelMemoryContext & { turnId: string }, brief: unknown) {
    if (!this.enabled() || ctx.mode !== 'real' || ctx.signal.aborted) return
    const candidates = this.delivered.get(ctx)
    if (!candidates) return
    const ids = new Set<string>()
    const root = object(brief)
    for (const receipt of Array.isArray(root?.evidence) ? root.evidence : []) {
      const value = object(receipt)
      for (const memory of Array.isArray(value?.memories) ? value.memories : []) {
        const id = object(memory)?.id
        if (typeof id === 'string') ids.add(id)
      }
      // Only assertion bodies actually retained in the final whole-page handoff count.
      for (const state of Array.isArray(value?.states) ? value.states : []) {
        const item = object(state),
          page = object(item?.page)
        for (const entry of Array.isArray(page?.items) ? page.items : []) {
          const record = object(entry)
          const snapshot = object(record?.snapshot)
          const ref = object(snapshot?.ref ?? record?.assertionRef)
          if (
            typeof ref?.id === 'string' &&
            (record?.kind === 'assertion' || record?.kind === 'reported_experience')
          )
            ids.add(ref.id)
        }
      }
    }
    const selections = [...ids].flatMap((id) => (candidates.has(id) ? [candidates.get(id)!] : []))
    for (let i = 0; i < selections.length; i += 40)
      this.workspace.access.recordSelected(
        { sceneId: ctx.scene.id, simulation: false },
        { turnId: ctx.turnId, selectedAt: Date.now(), selections: selections.slice(i, i + 40) },
      )
  }
}
