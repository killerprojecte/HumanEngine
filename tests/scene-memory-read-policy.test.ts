import { ModelReadPolicy } from '../src/memory/read/model-read-policy.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { Engine } from '../src/engine.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import {
  parseConfigurationRecoveryEvent,
  sceneIdentityHash,
} from '../src/memory/recovery/configuration-contract.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
const scene = {
  id: 'policy-scene',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'group',
  kind: 'group' as const,
  name: 'Synthetic policy scene',
  participationEnabled: false,
  learningEnabled: true,
  proactiveEnabled: false,
}

test('scene API keeps legacy true defaults, independent flags and server-owned monotonic revision', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  try {
    store.saveScene(scene)
    const app = createApp(store, { engine, token: 'synthetic-admin' })
    const put = async (patch: object) => {
      const result = await app.request(`/api/scenes/${scene.id}`, {
        method: 'PUT',
        headers: { Authorization: 'Bearer synthetic-admin', 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...scene, ...patch }),
      })
      assert.equal(result.status, 200)
      return result.json()
    }
    assert.equal(store.scene(scene.id)?.memoryReadEnabled, undefined)
    let result = await put({ memoryReadEnabled: false, memoryReadRevision: 999 })
    assert.equal(result.memoryReadRevision, 1)
    assert.equal(result.memoryReadEnabled, false)
    assert.equal(result.learningEnabled, true)
    result = await put({ name: 'Renamed' })
    assert.equal(result.memoryReadEnabled, false)
    assert.equal(result.memoryReadRevision, 1)
    result = await put({ personalizationEnabled: false })
    assert.equal(result.memoryReadEnabled, false)
    assert.equal(result.personalizationEnabled, false)
    assert.equal(result.memoryReadRevision, 2)
    result = await put({
      memoryReadEnabled: true,
      personalizationEnabled: true,
      memoryReadRevision: 0,
    })
    assert.equal(result.memoryReadRevision, 3)
    result = await put({ memoryReadEnabled: true, personalizationEnabled: true })
    assert.equal(result.memoryReadRevision, 3)
    assert.equal((await app.request('/api/scenes')).status, 401)
    assert.equal(
      (await app.request('/api/scenes', { headers: { Authorization: 'Bearer synthetic-admin' } }))
        .status,
      200,
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('scene flags legacy v1 remains exact and new v2 requires both boolean read controls', () => {
  const legacy = {
    kind: 'scene_flags',
    schemaVersion: 1,
    sceneId: scene.id,
    identityHash: sceneIdentityHash(scene),
    flags: { participationEnabled: false, learningEnabled: true, proactiveEnabled: false },
  }
  assert.deepEqual(parseConfigurationRecoveryEvent(legacy), legacy)
  assert.throws(
    () => parseConfigurationRecoveryEvent({ ...legacy, schemaVersion: 2 }),
    /recovery_configuration_invalid/,
  )
  const modern = {
    ...legacy,
    schemaVersion: 2,
    flags: { ...legacy.flags, memoryReadEnabled: false, personalizationEnabled: true },
  }
  assert.deepEqual(parseConfigurationRecoveryEvent(modern), modern)
  assert.throws(
    () =>
      parseConfigurationRecoveryEvent({
        ...modern,
        flags: { ...modern.flags, memoryReadRevision: 4 },
      }),
    /recovery_configuration_invalid/,
  )
})

test('old snapshot replays ordered read/personalization policy without enabling learning or resetting revision', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-read-policy-'))
  const database = join(root, 'live', 'db.sqlite')
  const mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  let restored: Store | undefined
  try {
    store.saveScene(scene)
    const authorityDirectory = metadata(store.db)!.authority
    await backup({ database, mediaRoot, destination: join(root, 'snapshot') })
    store.saveScene({ ...scene, memoryReadEnabled: false })
    store.saveScene({ ...scene, memoryReadEnabled: true, personalizationEnabled: false })
    store.saveScene({ ...scene, memoryReadEnabled: false, personalizationEnabled: true })
    const expected = store.scene(scene.id)!
    assert.equal(expected.memoryReadRevision, 3)
    assert.equal(metadata(store.db)!.minimumReader, 5)
    const authority = new RecoveryAuthority(authorityDirectory)
    try {
      assert.equal(authority.entries().filter((e) => e.revocation.kind === 'scene_flags').length, 3)
    } finally {
      authority.close()
    }
    const result = await restore({
      backupRoot: join(root, 'snapshot'),
      destination: join(root, 'restored'),
      authorityDirectory,
    })
    restored = new Store(result.database)
    assert.deepEqual(restored.scene(scene.id), expected)
    restored.saveScene({ ...scene, name: 'Legacy metadata update' })
    assert.equal(restored.scene(scene.id)?.memoryReadEnabled, false)
    assert.equal(restored.scene(scene.id)?.memoryReadRevision, 3)
    assert.equal(restored.scene(scene.id)?.learningEnabled, true)
  } finally {
    restored?.close()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('same scene id cannot carry an old read ticket across routing identity rebinding', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene(scene)
    const policy = new ModelReadPolicy(store)
    const ticket = policy.require(scene.id)
    store.saveScene({ ...scene, accountId: 'different-account' })
    assert.equal(store.scene(scene.id)?.memoryReadRevision, 1)
    assert.equal(policy.valid(ticket), false)
    assert.equal(policy.valid(policy.require(scene.id)), true)
    store.saveScene({ ...store.scene(scene.id)!, name: 'Display-only rename' })
    assert.equal(store.scene(scene.id)?.memoryReadRevision, 1)
  } finally {
    store.db.close()
  }
})
