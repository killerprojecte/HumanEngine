import type { Store } from '../../store.ts'
import type { CommitmentRef } from '../domain/commitments.ts'
import { AssertionAuthority } from './assertion-authority.ts'
import { AssertionRepository } from '../storage/assertion-repository.ts'
import { AssertionHistoryReader } from '../storage/assertion-history.ts'

/** Fixed synchronous pre-mutation read. Never runs extension callbacks or constructs
 * a mutable history service. Returned references still require exact requalification
 * after mutation; they are not reusable read permission. */
export function readCommitmentPrivacy(store: Store): Map<string, CommitmentRef> {
  const denyWrite = () => {
    throw new Error('commitment_privacy_read_only')
  }
  const repository = new AssertionRepository(store, {
    beforeWrite: denyWrite,
    removeProjection: denyWrite,
  })
  const history = new AssertionHistoryReader(store.db)
  const authority = new AssertionAuthority(store, repository, { history })
  return repository.withSupportReadStatements(() => {
    const refs = new Map<string, CommitmentRef>()
    for (const record of repository.all()) {
      if (record.scope.kind !== 'scene') continue
      const state = history.state(record.id)
      if (state.deleted || state.version < 1) continue
      const ref = { id: record.id, version: state.version, deletionEpoch: state.deletionEpoch }
      if (
        authority.relationAuthority(
          { sceneId: record.scope.sceneId, simulation: false, provenance: 'source-only' },
          ref,
        )
      )
        refs.set(record.id, ref)
    }
    return refs
  })
}
