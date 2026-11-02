import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer, request as httpRequest } from 'node:http'
import { once } from 'node:events'
import { createApp as createH3, eventHandler, toNodeListener, toWebRequest } from 'h3'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import { requestAbort } from '../server/utils/requestAbort.ts'
import { mergeRecentHistory } from '../src/runtime/chat-history.ts'

const scene = {
  id: 'fixture-group',
  kind: 'group' as const,
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'synthetic',
}
const message = (id: number): RuntimeMessage & { origin: 'live' } => ({
  id: `message-${id}`,
  sceneId: scene.id,
  senderId: 'fixture-person',
  sentAt: new Date(Date.now() - (100 - id) * 1000).toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `Synthetic ${id}` }],
})
function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const app = createApp(store, { engine, token: 'fixture-token' })
  const url = `/api/scenes/${scene.id}/history`
  const headers = { Authorization: 'Bearer fixture-token' }
  return {
    store,
    engine,
    app,
    url,
    headers,
    async close() {
      await engine.dispose()
      store.close()
    },
  }
}

test('local chat history is bounded, scene-scoped, recall-safe and independent of a stalled platform', async () => {
  const f = fixture()
  let calls = 0
  let release!: () => void
  f.engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    history: async () => {
      calls++
      await new Promise<void>((resolve) => {
        release = resolve
      })
      return { items: [], coverage: 'partial' }
    },
    dispose() {},
  })
  try {
    for (let i = 0; i < 40; i++) f.engine.runtime.ingest(message(i), scene, false)
    f.engine.runtime.recall(scene.id, 'message-39')
    const remote = f.app.request(f.url, { headers: f.headers })
    // Allow the actual API request to enter the pending adapter call.
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(calls, 1)
    const response = await f.app.request(f.url + '?source=local', { headers: f.headers })
    const result = await response.json()
    assert.equal(result.items.length, 30)
    assert.equal(result.coverage, 'partial')
    assert.equal(result.reason, 'local_history_window')
    assert.equal(result.items[0].id, 'message-10')
    assert.equal(result.items.at(-1).status, 'recalled')
    assert.deepEqual(result.items.at(-1).parts, [])
    assert.equal(calls, 1, 'local path does not contact the platform')
    assert.equal(
      (await f.app.request(f.url + '?source=invalid', { headers: f.headers })).status,
      400,
    )
    assert.equal((await f.app.request(f.url + '?source=local')).status, 401)
    const other = await f.app.request('/api/scenes/missing/history?source=local', {
      headers: f.headers,
    })
    assert.deepEqual((await other.json()).items, [])
    release()
    await remote
  } finally {
    release?.()
    await f.close()
  }
})

test('actual Node HTTP disconnect reaches only its history adapter request and cleans listeners', async () => {
  const f = fixture()
  const signals: AbortSignal[] = []
  let entered!: () => void
  const ready = new Promise<void>((resolve) => {
    entered = resolve
  })
  f.engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    history: async (_scene, _input, signal) => {
      assert.ok(signal)
      signals.push(signal)
      entered()
      await new Promise<void>((resolve) =>
        signal.addEventListener('abort', () => resolve(), { once: true }),
      )
      return { items: [], coverage: 'unavailable' }
    },
    dispose() {},
  })
  let cleaned = false
  const h3 = createH3().use(
    eventHandler(async (event) => {
      const web = toWebRequest(event)
      const cancellation = requestAbort(web, event.node)
      try {
        return await f.app.fetch(new Request(web, { signal: cancellation.signal }))
      } finally {
        cancellation.dispose()
        cleaned = event.node.req.listenerCount('aborted') === 0
      }
    }),
  )
  const server = createServer(toNodeListener(h3))
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const client = httpRequest(`http://127.0.0.1:${address.port}${f.url}`, { headers: f.headers })
  client.on('error', () => {})
  client.end()
  try {
    await ready
    const unrelated = new AbortController()
    const independent = f.engine.runtime
      .previewHistory(scene.id, 'remote', unrelated.signal)
      .catch(() => {})
    assert.equal(signals.length, 2)
    client.destroy()
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('disconnect was not propagated')), 2000)
      signals[0]!.addEventListener(
        'abort',
        () => {
          clearTimeout(timeout)
          resolve()
        },
        { once: true },
      )
    })
    assert.equal(signals[1]!.aborted, false)
    unrelated.abort()
    await independent
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(cleaned, true)
  } finally {
    client.destroy()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    await f.close()
  }
})

