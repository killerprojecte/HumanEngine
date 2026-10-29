import test from 'node:test'
import assert from 'node:assert/strict'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import { buildReplyPlan } from '../src/runtime/reply-plan.ts'
import type { Completion } from '../src/models.ts'
const enc = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
const scene = `ob:s:${enc(['qq', '999', 'group', '123'])}`
const mid = (id: string) => `${scene}:m:${enc(id)}`
const uid = (account: string, user = '456') => `ob:u:${enc(['qq', account, user])}`
const locator = `ob:media:${enc({ url: 'https://media.example/URL_CANARY?secret=SIGNED_SECRET' })}`
const body = '用户原文 [image/unknown] Untrusted image: ob:u:literal 不应该删除'
const message = {
  id: mid('-12345'),
  sceneId: scene,
  senderId: uid('999'),
  senderDisplayName: '昵称',
  senderPlatformId: '456',
  senderAvatarUrl: 'https://avatar.example/AVATAR_CANARY',
  sentAt: '2026-10-28T03:00:00.000Z',
  receivedAt: '2026-10-28T03:00:02.000Z',
  parts: [
    { type: 'text', text: body },
    { type: 'media', assetId: locator, mediaType: 'image' },
    { type: 'mention', userId: 'bot' },
  ],
  status: 'available',
  origin: 'live',
}
test('compact structural projection keeps exact text and full time; trusted maps round trip and reject forged refs', () => {
  const refs = new ContextProjection(scene)
  const outbound = refs.project({
    now: '2026-10-28T04:00:00.000Z',
    timezone: 'Asia/Shanghai',
    messages: [message],
  })
  assert.equal(outbound.messages[0]!.id, '-12345')
  assert.equal(outbound.messages[0]!.senderId, '456')
  assert.equal(outbound.messages[0]!.sentAt, message.sentAt)
  assert.deepEqual(outbound.messages[0]!.parts[0], message.parts[0])
  const serialized = JSON.stringify(outbound)
  // Genuine user text may itself contain an opaque-looking literal; never rewrite it.
  const metadataOnly = serialized.replace(body, '')
  assert.doesNotMatch(
    metadataOnly,
    /ob:u:|ob:s:|ob:media:|URL_CANARY|SIGNED_SECRET|AVATAR_CANARY|receivedAt|senderAvatar/,
  )
  const args = {
    replyToId: '-12345',
    userId: '456',
    assetId: 'a1',
    sourceMessageId: '-12345',
    sourceMessageIds: ['-12345'],
    text: body,
  }
  assert.deepEqual(refs.restoreArguments(args), {
    ...args,
    replyToId: message.id,
    userId: message.senderId,
    assetId: locator,
    sourceMessageId: message.id,
    sourceMessageIds: [message.id],
  })
  assert.throws(() => refs.restoreArguments({ replyToId: 'unknown' }), /context_reference_unknown/)
  assert.throws(() => refs.restoreArguments({ replyToId: message.id }), /context_reference_unknown/)
  assert.throws(
    () => refs.project({ ...message, sceneId: 'other' }),
    /context_reference_cross_scene/,
  )
  const otherAccount = refs.project({ userId: uid('another') })
  assert.notEqual(otherAccount.userId, '456')
  assert.equal(refs.restoreArguments(otherAccount).userId, uid('another'))
  assert.equal(refs.project({ userId: message.senderId }).userId, '456')
  const before = JSON.stringify({
    now: outbound.now,
    timezone: outbound.timezone,
    messages: [message],
  }).length
  console.log(
    `compact fixture actual JSON characters: ${before} -> ${serialized.length}; token count not measured`,
  )
  assert.ok(serialized.length < before)
})
const completion = (name: string, args: Record<string, unknown>): Completion => {
  const calls = [{ id: 'call', name, arguments: args }]
  return {
    text: '',
    modelId: 'mock',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    toolCalls: calls,
    assistant: { role: 'assistant', content: '', toolCalls: calls },
  }
}
test('actual planner outbound mocks see compact receipts; historical targets, evidence, assets and actions restore before validation', async () => {
  const projection = new ContextProjection(scene)
  const evidence = [
    {
      id: `message:${message.id}`,
      value: { ...message, parts: [{ type: 'text', text: 'Original body intact.' }] },
    },
  ]
  const outgoing: string[] = []
  let round = 0
  const history = {
    ...message,
    id: mid('-99'),
    parts: [{ type: 'text', text: 'Historical body intact.' }],
  }
  const canonicalAsset = '1234567890abcdef'.repeat(4)
  const built = await buildReplyPlan({
    projection,
    messages: [{ role: 'user', content: projection.stringify({ evidence }) }],
    evidence,
    targetMessageIds: [message.id],
    contextMessageIds: [message.id],
    readTools: ['messages.get', 'memes.search'].map((name) => ({
      name,
      description: '',
      inputSchema: {},
    })),
    actionTools: [{ name: 'memes.feedback', description: '', inputSchema: {} }],
    signal: new AbortController().signal,
    eligible: () => true,
    execute: async (name, args) => {
      if (name === 'messages.get') {
        assert.deepEqual(args, { ids: [message.id] })
        return { items: [history], coverage: 'partial' }
      }
      return { items: [{ id: canonicalAsset, tags: { emotion: ['happy'] } }], coverage: 'partial' }
    },
    complete: async (messages) => {
      outgoing.push(JSON.stringify(messages))
      if (round++ === 0) return completion('messages.get', { ids: ['-12345'] })
      if (round === 2) return completion('memes.search', { query: 'happy' })
      const receipts = messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
      const asset = receipts[1].value.items[0].id
      return completion('reply.plan', {
        action: 'reply',
        deliveryTool: 'messages.send',
        targetMessageIds: ['-99'],
        contextMessageIds: ['-12345', '-99'],
        strategy: 'Answer',
        constraints: [],
        facts: [{ text: 'Historical body intact.', evidenceIds: [receipts[0].id] }],
        assets: [{ assetId: asset, evidenceId: receipts[1].id }],
        emoji: [],
        expressionGoals: [],
        actions: [
          {
            toolName: 'memes.feedback',
            arguments: { assetId: asset, sourceMessageIds: ['-99'], positive: true },
          },
        ],
      })
    },
  })
  for (const wire of outgoing)
    assert.doesNotMatch(wire, /ob:u:|ob:s:|ob:media:|URL_CANARY|AVATAR_CANARY|receivedAt/)
  assert.ok(outgoing.at(-1)!.includes('Original body intact.'))
  assert.ok(outgoing.at(-1)!.includes('Historical body intact.'))
  assert.deepEqual(built.plan.targetMessageIds, [history.id])
  assert.equal(built.plan.assets[0]!.assetId, canonicalAsset)
  assert.deepEqual(built.plan.actions[0]!.arguments.sourceMessageIds, [history.id])
  assert.equal(built.evidence[1]!.id, 'tool:1')
})

