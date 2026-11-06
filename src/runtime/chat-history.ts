import type { RuntimeMessage } from '../runtime.ts'

/** Normalize platform bot identities without changing scene/message identifiers. */
export function normalizeBotMessage<T extends RuntimeMessage>(message: T, bot?: string): T {
  if (!bot) return message
  return {
    ...message,
    senderId: message.senderId === bot ? 'bot' : message.senderId,
    parts: message.parts.map((part) =>
      part.type === 'mention' && part.userId === bot ? { ...part, userId: 'bot' } : part,
    ),
  }
}

/** Preserve recent live messages while the platform catches up; fresh local copies win by ID. */
export function mergeRecentHistory<T extends { id: string; sentAt: string }>(
  local: T[],
  remote: T[],
): T[] {
  return [...new Map([...remote, ...local].map((message) => [message.id, message])).values()]
    .sort((a, b) => a.sentAt.localeCompare(b.sentAt))
    .slice(-60)
}
