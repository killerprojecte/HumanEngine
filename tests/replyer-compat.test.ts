import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { Store } from '../src/store.ts'
import { Router, type ModelLog } from '../src/models.ts'
import { ModelConfigSchema, SettingsSchema } from '../src/schema.ts'
import { createApp } from '../src/api.ts'

const config = {
  providers: [
    {
      id: 'p',
      name: 'Fixture',
      protocol: 'openai-chat',
      baseUrl: 'http://127.0.0.1:1',
      credentialEnv: 'REPLYER_COMPAT_FIXTURE',
      retryCount: 1,
      timeoutMs: 1000,
    },
  ],
  models: [
    {
      id: 'm',
      displayName: 'Saved model',
      providerId: 'p',
      apiModelName: 'saved-provider-model',
      capabilities: ['text' as const, 'tools' as const],
    },
  ],
  routes: [{ role: 'thinker' as const, orderedModelIds: ['m'], timeoutMs: 4321 }],
}

test('legacy stored models and explicit saves expose canonical Replyer without losing routing/provider preferences', () => {
  const store = new Store(':memory:')
  try {
    store.set('models', config)
    const loaded = store.models
    assert.deepEqual(loaded.routes, [{ ...config.routes[0], role: 'replyer' }])
    assert.equal(loaded.providers[0]!.retryCount, 1)
    assert.equal(loaded.providers[0]!.timeoutMs, 1000)
    assert.equal(loaded.models[0]!.apiModelName, 'saved-provider-model')
    // Loading an older database/backup is non-destructive; an explicit save canonicalizes it.
    assert.equal(store.get<typeof config>('models', config).routes[0]!.role, 'thinker')
    store.saveModels(config)
    assert.equal(
      store.get<{ routes: Array<{ role: string }> }>('models', { routes: [] }).routes[0]!.role,
      'replyer',
    )
    assert.equal(store.settings.replySuggestionsEnabled, false)
    assert.equal(
      SettingsSchema.parse({ ...store.settings, replySuggestionsEnabled: true })
        .replySuggestionsEnabled,
      true,
    )
    assert.throws(
      () =>
        ModelConfigSchema.parse({
          ...config,
          routes: [...config.routes, { ...config.routes[0], role: 'replyer' }],
        }),
      /Duplicate role routes/,
    )
  } finally {
    store.close()
  }
})

test('models API accepts legacy role input and returns canonical Replyer; alias collisions are rejected', async () => {
  const store = new Store(':memory:')
  const app = createApp(store, { token: 'admin' })
  const headers = { Authorization: 'Bearer admin', 'Content-Type': 'application/json' }
  try {
    const saved = await app.request('/api/models', {
      method: 'PUT',
      headers,
      body: JSON.stringify(config),
    })
    assert.equal(saved.status, 200)
    const output = (await saved.json()) as typeof config
    assert.equal(output.routes[0]!.role, 'replyer')
    const read = (await (await app.request('/api/models', { headers })).json()) as typeof config
    assert.equal(read.routes[0]!.timeoutMs, 4321)
    const conflict = await app.request('/api/models', {
      method: 'PUT',
      headers,
      body: JSON.stringify({
        ...config,
        routes: [...config.routes, { ...config.routes[0], role: 'replyer' }],
      }),
    })
    assert.equal(conflict.status, 400)
    assert.equal(store.models.routes.length, 1)
  } finally {
    store.close()
  }
})

test('real Router accepts either legacy or canonical callers against legacy routes and emits only Replyer observations', async () => {
  const requests: Array<{ model: string }> = []
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    requests.push(JSON.parse(Buffer.concat(chunks).toString()))
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [
          { finish_reason: 'stop', message: { role: 'assistant', content: 'Fixture response' } },
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      }),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const port = (server.address() as { port: number }).port
  const logs: ModelLog[] = []
  const diagnostics: string[] = []
  const router = new Router(
    { ...config, providers: [{ ...config.providers[0]!, baseUrl: `http://127.0.0.1:${port}` }] },
    (entry) => logs.push(entry),
    (entry) => diagnostics.push(entry.role),
    undefined,
    () => 'fixture-only',
  )
  try {
    for (const role of ['thinker', 'replyer'] as const) {
      const response = await router.complete(role, [{ role: 'user', content: 'Fixture' }])
      assert.equal(response.text, 'Fixture response')
    }
    assert.deepEqual(
      requests.map((request) => request.model),
      ['saved-provider-model', 'saved-provider-model'],
    )
    assert.deepEqual(
      logs.map((log) => log.role),
      ['replyer', 'replyer'],
    )
    assert.deepEqual(diagnostics, ['replyer', 'replyer'])
    assert.throws(
      () =>
        new Router({
          ...config,
          routes: [...config.routes, { ...config.routes[0]!, role: 'replyer' }],
        }),
      /duplicate_role_routes/,
    )
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('legacy usage remains readable and aggregates under Replyer without rewriting old rows', () => {
  const store = new Store(':memory:')
  const today = new Date().toISOString().slice(0, 10)
  try {
    store.set('models', config)
    store.db
      .prepare('INSERT INTO model_usage VALUES(?,?,?,?,?,?,?,?,?,?,?)')
      .run(today, 'm', 'p', 'saved-provider-model', 'thinker', 2, 10, 5, 20, 1, 0.25)
    store.recordModel({
      event: 'model.completed',
      modelId: 'm',
      providerId: 'p',
      role: 'thinker',
      durationMs: 10,
      usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 },
    })
    const rows = store.usage()
    assert.equal(rows.length, 1)
    assert.equal(rows[0]!.role, 'replyer')
    assert.equal(rows[0]!.calls, 3)
    assert.equal(rows[0]!.inputTokens, 13)
    assert.equal(rows[0]!.estimatedUsd, 0.25)
    assert.equal(
      (
        store.db.prepare("SELECT calls FROM model_usage WHERE role='thinker'").get() as {
          calls: number
        }
      ).calls,
      2,
    )
    const log = store.log('info', 'model', 'fixture', { role: 'thinker' })
    assert.equal(log.role, 'replyer')
  } finally {
    store.close()
  }
})
