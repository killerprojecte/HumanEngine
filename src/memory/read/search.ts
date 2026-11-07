import type { Store } from '../../store.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import { AssertionAuthority } from './assertion-authority.ts'
import { assertionScopeFilter } from './scope.ts'
import { AssertionHistoryReader } from '../storage/assertion-history.ts'
import {
  AssertionRepository,
  readAssertionRow as read,
  type AssertionRow as Row,
} from '../storage/assertion-repository.ts'

/** Plain search data only. No caller callback, SQL, repository or authority is accepted. */
export interface MemorySearchRead {
  sceneId: string
  simulation: boolean
  personId?: string
  query: string
  terms: string[]
  options?: { spaceId: string; vector: Uint8Array; limit?: number }
}

/** Internal fixed read operation. Recovery snapshots are owned by ledger.ts;
 * refreshSupport and vector validation must finish before entering this read.
 */
export function readMemorySearch(store: Store, input: MemorySearchRead): MemoryRecord[] {
  const { query, terms, options } = input
  const scope = assertionScopeFilter(input, input.personId)
  if (!scope) return []
  const vector = options ? Buffer.from(options.vector) : undefined
  const denyWrite = () => {
    throw new Error('memory_search_read_only')
  }
  const repository = new AssertionRepository(store, {
    beforeWrite: denyWrite,
    removeProjection: denyWrite,
  })
  const authority = new AssertionAuthority(store, repository, {
    history: new AssertionHistoryReader(store.db),
  })
  return repository.withSupportReadStatements(() => {
    const applicability = new Map<string, boolean>()
    // Scope only: do not prefilter time eligibility in an earlier statement.
    // A record becoming eligible before the final SQL still needs applicability.
    const candidates = store.db
      .prepare(`SELECT * FROM memories WHERE ${scope.sql} ORDER BY rowid DESC`)
      .all(...scope.params) as Row[]
    const unsupported = candidates
      .map(read)
      .filter(
        (record) =>
          !authority.currentApplicable(record, new Set(), applicability) ||
          (input.simulation && !authority.supported(record)),
      )
      .map((record) => record.id)
    const lexical = terms.length ? 'humanengine_lexical(scoped.content, ?)' : '0.0'
    const semantic = options
      ? 'CASE WHEN v.embedding IS NULL THEN 0.0 ELSE max(0.0, 1.0 - vec_distance_cosine(v.embedding, ?)) END'
      : '0.0'
    const join = options
      ? 'LEFT JOIN memory_vectors v ON v.memory_id=scoped.id AND v.space_id=?'
      : ''
    const limit = Math.max(1, Math.min(options?.limit ?? 20, 100))
    const rows = store.db
      .prepare(
        `
      WITH scoped AS MATERIALIZED (
        SELECT rowid AS ordinal, * FROM memories
        WHERE ${scope.sql} AND id NOT IN (SELECT value FROM json_each(?)) AND json_extract(value,'$.internalUse') IS NULL AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed')
          AND (json_extract(value,'$.validFrom') IS NULL OR julianday(json_extract(value,'$.validFrom')) <= julianday('now')) AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now'))
      ), relevant AS MATERIALIZED (
        SELECT scoped.value, scoped.ordinal, max(${lexical}, ${semantic}) AS relevance
        FROM scoped ${join}
      ), ranked AS (
        SELECT value, ordinal, relevance,
          relevance * 0.8 + json_extract(value,'$.confidence') * 0.1 +
          CASE json_extract(value,'$.importance') WHEN 'low' THEN 0.02 WHEN 'medium' THEN 0.06 ELSE 0.1 END AS score
        FROM relevant
      )
      SELECT value, score FROM ranked WHERE ? = 0 OR relevance > 0
      ORDER BY score DESC, json_extract(value,'$.learnedAt') DESC, ordinal DESC LIMIT ?
    `,
      )
      .all(
        ...scope.params,
        JSON.stringify(unsupported),
        ...(terms.length ? [JSON.stringify(terms)] : []),
        ...(options ? [vector!, options.spaceId] : []),
        query.trim() ? 1 : 0,
        Math.trunc(limit),
      ) as { value: string; score: number }[]
    return rows
      .filter((row) => authority.supported(JSON.parse(row.value) as MemoryRecord))
      .map((row) => ({
        ...authority.sanitize(JSON.parse(row.value) as MemoryRecord),
        score: row.score,
      }))
  })
}
