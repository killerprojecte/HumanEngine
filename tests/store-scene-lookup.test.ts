import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import Database from 'better-sqlite3'
import { metadata, revoke } from '../src/memory/recovery/ledger.ts'
import { Store } from '../src/store.ts'
import type { Scene } from '../src/schema.ts'

const scene = (id: string): Scene => ({
  id,
  name: id,
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'account',
  platformSceneId: id,
  participationEnabled: false,
  learningEnabled: false,
  proactiveEnabled: false,
})

test('scene lookup reads only the requested primary key and returns fresh current values', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene(scene('a'))
    store.saveScene(scene('b'))
    store.scenes = () => {
      throw new Error('unexpected_full_scene_scan')
    }
    assert.deepEqual(store.scene('a'), scene('a'))
    assert.equal(store.scene('missing'), undefined)
    assert.equal(store.scene("a' OR 1=1 --"), undefined)
    store.scene('a')!.name = 'local mutation'
    assert.equal(store.scene('a')!.name, 'a')
    store.saveScene({ ...scene('a'), name: 'updated', learningEnabled: true })
    assert.equal(store.scene('a')!.name, 'updated')
    assert.equal(store.scene('a')!.learningEnabled, true)
    assert.equal(store.scene('b')!.name, 'b')
    store.db.prepare('DELETE FROM scenes WHERE id=?').run('a')
    assert.equal(store.scene('a'), undefined)
  } finally {
    store.close()
  }
})

test('scene lookup retains the recovery readiness gate', () => {
  const store = new Store(':memory:')
  try {
    store.assertRecoveryReady = () => {
      throw new Error('recovery_authority_head_changed')
    }
    assert.throws(() => store.scene('a'), /recovery_authority_head_changed/)
  } finally {
    store.close()
  }
})

test('persistent scene lookup still rejects an externally corrupted recovery chain', () => {
  const root = mkdtempSync(join(tmpdir(), 'scene-lookup-recovery-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  try {
    store.saveScene(scene('a'))
    store.db.transaction(() =>
      revoke(store.db, { kind: 'source', sceneId: 'a', messageId: 'synthetic' }),
    )()
    assert.equal(store.scene('a')?.id, 'a')
    const ledger = new Database(join(metadata(store.db)!.authority, 'authority.sqlite'))
    try {
      ledger.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
      assert.throws(() => store.scene('a'), /recovery_chain_invalid/)
      assert.throws(() => store.scene('missing'), /recovery_chain_invalid/)
    } finally {
      ledger.close()
    }
  } finally {
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})
