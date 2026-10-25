import assert from 'node:assert/strict'
import { once } from 'node:events'
import test from 'node:test'
import { WebSocketServer, type WebSocket } from 'ws'
import { OneBotAdapter, ONEBOT_V11_PROFILE, type Message, type RecallEvent } from '../src/onebot.ts'

type Action = { action: string; params: Record<string, unknown>; echo: string }
async function fixture(handler: (action: Action, socket: WebSocket) => void) {
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  const sockets: WebSocket[] = []
  const requests: Action[] = []
  const headers: Array<string | undefined> = []
  server.on('connection', (socket, req) => {
    sockets.push(socket); headers.push(req.headers.authorization)
    socket.on('message', data => {
      const action: Action = JSON.parse(data.toString())
      requests.push(action)
      if (action.action === 'get_login_info') reply(socket, action, { user_id: 42 })
      else handler(action, socket)
    })
  })
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Missing fixture address')
  return { sockets, requests, headers, url: `ws://127.0.0.1:${address.port}`, close: async () => { for (const socket of sockets) socket.terminate(); await new Promise<void>(resolve => server.close(() => resolve())) } }
}
const reply = (socket: WebSocket, action: Action, data: unknown) => socket.send(JSON.stringify({ status: 'ok', retcode: 0, data, echo: action.echo }))
const event = (id: number, extra: Record<string, unknown> = {}) => ({ post_type: 'message', self_id: 42, time: 1000 + id, message_type: 'group', group_id: 9, user_id: 7, message_id: id, message: [{ type: 'text', data: { text: 'Hello' } }, { type: 'at', data: { qq: '42' } }, { type: 'reply', data: { id: -90 } }], ...extra })
const pause = (ms = 25) => new Promise(resolve => setTimeout(resolve, ms))

test('SnowLuma transport authenticates and scopes live messages, recalls, private peers, and history', async () => {
  const messages: Message[] = [], recalls: RecallEvent[] = []
  const server = await fixture((action, socket) => {
    if (action.action === 'get_msg') reply(socket, action, event(Number(action.params.message_id), { self_id: undefined, post_type: undefined }))
    else if (action.action === 'get_group_msg_history') reply(socket, action, { messages: [event(-2, { self_id: undefined }), event(-1, { self_id: undefined })] })
    else if (action.action === 'send_group_msg') reply(socket, action, { message_id: -123 })
  })
  process.env.SNOW_FIXTURE_TOKEN = 'fixture-secret'
  const adapter = new OneBotAdapter({ id: 'snow', accountId: '42', url: server.url, credentialEnv: 'SNOW_FIXTURE_TOKEN' }, { onMessage: message => messages.push(message), onRecall: recall => recalls.push(recall) })
  try {
    await adapter.connect()
    assert.equal(server.headers[0], 'Bearer fixture-secret')
    const scene = adapter.scene('group', '9')
    server.sockets[0]!.send(JSON.stringify(event(-1)))
    server.sockets[0]!.send(JSON.stringify(event(-1, { self_id: 99 })))
    server.sockets[0]!.send(JSON.stringify(event(-3, { message_type: 'private', group_id: undefined, user_id: 42, target_id: 7, message: [] })))
    await pause()
    assert.equal(messages.length, 2)
    assert.equal(messages[0]?.id, adapter.messageId(scene, '-1'))
    assert.equal(messages[1]?.sceneId, adapter.scene('private', '7').id)
    assert.equal(messages[0]?.replyToId, adapter.messageId(scene, '-90'))
    server.sockets[0]!.send(JSON.stringify({ post_type: 'notice', self_id: 42, time: 1001, notice_type: 'group_recall', group_id: 9, user_id: 7, operator_id: 8, message_id: -1 }))
    server.sockets[0]!.send(JSON.stringify({ post_type: 'notice', self_id: 42, time: 1001, notice_type: 'friend_recall', user_id: 7, message_id: -3 }))
    await pause()
    assert.equal(recalls[0]?.messageId, messages[0]?.id)
    assert.equal(recalls[1]?.sceneId, adapter.scene('private', '7').id)
    const recalled = await adapter.getMessage(scene, adapter.messageId(scene, '-1'))
    assert.equal(recalled.reason, 'recalled')
    const history = await adapter.history(scene, { limit: 10 })
    assert.equal(history.coverage, 'partial')
    assert.equal(history.items.length, 1)
    assert.equal(history.items[0]?.origin, 'history')
    assert.equal(messages.length, 2)
    const result = await adapter.send(scene, { parts: [{ type: 'text', text: 'Reply' }, { type: 'mention', userId: adapter.userId('7') }], replyToId: adapter.messageId(scene, '-2') })
    assert.equal(result.status, 'sent')
    assert.equal(result.messageId, adapter.messageId(scene, '-123'))
    const send = server.requests.find(item => item.action === 'send_group_msg')!
    assert.deepEqual((send.params.message as unknown[])[0], { type: 'reply', data: { id: '-2' } })
    const before = server.requests.length
    assert.equal((await adapter.getMessage(adapter.scene('group', '10'), adapter.messageId(scene, '-2'))).reason, 'message_scope_mismatch')
    assert.equal(server.requests.length, before)
  } finally { adapter.dispose(); delete process.env.SNOW_FIXTURE_TOKEN; await server.close() }
})

