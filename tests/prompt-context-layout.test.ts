import assert from 'node:assert/strict'
import test from 'node:test'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { Router, type ChatMessage, type RouterConfig } from '../src/models.ts'
import { expandContextMessages, contextLayoutInstruction } from '../src/models/prompt-context.ts'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'

function reconstruct(messages: readonly ChatMessage[]) {
  const value: Record<string, unknown> = {}
  for (const message of messages.filter((message) => message.role === 'user')) {
    const block = JSON.parse(message.content)
    for (const [key, item] of Object.entries(block)) {
      if ((key === 'messages' || key === 'evidence') && Array.isArray(item))
        value[key] = [...((value[key] as unknown[] | undefined) ?? []), ...item]
      else
        Object.defineProperty(value, key, {
          value: item,
          enumerable: true,
          configurable: true,
          writable: true,
        })
    }
  }
  return value
}
const payload = (now: string) => ({
  now,
  timezone: 'UTC',
  sceneKind: 'group',
  botUserId: 'bot',
  messages: [
    { id: 'a', parts: [{ type: 'text', text: 'untrusted "quoted" 中文' }] },
    { id: 'b', text: 'latest' },
  ],
  topics: [{ id: 't', attention: 0.5 }],
  policyContext: { allowed: true },
})

test('explicit context envelopes reconstruct exactly, preserve inputs and append current state last', () => {
  for (const field of ['messages', 'evidence'] as const) {
    for (const empty of [false, true]) {
      const data: Record<string, unknown> = payload('2026-11-04T01:00:00Z')
      if (field === 'evidence') {
        data.evidence = data.messages
        delete data.messages
      }
      if (empty) data[field] = []
      const original: ChatMessage[] = [
        { role: 'system', content: 'Trusted policy' },
        { role: 'user', contextLayout: field, content: JSON.stringify(data) },
      ]
      const before = structuredClone(original)
      const expanded = expandContextMessages(original)
      assert.deepEqual(reconstruct(expanded), data)
      assert.deepEqual(original, before)
      assert.equal(expanded[0]!.content, 'Trusted policy' + contextLayoutInstruction)
      assert.equal(JSON.parse(expanded.at(-1)!.content).now, data.now)
      assert.ok(expanded.every((message) => message.contextLayout === undefined))
      assert.deepEqual(expandContextMessages(expanded), expanded)
      assert.equal(
        new PlannerEvidenceBudget().measure(original),
        Buffer.byteLength(JSON.stringify(expanded)),
      )
    }
  }
})

test('append prefix stays fixed while current timestamp changes; guarded messages and tool continuations keep identity', () => {
  const first = payload('time one')
  const second = payload('time two')
  second.messages.push({ id: 'c', text: 'newest' })
  const expand = (value: unknown) =>
    expandContextMessages([
      { role: 'user', content: JSON.stringify(value), contextLayout: 'messages' },
    ])
  assert.deepEqual(expand(first).slice(0, -1), expand(second).slice(0, -2))
  const calls = [{ id: 'call', name: 'lookup', arguments: { id: 'a' } }]
  const guarded: ChatMessage[] = [
    { role: 'assistant', content: '', toolCalls: calls, contextLayout: 'messages' },
    { role: 'tool', content: '{}', toolCallId: 'call', contextLayout: 'messages' },
    { role: 'user', content: '{}', images: ['image'], contextLayout: 'messages' },
    { role: 'user', content: '{}', toolCalls: calls, contextLayout: 'messages' },
    { role: 'user', content: 'ordinary' },
  ]
  const result = expandContextMessages(guarded)
  guarded.forEach((message, index) => assert.equal(result[index], message))
  assert.equal(result[0]!.toolCalls, calls)
  assert.throws(() => expand([{ messages: [] }]), /invalid_envelope/)
  assert.throws(() => expand({ messages: 'invalid' }), /invalid_history/)
})

