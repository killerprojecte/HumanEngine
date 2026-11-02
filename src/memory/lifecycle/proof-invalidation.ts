/** Only provenance links are relevant here; temporal/version applicability belongs to the read layer. */
export type LifecycleProof = {
  readonly parents: readonly string[]
  readonly invalidatedBy?: string
}
export type LifecycleRecord = {
  readonly id: string
  readonly status: string
  readonly lineageStatus?: string
}

export type ProofInvalidationPorts<R extends LifecycleRecord, P extends LifecycleProof> = {
  records: () => readonly R[]
  proofs: (id: string) => readonly P[]
  historyDependents: (id: string) => Iterable<string>
  exists: (id: string) => boolean
  removeProjection: (id: string) => void
  purgeHistory: (id: string) => void
  /** Mark existing records for a provenance-safe snapshot rebuild at transaction flush. */
  touch: (id: string) => void
  saveProofs: (id: string, proofs: P[]) => void
  /** Provenance availability only. Do not pass current-time or parent-version applicability here. */
  supported: (record: R, path: Set<string>, cache: Map<string, boolean>) => boolean
  invalidateRecord: (record: R) => void
  removeVector: (id: string) => void
}

/**
 * Transaction-free lifecycle orchestration. The caller owns the surrounding atomic
 * transaction and all storage/authority checks; ports must not swallow failures.
 * Proof alternatives remain OR, while each proof's parent list remains AND.
 */
export class ProofInvalidation<R extends LifecycleRecord, P extends LifecycleProof> {
  constructor(private readonly ports: ProofInvalidationPorts<R, P>) {}

  dependsOn(id: string, wanted: string, seen = new Set<string>()): boolean {
    if (id === wanted) return true
    if (seen.has(id)) return false
    seen.add(id)
    return this.ports
      .proofs(id)
      .some((proof) => proof.parents.some((parent) => this.dependsOn(parent, wanted, seen)))
  }

  /**
   * Capture both current and historical reverse links before purging any history.
   * Touch surviving rows even when an independent OR proof keeps them supported:
   * their old history is gone, so the coordinator must rebuild a safe current snapshot.
   */
  purgeHistoryDependents(
    id: string,
    seen = new Set<string>(),
    alreadyPurged = new Set<string>(),
  ): void {
    if (seen.has(id)) return
    this.purgeWithGraph(id, seen, alreadyPurged, this.currentDependents(this.ports.records()))
  }

  /** Current proofs are immutable during a synchronous purge operation: production
   * privacy ports only remove history/projections and mark revision rows dirty.
   * Never retain this graph across operations or authority/mutation boundaries.
   */
  private currentDependents(records: readonly R[]): Map<string, Set<string>> {
    const graph = new Map<string, Set<string>>()
    for (const record of records)
      for (const proof of this.ports.proofs(record.id))
        for (const parent of proof.parents) {
          let children = graph.get(parent)
          if (!children) graph.set(parent, (children = new Set()))
          children.add(record.id)
        }
    return graph
  }

  private purgeWithGraph(
    id: string,
    seen: Set<string>,
    alreadyPurged: Set<string>,
    graph: ReadonlyMap<string, ReadonlySet<string>>,
  ): void {
    const pending = [id]
    while (pending.length) {
      const current = pending.pop()!
      if (seen.has(current)) continue
      seen.add(current)
      // Historical links remain live and must be captured before this node's
      // history is removed. Preserve history-first/current-record child order.
      const children = new Set(this.ports.historyDependents(current))
      for (const child of graph.get(current) ?? []) children.add(child)
      this.ports.removeProjection(current)
      if (!alreadyPurged.has(current)) this.ports.purgeHistory(current)
      if (this.ports.exists(current)) this.ports.touch(current)
      // Reverse push reproduces recursive depth-first event ordering, without
      // overflowing the JS stack on long provenance chains.
      for (const child of [...children].reverse()) pending.push(child)
    }
  }

  /** Mark only proofs requiring the replaced/deleted premise; do not erase independent alternatives. */
  invalidateDependents(id: string, shouldInvalidate: (proof: P) => boolean = () => true): void {
    for (const record of this.ports.records()) {
      const proofs = this.ports.proofs(record.id)
      if (!proofs.some((proof) => proof.parents.includes(id) && shouldInvalidate(proof))) continue
      this.ports.saveProofs(
        record.id,
        proofs.map((proof) =>
          proof.parents.includes(id) && shouldInvalidate(proof)
            ? { ...proof, invalidatedBy: id }
            : proof,
        ),
      )
    }
  }

  /** Caller wraps this entire operation in its revision transaction. */
  refreshSupport(): string[] {
    const invalidated: string[] = []
    const cache = new Map<string, boolean>()
    const purged = new Set<string>()
    const unsupported: R[] = []
    // Finish bounded analysis before emitting irreversible external privacy intents.
    // The surrounding synchronous transaction keeps this plan and its application atomic.
    const records = this.ports.records()
    for (const record of records) {
      if (
        record.lineageStatus !== 'verified' ||
        !['active', 'disputed'].includes(record.status) ||
        this.ports.supported(record, new Set(), cache)
      )
        continue
      unsupported.push(record)
    }
    const graph = unsupported.length
      ? this.currentDependents(records)
      : new Map<string, Set<string>>()
    for (const record of unsupported) {
      this.purgeWithGraph(record.id, purged, new Set(), graph)
      this.ports.invalidateRecord(record)
      this.ports.removeVector(record.id)
      invalidated.push(record.id)
    }
    return invalidated
  }
}
