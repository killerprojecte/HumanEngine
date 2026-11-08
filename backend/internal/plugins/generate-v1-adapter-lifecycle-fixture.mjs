// Execute actual source installAdapter for the legal lifecycle contract. Native
// registration can additionally fail; that new boundary owns its wrapper early.
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { pathToFileURL } from 'node:url'
const sourceRoot = process.env.V1_SOURCE_ROOT
  ? pathToFileURL(process.env.V1_SOURCE_ROOT + '/')
  : new URL('../../../', import.meta.url)
const source = new URL('src/plugins/adapters.ts', sourceRoot)
const hash = createHash('sha256').update(readFileSync(source)).digest('hex')
if (hash !== '02c38bed5d0065397f815b999666fdde504806377561bb61f9d115b47bf022c9')
  throw Error('Review adapter source lifecycle oracle: ' + hash)
const { installAdapter } = await import(source.href)
const root = new AbortController(),
  trace = [],
  received = []
let adapter,
  api,
  sends = 0,
  releaseSend,
  releaseHistory,
  sendEntered,
  historyEntered
const sendAdmission = new Promise((resolve) => {
    sendEntered = resolve
  }),
  historyAdmission = new Promise((resolve) => {
    historyEntered = resolve
  })
const installed = installAdapter(
  'source-lifecycle:a',
  (context) => {
    api = context
    return {
      async send(scene, segment) {
        sends++
        await Promise.resolve()
        if (segment.parts[0]?.text === 'pending') {
          sendEntered()
          await new Promise((resolve) => {
            releaseSend = resolve
          })
        }
        return { status: 'sent' }
      },
      async history(scene, input) {
        await Promise.resolve()
        if (input.cursor === 'pending') {
          historyEntered()
          await new Promise((resolve) => {
            releaseHistory = resolve
          })
        }
        return { coverage: 'complete', items: [] }
      },
      getMessage() {
        return Promise.resolve({ coverage: 'complete', items: [] })
      },
      dispose() {
        trace.push('adapter.dispose')
      },
    }
  },
  {
    addAdapter(id, value) {
      adapter = value
    },
    removeAdapter() {
      adapter.dispose()
    },
    ingest(message) {
      received.push(message)
    },
  },
  {
    recalled() {
      return false
    },
  },
  root.signal,
  {
    media() {
      return { file: 'synthetic', type: 'image/png' }
    },
  },
)
await installed.start()
const scene = api.scene('synthetic-account', 'group', 'synthetic-room')
const sendAbort = new AbortController()
const pending = adapter.send(
  scene,
  { parts: [{ type: 'text', text: 'pending' }] },
  sendAbort.signal,
)
await sendAdmission
sendAbort.abort(new Error('request_cancelled'))
const cancelled = await pending
const next = await adapter.send(scene, { parts: [{ type: 'text', text: 'healthy' }] })
releaseSend()
await Promise.resolve()
const readAbort = new AbortController()
const history = adapter.history(scene, { limit: 1, cursor: 'pending' }, readAbort.signal)
await historyAdmission
readAbort.abort(new Error('request_cancelled'))
let readError
try {
  await history
} catch (error) {
  readError = error.message
}
releaseHistory()
await Promise.resolve()
api.receive(
  {
    id: api.messageId(scene, 'background'),
    sceneId: scene.id,
    senderId: 'synthetic-user',
    sentAt: '2026-01-01T00:00:00Z',
    receivedAt: '2026-01-01T00:00:00Z',
    origin: 'live',
    status: 'available',
    parts: [],
  },
  scene,
  false,
)
root.abort('plugin_disabled')
installed.dispose()
installed.dispose()
const output =
  JSON.stringify(
    {
      sourceSHA256: hash,
      cancelledSend: cancelled,
      nextSend: next,
      sends,
      historyCancellation: readError,
      backgroundIngress: received.length,
      disposeTrace: trace,
    },
    null,
    2,
  ) + '\n'
const file = new URL('testdata/adapter-source-lifecycle.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(file, 'utf8') !== output) throw Error('Regenerate source lifecycle fixture')
} else writeFileSync(file, output)
