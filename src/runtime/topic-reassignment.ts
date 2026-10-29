/** Validate a complete model grouping before mutating any retained routing state. */
export function validTopicReassignment(input: {
  groups: readonly { topicId: string; messageIds: readonly string[] }[]
  deliveredIds: readonly string[]
  currentTopicId: string
  selectedTopicId?: string
  retained: readonly { id: string; messageIds: readonly string[]; protected: boolean }[]
  maxSlots?: number
}): boolean {
  const allowed = new Set(input.deliveredIds),
    seen = new Set<string>(),
    ids = new Set<string>()
  for (const group of input.groups) {
    if (ids.has(group.topicId)) return false
    ids.add(group.topicId)
    for (const id of group.messageIds) {
      if (!allowed.has(id) || seen.has(id)) return false
      seen.add(id)
      if (
        input.retained.some(
          (t) =>
            t.protected &&
            t.messageIds.includes(id) &&
            t.id !==
              (group.topicId === input.selectedTopicId ? input.currentTopicId : group.topicId),
        )
      )
        return false
    }
  }
  // Stable identities: selected output stays the running topic. No UUID remapping trick.
  const resulting = new Set(input.retained.map((t) => t.id))
  for (const group of input.groups) {
    const id = group.topicId === input.selectedTopicId ? input.currentTopicId : group.topicId
    if (id === input.currentTopicId && group.topicId !== input.selectedTopicId) return false
    if (input.retained.some((t) => t.id === id && t.id !== input.currentTopicId && t.protected))
      return false
    resulting.add(id)
  }
  return resulting.size <= (input.maxSlots ?? 4)
}
