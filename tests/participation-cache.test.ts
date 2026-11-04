import assert from 'node:assert/strict'
import test from 'node:test'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { Router, type RouterConfig } from '../src/models.ts'
import { Runtime } from '../src/runtime.ts'
import { Store } from '../src/store.ts'
import type { SceneRef } from '../src/onebot.ts'

type Protocol = 'openai-chat' | 'openai-responses'
type Wire = {
  role: string
  content: string | Array<{ text: string; prompt_cache_breakpoint?: unknown }>
}
const scene: SceneRef = {
  id: 'participation-cache',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
async function setup(protocol: Protocol, mode?: 'openai' | 'off') {
  const requests: Array<Record<string, unknown>> = []
  let next: 'ok' | 'fail' | 'hold' = 'ok'
  const started = deferred(),
    release = deferred()
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    requests.push(JSON.parse(Buffer.concat(chunks).toString()))
    const behavior = next
    next = 'ok'
    if (behavior === 'hold') {
      started.resolve()
      await release.promise
    }
    if (behavior === 'fail') {
      res.writeHead(400)
      res.end()
      return
    }
    const args = JSON.stringify({ action: 'silent', reasonCode: 'fixture', targetMessageIds: [] })
    res.setHeader('content-type', 'application/json')
    res.end(
      JSON.stringify(
        protocol === 'openai-chat'
          ? {
              choices: [
                {
                  finish_reason: 'tool_calls',
                  message: {
                    content: null,
                    tool_calls: [
                      {
                        id: 'decision',
                        type: 'function',
                        function: { name: 'participation__decide', arguments: args },
                      },
                    ],
                  },
                },
              ],
            }
          : {
              status: 'completed',
              output: [
                {
                  id: 'item',
                  type: 'function_call',
                  call_id: 'decision',
                  name: 'participation__decide',
                  arguments: args,
                  status: 'completed',
                },
              ],
            },
      ),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string')
  const config: RouterConfig = {
    providers: [
      {
        id: 'p',
        name: 'Fixture',
        protocol,
        baseUrl: `http://127.0.0.1:${address.port}/v1`,
        credentialEnv: '',
        timeoutMs: 10000,
        retryCount: 0,
      },
    ],
    models: [
      {
        id: 'm',
        providerId: 'p',
        displayName: 'Fixture',
        apiModelName: 'gpt-6.1-terra',
        capabilities: ['text', 'tools'],
        ...(mode ? { promptCacheMode: mode } : {}),
      },
    ],
    routes: [{ role: 'planner', orderedModelIds: ['m'], timeoutMs: 10000 }],
  }
  const store = new Store(':memory:')
  store.saveModels(config)
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    proactiveEnabled: true,
    learningEnabled: false,
  })
  const router = new Router(config)
  const runtime = new Runtime(store, () => router)
  runtime.addAdapter('fixture', {
    send: async () => {
      throw new Error('Silent participation must never send')
    },
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    history: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  return {
    runtime,
    store,
    requests,
    started,
    release,
    next: (behavior: typeof next) => {
      next = behavior
    },
    ingest: (id: string) => {
      const now = new Date().toISOString()
      runtime.ingest(
        {
          id,
          sceneId: scene.id,
          senderId: 'user',
          sentAt: now,
          receivedAt: now,
          origin: 'live',
          status: 'available',
          parts: [{ type: 'text', text: `Source body ${id}` }],
        },
        scene,
        false,
      )
    },
    turn: () => runtime.proactive(scene.id),
    close: async () => {
      release.resolve()
      runtime.dispose()
      store.close()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
function view(request: Record<string, unknown>, protocol: Protocol) {
  const wire = request[protocol === 'openai-chat' ? 'messages' : 'input'] as Wire[]
  const history = wire
    .filter((row) => row.role === 'user')
    .map((row) => ({
      marked:
        Array.isArray(row.content) && row.content.some((part) => !!part.prompt_cache_breakpoint),
      data: JSON.parse(
        typeof row.content === 'string'
          ? row.content
          : row.content.map((part) => part.text).join(''),
      ),
    }))
    .filter((row) => Array.isArray(row.data.messages))
  const tail = wire.at(-1)!
  assert.equal(typeof tail.content, 'string')
  return {
    wire,
    history,
    boundaries: history.flatMap((row, index) => (row.marked ? [index] : [])),
    tail: JSON.parse(tail.content as string),
  }
}

for (const protocol of ['openai-chat', 'openai-responses'] as const) {
  test(`${protocol} real participation retains prior/current boundaries and refreshes volatile tail`, async () => {
    const f = await setup(protocol, 'openai')
    try {
      f.ingest('one')
      await f.turn()
      await new Promise((resolve) => setTimeout(resolve, 5))
      f.ingest('two')
      await f.turn()
      assert.equal(f.requests.length, 2)
      const first = view(f.requests[0]!, protocol),
        second = view(f.requests[1]!, protocol)
      assert.deepEqual(first.boundaries, [0])
      assert.deepEqual(second.boundaries, [0, 1])
      assert.deepEqual(second.history[0]!.data, first.history[0]!.data)
      assert.notEqual(second.tail.now, first.tail.now)
      assert.ok(Number.isFinite(Date.parse(second.tail.now)))
      assert.equal(second.tail.messages, undefined)
      assert.deepEqual(f.requests[1]!.prompt_cache_options, { mode: 'implicit' })
      assert.ok(Array.isArray(second.wire[0]!.content))
      assert.equal(JSON.stringify(f.requests).includes('contextCacheBoundaries'), false)
      assert.equal(f.runtime.activeTasks, 0)
    } finally {
      await f.close()
    }
  })
  for (const mode of [undefined, 'off'] as const)
    test(`${protocol} real participation ${mode ?? 'gateway auto'} sends no markers`, async () => {
      const f = await setup(protocol, mode)
      try {
        f.ingest('one')
        await f.turn()
        f.ingest('two')
        await f.turn()
        assert.equal(f.requests.length, 2)
        for (const request of f.requests) {
          assert.equal(request.prompt_cache_options, undefined)
          assert.equal(JSON.stringify(request).includes('prompt_cache_breakpoint'), false)
          assert.equal(JSON.stringify(request).includes('contextCacheBoundaries'), false)
        }
      } finally {
        await f.close()
      }
    })
  for (const outcome of ['fail', 'cancel', 'recall'] as const)
    test(`${protocol} ${outcome} participation cannot promote its pending boundary`, async () => {
      const f = await setup(protocol, 'openai')
      try {
        f.ingest('one')
        await f.turn()
        f.ingest('two')
        f.next(outcome === 'fail' ? 'fail' : 'hold')
        const pending = f.turn()
        if (outcome !== 'fail') {
          await f.started.promise
          if (outcome === 'recall') f.runtime.recall(scene.id, 'two')
          else f.runtime.cancel(scene.id)
          f.release.resolve()
        }
        await pending
        f.ingest('three')
        f.ingest('four')
        await f.turn()
        assert.equal(f.requests.length, 3)
        const final = view(f.requests[2]!, protocol)
        assert.deepEqual(final.boundaries, outcome === 'recall' ? [0, 2] : [0, 3])
        if (outcome === 'recall')
          assert.equal(JSON.stringify(final.history).includes('Source body two'), false)
        assert.equal(f.runtime.activeTasks, 0)
      } finally {
        await f.close()
      }
    })
}
