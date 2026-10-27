import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { OneBotAdapter, type Message } from '../src/onebot.ts'

async function listen(server: ReturnType<typeof createServer>) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string')
  return address.port
}
const ok = (response: ServerResponse, data: unknown) => {
  response.writeHead(200, { 'Content-Type': 'application/json' })
  response.end(JSON.stringify({ status: 'ok', retcode: 0, data }))
}
const event = (extra: Record<string, unknown> = {}) => ({
  post_type: 'message',
  self_id: 42,
  time: 1000,
  message_type: 'group',
  group_id: 9,
  user_id: 7,
  message_id: 101,
  message: [{ type: 'text', data: { text: 'Transport fixture' } }],
  ...extra,
})
async function fixture(
  handler?: (request: IncomingMessage, response: ServerResponse, body: unknown) => void,
) {
  const requests: string[] = [],
    messages: Message[] = []
  const server = createServer(async (request, response) => {
    requests.push(request.url!)
    assert.equal(request.headers.authorization, 'Bearer test-api-token')
    const chunks: Buffer[] = []
    for await (const chunk of request) chunks.push(chunk)
    if (request.url === '/prefix/get_login_info') ok(response, { user_id: 42 })
    else handler?.(request, response, JSON.parse(Buffer.concat(chunks).toString()))
  })
  const apiPort = await listen(server)
  const reservation = createServer()
  const eventPort = await listen(reservation)
  await new Promise<void>((resolve) => reservation.close(() => resolve()))
  process.env.HTTP_TEST_API = 'test-api-token'
  process.env.HTTP_TEST_EVENTS = 'test-event-secret'
  const config = {
    id: 'snowluma',
    accountId: '42',
    transport: 'http' as const,
    url: `http://127.0.0.1:${apiPort}/prefix/`,
    credentialEnv: 'HTTP_TEST_API',
    eventPort,
    eventCredentialEnv: 'HTTP_TEST_EVENTS',
    timeoutMs: 250,
  }
  const adapter = new OneBotAdapter(config, { onMessage: (message) => messages.push(message) })
  const post = async (
    body: string,
    headers: Record<string, string> = {},
    path = '/onebot/events',
  ) =>
    fetch(`http://127.0.0.1:${eventPort}${path}`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Self-ID': '42',
        'X-Signature':
          'sha1=' + createHmac('sha1', process.env.HTTP_TEST_EVENTS!).update(body).digest('hex'),
        ...headers,
      },
      body,
    })
  return {
    adapter,
    config,
    messages,
    requests,
    post,
    close: async () => {
      adapter.dispose()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
      delete process.env.HTTP_TEST_API
      delete process.env.HTTP_TEST_EVENTS
    },
  }
}

test('HTTP sends preserve IDs, signed events, account isolation, recalls and history', async () => {
  const f = await fixture((request, response, body) => {
    if (request.url === '/prefix/send_group_msg') {
      assert.deepEqual(body, {
        group_id: '9',
        message: [{ type: 'text', data: { text: 'Outbound fixture' } }],
      })
      ok(response, { message_id: 102 })
    } else if (request.url === '/prefix/get_msg') ok(response, event())
    else ok(response, { messages: [event()] })
  })
  try {
    await f.adapter.connect()
    const scope = f.adapter.scene('group', '9')
    const legacy = new OneBotAdapter({ ...f.config, transport: 'websocket' })
    assert.deepEqual(scope, legacy.scene('group', '9'))
    assert.equal((await f.post(JSON.stringify(event()))).status, 204)
    assert.equal(f.messages.length, 1)
    assert.equal(
      (await f.post(JSON.stringify(event()), { 'X-Signature': 'sha1=' + '0'.repeat(40) })).status,
      401,
    )
    assert.equal((await f.post(JSON.stringify(event({ self_id: 43 })))).status, 403)
    assert.equal((await f.post(JSON.stringify(event()), {}, '/wrong')).status, 404)
    assert.equal((await f.post('{')).status, 400)
    assert.equal(f.messages.length, 1)
    const result = await f.adapter.send(scope, {
      parts: [{ type: 'text', text: 'Outbound fixture' }],
    })
    assert.equal(result.status, 'sent')
    assert.equal(result.messageId, f.adapter.messageId(scope, '102'))
    assert.equal((await f.adapter.history(scope, { limit: 10 })).items.length, 1)
    await f.post(
      JSON.stringify({
        post_type: 'notice',
        self_id: 42,
        time: 1001,
        notice_type: 'group_recall',
        group_id: 9,
        message_id: 101,
      }),
    )
    assert.equal(
      (await f.adapter.getMessage(scope, f.adapter.messageId(scope, '101'))).reason,
      'recalled',
    )
    await f.post(JSON.stringify(event()))
    assert.equal(f.messages[1]?.status, 'recalled')
    assert.deepEqual(f.messages[1]?.parts, [])
  } finally {
    await f.close()
  }
})

