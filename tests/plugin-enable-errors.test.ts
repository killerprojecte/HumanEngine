import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createHmac } from 'node:crypto'
import { createServer } from 'node:http'
import { WebSocketServer } from 'ws'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'

const headers = { Authorization: 'Bearer enable-test-admin', 'Content-Type': 'application/json' }
async function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  await engine.start()
  const app = createApp(store, { engine, token: 'enable-test-admin' })
  const enabled = (value: boolean, id = 'snowluma') =>
    app.request(`/api/plugins/${id}/enabled`, {
      method: 'PUT',
      headers,
      body: JSON.stringify({ enabled: value }),
    })
  return {
    store,
    engine,
    app,
    enabled,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}
async function assertApiError(
  f: Awaited<ReturnType<typeof fixture>>,
  response: Response,
  code: string,
  details: { systemCode?: string; httpStatus?: number } = {},
  id = 'snowluma',
) {
  const body = await response.json()
  assert.equal(body.error, code)
  assert.match(body.requestId, /^[a-f0-9-]{36}$/)
  assert.equal(response.headers.get('X-Request-ID'), body.requestId)
  assert.deepEqual(body, { error: code, requestId: body.requestId, ...details })
  const log = f.store.logs.find(
    (log) => log.module === `plugin:${id}` && log.code === code && log.requestId === body.requestId,
  )
  assert.ok(log, 'API failure must correlate with a safe structured diagnostic')
  assert.equal(log.systemCode, details.systemCode)
  assert.equal(log.httpStatus, details.httpStatus)
}
function assertStopped(f: Awaited<ReturnType<typeof fixture>>, code: string) {
  const plugin = f.engine.plugins.list().find((p) => p.id === 'snowluma')!
  assert.equal(plugin.enabled, false)
  assert.equal(plugin.error, code)
  assert.equal(f.store.get('plugin-enabled:snowluma', true), false)
  assert.deepEqual(f.engine.runtime.adapterStatuses, [])
  assert.ok(f.store.logs.some((log) => log.module === 'plugin:snowluma' && log.code === code))
}
async function websocket(accountId: number) {
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  server.on('connection', (socket) =>
    socket.on('message', (raw) => {
      const action = JSON.parse(raw.toString())
      socket.send(
        JSON.stringify({
          status: 'ok',
          retcode: 0,
          data: { user_id: accountId },
          echo: action.echo,
        }),
      )
    }),
  )
  return {
    url: `ws://127.0.0.1:${address.port}`,
    close: async () => {
      for (const socket of server.clients) socket.terminate()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}

test('enable reports missing server credentials, rolls back, and can retry after configuration is repaired', async () => {
  const f = await fixture(),
    server = await websocket(42)
  const name = 'HUMANENGINE_ENABLE_TEST_TOKEN'
  const old = process.env[name]
  delete process.env[name]
  try {
    await f.engine.plugins.configure('snowluma', {
      url: server.url,
      accountId: '42',
      credentialEnv: name,
    })
    const response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'credential_not_configured')
    assertStopped(f, 'credential_not_configured')
    process.env[name] = 'generated-local-fixture-token'
    assert.equal((await f.enabled(true)).status, 200)
    assert.equal(f.engine.plugins.list().find((p) => p.id === 'snowluma')?.enabled, true)
    assert.equal(f.engine.plugins.list().find((p) => p.id === 'snowluma')?.error, undefined)
    assert.equal(f.engine.runtime.adapterStatuses.length, 1)
    assert.equal((await f.enabled(false)).status, 200)
    assert.equal((await f.enabled(false)).status, 200)
    assert.deepEqual(f.engine.runtime.adapterStatuses, [])
  } finally {
    await f.close()
    await server.close()
    if (old === undefined) delete process.env[name]
    else process.env[name] = old
  }
})

test('invalid saved configuration is a 400 enable error rather than a generic service failure', async () => {
  const f = await fixture()
  try {
    f.store.set('plugin:snowluma', {
      url: 'ws://127.0.0.1:1',
      accountId: '',
      credentialEnv: 'TEST_TOKEN',
    })
    const response = await f.enabled(true)
    assert.equal(response.status, 400)
    await assertApiError(f, response, 'invalid_plugin_configuration')
    assertStopped(f, 'invalid_plugin_configuration')
    assert.equal((await f.enabled(false)).status, 200)
  } finally {
    await f.close()
  }
})

test('account handshake mismatch remains a fail-closed safe diagnostic and leaves no adapter', async () => {
  const f = await fixture(),
    server = await websocket(99)
  process.env.HUMANENGINE_ENABLE_ACCOUNT_TOKEN = 'generated-account-fixture-token'
  try {
    await f.engine.plugins.configure('snowluma', {
      url: server.url,
      accountId: '42',
      credentialEnv: 'HUMANENGINE_ENABLE_ACCOUNT_TOKEN',
    })
    const response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'account_mismatch')
    assertStopped(f, 'account_mismatch')
    await f.engine.plugins.configure('snowluma', {
      url: server.url,
      accountId: '99',
      credentialEnv: 'HUMANENGINE_ENABLE_ACCOUNT_TOKEN',
    })
    assert.equal((await f.enabled(true)).status, 200)
  } finally {
    await f.close()
    await server.close()
    delete process.env.HUMANENGINE_ENABLE_ACCOUNT_TOKEN
  }
})

