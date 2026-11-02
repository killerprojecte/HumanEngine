import type { MemoryRecord } from '../domain/contracts.ts'

export type AdminMemoryPageQuery = {
  page: number
  limit: number
  scene: string
  person: string
  kind: string
  status: string
  q: string
  locale: string
}

/** Administration selection only. The route must complete the original eager
 * read, internal-record exclusion and revision presentation before calling this.
 * This does not replace model-read authorization or lifecycle maintenance.
 */
export function selectAdminMemoryPage<T extends MemoryRecord>(
  records: readonly T[],
  query: AdminMemoryPageQuery,
) {
  const sceneId = query.scene.startsWith('scene:') ? query.scene.slice(6) : ''
  // Keep the administrative person domain: future/inactive records count too,
  // independent of text, kind, status and person filters. These compact facets
  // are not capped and must not be advertised as a bounded directory response.
  const personIds = sceneId
    ? [
        ...new Set(
          records.flatMap((record) =>
            record.scope.kind === 'scene' &&
            record.scope.sceneId === sceneId &&
            record.scope.personId
              ? [record.scope.personId]
              : [],
          ),
        ),
      ]
    : []
  const person = query.person.trim()
  const text = query.q.trim().toLocaleLowerCase(query.locale)
  const matching = records.filter((record) => {
    const scope = record.scope
    return (
      (query.scene === 'all' ||
        (query.scene === 'public' && scope.kind === 'sharedPublicKnowledge') ||
        (scope.kind === 'scene' && scope.sceneId === sceneId)) &&
      (!person ||
        (!!sceneId &&
          scope.kind === 'scene' &&
          scope.sceneId === sceneId &&
          scope.personId === person)) &&
      (query.kind === 'all' || record.kind === query.kind) &&
      (query.status === 'all' || record.status === query.status) &&
      (!text || record.content.toLocaleLowerCase(query.locale).includes(text))
    )
  })
  // Stable sort retains original rowid-desc order for equal learnedAt values.
  matching.sort((a, b) => b.learnedAt.localeCompare(a.learnedAt, query.locale))
  const page = Math.min(query.page, Math.max(1, Math.ceil(matching.length / query.limit)))
  return {
    items: matching.slice((page - 1) * query.limit, page * query.limit),
    total: records.length,
    matched: matching.length,
    page,
    limit: query.limit,
    personIds,
  }
}
