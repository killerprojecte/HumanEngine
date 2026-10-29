import type { MemoryContext, MemoryRecord } from '../domain/contracts.ts'

export interface PersonMemoryProfile {
  sceneId: string
  personId: string
  records: MemoryRecord[]
  timeline: Array<{
    memoryId: string
    recordedAt: string
    observedAt?: string
    validFrom?: string
    occurredAt?: string
    validUntil?: string
    status: MemoryRecord['status']
    origin: MemoryRecord['origin']
    sourceCount: number
  }>
}

/** Input must already pass the authoritative service's scope and support checks.
 * Nicknames and source speakers never imply the subject of a memory.
 */
export function projectPerson(
  ctx: MemoryContext,
  personId: string,
  visible: readonly MemoryRecord[],
): PersonMemoryProfile {
  const records =
    ctx.simulation || !personId
      ? []
      : visible.filter(
          (record) =>
            record.scope.kind === 'scene' &&
            record.scope.sceneId === ctx.sceneId &&
            record.scope.personId === personId,
        )
  return {
    sceneId: ctx.sceneId,
    personId,
    records,
    timeline: records
      .map((record) => ({
        memoryId: record.id,
        recordedAt: record.learnedAt,
        ...(record.observedAt ? { observedAt: record.observedAt } : {}),
        ...(record.validFrom ? { validFrom: record.validFrom } : {}),
        ...(record.occurredAt ? { occurredAt: record.occurredAt } : {}),
        ...(record.validUntil ? { validUntil: record.validUntil } : {}),
        status: record.status,
        origin: record.origin,
        sourceCount: record.sources.length,
      }))
      .sort(
        (a, b) => a.recordedAt.localeCompare(b.recordedAt) || a.memoryId.localeCompare(b.memoryId),
      ),
  }
}

export function projectPeople(ctx: MemoryContext, visible: readonly MemoryRecord[]) {
  const counts = new Map<string, number>()
  if (!ctx.simulation)
    for (const record of visible) {
      if (
        record.scope.kind !== 'scene' ||
        record.scope.sceneId !== ctx.sceneId ||
        !record.scope.personId
      )
        continue
      counts.set(record.scope.personId, (counts.get(record.scope.personId) ?? 0) + 1)
    }
  return [...counts]
    .map(([personId, recordCount]) => ({ personId, recordCount }))
    .sort((a, b) => a.personId.localeCompare(b.personId))
}
