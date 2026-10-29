import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import {
  configurationFlags,
  parseConfigurationRecoveryEvent,
} from '../src/memory/recovery/configuration-contract.ts'

test('topic flag is default-off, authenticated/strict, persisted and latest disabled state survives old-backup recovery', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-topic-settings-'))
  const database = join(root, 'live', 'humanengine.sqlite'),
    mediaRoot = join(root, 'media')
  await mkdir(mediaRoot)
  const store = new Store(database),
    engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-topic-admin' })
  const headers = {
    Authorization: 'Bearer synthetic-topic-admin',
    'Content-Type': 'application/json',
  }
  let recovered: Store | undefined
  try {
    assert.equal(store.settings.topicAdmissionEnabled, false)
    store.saveScene({
      id: 'topic-stats',
      adapterId: 'offline',
      accountId: 'offline',
      platformSceneId: 'topic-stats',
      kind: 'group',
      name: 'Synthetic',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const diagnostics = await (
      await app.request('/api/scenes/topic-stats/state', { headers })
    ).json()
    assert.deepEqual(diagnostics.topicRouting, {
      enabled: false,
      deferred: 0,
      overflowed: 0,
      provisional: 0,
      maxTopics: 4,
      maxDeferred: 60,
    })
    assert.equal(
      (
        await app.request('/api/settings', {
          method: 'PUT',
          body: JSON.stringify({ ...store.settings, topicAdmissionEnabled: true }),
        })
      ).status,
      401,
    )
    const put = (value: unknown) =>
      app.request('/api/settings', {
        method: 'PUT',
        headers,
        body: JSON.stringify({ ...store.settings, topicAdmissionEnabled: value }),
      })
    assert.equal((await put('yes')).status, 400)
    assert.equal(store.settings.topicAdmissionEnabled, false)
    assert.equal((await put(true)).status, 200)
    assert.equal(
      (await (await app.request('/api/settings', { headers })).json()).topicAdmissionEnabled,
      true,
    )
    await backup({ database, mediaRoot, destination: join(root, 'snapshot') })
    assert.equal((await put(false)).status, 200)
    await restore({
      backupRoot: join(root, 'snapshot'),
      destination: join(root, 'restored'),
      authorityDirectory: metadata(store.db)!.authority,
    })
    recovered = new Store(join(root, 'restored', 'humanengine.sqlite'))
    assert.equal(recovered.settings.topicAdmissionEnabled, false)
  } finally {
    recovered?.close()
    await engine.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('configuration v5 adds only topic flag; older versions cannot smuggle it or unknown future flags', () => {
  const {
    memoryExperienceFeedbackEnabled,
    memorySemanticEnabled,
    memorySemanticAutoAdmissionEnabled,
    memorySemanticAnswerEnabled,
    memoryKnowledgeEnabled,
    memoryKnowledgeAutoAdmissionEnabled,
    memoryKnowledgeAnswerEnabled,
    ...flags
  } = configurationFlags({ topicAdmissionEnabled: true })
  assert.deepEqual(
    [memoryKnowledgeEnabled, memoryKnowledgeAutoAdmissionEnabled, memoryKnowledgeAnswerEnabled],
    [false, false, false],
  )
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 5, flags }).flags,
    flags,
  )
  const { topicAdmissionEnabled, ...v4 } = flags
  assert.equal(topicAdmissionEnabled, true)
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 4, flags: v4 })
      .flags,
    v4,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 4, flags }),
    /invalid/,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 8, flags }),
    /invalid/,
  )
})
