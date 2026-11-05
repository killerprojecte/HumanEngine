import { sanitizeError } from '../src/diagnostics/errors.ts'
import { nativeStreamDispatcher } from '../src/models/stream-dispatcher.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer, type ServerResponse } from 'node:http'
import { once } from 'node:events'
import { setTimeout as delay } from 'node:timers/promises'
import { ModelError, Router, type RouterConfig } from '../src/models.ts'
import { modelScheduler } from '../src/scheduler.ts'
import type { ModelCallObservation } from '../src/model-calls.ts'
import { progressDeadline } from '../src/models/stream-progress.ts'

const frame = (value: unknown) => `data: ${JSON.stringify(value)}\n\n`
const text = (value: string) =>
  frame({ choices: [{ index: 0, delta: { content: value }, finish_reason: null }] })
const terminal =
  frame({ choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }) +
  frame({ choices: [], usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 } }) +
  'data: [DONE]\n\n'
async function fixture(
  handler: (res: ServerResponse, body: Record<string, unknown>) => Promise<void> | void,
) {
  await nativeStreamDispatcher() // Keep fixture deadlines about transport, not first-time module loading.
  const requests: Record<string, unknown>[] = []
  const server = createServer(async (req, res) => {
    let data = ''
    for await (const chunk of req) data += chunk
    const body = JSON.parse(data)
    requests.push(body)
    res.setHeader('Content-Type', 'text/event-stream')
    try {
      await handler(res, body)
    } catch {
      res.destroy()
    }
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const config: RouterConfig = {
    providers: [
      {
        id: 'p',
        name: 'P',
        protocol: 'openai-chat',
        baseUrl: `http://127.0.0.1:${address.port}/v1`,
        credentialEnv: '',
        timeoutMs: 100,
        retryCount: 0,
      },
    ],
    models: [
      {
        id: 'm',
        displayName: 'M',
        providerId: 'p',
        apiModelName: 'gpt-fixture',
        capabilities: ['text', 'tools'],
      },
    ],
    routes: [{ role: 'planner', orderedModelIds: ['m'], timeoutMs: 100 }],
  }
  const calls: ModelCallObservation[] = []
  return {
    config,
    requests,
    calls,
    router: () =>
      new Router(
        config,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        (e) => calls.push(e),
      ),
    close: async () => {
      server.closeAllConnections()
      await new Promise<void>((r) => server.close(() => r()))
    },
  }
}
test('default stream survives provider, role and managed outer deadlines after first meaningful content', async () => {
  const f = await fixture(async (res) => {
    res.write(text('hello'))
    await delay(180)
    res.end(terminal)
  })
  const parent = new AbortController(),
    outer = progressDeadline(parent.signal, 100, () => new Error('outer_timeout'))
  try {
    const result = await f
      .router()
      .complete('planner', [{ role: 'user', content: 'fixture' }], [], outer.signal)
    assert.equal(result.text, 'hello')
    assert.equal(result.usage.inputTokens, 10)
    assert.equal(f.requests[0]!.stream, true)
    assert.deepEqual(f.requests[0]!.stream_options, { include_usage: true })
    assert.equal(f.calls[0]!.transportMode, 'stream')
    assert.equal(f.calls[0]!.firstTokenKind, 'text')
    assert.ok(f.calls[0]!.firstTokenMs !== null && f.calls[0]!.firstTokenMs! < 100)
    assert.ok(f.calls[0]!.durationMs >= 180)
    assert.equal(outer.signal.aborted, false)
  } finally {
    outer.dispose()
    parent.abort()
    await f.close()
  }
})
test('role-only chunks and heartbeat do not disable first-content timeout', async () => {
  const f = await fixture(async (res) => {
    res.write(
      ': heartbeat\n\n' +
        frame({ choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }),
    )
    await delay(160)
    res.end(text('late') + terminal)
  })
  try {
    await assert.rejects(f.router().complete('planner', [{ role: 'user', content: 'fixture' }]))
    await delay(10)
    assert.equal(f.calls[0]!.firstTokenMs, null)
  } finally {
    await f.close()
  }
})
test('partial streaming interruption is not replayed by retry or model fallback', async () => {
  const f = await fixture((res) => {
    res.end(text('partial'))
  })
  f.config.providers[0]!.retryCount = 2
  f.config.models.push({ ...f.config.models[0]!, id: 'fallback' })
  f.config.routes[0]!.orderedModelIds.push('fallback')
  try {
    await assert.rejects(f.router().complete('planner', [{ role: 'user', content: 'fixture' }]), {
      code: 'provider_stream_interrupted',
    })
    assert.equal(f.requests.length, 1)
    assert.equal(f.calls.length, 1)
    assert.equal(f.calls[0]!.event, 'model.failed')
    assert.equal(f.calls[0]!.firstTokenKind, 'text')
  } finally {
    await f.close()
  }
})
test('caller cancellation still stops a stream after first content and releases scheduler resources', async () => {
  const controller = new AbortController()
  const f = await fixture(async (res) => {
    res.write(text('started'))
    setTimeout(() => controller.abort(), 25)
    await delay(100)
    res.end(terminal)
  })
  try {
    await assert.rejects(
      f.router().complete('planner', [{ role: 'user', content: 'fixture' }], [], controller.signal),
    )
    assert.equal(f.calls[0]!.event, 'model.failed')
    assert.equal(modelScheduler.status.running, 0)
  } finally {
    await f.close()
  }
})
test('explicit synchronous mode and compatible JSON fallback keep first token unknown', async () => {
  const f = await fixture((res) => {
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [{ finish_reason: 'stop', message: { content: 'ok' } }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      }),
    )
  })
  try {
    await f.router().complete('planner', [{ role: 'user', content: 'fixture' }])
    assert.equal(f.requests[0]!.stream, true)
    assert.equal(f.calls[0]!.transportMode, 'non_stream')
    assert.equal(f.calls[0]!.firstTokenMs, null)
    f.config.models[0]!.streaming = false
    await f.router().complete('planner', [{ role: 'user', content: 'fixture' }])
    assert.equal(f.requests[1]!.stream, false)
    assert.equal(f.requests[1]!.stream_options, undefined)
  } finally {
    await f.close()
  }
})

