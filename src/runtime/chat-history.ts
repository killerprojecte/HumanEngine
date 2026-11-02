/** Preserve recent live messages while the platform catches up; fresh local copies win by ID. */
export function mergeRecentHistory<T extends { id: string; sentAt: string }>(
  local: T[],
  remote: T[],
): T[] {
  return [...new Map([...remote, ...local].map((message) => [message.id, message])).values()]
    .sort((a, b) => a.sentAt.localeCompare(b.sentAt))
    .slice(-60)
}
