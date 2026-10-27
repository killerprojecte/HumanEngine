import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { RebuildJob } from '../src/embedding-index.ts'

test('embedding settings API validates optional dimensions and configuration changes never start paid rebuilds', async () => {
  const store = new Store(':memory:'),
    engine = new Engine(store)
  const app = createApp(store, { engine, token: 'local-admin-fixture' })
  const headers = {
    Authorization: 'Bearer local-admin-fixture',
    'Content-Type': 'application/json',
  }
  const config = {
    providers: [
      {
        id: 'p',
        name: 'Synthetic',
        protocol: 'openai-chat',
        baseUrl: 'https://example.invalid/v1',
        credentialEnv: 'LOCAL_UNUSED_KEY',
      },
    ],
    models: [
      {
        id: 'm',
        displayName: 'Synthetic',
        providerId: 'p',
        apiModelName: 'synthetic',
        capabilities: ['embedding'],
        inputUsdPerMillion: 0.1,
      },
    ],
    routes: [{ role: 'embedding', orderedModelIds: ['m'], timeoutMs: 10000 }],
  }
  try {
    assert.equal((await app.request('/api/embedding-index')).status, 401)
    for (const dimensions of [undefined, 1024, 4096, 16384]) {
      const response = await app.request('/api/models', {
        method: 'PUT',
        headers,
        body: JSON.stringify({
          ...config,
          models: [{ ...config.models[0], embeddingDimensions: dimensions }],
        }),
      })
      assert.equal(response.status, 200)
      assert.equal(store.models.models[0]?.embeddingDimensions, dimensions)
      assert.equal(store.models.models[0]?.inputUsdPerMillion, 0.1)
      assert.equal(engine.embeddingIndex.status().job, undefined)
    }
    for (const dimensions of [0, -1, 1.5, 16385, '1024', null]) {
      const response = await app.request('/api/models', {
        method: 'PUT',
        headers,
        body: JSON.stringify({
          ...config,
          models: [{ ...config.models[0], embeddingDimensions: dimensions }],
        }),
      })
      assert.equal(response.status, 400)
      assert.equal(store.models.models[0]?.embeddingDimensions, 16384)
    }
    const status = await (await app.request('/api/embedding-index', { headers })).json()
    assert.equal(status.state, 'needs_rebuild')
    assert.deepEqual(status.coverage, { eligible: 0, indexed: 0, pending: 0 })
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('embedding actions require authentication, strict consent, and sanitize failures', async () => {
  const store = new Store(':memory:'),
    engine = new Engine(store)
  const app = createApp(store, { engine, token: 'local-admin-fixture' })
  const headers = {
    Authorization: 'Bearer local-admin-fixture',
    'Content-Type': 'application/json',
  }
  const calls: unknown[] = []
  const job: RebuildJob = {
    id: 'local-job',
    encoderRevision: 'local',
    modelId: 'm',
    state: 'paused',
    processed: 0,
    total: 1,
    requiresRetryConfirmation: true,
  }
  engine.embeddingIndex.start = (expectedJobId, expectedEncoderRevision) => {
    calls.push(['start', expectedJobId, expectedEncoderRevision])
    return job
  }
  engine.embeddingIndex.resume = (confirm, expectedJobId) => {
    calls.push(['resume', confirm, expectedJobId])
    return job
  }
  engine.embeddingIndex.pause = (expectedJobId) => {
    calls.push(['pause', expectedJobId])
    return job
  }
  engine.embeddingIndex.cancel = () => {
    throw new Error('raw-provider-secret-must-not-leak')
  }
  try {
    assert.equal(
      (await app.request('/api/embedding-index/start', { method: 'POST', body: '{}' })).status,
      401,
    )
    assert.deepEqual(calls, [])
    for (const body of [
      { confirmUnknownRequest: 'true' },
      { confirmUnknownRequest: true, injected: 1 },
    ]) {
      assert.equal(
        (
          await app.request('/api/embedding-index/resume', {
            method: 'POST',
            headers,
            body: JSON.stringify(body),
          })
        ).status,
        400,
      )
    }
    assert.deepEqual(calls, [])
    for (const [action, body] of [
      ['start', { expectedJobId: null, expectedEncoderRevision: 'selected-revision' }],
      ['pause', { expectedJobId: 'local-job' }],
      ['resume', { expectedJobId: 'local-job' }],
      ['resume', { confirmUnknownRequest: true, expectedJobId: 'local-job' }],
    ] as const) {
      assert.equal(
        (
          await app.request(`/api/embedding-index/${action}`, {
            method: 'POST',
            headers,
            body: JSON.stringify(body),
          })
        ).status,
        200,
      )
    }
    assert.deepEqual(calls, [
      ['start', null, 'selected-revision'],
      ['pause', 'local-job'],
      ['resume', false, 'local-job'],
      ['resume', true, 'local-job'],
    ])
    const failure = await app.request('/api/embedding-index/cancel', {
      method: 'POST',
      headers,
      body: JSON.stringify({ expectedJobId: 'local-job' }),
    })
    assert.equal(failure.status, 400)
    assert.deepEqual(await failure.json(), { error: 'embedding_index_action_unavailable' })
    assert.equal(
      (await app.request('/api/embedding-index/unknown', { method: 'POST', headers, body: '{}' }))
        .status,
      400,
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})