test('HTTP failures are truthful, never retried; cancellation and disposal invalidate sends', async () => {
  let mode = 'business'
  const f = await fixture((_request, response) => {
    if (mode === 'business') response.end(JSON.stringify({ status: 'failed', retcode: 1400 }))
    else if (mode === 'redirect') {
      response.writeHead(307, { Location: '/must-not-follow' })
      response.end()
    } else if (mode === 'invalid') response.end('{')
    // pending mode deliberately does not acknowledge transmission.
  })
  try {
    await f.adapter.connect()
    const scope = f.adapter.scene('group', '9'),
      segment = { parts: [{ type: 'text' as const, text: 'Outcome fixture' }] }
    assert.equal((await f.adapter.send(scope, segment)).status, 'failed')
    mode = 'redirect'
    assert.equal((await f.adapter.send(scope, segment)).status, 'unknown')
    assert(!f.requests.includes('/must-not-follow'))
    mode = 'invalid'
    assert.equal((await f.adapter.send(scope, segment)).status, 'unknown')
    const cancelled = new AbortController()
    cancelled.abort()
    const count = f.requests.length
    assert.equal((await f.adapter.send(scope, segment, cancelled.signal)).status, 'cancelled')
    assert.equal(f.requests.length, count)
    mode = 'pending'
    const pending = f.adapter.send(scope, segment)
    while (f.requests.length === count) await new Promise((resolve) => setTimeout(resolve, 5))
    f.adapter.dispose()
    assert.equal((await pending).status, 'unknown')
    assert.equal((await f.adapter.send(scope, segment)).status, 'failed')
    await assert.rejects(f.post(JSON.stringify(event())))
    assert.equal(f.messages.length, 0)
  } finally {
    await f.close()
  }
})

test('HTTP refuses missing credentials, mismatched account, unsafe URL and oversized events', async () => {
  const f = await fixture()
  try {
    const unsafe = new OneBotAdapter({ ...f.config, url: 'http://user:password@127.0.0.1/' })
    await assert.rejects(unsafe.connect(), /invalid_http_url/)
    const wrong = new OneBotAdapter({ ...f.config, accountId: '43' })
    await assert.rejects(wrong.connect(), /account_mismatch/)
    wrong.dispose()
    const missing = new OneBotAdapter({ ...f.config, eventCredentialEnv: 'NOT_CONFIGURED_TEST' })
    await assert.rejects(missing.connect(), /event_credential_not_configured/)
    await f.adapter.connect()
    const response = await f.post(JSON.stringify(event({ message: 'x'.repeat(1024 * 1024) })))
    assert.equal(response.status, 413)
    assert.equal(f.messages.length, 0)
  } finally {
    await f.close()
  }
})

test('HTTP handshake disposal does not create a listener or accept late login', async () => {
  let loginResponse: ServerResponse | undefined
  const server = createServer((_request, response) => {
    loginResponse = response
  })
  const port = await listen(server)
  process.env.HTTP_RACE_EVENT = 'race-event-secret'
  const adapter = new OneBotAdapter({
    id: 'snowluma',
    accountId: '42',
    transport: 'http',
    url: `http://127.0.0.1:${port}/`,
    credentialEnv: '',
    eventPort: 0,
    eventCredentialEnv: 'HTTP_RACE_EVENT',
    timeoutMs: 500,
  })
  try {
    const opening = adapter.connect()
    const rejected = assert.rejects(opening, /request_cancelled/)
    while (!loginResponse) await new Promise((resolve) => setTimeout(resolve, 5))
    adapter.dispose()
    ok(loginResponse, { user_id: 42 })
    await rejected
    assert.equal(adapter.status, 'disposed')
    await assert.rejects(adapter.connect(), /adapter_disposed/)
  } finally {
    adapter.dispose()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    delete process.env.HTTP_RACE_EVENT
  }
})

test('HTTP timed-out send is unknown and is not replayed by a later read', async () => {
  const f = await fixture((_request, _response) => {})
  try {
    await f.adapter.connect()
    const scope = f.adapter.scene('group', '9')
    const result = await f.adapter.send(scope, {
      parts: [{ type: 'text', text: 'Timeout fixture' }],
    })
    assert.equal(result.status, 'unknown')
    assert.equal(f.requests.filter((path) => path === '/prefix/send_group_msg').length, 1)
    assert.equal((await f.adapter.history(scope, { limit: 1 })).coverage, 'unavailable')
    assert.equal(f.requests.filter((path) => path === '/prefix/send_group_msg').length, 1)
  } finally {
    await f.close()
  }
})
