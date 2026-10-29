import type { SemanticAssertion, SemanticContext, SemanticRef } from './contracts.ts'
import type { SemanticEpisodeGroup } from './episode-anchors.ts'
import type { SemanticDirty } from './input.ts'

/** Post-stack, exact-ref notifications only. A rolled-back checkpoint cannot become a model job. */
export class SemanticNotifications {
  private readonly pending = new Map<
    string,
    { sceneId: string; ref: SemanticRef; reason: SemanticDirty['reason']; policy: string }
  >()
  private readonly listeners = new Set<(dirty: SemanticDirty) => void>()
  private scheduled = false
  private disposed = false
  constructor(
    private readonly ports: {
      enabled(sceneId: string): boolean
      policyIdentity(sceneId: string): string
      current(ctx: SemanticContext, ref: SemanticRef): SemanticAssertion | undefined
      episodes?(ctx: SemanticContext, ref: SemanticRef): readonly SemanticEpisodeGroup[]
      failed(): void
    },
  ) {}
  subscribe(listener: (dirty: SemanticDirty) => void) {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }
  notify(sceneId: string, ref: SemanticRef, reason: SemanticDirty['reason']) {
    if (this.disposed || !this.listeners.size || !this.ports.enabled(sceneId)) return
    const key = JSON.stringify([sceneId, ref])
    if (!this.pending.has(key) && this.pending.size >= 1024) {
      this.ports.failed()
      return
    }
    this.pending.set(key, {
      sceneId,
      ref: structuredClone(ref),
      reason,
      policy: this.ports.policyIdentity(sceneId),
    })
    if (this.scheduled) return
    this.scheduled = true
    queueMicrotask(() => this.flush())
  }
  private flush() {
    this.scheduled = false
    const batch = [...this.pending.values()]
    this.pending.clear()
    if (this.disposed) return
    for (const { sceneId, ref, reason, policy } of batch) {
      try {
        if (!this.ports.enabled(sceneId) || this.ports.policyIdentity(sceneId) !== policy) continue
        const current = this.ports.current({ sceneId, simulation: false }, ref)
        if (current && !current.derived && current.rootCoverage === 'complete') {
          const personId =
            current.record.scope.kind === 'scene' ? (current.record.scope.personId ?? null) : null
          const dirty: SemanticDirty = {
            sceneId,
            personId,
            trigger: { ref, dependencyKey: current.dependencyKey },
            reason,
          }
          for (const listener of this.listeners) listener(structuredClone(dirty))
        }
        for (const group of this.ports.episodes?.({ sceneId, simulation: false }, ref) ?? []) {
          const trigger = group.parents[0]
          if (!trigger) continue
          const dirty: SemanticDirty = {
            sceneId,
            personId: null,
            trigger,
            episodeAnchor: group.anchor,
            reason,
          }
          for (const listener of this.listeners) listener(structuredClone(dirty))
        }
      } catch {
        // Optional maintenance must not retroactively fail an already committed assertion.
        this.ports.failed()
      }
    }
  }
  dispose() {
    this.disposed = true
    this.pending.clear()
    this.listeners.clear()
  }
}
