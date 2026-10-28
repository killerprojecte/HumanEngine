import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'

const headers = { Authorization: 'Bearer integration-secret', 'Content-Type': 'application/json' }
test('API authentication, validated configuration, independent model IDs, SQLite restart and vector extension', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'humanengine-'))
  let store = new Store(join(dir, 'test.sqlite'))
  try {
    const app = createApp(store, { token: 'integration-secret' })
    assert.equal((await app.request('/api/settings')).status, 401)
    assert.equal(
      (await app.request('/api/settings', { headers: { Authorization: 'Bearer wrong' } })).status,
      401,
    )
    const config = {
      providers: [
        {
          id: 'a',
          name: 'A',
          protocol: 'openai-chat',
          baseUrl: 'https://example.test/v1',
          credentialEnv: 'TEST_KEY',
        },
        {
          id: 'b',
          name: 'B',
          protocol: 'openai-responses',
          baseUrl: 'https://example.test/v1',
          credentialEnv: 'TEST_KEY',
        },
      ],
      models: [
        {
          id: 'first',
          displayName: 'First',
          providerId: 'a',
          apiModelName: 'same-model',
          capabilities: ['text', 'tools'],
        },
        {
          id: 'second',
          displayName: 'Second',
          providerId: 'b',
          apiModelName: 'same-model',
          capabilities: ['text', 'tools'],
        },
      ],
      routes: [{ role: 'thinker', orderedModelIds: ['second', 'first'], timeoutMs: 1000 }],
    }
    assert.equal(
      (await app.request('/api/models', { method: 'PUT', headers, body: JSON.stringify(config) }))
        .status,
      200,
    )
    assert.equal(
      (
        await app.request('/api/models', {
          method: 'PUT',
          headers,
          body: JSON.stringify({ ...config, models: [...config.models, config.models[0]] }),
        })
      ).status,
      400,
    )
    assert.equal(
      (
        await app.request('/api/models', {
          method: 'PUT',
          headers,
          body: JSON.stringify({
            ...config,
            providers: [{ ...config.providers[0], apiKey: 'secret' }],
          }),
        })
      ).status,
      400,
    )
    const scene = {
      id: 'private-a',
      kind: 'private',
      adapterId: 'test',
      accountId: 'bot',
      platformSceneId: 'person',
      name: 'Person',
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: true,
    }
    assert.equal(
      (
        await app.request('/api/scenes/private-a', {
          method: 'PUT',
          headers,
          body: JSON.stringify(scene),
        })
      ).status,
      400,
    )
    store.recall('group-a', 'message-a')
    store.log('error', 'test', 'safe_code')
    store.close()
    store = new Store(join(dir, 'test.sqlite'))
    assert.deepEqual(store.models, {
      ...config,
      providers: config.providers.map((provider) => ({
        ...provider,
        credentialConfigured: false,
        retryCount: 2,
        timeoutMs: 30000,
      })),
      models: config.models.map((model) => ({ ...model, embeddingBatchSize: 4 })),
      routes: config.routes.map((route) => ({ ...route, role: 'replyer' })),
    })
    assert.equal(store.recalled('group-a', 'message-a'), true)
    assert.equal(store.recalled('group-b', 'message-a'), false)
    assert.equal(store.logs.length, 0)
    assert.match(store.vectorVersion, /v0\.1\.9/)
    const distance = store.db
      .prepare('SELECT vec_distance_cosine(?,?) AS distance')
      .get('[1,0]', '[1,0]') as { distance: number }
    assert.equal(distance.distance, 0)
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('OpenAPI exposes validated routes and no credential values', async () => {
  const store = new Store(':memory:')
  try {
    const app = createApp(store, { token: 'integration-secret' })
    const response = await app.request('/api/openapi.json', { headers })
    const spec = (await response.json()) as {
      paths: Record<string, unknown>
      components: { schemas: Record<string, unknown> }
    }
    assert.equal(response.status, 200)
    assert.ok(spec.paths['/api/models'])
    assert.ok(spec.components.schemas.ModelConfig)
    assert.ok(!JSON.stringify(spec).includes('integration-secret'))
  } finally {
    store.close()
  }
})

test('Meme list API bounds pages, validates filters, and requires authentication on thumbnails', async () => {
  const store = new Store(':memory:')
  try {
    const app = createApp(store, { token: 'integration-secret' })
    const result = await app.request(
      '/api/memes?page=2&pageSize=48&query=hello&scope=shared&state=favorite',
      { headers },
    )
    assert.equal(result.status, 200)
    assert.deepEqual(await result.json(), { items: [], total: 0, page: 1, pageSize: 48 })
    for (const query of [
      'page=0',
      'page=1.5',
      'pageSize=0',
      'pageSize=97',
      'state=unknown',
      'scope=unknown',
    ]) {
      assert.equal((await app.request('/api/memes?' + query, { headers })).status, 400, query)
    }
    assert.equal((await app.request('/api/memes/missing/thumbnail')).status, 401)
    assert.equal((await app.request('/api/memes/missing', { headers })).status, 400)
  } finally {
    store.close()
  }
})
