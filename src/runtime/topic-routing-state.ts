/** Scene-local routing metadata. Never owns a body, transport retry or turn generation. */
export class TopicRoutingState {
  overflowed = 0
  readonly metadata = new Map<
    string,
    { provisional: boolean; cueSourceId?: string; cue?: { initiatorId: string; label: string } }
  >()
  readonly deferred = new Map<string, { schedule: boolean }>()
  readonly acknowledged = new Map<string, string>()
  readonly protectedSends = new Set<string>()
  readonly turns = new Map<string, string>()
  defer(id: string, schedule: boolean): 'queued' | 'duplicate' | 'full' {
    if (this.deferred.has(id)) return 'duplicate'
    if (this.deferred.size >= 60) {
      this.overflowed = Math.min(Number.MAX_SAFE_INTEGER, this.overflowed + 1)
      return 'full'
    }
    this.deferred.set(id, { schedule })
    return 'queued'
  }
  acknowledge(messageId: string, topicId: string): void {
    this.acknowledged.set(messageId, topicId)
    if (this.acknowledged.size > 500)
      this.acknowledged.delete(this.acknowledged.keys().next().value!)
  }
  forgetMessage(id: string): void {
    this.deferred.delete(id)
    for (const metadata of this.metadata.values())
      if (metadata.cueSourceId === id) {
        delete metadata.cue
        delete metadata.cueSourceId
      }
    this.acknowledged.delete(id)
  }
  evict(topicId: string): void {
    this.metadata.delete(topicId)
    for (const [id, topic] of this.acknowledged) if (topic === topicId) this.acknowledged.delete(id)
  }
  cancel(): void {
    this.deferred.clear()
    // Never turn uncertain already-attempted output into a fresh send opportunity.
    this.acknowledged.clear()
  }
}
