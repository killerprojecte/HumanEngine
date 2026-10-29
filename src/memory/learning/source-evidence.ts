import type { RuntimeMessage } from '../../runtime.ts'
import type { SourceEvidence } from '../domain/contracts.ts'

/** Only normalized runtime messages may supply identity and observation clocks. */
export function trustedSourceEvidence(
  messages: readonly RuntimeMessage[],
  recalled: (sceneId: string, messageId: string) => boolean,
): (SourceEvidence & { origin: 'live' })[] {
  const timestamp = (value: string | undefined) =>
    value &&
    /^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value))
      ? value
      : undefined
  return messages
    .filter((message) => message.origin === 'live')
    .map((message) => ({
      origin: 'live' as const,
      sceneId: message.sceneId,
      messageId: message.id,
      senderId: message.senderId,
      parts: message.parts.map((part) =>
        part.type === 'text'
          ? { type: 'text' as const, text: part.text }
          : { type: 'other' as const },
      ),
      sourceText: message.parts
        .filter((part) => part.type === 'text')
        .map((part) => part.text ?? '')
        .join('\n'),
      ...(message.sourceEventRef ? { eventRef: structuredClone(message.sourceEventRef) } : {}),
      available:
        message.status === 'available' &&
        !message.sourceEventRegistrationLimited &&
        !recalled(message.sceneId, message.id),
      isBot: message.senderId === 'bot',
      mentions: message.parts.flatMap((part, partIndex) =>
        part.type === 'mention' && part.userId
          ? [{ partIndex, userId: part.userId, isBot: part.userId === 'bot' }]
          : [],
      ),
      sentAt: timestamp(message.sentAt),
      receivedAt: timestamp(message.receivedAt),
    }))
}
