/** Process-local routing metadata only: never source authority, message bodies or replay state. */
export class TopicRoutingHistory {
  private readonly entries = new Map<string, number>()
  get size(): number {
    return this.entries.size
  }
  has(id: string): boolean {
    return this.entries.has(id)
  }
  observe(id: string, receivedAt: string): void {
    const at = Date.parse(receivedAt)
    if (id && Number.isFinite(at)) this.entries.set(id, at)
  }
  prune(input: {
    cutoff: number
    maxUnprotected: number
    preferred: ReadonlySet<string>
    protected: ReadonlySet<string>
  }): number {
    if (!Number.isSafeInteger(input.maxUnprotected) || input.maxUnprotected < 0)
      throw new Error('invalid_topic_history_limit')
    let removed = 0,
      unprotected = 0
    for (const [id, at] of this.entries) {
      if (input.protected.has(id)) continue
      if (at < input.cutoff) {
        this.entries.delete(id)
        removed++
      } else unprotected++
    }
    // Map order is original observed ingress order; updating a body never fabricates recency.
    for (const id of this.entries.keys()) {
      if (unprotected <= input.maxUnprotected) break
      if (input.protected.has(id) || input.preferred.has(id)) continue
      this.entries.delete(id)
      removed++
      unprotected--
    }
    // The caller's preferred body window is <=500 and the configured cap is >=500.
    // A zero limit is the explicit cache-disabled cleanup path, with no preferred IDs.
    if (unprotected > input.maxUnprotected) throw new Error('topic_history_preferred_overflow')
    return removed
  }
}