test('HTTP enable distinguishes missing event signature configuration and occupied listener port', async () => {
  const f = await fixture()
  const server = createServer((_request, response) => {
    response.setHeader('Content-Type', 'application/json')
    response.end(JSON.stringify({ status: 'ok', retcode: 0, data: { user_id: 42 } }))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  process.env.HUMANENGINE_ENABLE_HTTP_TOKEN = 'generated-http-fixture-token'
  delete process.env.HUMANENGINE_ENABLE_EVENT_TOKEN
  try {
    await f.engine.plugins.configure('snowluma', {
      transport: 'http',
      url: `http://127.0.0.1:${address.port}`,
      accountId: '42',
      credentialEnv: 'HUMANENGINE_ENABLE_HTTP_TOKEN',
      eventPort: address.port,
      eventCredentialEnv: 'HUMANENGINE_ENABLE_EVENT_TOKEN',
    })
    let response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'event_credential_not_configured')
    assertStopped(f, 'event_credential_not_configured')
    process.env.HUMANENGINE_ENABLE_EVENT_TOKEN = 'generated-event-fixture-secret'
    response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'http_event_listen_failed', { systemCode: 'EADDRINUSE' })
    assertStopped(f, 'http_event_listen_failed')
    assert.equal((await f.enabled(false)).status, 200)
  } finally {
    await f.close()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    delete process.env.HUMANENGINE_ENABLE_HTTP_TOKEN
    delete process.env.HUMANENGINE_ENABLE_EVENT_TOKEN
  }
})

test('untrusted exception content is never copied into API errors, plugin state, or diagnostics', async () => {
  const f = await fixture()
  const privateMarker = 'generated_sensitive_marker'
  try {
    await f.engine.plugins.register({
      manifest: {
        id: 'failure-fixture',
        name: 'Failure fixture',
        version: '1.0.0',
        description: 'Technical exception fixture',
        configSchema: { type: 'object', properties: {}, additionalProperties: false },
        capabilities: [],
      },
      setup() {
        throw new Error(`http://example.invalid/?token=${privateMarker}`)
      },
    })
    const response = await f.enabled(true, 'failure-fixture')
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'plugin_setup_failed', {}, 'failure-fixture')
    assert.equal(f.engine.plugins.list().find((p) => p.id === 'failure-fixture')?.enabled, false)
    assert.equal(JSON.stringify(f.engine.plugins.list()).includes(privateMarker), false)
    assert.equal(JSON.stringify(f.store.logs).includes(privateMarker), false)
    assert.equal((await f.enabled(false, 'failure-fixture')).status, 200)
  } finally {
    await f.close()
  }
})