test('Responses streaming preserves tool IDs and opaque continuation across a tool round', async () => {
  let round = 0
  const opaque = { type: 'reasoning', id: 'r1', summary: [], encrypted_content: 'opaque-fixture' }
  const tool = {
    type: 'function_call',
    id: 'item1',
    call_id: 'call1',
    name: 'probe__echo',
    arguments: '{"value":"ok"}',
    status: 'completed',
  }
  const f = await fixture((res) => {
    if (++round === 1) {
      res.end(
        frame({ type: 'response.output_item.added', output_index: 0, item: opaque }) +
          frame({
            type: 'response.output_item.added',
            output_index: 1,
            item: { ...tool, arguments: '', status: 'in_progress' },
          }) +
          frame({
            type: 'response.function_call_arguments.delta',
            output_index: 1,
            item_id: 'item1',
            delta: '{"value":',
          }) +
          frame({
            type: 'response.function_call_arguments.delta',
            output_index: 1,
            item_id: 'item1',
            delta: '"ok"}',
          }) +
          frame({
            type: 'response.completed',
            response: {
              id: 'response1',
              status: 'completed',
              output: [opaque, tool],
              usage: { input_tokens: 10, output_tokens: 2, total_tokens: 12 },
            },
          }),
      )
    } else {
      res.end(
        frame({
          type: 'response.completed',
          response: {
            id: 'response2',
            status: 'completed',
            output: [
              {
                type: 'message',
                role: 'assistant',
                content: [{ type: 'output_text', text: 'done' }],
              },
            ],
            usage: { input_tokens: 12, output_tokens: 2, total_tokens: 14 },
          },
        }),
      )
    }
  })
  f.config.providers[0]!.protocol = 'openai-responses'
  try {
    const router = f.router(),
      messages = [{ role: 'user' as const, content: 'fixture' }]
    const first = await router.complete('planner', messages, [
      {
        name: 'probe.echo',
        description: 'fixture',
        inputSchema: {
          type: 'object',
          properties: { value: { type: 'string' } },
          required: ['value'],
          additionalProperties: false,
        },
      },
    ])
    assert.equal(first.toolCalls[0]!.id, 'call1')
    assert.equal(first.toolCalls[0]!.name, 'probe.echo')
    assert.equal(f.calls[0]!.firstTokenKind, 'tool')
    assert.equal(f.requests[0]!.stream_options, undefined)
    const second = await router.complete('planner', [
      ...messages,
      first.assistant,
      { role: 'tool', content: '{"ok":true}', toolCallId: 'call1' },
    ])
    assert.equal(second.text, 'done')
    const input = f.requests[1]!.input as Array<Record<string, unknown>>
    assert.ok(
      input.some(
        (item) => item.type === 'reasoning' && item.encrypted_content === 'opaque-fixture',
      ),
    )
    assert.ok(
      input.some((item) => item.type === 'function_call_output' && item.call_id === 'call1'),
    )
  } finally {
    await f.close()
  }
})

for (const protocol of ['openai-chat', 'openai-responses'] as const)
  test(`${protocol} Router preserves safe upstream stream diagnostics without retry or fallback`, async () => {
    const secret = 'PRIVATE_UPSTREAM_ERROR_BODY_AND_KEY'
    const f = await fixture((res) => {
      res.end(
        frame(
          protocol === 'openai-chat'
            ? { error: { message: secret } }
            : { type: 'response.failed', response: { error: { message: secret } } },
        ),
      )
    })
    f.config.providers[0]!.protocol = protocol
    f.config.providers[0]!.retryCount = 2
    f.config.providers[0]!.timeoutMs = 2000
    f.config.routes[0]!.timeoutMs = 2000
    f.config.models.push({ ...f.config.models[0]!, id: 'fallback' })
    f.config.routes[0]!.orderedModelIds.push('fallback')
    try {
      await assert.rejects(
        f.router().complete('planner', [{ role: 'user', content: 'synthetic' }]),
        (error) => {
          assert.ok(error instanceof ModelError)
          const diagnostic = sanitizeError(error)
          assert.equal(JSON.stringify(diagnostic).includes('provider_stream_upstream_error'), true)
          assert.equal(JSON.stringify(diagnostic).includes(secret), false)
          assert.equal(error.retryable, false)
          return true
        },
      )
      assert.equal(f.requests.length, 1)
      assert.equal(f.calls[0]!.firstTokenMs, null)
      assert.equal(modelScheduler.status.running, 0)
    } finally {
      await f.close()
    }
  })
