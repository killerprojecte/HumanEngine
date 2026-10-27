import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, statSync, chmodSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { Router } from '../src/models.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

const headers = { Authorization: 'Bearer test-admin', 'Content-Type': 'application/json' }
const config = {
  providers: [
    {
      id: 'p',
      name: 'Provider',
      protocol: 'openai-chat',
      baseUrl: 'https://mock.invalid/v1',
      credentialEnv: '',
    },
  ],
  models: [
    {
      id: 'e',
      displayName: 'Embedding',
      providerId: 'p',
      apiModelName: 'e',
      capabilities: ['embedding' as const],
      embeddingBatchSize: 4,
    },
  ],
  routes: [],
}

test('write-only API saves encrypted credentials without env, preserves/replaces/clears and authenticates', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-secrets-'))
  const path = join(dir, 'db.sqlite')
  let store = new Store(path)
  try {
    let app = createApp(store, { token: 'test-admin' })
    const put = (body: unknown, auth = headers) =>
      app.request('/api/models', { method: 'PUT', headers: auth, body: JSON.stringify(body) })
    const payload = {
      ...config,
      credentials: { p: { action: 'set', value: 'mock-only-secret-123' } },
    }
    assert.equal(
      (await put(payload, { Authorization: '', 'Content-Type': 'application/json' })).status,
      401,
    )
    assert.equal(store.secrets.has('model', 'p'), false)
    let result = await put(payload)
    assert.equal(result.status, 200)
    let body = await result.json()
    assert.equal(body.providers[0].credentialConfigured, true)
    assert.equal(JSON.stringify(body).includes('mock-only-secret'), false)
    assert.equal(JSON.stringify(store.get('models', {})).includes('mock-only-secret'), false)
    assert.equal(statSync(join(dir, '.secrets.key')).mode & 0o777, 0o600)
    assert.equal(store.resolveCredential(store.models.providers[0]!), 'mock-only-secret-123')
    assert.equal((await put(body)).status, 200)
    assert.equal(store.resolveCredential(store.models.providers[0]!), 'mock-only-secret-123')
    const before = JSON.stringify(store.db.prepare('SELECT * FROM secrets').all())
    assert.equal(before.includes('mock-only-secret'), false)
    assert.equal(
      (await put({ ...config, credentials: { p: { action: 'set', value: '' } } })).status,
      400,
    )
    assert.equal(
      (await put({ ...config, credentials: { p: { action: 'set', value: 'mock-rotated-456' } } }))
        .status,
      200,
    )
    assert.notEqual(JSON.stringify(store.db.prepare('SELECT * FROM secrets').all()), before)
    store.close()
    store = new Store(path)
    app = createApp(store, { token: 'test-admin' })
    assert.equal(store.resolveCredential(store.models.providers[0]!), 'mock-rotated-456')
    assert.equal((await put({ ...config, credentials: { p: { action: 'preserve' } } })).status, 200)
    assert.equal(store.resolveCredential(store.models.providers[0]!), 'mock-rotated-456')
    assert.equal((await put({ ...config, credentials: { p: { action: 'clear' } } })).status, 200)
    assert.equal(store.secrets.has('model', 'p'), false)
    assert.equal((await put(payload)).status, 200)
    assert.equal((await put({ providers: [], models: [], routes: [] })).status, 200)
    assert.equal(store.secrets.has('model', 'p'), false)
    assert.equal(JSON.stringify(store.logs).includes('mock-only-secret'), false)
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('database credentials serve embeddings, diagnostics redact them', async () => {
  const store = new Store(':memory:')
  const original = globalThis.fetch
  try {
    store.saveModels(config, { p: { action: 'set', value: 'mock-embedding-key' } })
    globalThis.fetch = async (_url, init) => {
      assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer mock-embedding-key')
      return Response.json({
        data: [{ index: 0, embedding: [0.5, 0.3] }],
        usage: { prompt_tokens: 1, total_tokens: 1 },
      })
    }
    const router = new Router(
      store.models,
      undefined,
      undefined,
      undefined,
      store.resolveCredential,
    )
    assert.deepEqual((await router.embeddings('e', ['mock input'])).vectors, [[0.5, 0.3]])
    store.secrets.apply('plugin:snowluma', 'token', {
      action: 'set',
      value: 'mock-snow-diagnostic',
    })
    store.set('settings', { ...store.settings, debugModelIO: true })
    store.captureDiagnostic({
      role: 'thinker',
      modelId: 'e',
      messages: [{ role: 'user', content: 'mock-embedding-key' }],
      output: { text: 'mock-embedding-key mock-snow-diagnostic', toolCalls: [] },
    })
    assert.equal(JSON.stringify(store.diagnostics()).includes('mock-embedding-key'), false)
    assert.ok(JSON.stringify(store.diagnostics()).includes('[redacted]'))
    assert.equal(JSON.stringify(store.diagnostics()).includes('mock-snow-diagnostic'), false)
  } finally {
    globalThis.fetch = original
    store.close()
  }
})

test('tampering, namespace substitution, lost keys and unsafe permissions fail closed', () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-secrets-'))
  const path = join(dir, 'db.sqlite'),
    keyPath = join(dir, '.secrets.key')
  let store = new Store(path)
  try {
    store.secrets.apply('plugin:snowluma', 'token', { action: 'set', value: 'mock-snow-token' })
    store.db.prepare("UPDATE secrets SET name='eventToken'").run()
    assert.throws(
      () => store.secrets.get('plugin:snowluma', 'eventToken'),
      /secret_decryption_failed/,
    )
    store.db.prepare("UPDATE secrets SET name='token'").run()
    const key = readFileSync(keyPath)
    store.close()
    rmSync(keyPath)
    store = new Store(path)
    assert.throws(() => store.secrets.get('plugin:snowluma', 'token'), /secret_key_missing/)
    assert.throws(
      () => store.secrets.apply('model', 'new', { action: 'set', value: 'mock-new' }),
      /secret_key_missing/,
    )
    writeFileSync(keyPath, key, { mode: 0o644 })
    chmodSync(keyPath, 0o644)
    assert.throws(() => store.secrets.get('plugin:snowluma', 'token'), /secret_key_permissions/)
    chmodSync(keyPath, 0o600)
    assert.equal(store.secrets.get('plugin:snowluma', 'token'), 'mock-snow-token')
    store.db
      .prepare("UPDATE secrets SET ciphertext=substr(ciphertext,1,length(ciphertext)-4)||'AAAA'")
      .run()
    assert.throws(() => store.secrets.get('plugin:snowluma', 'token'), /secret_decryption_failed/)
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('backup restores encrypted secrets with separate installation key', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-secrets-backup-'))
  const database = join(dir, 'db.sqlite')
  const store = new Store(database)
  let copy: Store | undefined
  try {
    store.secrets.apply('plugin:snowluma', 'token', { action: 'set', value: 'mock-backup-key' })
    const destination = join(dir, 'backup')
    const manifest = await backup({ database, mediaRoot: join(dir, 'media'), destination })
    assert.ok(manifest.files.some((f) => f.path === '.secrets.key'))
    const result = await restore({ backupRoot: destination, destination: join(dir, 'restored') })
    copy = new Store(result.database)
    assert.equal(copy.secrets.get('plugin:snowluma', 'token'), 'mock-backup-key')
  } finally {
    copy?.close()
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('legacy env refs remain optional and are never migrated; plugin protocols use stored keys', async () => {
  const { ModelProtocolRegistry } = await import('../src/model-protocols.ts')
  const store = new Store(':memory:')
  const previous = process.env.HUMANENGINE_MOCK_LEGACY
  process.env.HUMANENGINE_MOCK_LEGACY = 'mock-legacy-only'
  try {
    const provider = {
      ...config.providers[0]!,
      protocol: 'test:protocol',
      credentialEnv: 'HUMANENGINE_MOCK_LEGACY',
    }
    store.saveModels({ ...config, providers: [provider] })
    assert.equal(store.secrets.has('model', 'p'), false)
    assert.equal(store.resolveCredential(provider), 'mock-legacy-only')
    store.secrets.apply('model', 'p', { action: 'set', value: 'mock-database-priority' })
    assert.equal(store.resolveCredential(provider), 'mock-database-priority')
    const registry = new ModelProtocolRegistry()
    registry.register(
      'test:protocol',
      'Test',
      {
        async complete() {
          throw new Error('unused')
        },
        async embeddings(context) {
          assert.equal(context.credential, 'mock-database-priority')
          assert.equal(JSON.stringify(context.provider).includes('mock-database-priority'), false)
          return { vectors: [[1, 2]], usage: { inputTokens: 1, outputTokens: 0, totalTokens: 1 } }
        },
        async listModels(context) {
          assert.equal(context.credential, 'mock-database-priority')
          return ['e']
        },
      },
      new AbortController().signal,
    )
    const router = new Router(store.models, undefined, undefined, registry, store.resolveCredential)
    assert.deepEqual(await router.listModels('p'), ['e'])
    assert.deepEqual((await router.embeddings('e', ['test'])).vectors, [[1, 2]])
    store.secrets.apply('model', 'p', { action: 'clear' })
    assert.equal(store.resolveCredential(provider), 'mock-legacy-only')
  } finally {
    if (previous === undefined) delete process.env.HUMANENGINE_MOCK_LEGACY
    else process.env.HUMANENGINE_MOCK_LEGACY = previous
    store.close()
  }
})
