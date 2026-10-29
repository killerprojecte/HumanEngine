import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, mkdir, readFile, access, rm } from 'node:fs/promises'
import fs from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { Router } from '../src/models.ts'
import type { LocalPlugin } from '../src/plugins.ts'
import type { Settings } from '../src/schema.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import {
  configurationKeys,
  configurationFlags,
  sceneFlags,
} from '../src/memory/recovery/configuration-contract.ts'
import { backup, digest } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

// Only synthetic credentials; assertions and logs deliberately never print their values.
const scene = {
  id: 'authorization-scene',
  kind: 'group' as const,
  adapterId: 'offline',
  accountId: 'synthetic-bot',
  platformSceneId: 'synthetic-group',
  name: 'Snapshot scene label',
  participationEnabled: true,
  learningEnabled: true,
  proactiveEnabled: false,
}
const adminToken = 'synthetic-private-admin-token'
const configuration = (baseUrl = 'https://synthetic-old.invalid/v1') => ({
  providers: [
    {
      id: 'synthetic-provider',
      name: 'Synthetic provider',
      protocol: 'openai-chat',
      baseUrl,
      credentialEnv: '',
    },
  ],
  models: [],
  routes: [],
})
const allFlags = (enabled: boolean) =>
  Object.fromEntries(configurationKeys.map((key) => [key, enabled])) as Partial<Settings>
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-authorization-')),
    database = join(root, 'live', 'humanengine.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  let modelCalls = 0,
    sends = 0,
    networkCalls = 0
  const fetch = globalThis.fetch
  globalThis.fetch = async () => {
    networkCalls++
    throw new Error('Unexpected network use in recovery test')
  }
  const opened: Array<{ store: Store; engine: Engine }> = []
  const open = (path: string) => {
    const store = new Store(path)
    if (!store.scene(scene.id)) store.saveScene(scene)
    const engine = new Engine(store)
    engine.router = () =>
      ({
        complete: async () => {
          modelCalls++
          throw new Error('Unexpected model use in recovery test')
        },
      }) as unknown as Router
    engine.runtime.addAdapter('offline', {
      send: async () => {
        sends++
        throw new Error('Unexpected platform send')
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    opened.push({ store, engine })
    return { store, engine, app: createApp(store, { engine, token: adminToken }) }
  }
  const live = open(database),
    authorityDirectory = metadata(live.store.db)!.authority
  return {
    root,
    database,
    mediaRoot,
    authorityDirectory,
    ...live,
    request(path: string, body?: unknown, method = 'PUT') {
      return live.app.request(path, {
        method: body === undefined ? 'GET' : method,
        headers: { Authorization: `Bearer ${adminToken}`, 'Content-Type': 'application/json' },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      })
    },
    async settings(patch: Partial<Settings>) {
      const result = await this.request('/api/settings', { ...live.store.settings, ...patch })
      assert.equal(result.status, 200)
    },
    async scene(patch: Partial<typeof scene>) {
      const result = await this.request(`/api/scenes/${scene.id}`, {
        ...live.store.scene(scene.id)!,
        ...patch,
      })
      assert.equal(result.status, 200)
    },
    async models(config = configuration(), credentials?: Record<string, unknown>) {
      const result = await this.request('/api/models', {
        ...config,
        ...(credentials ? { credentials } : {}),
      })
      assert.equal(result.status, 200)
      return result.json()
    },
    async snapshot(name = 'backup') {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    async recover(backupRoot: string, name = 'restored') {
      const output = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory,
      })
      return { ...output, ...open(output.database) }
    },
    async rejected(
      backupRoot: string,
      name = 'denied',
      pattern = /recovery_security_state_unrecoverable/,
    ) {
      await assert.rejects(() => this.recover(backupRoot, name), pattern)
      await assert.rejects(() => access(join(root, name)), { code: 'ENOENT' })
      await assert.rejects(() => access(join(root, `${name}.quarantine`)), { code: 'ENOENT' })
    },
    authority() {
      const authority = new RecoveryAuthority(authorityDirectory)
      try {
        return authority.entries()
      } finally {
        authority.close()
      }
    },
    async coldStart(engine: Engine) {
      const previous = process.env.HUMANENGINE_PLUGINS
      process.env.HUMANENGINE_PLUGINS = ''
      try {
        await engine.start()
        await new Promise((resolve) => setImmediate(resolve))
        assert.equal(engine.runtime.activeTasks, 0)
      } finally {
        if (previous === undefined) delete process.env.HUMANENGINE_PLUGINS
        else process.env.HUMANENGINE_PLUGINS = previous
      }
      assert.deepEqual(
        { modelCalls, sends, networkCalls },
        { modelCalls: 0, sends: 0, networkCalls: 0 },
      )
    },
    async close() {
      try {
        for (const { engine, store } of opened.reverse()) {
          try {
            await engine.dispose()
          } finally {
            store.close()
          }
        }
      } finally {
        globalThis.fetch = fetch
        await rm(root, { recursive: true, force: true })
      }
      assert.deepEqual(
        { modelCalls, sends, networkCalls },
        { modelCalls: 0, sends: 0, networkCalls: 0 },
      )
    },
  }
}
function revision(store: Store) {
  return (
    store.db.prepare('SELECT revision FROM recovery_security_state WHERE id=1').get() as
      { revision: string } | undefined
  )?.revision
}
async function plugin(engine: Engine) {
  let setups = 0
  const definition: LocalPlugin = {
    manifest: {
      id: 'synthetic-connector',
      name: 'Synthetic connector',
      description: 'No actual network transport',
      version: '1',
      capabilities: [],
      configSchema: {
        type: 'object',
        properties: { endpoint: { type: 'string', minLength: 1 } },
        required: ['endpoint'],
        additionalProperties: false,
      },
    },
    setup(context) {
      setups++
      context.onDispose(() => {})
    },
  }
  await engine.plugins.register(definition)
  return { setups: () => setups }
}

test('actual settings API denies all protected flags after an old backup without replacing unrelated snapshot settings', async () => {
  const h = await fixture()
  try {
    await h.settings({ ...allFlags(true), name: 'Snapshot label', replyEvidenceMaxBytes: 98304 })
    const snapshot = await h.snapshot()
    await h.settings({
      ...allFlags(false),
      name: 'New unjournaled label',
      replyEvidenceMaxBytes: 131072,
    })
    const recovered = await h.recover(snapshot)
    assert.deepEqual(
      configurationFlags(recovered.store.settings),
      configurationFlags(h.store.settings),
    )
    assert.ok(
      Object.values(configurationFlags(recovered.store.settings)).every((value) => value === false),
    )
    assert.equal(recovered.store.settings.cacheEnabled, false)
    assert.equal(recovered.store.settings.debugModelIO, false)
    assert.equal(recovered.store.settings.name, 'Snapshot label')
    assert.equal(recovered.store.settings.replyEvidenceMaxBytes, 98304)
    await h.coldStart(recovered.engine)
  } finally {
    await h.close()
  }
})

test('actual settings and scene deny then explicit grant replay in order rather than remaining restrict-only', async () => {
  const h = await fixture()
  try {
    await h.settings(allFlags(true))
    await h.scene({ proactiveEnabled: true })
    const snapshot = await h.snapshot()
    await h.settings(allFlags(false))
    await h.scene({ participationEnabled: false, learningEnabled: false, proactiveEnabled: false })
    await h.settings({
      memoryProjectionEnabled: true,
      memoryGraphEnabled: true,
      memoryGraphAnswerEnabled: true,
    })
    await h.scene({ participationEnabled: true, learningEnabled: true })
    const recovered = await h.recover(snapshot)
    assert.deepEqual(
      configurationFlags(recovered.store.settings),
      configurationFlags(h.store.settings),
    )
    assert.deepEqual(sceneFlags(recovered.store.scene(scene.id)!), {
      memoryReadEnabled: true,
      personalizationEnabled: true,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    assert.equal(recovered.store.settings.memoryExperienceEnabled, false)
    await h.coldStart(recovered.engine)
  } finally {
    await h.close()
  }
})

test('post-backup scene OFF flags cannot resume automatic participation or learning on restored startup', async () => {
  const h = await fixture()
  try {
    await h.scene({ proactiveEnabled: true })
    const snapshot = await h.snapshot()
    await h.scene({
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
      name: 'New unjournaled scene label',
    })
    const recovered = await h.recover(snapshot)
    assert.deepEqual(sceneFlags(recovered.store.scene(scene.id)!), {
      memoryReadEnabled: true,
      personalizationEnabled: true,
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    assert.equal(recovered.store.scene(scene.id)?.name, scene.name)
    await h.coldStart(recovered.engine)
  } finally {
    await h.close()
  }
})

for (const mismatch of ['different-identity', 'missing-scene'] as const)
  test(`scene flag replay with ${mismatch} does not grant or revoke a guessed scene`, async () => {
    const h = await fixture()
    try {
      const snapshot = await h.snapshot()
      if (mismatch === 'different-identity')
        await h.scene({ accountId: 'different-synthetic-account', learningEnabled: false })
      else {
        h.store.saveScene({ ...scene, id: 'created-after-backup' })
        h.store.saveScene({ ...scene, id: 'created-after-backup', learningEnabled: false })
      }
      await h.rejected(snapshot, mismatch, /recovery_scene_identity_mismatch/)
    } finally {
      await h.close()
    }
  })

for (const operation of ['rotate', 'clear', 'endpoint'] as const)
  test(`actual model API ${operation} invalidates an older security snapshot but a current backup passes`, async () => {
    const h = await fixture()
    const originalSecret = 'synthetic-credential-initial',
      newSecret = 'synthetic-credential-new'
    try {
      const result = await h.models(configuration(), {
        'synthetic-provider': { action: 'set', value: originalSecret },
      })
      assert.ok(!JSON.stringify(result).includes(originalSecret), 'API never returns a credential')
      const before = revision(h.store),
        snapshot = await h.snapshot()
      const endpoint =
        operation === 'endpoint'
          ? 'https://synthetic-new.invalid/v2'
          : configuration().providers[0]!.baseUrl
      await h.models(configuration(endpoint), {
        'synthetic-provider':
          operation === 'rotate'
            ? { action: 'set', value: newSecret }
            : { action: operation === 'clear' ? 'clear' : 'preserve' },
      })
      assert.notEqual(revision(h.store), before)
      await h.rejected(snapshot)
      const latest = await h.snapshot('current-backup'),
        recovered = await h.recover(latest, 'current')
      assert.equal(revision(recovered.store), revision(h.store))
      const expected =
        operation === 'clear' ? undefined : operation === 'rotate' ? newSecret : originalSecret
      assert.ok(
        recovered.store.resolveCredential(recovered.store.models.providers[0]!) === expected,
        'only current synthetic credential material may be recovered',
      )
      assert.equal(recovered.store.models.providers[0]?.baseUrl, endpoint)
      const journal = JSON.stringify(h.authority())
      for (const value of [originalSecret, newSecret, endpoint, 'synthetic-provider'])
        assert.ok(
          !journal.includes(value),
          'authority contains opaque revision, never credential material or endpoint',
        )
      assert.ok(!JSON.stringify(h.store.logs).includes(originalSecret))
      assert.ok(!JSON.stringify(h.store.logs).includes(newSecret))
      await h.coldStart(recovered.engine)
    } finally {
      await h.close()
    }
  })

test('preserving credentials and unrelated settings do not create false security revision mismatches', async () => {
  const h = await fixture()
  try {
    await h.models(configuration(), {
      'synthetic-provider': { action: 'set', value: 'synthetic-preserved-credential' },
    })
    const before = revision(h.store),
      snapshot = await h.snapshot()
    await h.models(configuration(), { 'synthetic-provider': { action: 'preserve' } })
    await h.settings({ name: 'Unrelated UI name', replyEvidenceMaxBytes: 98304 })
    assert.equal(revision(h.store), before)
    const recovered = await h.recover(snapshot)
    assert.equal(revision(recovered.store), before)
    assert.ok(
      recovered.store.resolveCredential(recovered.store.models.providers[0]!) ===
        'synthetic-preserved-credential',
      'preserve keeps the known synthetic credential',
    )
  } finally {
    await h.close()
  }
})

for (const operation of ['endpoint', 'enable', 'disable'] as const)
  test(`actual plugin ${operation} changes its opaque authority and cannot be reconstructed from an old snapshot`, async () => {
    const h = await fixture()
    try {
      const livePlugin = await plugin(h.engine)
      await h.engine.plugins.configure('synthetic-connector', {
        endpoint: 'https://synthetic-plugin-old.invalid',
      })
      if (operation === 'disable') await h.engine.plugins.enable('synthetic-connector')
      const before = revision(h.store),
        snapshot = await h.snapshot()
      if (operation === 'endpoint')
        await h.engine.plugins.configure('synthetic-connector', {
          endpoint: 'https://synthetic-plugin-new.invalid',
        })
      else if (operation === 'enable') await h.engine.plugins.enable('synthetic-connector')
      else await h.engine.plugins.disable('synthetic-connector')
      assert.notEqual(revision(h.store), before)
      assert.equal(livePlugin.setups(), operation === 'endpoint' ? 0 : 1)
      await h.rejected(snapshot)
      const latest = await h.snapshot('current-plugin-backup'),
        recovered = await h.recover(latest, 'current-plugin')
      assert.deepEqual(
        recovered.store.get('plugin:synthetic-connector', {}),
        h.store.get('plugin:synthetic-connector', {}),
      )
      assert.equal(
        recovered.store.get('plugin-enabled:synthetic-connector', false),
        operation === 'enable',
      )
      const restoredPlugin = await plugin(recovered.engine)
      assert.equal(restoredPlugin.setups(), 0, 'restore and registration do not auto-run a plugin')
      await h.coldStart(recovered.engine)
      assert.equal(
        restoredPlugin.setups(),
        operation === 'enable' ? 1 : 0,
        'only explicit startup applies current enable state to the no-network fixture',
      )
      const journal = JSON.stringify(h.authority())
      assert.ok(!journal.includes('synthetic-plugin-old.invalid'))
      assert.ok(!journal.includes('synthetic-plugin-new.invalid'))
    } finally {
      await h.close()
    }
  })

test('host environment credentials and admin authentication are not serialized into the restored backup', async () => {
  const h = await fixture(),
    env = 'HUMANENGINE_SYNTHETIC_RECOVERY_CREDENTIAL',
    previous = process.env[env]
  const hostSecret = 'synthetic-host-only-credential'
  process.env[env] = hostSecret
  try {
    const config = configuration()
    config.providers[0]!.credentialEnv = env
    await h.models(config)
    assert.equal(h.store.secrets.has('model', 'synthetic-provider'), false)
    const snapshot = await h.snapshot(),
      dbBytes = await readFile(join(snapshot, 'humanengine.sqlite'))
    assert.ok(!dbBytes.includes(Buffer.from(hostSecret)), 'host-only credential is not in snapshot')
    assert.ok(!dbBytes.includes(Buffer.from(adminToken)), 'admin bearer is not in snapshot')
    const manifest = JSON.parse(await readFile(join(snapshot, 'manifest.json'), 'utf8')) as {
      files: Array<{ path: string }>
    }
    assert.ok(
      manifest.files.every(
        (file) => file.path === 'humanengine.sqlite' || file.path.startsWith('media/'),
      ),
    )
    delete process.env[env]
    const recovered = await h.recover(snapshot)
    assert.equal(recovered.store.resolveCredential(recovered.store.models.providers[0]!), undefined)
    const denied = await recovered.app.request('/api/settings')
    assert.equal(denied.status, 401)
    assert.ok(!JSON.stringify(h.authority()).includes(hostSecret))
    await h.coldStart(recovered.engine)
  } finally {
    if (previous === undefined) delete process.env[env]
    else process.env[env] = previous
    await h.close()
  }
})

test('authority advancement in the actual publish rename await rejects success and removes only the new output', async () => {
  const h = await fixture(),
    originalRename = fs.promises.rename
  try {
    const snapshot = await h.snapshot()
    const before = digest(await readFile(join(snapshot, 'humanengine.sqlite')))
    let intercepted = 0
    fs.promises.rename = async (from, to) => {
      if (String(from) === join(h.root, 'publication-race.quarantine')) {
        intercepted++
        const authority = new RecoveryAuthority(h.authorityDirectory)
        try {
          authority.append(
            { kind: 'source', sceneId: scene.id, messageId: 'during-publish' },
            authority.head(),
          )
        } finally {
          authority.close()
        }
      }
      return originalRename(from, to)
    }
    syncBuiltinESMExports()
    await h.rejected(snapshot, 'publication-race', /recovery_authority_head_changed/)
    assert.equal(
      intercepted,
      1,
      'fault is injected at the actual asynchronous publication boundary',
    )
    assert.equal(digest(await readFile(join(snapshot, 'humanengine.sqlite'))), before)
    assert.throws(() => h.store.assertRecoveryReady(), /recovery_authority_head_changed/)
  } finally {
    fs.promises.rename = originalRename
    syncBuiltinESMExports()
    await h.close()
  }
})

test('absent persisted settings retain cache and debug defaults through an unrelated settings API edit and old-snapshot restore', async () => {
  const h = await fixture()
  try {
    // cacheEnabled is required inside a stored settings object; the valid omitted case is no row.
    assert.equal(h.store.get('settings', undefined), undefined)
    assert.equal(h.store.settings.cacheEnabled, true)
    assert.equal(h.store.settings.debugModelIO, false)
    const originalName = h.store.settings.name,
      snapshot = await h.snapshot()
    await h.settings({ name: 'Unrelated later settings label' })
    const recovered = await h.recover(snapshot)
    assert.equal(
      recovered.store.settings.cacheEnabled,
      true,
      'omission is not a cache-disable authorization',
    )
    assert.equal(recovered.store.settings.debugModelIO, false)
    assert.equal(recovered.store.settings.name, originalName)
    await h.coldStart(recovered.engine)
  } finally {
    await h.close()
  }
})

test('removing the local plugin execution list invalidates an older snapshot rather than resurrecting old code authority', async () => {
  const h = await fixture()
  try {
    h.store.set('localPlugins', ['synthetic-not-executed.mjs'])
    const before = revision(h.store),
      snapshot = await h.snapshot()
    h.store.set('localPlugins', [])
    assert.notEqual(revision(h.store), before)
    await h.rejected(snapshot)
    const latest = await h.snapshot('current-plugin-list'),
      recovered = await h.recover(latest, 'without-old-plugin')
    assert.deepEqual(recovered.store.get('localPlugins', []), [])
    assert.ok(
      !JSON.stringify(h.authority()).includes('synthetic-not-executed.mjs'),
      'opaque security state does not retain executable paths',
    )
    await h.coldStart(recovered.engine)
  } finally {
    await h.close()
  }
})
