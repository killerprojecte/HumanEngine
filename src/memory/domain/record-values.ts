import { createHash } from 'node:crypto'
import type { MemoryRecord, MemoryScope, MemorySource } from './contracts.ts'
export const hash = (value: string) => createHash('sha256').update(value).digest('hex')
export const scopeKey = (scope: MemoryScope) =>
  scope.kind === 'scene' ? `scene:${scope.sceneId}` : 'shared:public'
export const fingerprint = (scope: MemoryScope, content: string) =>
  hash(
    JSON.stringify([
      scopeKey(scope),
      scope.kind === 'scene' ? (scope.personId ?? '') : '',
      content.trim().replace(/\s+/g, ' ').toLowerCase(),
    ]),
  )
export const historicalFingerprint = (memory: MemoryRecord) =>
  `history:${fingerprint(memory.scope, memory.content)}:${memory.id}`

export const date = (value: string | undefined) =>
  value === undefined || (typeof value === 'string' && Number.isFinite(Date.parse(value)))

export function uniqueSources(sources: MemorySource[]): MemorySource[] {
  return [
    ...new Map(
      sources.map((source) => [
        JSON.stringify([
          source.sceneId,
          source.messageId,
          source.senderId,
          source.eventRef ?? null,
        ]),
        {
          sceneId: source.sceneId,
          messageId: source.messageId,
          senderId: source.senderId,
          ...(source.eventRef ? { eventRef: structuredClone(source.eventRef) } : {}),
        },
      ]),
    ).values(),
  ]
}

export function validateMemory(memory: MemoryRecord) {
  if (
    typeof memory.content !== 'string' ||
    !memory.content ||
    memory.content.length > 20_000 ||
    typeof memory.locked !== 'boolean' ||
    !['active', 'disputed', 'superseded', 'invalidated'].includes(memory.status) ||
    !['fact', 'claim', 'term', 'experience', 'summary'].includes(memory.kind) ||
    !['low', 'medium', 'high'].includes(memory.importance) ||
    !Number.isFinite(memory.confidence) ||
    memory.confidence < 0 ||
    memory.confidence > 1 ||
    (memory.epistemicKind !== undefined &&
      ['reported', 'inferred', 'hypothesis'].includes(memory.epistemicKind) &&
      memory.kind !== 'claim') ||
    !date(memory.validFrom) ||
    (memory.validFrom !== undefined &&
      memory.validUntil !== undefined &&
      Date.parse(memory.validFrom) > Date.parse(memory.validUntil)) ||
    (memory.epistemicKind !== undefined &&
      !['explicit', 'reported', 'observed', 'inferred', 'hypothesis'].includes(
        memory.epistemicKind,
      )) ||
    !date(memory.occurredAt) ||
    !date(memory.validUntil)
  )
    throw new Error('invalid_memory')
  if (memory.scope.kind === 'scene') {
    const sceneId = memory.scope.sceneId
    if (!sceneId || memory.sources.some((source) => source.sceneId !== sceneId))
      throw new Error('memory_scope_mismatch')
  } else if (
    memory.scope.kind !== 'sharedPublicKnowledge' ||
    'personId' in memory.scope ||
    'sceneId' in memory.scope
  )
    throw new Error('invalid_public_scope')
  if (memory.sources.some((source) => !source.sceneId || !source.messageId || !source.senderId))
    throw new Error('invalid_source')
}