test('WebSocket rejection is a connection diagnostic without copying upstream wording', async () => {
  const f = await fixture()
  const server = new WebSocketServer({
    host: '127.0.0.1',
    port: 0,
    verifyClient: (_info, done) => done(false, 401, 'generated-upstream-private-detail'),
  })
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  process.env.HUMANENGINE_ENABLE_REJECT_TOKEN = 'generated-rejection-fixture-token'
  try {
    await f.engine.plugins.configure('snowluma', {
      url: `ws://127.0.0.1:${address.port}`,
      accountId: '42',
      credentialEnv: 'HUMANENGINE_ENABLE_REJECT_TOKEN',
    })
    const response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'connection_failed')
    assertStopped(f, 'connection_failed')
    assert.equal(JSON.stringify(f.store.logs).includes('generated-upstream-private-detail'), false)
  } finally {
    await f.close()
    delete process.env.HUMANENGINE_ENABLE_REJECT_TOKEN
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('HTTP startup reports bounded action, malformed response, and transport failure codes', async () => {
  const f = await fixture()
  let mode: 'status' | 'malformed' | 'disconnect' = 'status'
  const server = createServer((request, response) => {
    if (mode === 'disconnect') {
      request.socket.destroy()
      return
    }
    response.statusCode = mode === 'status' ? 503 : 200
    response.end('generated-upstream-private-detail')
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  process.env.HUMANENGINE_ENABLE_FAILURE_TOKEN = 'generated-failure-fixture-token'
  process.env.HUMANENGINE_ENABLE_FAILURE_EVENT = 'generated-failure-fixture-signature'
  try {
    await f.engine.plugins.configure('snowluma', {
      transport: 'http',
      url: `http://127.0.0.1:${address.port}`,
      accountId: '42',
      credentialEnv: 'HUMANENGINE_ENABLE_FAILURE_TOKEN',
      eventPort: address.port,
      eventCredentialEnv: 'HUMANENGINE_ENABLE_FAILURE_EVENT',
    })
    for (const [scenario, code] of [
      ['status', 'http_action_failed'],
      ['malformed', 'invalid_http_response'],
      ['disconnect', 'http_request_failed'],
    ] as const) {
      mode = scenario
      const response = await f.enabled(true)
      assert.equal(response.status, 503)
      await assertApiError(f, response, code, scenario === 'status' ? { httpStatus: 503 } : {})
      assertStopped(f, code)
    }
    assert.equal(JSON.stringify(f.store.logs).includes('generated-upstream-private-detail'), false)
  } finally {
    await f.close()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    delete process.env.HUMANENGINE_ENABLE_FAILURE_TOKEN
    delete process.env.HUMANENGINE_ENABLE_FAILURE_EVENT
  }
})

test('WebSocket refusal preserves allowlisted syscall detail and correlated request ID', async () => {
  const reservation = createServer()
  reservation.listen(0, '127.0.0.1')
  await once(reservation, 'listening')
  const address = reservation.address()
  assert.ok(address && typeof address !== 'string')
  await new Promise<void>((resolve) => reservation.close(() => resolve()))
  const f = await fixture()
  process.env.HUMANENGINE_ENABLE_REFUSED_TOKEN = 'generated-refusal-fixture-token'
  try {
    await f.engine.plugins.configure('snowluma', {
      url: `ws://127.0.0.1:${address.port}`,
      accountId: '42',
      credentialEnv: 'HUMANENGINE_ENABLE_REFUSED_TOKEN',
    })
    const response = await f.enabled(true)
    assert.equal(response.status, 503)
    await assertApiError(f, response, 'connection_failed', { systemCode: 'ECONNREFUSED' })
    assertStopped(f, 'connection_failed')
  } finally {
    await f.close()
    delete process.env.HUMANENGINE_ENABLE_REFUSED_TOKEN
  }
})

test('diagnostic detail boundaries drop hostname, URL, arbitrary codes and non-integer status', async () => {
  const { OneBotError } = await import('../src/onebot.ts')
  const { PluginStartupError } = await import('../src/plugins.ts')
  const privateMarker = 'generated-private-hostname.invalid'
  const rawDetails = {
    systemCode: 'ENOTFOUND',
    httpStatus: 401,
    hostname: privateMarker,
    url: `https://${privateMarker}/?token=private-marker`,
  }
  const transportError = new OneBotError('connection_failed', false, rawDetails)
  assert.deepEqual(transportError.details, { systemCode: 'ENOTFOUND', httpStatus: 401 })
  assert.deepEqual(
    new OneBotError('connection_failed', false, { systemCode: privateMarker, httpStatus: 401.5 })
      .details,
    {},
  )
  const f = await fixture()
  try {
    await f.engine.plugins.register({
      manifest: {
        id: 'detail-fixture',
        name: 'Detail fixture',
        version: '1.0.0',
        description: 'Technical diagnostic boundary fixture',
        configSchema: { type: 'object', properties: {}, additionalProperties: false },
        capabilities: [],
      },
      setup() {
        throw new PluginStartupError('connection_failed', rawDetails)
      },
    })
    const response = await f.enabled(true, 'detail-fixture')
    assert.equal(response.status, 503)
    await assertApiError(
      f,
      response,
      'connection_failed',
      { systemCode: 'ENOTFOUND', httpStatus: 401 },
      'detail-fixture',
    )
    assert.equal(JSON.stringify(f.engine.plugins.list()).includes(privateMarker), false)
    assert.equal(JSON.stringify(f.store.logs).includes(privateMarker), false)
  } finally {
    await f.close()
  }
})

test('SnowLuma database token is write-only, encrypted, preserved on omission, and explicitly clearable', async () => {
  const f = await fixture(),
    server = await websocket(42)
  const token = 'generated-database-adapter-fixture-token'
  const config = { url: server.url, accountId: '42' }
  const configure = (value: unknown) =>
    f.app.request('/api/plugins/snowluma/config', {
      method: 'PUT',
      headers,
      body: JSON.stringify(value),
    })
  try {
    const saved = await configure({ ...config, token })
    assert.equal(saved.status, 200)
    const savedBody = await saved.json()
    assert.equal(savedBody.token, undefined)
    assert.equal(JSON.stringify(savedBody).includes(token), false)
    const read = await f.app.request('/api/plugins/snowluma/config', { headers })
    assert.equal(JSON.stringify(await read.json()).includes(token), false)
    const list = await (await f.app.request('/api/plugins', { headers })).json()
    assert.equal(
      list.find((plugin: { id: string }) => plugin.id === 'snowluma').configuredSecrets.token,
      true,
    )
    assert.equal(JSON.stringify(list).includes(token), false)
    const rows = f.store.db.prepare('SELECT ciphertext FROM secrets').all() as {
      ciphertext: string
    }[]
    assert.equal(rows.length, 1)
    assert.ok(rows[0]!.ciphertext.startsWith('v1.'))
    assert.equal(rows[0]!.ciphertext.includes(token), false)
    assert.equal(JSON.stringify(f.store.get('plugin:snowluma', {})).includes(token), false)
    assert.equal((await f.enabled(true)).status, 200)
    assert.equal((await configure(config)).status, 200)
    assert.equal((await configure({ ...config, token: '' })).status, 200)
    assert.equal(f.store.secrets.get('plugin:snowluma', 'token'), token)
    assert.equal(f.engine.plugins.list().find((plugin) => plugin.id === 'snowluma')?.enabled, true)
    assert.equal((await f.enabled(false)).status, 200)
    assert.equal((await configure({ ...config, clearToken: true })).status, 200)
    assert.equal(f.store.secrets.has('plugin:snowluma', 'token'), false)
    const failed = await f.enabled(true)
    assert.equal(failed.status, 503)
    await assertApiError(f, failed, 'credential_not_configured')
    assert.equal(JSON.stringify(f.store.logs).includes(token), false)
  } finally {
    await f.close()
    await server.close()
  }
})

test('HTTP built-in enables with database action and event secrets without environment references', async () => {
  const f = await fixture()
  let authorization: string | undefined
  const upstream = createServer((request, response) => {
    authorization = request.headers.authorization
    response.setHeader('Content-Type', 'application/json')
    response.end(JSON.stringify({ status: 'ok', retcode: 0, data: { user_id: 42 } }))
  })
  upstream.listen(0, '127.0.0.1')
  await once(upstream, 'listening')
  const upstreamAddress = upstream.address()
  assert.ok(upstreamAddress && typeof upstreamAddress !== 'string')
  const reservation = createServer()
  reservation.listen(0, '127.0.0.1')
  await once(reservation, 'listening')
  const eventAddress = reservation.address()
  assert.ok(eventAddress && typeof eventAddress !== 'string')
  await new Promise<void>((resolve) => reservation.close(() => resolve()))
  try {
    const saved = await f.app.request('/api/plugins/snowluma/config', {
      method: 'PUT',
      headers,
      body: JSON.stringify({
        transport: 'http',
        url: `http://127.0.0.1:${upstreamAddress.port}`,
        accountId: '42',
        token: 'generated-database-http-token',
        eventToken: 'generated-database-signature-token',
        eventPort: eventAddress.port,
      }),
    })
    assert.equal(saved.status, 200)
    assert.equal((await f.enabled(true)).status, 200)
    assert.equal(authorization, 'Bearer generated-database-http-token')
    assert.equal(f.engine.runtime.adapterStatuses[0]?.status, 'connected')
    const plugin = f.engine.plugins.list().find((plugin) => plugin.id === 'snowluma')!
    assert.deepEqual(plugin.configuredSecrets, { token: true, eventToken: true })
    const body = JSON.stringify({
      self_id: 42,
      post_type: 'meta_event',
      meta_event_type: 'heartbeat',
    })
    const post = (secret: string) =>
      fetch(`http://127.0.0.1:${eventAddress.port}/onebot/events`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Self-ID': '42',
          'X-Signature': 'sha1=' + createHmac('sha1', secret).update(body).digest('hex'),
        },
        body,
      })
    assert.equal((await post('generated-database-signature-token')).status, 204)
    assert.equal((await post('generated-wrong-signature-token')).status, 401)
    assert.equal((await f.enabled(false)).status, 200)
    assert.deepEqual(f.engine.runtime.adapterStatuses, [])
  } finally {
    await f.close()
    upstream.closeAllConnections()
    await new Promise<void>((resolve) => upstream.close(() => resolve()))
  }
})
