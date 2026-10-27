import test from 'node:test'
import assert from 'node:assert/strict'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'

const headers = {
  Authorization: 'Bearer configuration-test-admin',
  'Content-Type': 'application/json',
}
const websocket = { transport: 'websocket', url: 'ws://localhost:49152', accountId: '42' }
async function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  await engine.start()
  const app = createApp(store, { engine, token: 'configuration-test-admin' })
  return {
    store,
    engine,
    configure: (value: unknown) =>
      app.request('/api/plugins/snowluma/config', {
        method: 'PUT',
        headers,
        body: JSON.stringify(value),
      }),
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}
async function rejected(response: Response, expected: { field: string; reason: string }[]) {
  assert.equal(response.status, 400)
  const body = await response.json()
  assert.equal(body.error, 'invalid_plugin_configuration')
  assert.match(body.requestId, /^[a-f0-9-]{36}$/)
  assert.equal(response.headers.get('X-Request-ID'), body.requestId)
  assert.deepEqual(body.issues, expected)
  assert.deepEqual(Object.keys(body).sort(), ['error', 'issues', 'requestId'])
  return body
}

test('SnowLuma reports a missing WebSocket scheme without reflecting submitted settings or secrets', async () => {
  const f = await fixture()
  try {
    const rawUrl = 'localhost:49152'
    const token = 'generated-configuration-private-marker'
    const body = await rejected(await f.configure({ ...websocket, url: rawUrl, token }), [
      { field: 'url', reason: 'websocket_scheme_required' },
    ])
    const diagnostics = JSON.stringify([body, f.store.logs])
    assert.equal(diagnostics.includes(rawUrl), false)
    assert.equal(diagnostics.includes(token), false)
    assert.equal(f.store.secrets.has('plugin:snowluma', 'token'), false)
    assert.equal((await f.configure(websocket)).status, 200)
    assert.deepEqual(f.engine.runtime.adapterStatuses, [])
  } finally {
    await f.close()
  }
})

test('SnowLuma validates schemes by selected protocol and requires an HTTP event port', async () => {
  const f = await fixture()
  try {
    await rejected(await f.configure({ ...websocket, url: 'http://localhost:49152' }), [
      { field: 'url', reason: 'websocket_scheme_required' },
    ])
    await rejected(await f.configure({ ...websocket, transport: 'http', eventPort: 49153 }), [
      { field: 'url', reason: 'http_scheme_required' },
    ])
    await rejected(
      await f.configure({ ...websocket, transport: 'http', url: 'http://localhost:49152' }),
      [{ field: 'eventPort', reason: 'required' }],
    )
  } finally {
    await f.close()
  }
})

test('SnowLuma returns fixed reasons for credentials, queries, and fragments in transport URLs', async () => {
  const f = await fixture()
  try {
    for (const [url, reason] of [
      ['ws://fixture-user:fixture-password@localhost:49152', 'url_credentials_forbidden'],
      ['ws://localhost:49152?token=fixture-query-secret', 'url_query_forbidden'],
      ['ws://localhost:49152#fixture-private-fragment', 'url_fragment_forbidden'],
    ]) {
      const body = await rejected(await f.configure({ ...websocket, url }), [
        { field: 'url', reason: reason! },
      ])
      assert.equal(JSON.stringify([body, f.store.logs]).includes(url!), false)
    }
  } finally {
    await f.close()
  }
})

test('Irrelevant HTTP event fields do not block WebSocket configuration but are validated for HTTP', async () => {
  const f = await fixture()
  try {
    const ignored = {
      eventHost: 'not-a-bind-host',
      eventPort: -1,
      eventPath: 'not-a-path',
      eventCredentialEnv: 'not-an-env',
    }
    assert.equal((await f.configure({ ...websocket, ...ignored })).status, 200)
    assert.deepEqual(f.engine.runtime.adapterStatuses, [])
    const response = await f.configure({
      ...websocket,
      ...ignored,
      transport: 'http',
      url: 'http://localhost:49152',
    })
    assert.equal(response.status, 400)
    const body = await response.json()
    assert.equal(body.error, 'invalid_plugin_configuration')
    assert.ok(body.issues.some((issue: { field: string }) => issue.field === 'eventPort'))
    assert.ok(body.issues.some((issue: { field: string }) => issue.field === 'eventHost'))
    assert.equal(JSON.stringify(body).includes('not-a-bind-host'), false)
  } finally {
    await f.close()
  }
})

test('Secret clear/set conflicts return only safe field diagnostics and cannot persist secrets', async () => {
  const f = await fixture()
  try {
    for (const [field, clear] of [
      ['token', 'clearToken'],
      ['eventToken', 'clearEventToken'],
    ] as const) {
      const secret = 'generated-conflicting-secret-marker'
      const body = await rejected(
        await f.configure({
          ...websocket,
          ...(field === 'eventToken'
            ? { transport: 'http', url: 'http://localhost:49152', eventPort: 49153 }
            : {}),
          [field]: secret,
          [clear]: true,
        }),
        [{ field, reason: 'conflicting_credential_change' }],
      )
      assert.equal(JSON.stringify([body, f.store.logs]).includes(secret), false)
      assert.equal(f.store.secrets.has('plugin:snowluma', field), false)
    }
  } finally {
    await f.close()
  }
})

test('Unrecognized property names are never reflected in configuration diagnostics', async () => {
  const f = await fixture()
  try {
    const privateName = 'generated-private-property-name'
    const response = await f.configure({ ...websocket, [privateName]: 'generated-private-value' })
    const body = await rejected(response, [{ field: 'configuration', reason: 'invalid_value' }])
    assert.equal(JSON.stringify([body, f.store.logs]).includes(privateName), false)
    assert.equal(JSON.stringify([body, f.store.logs]).includes('generated-private-value'), false)
  } finally {
    await f.close()
  }
})

test('WebSocket saves preserve previous HTTP settings and ignore hidden event secret mutation fields', async () => {
  const f = await fixture()
  try {
    const eventToken = 'generated-preserved-event-secret'
    assert.equal(
      (
        await f.configure({
          ...websocket,
          transport: 'http',
          url: 'http://localhost:49152',
          eventPort: 49153,
          eventPath: '/fixture/events',
          eventToken,
        })
      ).status,
      200,
    )
    assert.equal(
      (
        await f.configure({
          ...websocket,
          eventPort: -1,
          eventPath: 'invalid-hidden-path',
          eventToken: 'generated-ignored-secret',
          clearEventToken: true,
        })
      ).status,
      200,
    )
    const saved = f.engine.plugins.config('snowluma')
    assert.equal(saved.eventPort, 49153)
    assert.equal(saved.eventPath, '/fixture/events')
    assert.equal(saved.eventToken, undefined)
    assert.equal(f.store.secrets.get('plugin:snowluma', 'eventToken'), eventToken)
    assert.deepEqual(f.engine.runtime.adapterStatuses, [])
  } finally {
    await f.close()
  }
})