test('runtime decision, planner, attributed vision context and composer use the same refs; hooks and adapter receive canonical IDs once', async (t) => {
  // This test checks preserved fixed timestamps, not expiry against the runner's wall clock.
  t.mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T04:00:00.000Z') })
  const { Store } = await import('../src/store.ts')
  const { Runtime } = await import('../src/runtime.ts')
  const { fixtureReplyPlan } = await import('./helpers/reply-plan.ts')
  const store = new Store(':memory:')
  const scope = {
    id: scene,
    kind: 'group' as const,
    adapterId: 'qq',
    accountId: '999',
    platformSceneId: '123',
  }
  store.saveScene({
    ...scope,
    name: 'Fixture',
    participationEnabled: true,
    proactiveEnabled: true,
    learningEnabled: false,
  })
  const calls: string[] = []
  let sends = 0,
    hooks = 0
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (
          role: string,
          messages: import('../src/models.ts').ChatMessage[],
          tools: import('../src/models.ts').ToolDefinition[],
        ) => {
          const wire = JSON.stringify(messages)
          assert.doesNotMatch(wire, /ob:u:|ob:s:|ob:media:|URL_CANARY|AVATAR_CANARY|receivedAt/)
          const data = JSON.parse(messages.find((m) => m.role === 'user')!.content)
          assert.ok(data.now)
          assert.equal(data.timezone, store.settings.timezone)
          calls.push(tools[0]!.name)
          if (tools.some((t) => t.name === 'participation.decide')) {
            assert.equal(data.messages[0].id, '-12345')
            assert.equal(data.messages[0].sentAt, message.sentAt)
            assert.equal(data.imageContext[0].images[0].messageId, '-12345')
            return completion('participation.decide', {
              action: 'reply',
              reasonCode: 'fixture',
              targetMessageIds: ['-12345'],
            })
          }
          if (role === 'planner')
            return completion(
              'reply.plan',
              fixtureReplyPlan(messages)! as unknown as Record<string, unknown>,
            )
          assert.equal(data.messages[0].sentAt, message.sentAt)
          assert.equal(data.messages[0].senderId, '456')
          return completion('messages.send', {
            operationId: 'once',
            segments: [
              {
                replyToId: '-12345',
                parts: [
                  { type: 'mention', userId: '456' },
                  { type: 'text', text: '收到。' },
                ],
              },
            ],
          })
        },
      }) as import('../src/models.ts').Router,
  )
  runtime.addAdapter('qq', {
    send: async (_scene, segment) => {
      sends++
      assert.equal(segment.replyToId, message.id)
      assert.deepEqual(segment.parts[0], { type: 'mention', userId: message.senderId })
      return { status: 'sent', messageId: mid('100') }
    },
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    history: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  runtime.addExtension({
    tools: [],
    contextData: () => ({
      learnedHints: [
        {
          id: 'hint-with-a-long-identifier-012345678901234567890',
          sources: [{ sceneId: scene, messageId: message.id, senderId: message.senderId }],
        },
      ],
    }),
    describeImages: async () => ({
      images: [
        { messageId: message.id, description: 'A fixture illustration.', status: 'described' },
      ],
    }),
    execute: async () => ({}),
  })
  runtime.beforeSend({
    id: 'canonical-check',
    priority: 1,
    handler: async (segments) => {
      hooks++
      assert.equal(segments[0]!.replyToId, message.id)
      return { action: 'continue', segments }
    },
  })
  try {
    runtime.ingest(
      {
        ...message,
        parts: [
          { type: 'text', text: 'Actual body intact.' },
          { type: 'media', assetId: locator, mediaType: 'image' },
        ],
        status: 'available',
        origin: 'live',
      },
      scope,
      false,
    )
    await runtime.proactive(scene)
    assert.equal(sends, 1)
    assert.equal(hooks, 1)
    assert.equal(calls.length, 3)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('background extraction wire stays compact and restores source/person provenance before persistence', async () => {
  const fixture = await import('./helpers/learning-technical-fixture.ts')
  const h = fixture.harness()
  const source = {
    ...fixture.message(601, 'My preferred serializer is CBOR.'),
    id: 'a-canonical-message-reference-that-is-longer-than-thirty-two-characters',
    senderId: uid('999'),
    senderAvatarUrl: 'https://avatar.example/AVATAR_CANARY',
  }
  let extracted = false
  h.install(async (_role, messages, tools) => {
    const wire = JSON.stringify(messages)
    assert.doesNotMatch(wire, /ob:u:|ob:s:|ob:media:|AVATAR_CANARY|a-canonical-message/)
    const payload = JSON.parse(messages[1]!.content)
    // Guidance may name server-owned fields; their values must not enter model payloads.
    assert.doesNotMatch(JSON.stringify(payload), /"receivedAt"\s*:/)
    assert.ok(payload.now)
    assert.equal(payload.timezone, h.store.settings.timezone)
    if (tools?.some((t) => t.name === 'learning.propose')) {
      extracted = true
      assert.equal(payload.messages[0].sentAt, source.sentAt)
      assert.equal(
        payload.messages[0].parts[0].text,
        source.parts[0]!.type === 'text' ? source.parts[0].text : '',
      )
      return completion('learning.propose', {
        kind: 'fact',
        content: 'The participant prefers the CBOR serializer.',
        sourceMessageIds: [payload.messages[0].id],
        personId: payload.messages[0].senderId,
      })
    }
    return fixture.completed()
  })
  try {
    await h.run([source])
    assert.equal(extracted, true)
    const saved = h.engine.memory.adminList()[0]!
    assert.ok(saved)
    assert.deepEqual(saved.sources, [
      { sceneId: source.sceneId, messageId: source.id, senderId: source.senderId },
    ])
    assert.equal(saved.scope.kind === 'scene' && saved.scope.personId, source.senderId)
  } finally {
    await h.close()
  }
})
