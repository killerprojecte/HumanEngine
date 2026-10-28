import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { ProviderSchema } from '../src/schema.ts'

const provider = {
  id: 'mock-provider',
  name: 'Mock provider',
  protocol: 'openai-chat',
  baseUrl: 'http://127.0.0.1:4321/v1',
  credentialEnv: '',
}
const legacy = {
  providers: [provider],
  models: [],
  routes: [{ role: 'thinker' as const, orderedModelIds: [], timeoutMs: 12345 }],
}

test('provider defaults normalize legacy reads and saves without changing route budget or secret', () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-provider-config-'))
  const path = join(dir, 'db.sqlite')
  let store = new Store(path)
  try {
    store.set('models', legacy)
    store.secrets.apply('model', provider.id, { action: 'set', value: 'mock-provider-secret' })
    store.close()
    store = new Store(path)
    assert.equal(store.models.providers[0]?.retryCount, 2)
    assert.equal(store.models.providers[0]?.timeoutMs, 30000)
    assert.equal(store.models.routes[0]?.timeoutMs, 12345)
    // Reading is non-mutating; saving materializes the backward-compatible defaults.
    assert.deepEqual(store.get('models', null), legacy)
    store.saveModels(legacy)
    assert.equal(store.get<any>('models', {}).providers[0].retryCount, 2)
    store.saveModels({ ...legacy, providers: [{ ...provider, retryCount: 0, timeoutMs: 9876 }] })
    store.close()
    store = new Store(path)
    assert.equal(store.models.providers[0]?.retryCount, 0)
    assert.equal(store.models.providers[0]?.timeoutMs, 9876)
    assert.equal(store.models.routes[0]?.timeoutMs, 12345)
    assert.equal(store.resolveCredential(store.models.providers[0]!), 'mock-provider-secret')
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('provider retry and timeout bounds reject invalid values and accept endpoints', () => {
  for (const retryCount of [-1, 6, 1.5, null, '2'])
    assert.equal(ProviderSchema.safeParse({ ...provider, retryCount }).success, false)
  for (const timeoutMs of [99, 300001, 100.5, null, '30000'])
    assert.equal(ProviderSchema.safeParse({ ...provider, timeoutMs }).success, false)
  for (const retryCount of [0, 5])
    for (const timeoutMs of [100, 300000])
      assert.equal(ProviderSchema.safeParse({ ...provider, retryCount, timeoutMs }).success, true)
})

test('model configuration API returns defaults and persists explicit provider edits safely', async () => {
  const store = new Store(':memory:')
  const app = createApp(store, { token: 'mock-admin' })
  const headers = { Authorization: 'Bearer mock-admin', 'Content-Type': 'application/json' }
  const put = (body: unknown) =>
    app.request('/api/models', { method: 'PUT', headers, body: JSON.stringify(body) })
  try {
    let response = await put(legacy)
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.providers[0].retryCount, 2)
    assert.equal(body.providers[0].timeoutMs, 30000)
    body.providers[0].retryCount = 5
    body.providers[0].timeoutMs = 300000
    assert.equal((await put(body)).status, 200)
    response = await app.request('/api/models', { headers })
    assert.deepEqual((await response.json()).providers[0], body.providers[0])
    body.providers[0].retryCount = 6
    assert.equal((await put(body)).status, 400)
    assert.equal(store.models.providers[0]?.retryCount, 5)
  } finally {
    store.close()
  }
})
