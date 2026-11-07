import type { Store } from '../../store.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import { AssertionAuthority } from './assertion-authority.ts'
import { assertionScopeFilter } from './scope.ts'
import { AssertionHistoryReader } from '../storage/assertion-history.ts'
import {
  AssertionRepository,
  readAssertionRow as read,
  type AssertionRow,
} from '../storage/assertion-repository.ts'

/** Plain scope/filter data only. Lifecycle maintenance completes before this read. */
export interface MemoryListRead {
  sceneId: string
  simulation: boolean
  personId?: string
  ids?: string[]
}

/** Fixed synchronous authorized listing. No result/permission persists between calls. */
export function readMemoryList(store: Store, input: MemoryListRead): MemoryRecord[] {
  store.assertRecoveryReady()
  const scope = assertionScopeFilter(input, input.personId)
  if (!scope) return []
  const denyWrite = () => {
    throw new Error('memory_list_read_only')
  }
  const repository = new AssertionRepository(store, {
    beforeWrite: denyWrite,
    removeProjection: denyWrite,
  })
  const authority = new AssertionAuthority(store, repository, {
    history: new AssertionHistoryReader(store.db),
  })
  const { ids } = input
  const rows = store.db
    .prepare(
      `SELECT memories.* FROM ${ids === undefined ? 'memories' : 'json_each(?) AS wanted CROSS JOIN memories'} WHERE ${ids === undefined ? '' : 'memories.id=wanted.value AND '}${scope.sql} AND json_extract(memories.value,'$.internalUse') IS NULL AND COALESCE(json_extract(memories.value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(memories.value,'$.status') IN ('active','disputed') AND (json_extract(memories.value,'$.validFrom') IS NULL OR julianday(json_extract(memories.value,'$.validFrom')) <= julianday('now')) AND (json_extract(memories.value,'$.validUntil') IS NULL OR julianday(json_extract(memories.value,'$.validUntil')) > julianday('now')) ORDER BY memories.rowid DESC`,
    )
    .all(
      ...(ids === undefined ? [] : [JSON.stringify([...new Set(ids)])]),
      ...scope.params,
    ) as AssertionRow[]
  return repository.withSupportReadStatements(() =>
    rows
      .map(read)
      .filter((record) => authority.supported(record) && authority.currentApplicable(record))
      .map((record) => authority.sanitize(record)),
  )
}