test('Recall while history is in flight strips returned content and persisted tombstones prevent resurrection', async () => {
  let pending: { action: Action; socket: WebSocket } | undefined
  const server = await fixture((action, socket) => { pending = { action, socket } })
  const adapter = new OneBotAdapter({ id: 'snow', accountId: '42', url: server.url, credentialEnv: '' }, { isRecalled: (_scene, id) => id.endsWith('m:Ijk5Ig') })
  try {
    await adapter.connect()
    const scene = adapter.scene('group', '9')
    const history = adapter.history(scene, { limit: 10 })
    await pause()
    server.sockets[0]!.send(JSON.stringify({ post_type: 'notice', self_id: 42, time: 1001, notice_type: 'group_recall', group_id: 9, message_id: -1 }))
    await pause()
    reply(pending!.socket, pending!.action, { messages: [event(-1), event(99)] })
    assert.deepEqual((await history).items, [])
    server.sockets[0]!.send(JSON.stringify(event(-1)))
    const before = server.requests.length
    assert.equal((await adapter.getMessage(scene, adapter.messageId(scene, '99'))).reason, 'recalled')
    assert.equal(server.requests.length, before)
  } finally { adapter.dispose(); await server.close() }
})

test('Unknown sends are never retried, cancelled-before-send is inert, and dispose stops reconnection', async () => {
  const server = await fixture(() => {})
  const adapter = new OneBotAdapter({ id: 'snow', accountId: '42', url: server.url, credentialEnv: '', timeoutMs: 30, reconnectMs: 100 })
  try {
    await adapter.connect()
    const scene = adapter.scene('group', '9'), segment = { parts: [{ type: 'text' as const, text: 'Hello' }] }
    const signal = AbortSignal.abort()
    assert.equal((await adapter.send(scene, segment, signal)).status, 'cancelled')
    assert.equal(server.requests.length, 1)
    const unknown = await adapter.send(scene, segment)
    assert.equal(unknown.status, 'unknown')
    assert.equal(unknown.detail, 'request_timeout')
    const pending = adapter.send(scene, segment)
    await pause(5)
    server.sockets[0]!.terminate()
    assert.equal((await pending).status, 'unknown')
    await pause(140)
    assert.equal(adapter.status, 'connected')
    assert.equal(server.requests.filter(item => item.action === 'send_group_msg').length, 2)
    adapter.dispose()
    await pause(140)
    assert.equal(adapter.status, 'disposed')
    assert.equal(server.sockets.length, 2)
  } finally { adapter.dispose(); await server.close() }
})

test('Generic profile does not claim SnowLuma history extensions; wrong account and cross-scene results fail closed', async () => {
  const server = await fixture((action, socket) => reply(socket, action, event(-5, { group_id: 10 })))
  const adapter = new OneBotAdapter({ id: 'generic', accountId: '42', url: server.url, credentialEnv: '' }, {}, ONEBOT_V11_PROFILE)
  const wrong = new OneBotAdapter({ id: 'wrong', accountId: '99', url: server.url, credentialEnv: '' })
  try {
    await adapter.connect()
    const scene = adapter.scene('group', '9')
    assert.equal((await adapter.history(scene, { limit: 5 })).reason, 'unsupported')
    assert.equal((await adapter.getMessage(scene, adapter.messageId(scene, '-5'))).reason, 'scene_mismatch')
    await assert.rejects(wrong.connect(), { message: 'account_mismatch' })
    assert.notEqual(wrong.status, 'connected')
  } finally { adapter.dispose(); wrong.dispose(); await server.close() }
})