for (const protocol of ['openai-chat', 'openai-responses'] as const) {
  test(`${protocol} transports real context message boundaries through a local provider`, async () => {
    const bodies: Array<Record<string, any>> = []
    const server = createServer(async (req, res) => {
      const chunks: Buffer[] = []
      for await (const chunk of req) chunks.push(Buffer.from(chunk))
      bodies.push(JSON.parse(Buffer.concat(chunks).toString()))
      res.setHeader('content-type', 'application/json')
      res.end(
        JSON.stringify(
          protocol === 'openai-chat'
            ? { choices: [{ finish_reason: 'stop', message: { content: 'Done' } }] }
            : {
                status: 'completed',
                output: [
                  {
                    type: 'message',
                    role: 'assistant',
                    content: [{ type: 'output_text', text: 'Done' }],
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
          name: 'Local',
          protocol,
          baseUrl: `http://127.0.0.1:${address.port}/v1`,
          credentialEnv: '',
          retryCount: 0,
          timeoutMs: 10000,
        },
      ],
      models: [
        {
          id: 'm',
          providerId: 'p',
          displayName: 'Local',
          apiModelName: 'local',
          capabilities: ['text', 'tools'],
        },
      ],
      routes: [{ role: 'planner', orderedModelIds: ['m'], timeoutMs: 10000 }],
    }
    try {
      const router = new Router(config)
      for (const now of ['time one', 'time two']) {
        const data = payload(now)
        const messages: ChatMessage[] = [
          { role: 'system', content: 'Policy' },
          { role: 'user', content: JSON.stringify(data), contextLayout: 'messages' },
        ]
        const before = structuredClone(messages)
        await router.complete('planner', messages)
        const sent = bodies.at(-1)![
          protocol === 'openai-chat' ? 'messages' : 'input'
        ] as ChatMessage[]
        assert.equal(sent.length, 5)
        assert.deepEqual(reconstruct(sent), data)
        assert.deepEqual(messages, before)
        assert.ok(sent.every((message) => message.contextLayout === undefined))
      }
      const key = protocol === 'openai-chat' ? 'messages' : 'input'
      assert.deepEqual(bodies[0]![key].slice(0, -1), bodies[1]![key].slice(0, -1))
    } finally {
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  })
}

test('real per-turn projection keeps historical evidence blocks stable when topics grow', async () => {
  const { ContextProjection } = await import('../src/runtime/context-projection.ts')
  const history = Array.from({ length: 3 }, (_, index) => ({
    id: `long-canonical-message-identity-number-${index}`,
    senderId: 'person',
    parts: [{ type: 'text', text: `History ${index}` }],
    sentAt: '2026-11-04T00:00:00.000Z',
    sourceEventRef: {
      eventId: `long-source-event-identity-number-${index}`,
      revision: 1,
      deletionEpoch: 0,
    },
  }))
  const make = (topics: number, append = false) => {
    const recent = append
      ? [
          ...history,
          {
            ...history[0]!,
            id: 'long-canonical-message-identity-number-3',
            sourceEventRef: {
              eventId: 'long-source-event-identity-number-3',
              revision: 1,
              deletionEpoch: 0,
            },
          },
        ]
      : history
    const projection = new ContextProjection('scene')
    projection.project(
      recent.map((message) => ({ evidenceId: `message:${message.id}`, value: message })),
    )
    projection.project(recent)
    projection.project({
      topics: Array.from({ length: topics }, (_, index) => ({
        topicId: `long-dynamic-topic-identity-number-${index}`,
      })),
    })
    return expandContextMessages([
      {
        role: 'user',
        contextLayout: 'evidence',
        content: projection.stringify({
          timezone: 'UTC',
          sceneKind: 'group',
          evidence: recent.map((message) => ({ id: `message:${message.id}`, value: message })),
          now: `2026-11-04T00:00:0${topics}.000Z`,
        }),
      },
    ])
  }
  const first = make(1),
    second = make(2, true)
  assert.deepEqual(second.slice(0, first.length - 1), first.slice(0, -1))
  assert.notEqual(second.at(-1)!.content, first.at(-1)!.content)
})