test('recent-page merge deduplicates current local updates, preserves newer live arrivals, and bounds rendering', () => {
  const local = Array.from({ length: 100 }, (_, i) => message(i))
  const update = { ...local[98]!, parts: [{ type: 'text' as const, text: 'Remote edited text' }] }
  const merged = mergeRecentHistory([update, local[99]!], local)
  assert.equal(merged.length, 60)
  assert.equal(merged.at(-1)!.id, local[99]!.id)
  assert.equal(
    merged.find((m) => m.id === update.id),
    update,
  )
  assert.equal(new Set(merged.map((m) => m.id)).size, merged.length)
  assert.deepEqual(
    merged.map((m) => m.sentAt),
    merged.map((m) => m.sentAt).toSorted(),
  )
})

test('remote completion reconciles fresh live arrivals, edits and recalls instead of stale client text', async () => {
  const f = fixture()
  f.store.set('settings', { ...f.store.settings, sourceEventRevisionsEnabled: true })
  let release!: () => void
  let fail = false
  const stale = [message(1), message(2), message(3)]
  f.engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    history: async () => {
      await new Promise<void>((resolve) => {
        release = resolve
      })
      if (fail) return { items: [], coverage: 'unavailable', reason: 'request_timeout' }
      return { items: [...stale, { ...message(90), sceneId: 'other-scene' }], coverage: 'complete' }
    },
    dispose() {},
  })
  try {
    stale.forEach((m) => f.engine.runtime.ingest(m, scene, false))
    const local = await f.engine.runtime.previewHistory(scene.id, 'local')
    const pending = f.engine.runtime.previewHistory(scene.id)
    const original = f.engine.runtime.preview(scene.id).find((m) => m.id === stale[0]!.id)!
    const edit = f.engine.runtime.edit(
      { ...original, parts: [{ type: 'text', text: 'Fresh explicit edit' }] },
      scene,
      {
        editId: 'fixture-edit',
        order: {
          kind: 'observed_cas',
          expectedBase: f.store.sourceEvents.registered(scene.id, original.id)!.ref,
        },
      },
      false,
    )
    assert.equal(edit.status, 'edited')
    f.engine.runtime.recall(scene.id, stale[1]!.id)
    f.engine.runtime.ingest(message(4), scene, false)
    release()
    const result = await pending
    assert.equal(result.coverage, 'partial')
    assert.ok(result.items.some((m) => m.id === 'message-4'))
    assert.ok(result.items.every((m) => m.sceneId === scene.id))
    assert.equal(result.items.find((m) => m.id === stale[0]!.id)!.parts[0]!.type, 'text')
    assert.deepEqual(result.items.find((m) => m.id === stale[0]!.id)!.parts, [
      { type: 'text', text: 'Fresh explicit edit' },
    ])
    assert.equal(
      result.items.some((m) => m.id === stale[1]!.id),
      false,
      'remote reconciliation omits the recalled ID, not merely its body',
    )
    assert.ok(
      local.items.find((m) => m.id === stale[1]!.id)!.parts.length,
      'old browser snapshot is stale and must be replaced',
    )
    assert.ok(!JSON.stringify(result).includes('Synthetic 2'))
    const projected = await f.app.request(f.url + '?source=local', { headers: f.headers })
    assert.doesNotMatch(await projected.text(), /sourceEventRef|sourceEventRegistrationLimited/)
    fail = true
    const failed = f.engine.runtime.previewHistory(scene.id)
    f.engine.runtime.recall(scene.id, 'message-3')
    release()
    const fallback = await failed
    assert.equal(fallback.coverage, 'unavailable')
    assert.equal(
      fallback.items.some((m) => ['message-2', 'message-3'].includes(m.id)),
      false,
      'platform failure also omits recalled IDs from the fresh local fallback',
    )
    assert.ok(fallback.items.some((m) => m.id === 'message-4'))
    assert.ok(
      !JSON.stringify(fallback).includes('Synthetic 3'),
      'failed platform refresh still returns a current local snapshot',
    )
    f.engine.runtime.removeAdapter('fixture')
    const disconnected = await f.engine.runtime.previewHistory(scene.id)
    assert.equal(disconnected.reason, 'adapter_unavailable')
    assert.equal(
      disconnected.items.some((m) => ['message-2', 'message-3'].includes(m.id)),
      false,
    )
    const retained = await f.engine.runtime.previewHistory(scene.id, 'local')
    for (const id of ['message-2', 'message-3']) {
      const marker = retained.items.find((m) => m.id === id)!
      assert.equal(
        marker.status,
        'recalled',
        'explicit local snapshots retain safe provisional markers',
      )
      assert.deepEqual(marker.parts, [])
    }
  } finally {
    release?.()
    await f.close()
  }
})
