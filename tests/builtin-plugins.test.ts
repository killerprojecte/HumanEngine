import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { WebSocketServer } from 'ws'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { OneBotAdapter } from '../src/onebot.ts'

const headers = { Authorization: 'Bearer local-test-admin', 'Content-Type': 'application/json' }
test('bundled plugins default disabled, validate config through API and persist explicit toggles', async () => {
  const store = new Store(':memory:')
  let engine = new Engine(store)
  try {
    await engine.start()
    const app = createApp(store, { engine, token: 'local-test-admin' })
    const list = await (await app.request('/api/plugins', { headers })).json()
    assert.deepEqual(
      list.map((p: { id: string; enabled: boolean; builtin: boolean }) => [
        p.id,
        p.enabled,
        p.builtin,
      ]),
      [
        ['snowluma', false, true],
        ['typing-delay', false, true],
      ],
    )
    const configure = (id: string, value: unknown) =>
      app.request(`/api/plugins/${id}/config`, {
        method: 'PUT',
        headers,
        body: JSON.stringify(value),
      })
    for (const value of [0, -1, 11, '2', null])
      assert.equal((await configure('typing-delay', { speedFactor: value })).status, 400)
    assert.equal((await configure('typing-delay', {})).status, 200)
    await engine.plugins.enable('typing-delay')
    assert.equal(engine.plugins.list().find((p) => p.id === 'typing-delay')?.enabled, true)
    const config = { url: 'ws://127.0.0.1:1', accountId: '42', credentialEnv: 'BUILTIN_TEST_TOKEN' }
    assert.equal((await configure('snowluma', config)).status, 200)
    for (const value of [
      { ...config, url: 'http://127.0.0.1:1' },
      { ...config, url: 'ws://127.0.0.1:1/?access_token=invalid' },
      { ...config, credentialEnv: 'invalid-name' },
      { ...config, transport: 'http', url: 'http://127.0.0.1:1', eventPort: 8080 },
      {
        ...config,
        transport: 'http',
        url: 'http://127.0.0.1:1',
        eventPort: 0,
        eventCredentialEnv: 'EVENT_TEST_TOKEN',
      },
    ])
      assert.equal((await configure('snowluma', value)).status, 400)
    assert.deepEqual(engine.plugins.config('snowluma'), config)
    assert.deepEqual(engine.runtime.adapterStatuses, []) // Saving does not establish a connection.
    await engine.dispose()
    engine = new Engine(store)
    await engine.start()
    assert.equal(engine.plugins.list().find((p) => p.id === 'typing-delay')?.enabled, true)
    assert.equal(engine.plugins.list().find((p) => p.id === 'snowluma')?.enabled, false)
    await engine.plugins.disable('typing-delay')
    assert.equal(store.get('plugin-enabled:typing-delay', true), false)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('legacy SnowLuma config and scene IDs survive extraction; disabling invalidates transport immediately', async () => {
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  let connections = 0
  server.on('connection', (socket) => {
    connections++
    socket.on('message', (raw) => {
      const action = JSON.parse(raw.toString())
      if (action.action === 'get_login_info')
        socket.send(
          JSON.stringify({ status: 'ok', retcode: 0, data: { user_id: 42 }, echo: action.echo }),
        )
    })
  })
  const store = new Store(':memory:')
  const config = {
    url: `ws://127.0.0.1:${address.port}`,
    accountId: '42',
    credentialEnv: 'BUILTIN_TEST_TOKEN',
  }
  process.env.BUILTIN_TEST_TOKEN = 'local-fixture-token'
  store.set('plugin:snowluma', config)
  store.set('plugin-enabled:snowluma', true)
  const previous = new OneBotAdapter({ id: 'snowluma', ...config })
  const scene = previous.scene('group', '99')
  store.saveScene({
    ...scene,
    name: 'Technical fixture',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  try {
    await engine.start()
    assert.equal(connections, 1)
    assert.equal(engine.runtime.adapterStatuses[0]?.id, 'snowluma')
    assert.equal(store.scene(scene.id)?.adapterId, 'snowluma')
    assert.deepEqual(engine.plugins.config('snowluma'), config)
    const disabling = engine.plugins.disable('snowluma')
    assert.deepEqual(engine.runtime.adapterStatuses, [])
    await disabling
    assert.equal(store.get('plugin-enabled:snowluma', true), false)
    await engine.plugins.enable('snowluma')
    assert.equal(connections, 2)
    assert.equal(engine.runtime.adapterStatuses.length, 1)
    await engine.plugins.disable('snowluma')
  } finally {
    await engine.dispose()
    previous.dispose()
    store.close()
    delete process.env.BUILTIN_TEST_TOKEN
    for (const socket of server.clients) socket.terminate()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})
