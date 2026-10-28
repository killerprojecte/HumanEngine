import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'

test('authenticated cache prices round-trip independently, preserve explicit zero and reject negative rates', async () => {
  const store = new Store(':memory:')
  const app = createApp(store, { token: 'synthetic-cache-test' })
  const headers = {
    Authorization: 'Bearer synthetic-cache-test',
    'Content-Type': 'application/json',
  }
  const rates = {
    inputUsdPerMillion: 2,
    outputUsdPerMillion: 8,
    cacheReadUsdPerMillion: 0,
    cacheWriteUsdPerMillion: 3,
    cacheWrite5mUsdPerMillion: 2.5,
    cacheWrite1hUsdPerMillion: 4,
    cacheWrite30mUsdPerMillion: 3.5,
    cacheWrite12hUsdPerMillion: 5,
  }
  const model = {
    id: 'm',
    providerId: 'p',
    displayName: 'Synthetic',
    apiModelName: 'fixture',
    capabilities: ['text'],
    ...rates,
  }
  const config = {
    providers: [
      {
        id: 'p',
        name: 'Fixture',
        protocol: 'openai-chat',
        baseUrl: 'https://example.test/v1',
        credentialEnv: '',
      },
    ],
    models: [model],
    routes: [],
  }
  try {
    assert.equal(
      (await app.request('/api/models', { method: 'PUT', body: JSON.stringify(config) })).status,
      401,
    )
    assert.equal(
      (await app.request('/api/models', { method: 'PUT', headers, body: JSON.stringify(config) }))
        .status,
      200,
    )
    const read = await (await app.request('/api/models', { headers })).json()
    for (const [key, value] of Object.entries(rates)) assert.equal(read.models[0][key], value)
    for (const key of Object.keys(rates)) {
      assert.equal(
        (
          await app.request('/api/models', {
            method: 'PUT',
            headers,
            body: JSON.stringify({ ...config, models: [{ ...model, [key]: -1 }] }),
          })
        ).status,
        400,
      )
    }
    const withoutRates = {
      id: 'm',
      providerId: 'p',
      displayName: 'Synthetic',
      apiModelName: 'fixture',
      capabilities: ['text'],
    }
    assert.equal(
      (
        await app.request('/api/models', {
          method: 'PUT',
          headers,
          body: JSON.stringify({ ...config, models: [withoutRates] }),
        })
      ).status,
      200,
    )
    const cleared = await (await app.request('/api/models', { headers })).json()
    for (const key of Object.keys(rates)) assert.equal(cleared.models[0][key], undefined)
  } finally {
    store.close()
  }
})
