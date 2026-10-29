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
    seen.add(id)
    const children = new Set(this.ports.historyDependents(id))
    for (const record of this.ports.records()) {
      if (this.ports.proofs(record.id).some((proof) => proof.parents.includes(id))) {
        children.add(record.id)
      }
    }
    this.ports.removeProjection(id)
    if (!alreadyPurged.has(id)) this.ports.purgeHistory(id)
    if (this.ports.exists(id)) this.ports.touch(id)
    for (const child of children) this.purgeHistoryDependents(child, seen, alreadyPurged)
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
    for (const record of this.ports.records()) {
      if (
        record.lineageStatus !== 'verified' ||
        !['active', 'disputed'].includes(record.status) ||
        this.ports.supported(record, new Set(), cache)
      )
        continue
      unsupported.push(record)
    }
    for (const record of unsupported) {
      this.purgeHistoryDependents(record.id, purged)
      this.ports.invalidateRecord(record)
      this.ports.removeVector(record.id)
      invalidated.push(record.id)
    }
    return invalidated
  }
}
