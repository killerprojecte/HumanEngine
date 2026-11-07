import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { ensureAssertionEligibilityBaseline } from '../src/memory/recovery/eligibility.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

test('automatic startup census protects a prebaseline backup without recreating later roots or changing history', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-automatic-restore-review-'))
  const database = join(root, 'live', 'db.sqlite')
  const mediaRoot = join(root, 'media')
  await mkdir(mediaRoot)
  const store = new Store(database)
  const memory = new MemoryService(store)
  let restored: Store | undefined
  let authority: RecoveryAuthority | undefined
  try {
    const create = (content: string) =>
      memory.create({
        scope: { kind: 'sharedPublicKnowledge' },
        kind: 'fact',
        content,
      })
    const invalidated = create('synthetic legacy invalidation')
    const quarantined = create('synthetic legacy quarantine')
    const history = store.db
      .prepare('SELECT * FROM memory_assertion_revisions ORDER BY assertion_id,version')
      .all()
    const old = join(root, 'prebaseline-backup')
    await backup({ database, mediaRoot, destination: old })
    const later = create('synthetic post-backup root')
    // These direct writes represent pre-upgrade main-only withdrawals.
    const write = store.db.prepare('UPDATE memories SET value=? WHERE id=?')
    write.run(JSON.stringify({ ...invalidated, status: 'invalidated' }), invalidated.id)
    write.run(JSON.stringify({ ...quarantined, lineageStatus: 'quarantined' }), quarantined.id)
    write.run(JSON.stringify({ ...later, status: 'invalidated' }), later.id)
    ensureAssertionEligibilityBaseline(store.db)
    authority = new RecoveryAuthority(metadata(store.db)!.authority)
    const entries = authority.entries()
    assert.equal(
      entries.filter(({ revocation }) => revocation.kind === 'assertion_eligibility').length,
      1,
    )
    const result = await restore({
      backupRoot: old,
      destination: join(root, 'restored'),
      authorityDirectory: authority.path,
    })
    restored = new Store(result.database)
    const read = (id: string) => {
      const row = restored!.db.prepare('SELECT value FROM memories WHERE id=?').get(id) as
        { value: string } | undefined
      return row ? JSON.parse(row.value) : undefined
    }
    assert.deepEqual(read(invalidated.id), { ...invalidated, status: 'invalidated' })
    assert.deepEqual(read(quarantined.id), { ...quarantined, lineageStatus: 'quarantined' })
    assert.equal(read(later.id), undefined)
    assert.deepEqual(
      restored.db
        .prepare('SELECT * FROM memory_assertion_revisions ORDER BY assertion_id,version')
        .all(),
      history,
    )
    assert.equal(metadata(restored.db)!.minimumReader, 11)
    assert.deepEqual(metadata(restored.db)!.head, authority.head())
    ensureAssertionEligibilityBaseline(restored.db)
    assert.deepEqual(authority.entries(), entries)
  } finally {
    restored?.close()
    authority?.close()
    memory.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
