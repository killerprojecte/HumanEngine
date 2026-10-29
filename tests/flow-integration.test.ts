import { fixtureReplyPlan } from './helpers/reply-plan.ts'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { RuntimeMessage } from '../src/runtime.ts'

test('actual mock group lifecycle includes real Router calls, memory tool, confirmed segments and linked learning writes', async (t) => {
  let thinkerCalls = 0
  const captured: Array<{ model: string; messages: Array<{ role: string; content: string }> }> = []
  const tool = (name: string, args: unknown) => ({
    id: `call-${name}`,
    type: 'function',
    function: { name: name.replaceAll('.', '__'), arguments: JSON.stringify(args) },
  })
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString())
    captured.push(body)
    let calls: unknown[] = []
    if (body.model === 'fixture-planner') {
      const plan = fixtureReplyPlan(body.messages)
      if (!plan)
        calls = [
          tool('participation.decide', { action: 'reply', reasonCode: 'private-decision-canary' }),
        ]
      else if (!body.messages.some((message: { role: string }) => message.role === 'tool'))
        calls = [tool('memory.search', { query: 'private-query-canary' })]
      else calls = [tool('reply.plan', plan)]
    }
    if (body.model === 'fixture-thinker') {
      thinkerCalls++
      assert.equal(thinkerCalls, 1)
      calls = [
        tool('messages.send', {
          operationId: 'mock-send',
          segments: [
            { parts: [{ type: 'text', text: 'private-answer-canary' }] },
            { parts: [{ type: 'text', text: 'private-followup-canary' }] },
          ],
        }),
      ]
    }
    if (body.model === 'fixture-learning')
      calls = [
        tool('learning.propose', {
          content: 'The participant prefers concise technical explanations.',
          kind: 'fact',
          sourceMessageIds: ['source'],
          confidence: 0.9,
        }),
      ]
    res.setHeader('content-type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [
          {
            finish_reason: calls.length ? 'tool_calls' : 'stop',
            message: {
              role: 'assistant',
              content: '',
              ...(calls.length ? { tool_calls: calls } : {}),
            },
          },
        ],
        usage: { prompt_tokens: 64, completion_tokens: 16, total_tokens: 80 },
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(async () => {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  })
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const store = new Store(':memory:')
  store.saveModels(
    {
      providers: [
        {
          id: 'fixture-provider',
          name: 'Fixture',
          protocol: 'openai-chat',
          credentialEnv: '',
          baseUrl: `http://127.0.0.1:${address.port}/v1`,
        },
      ],
      models: ['planner', 'thinker', 'learning'].map((role) => ({
        id: `fixture-${role}`,
        displayName: `Fixture ${role}`,
        providerId: 'fixture-provider',
        apiModelName: `fixture-${role}`,
        capabilities: ['text', 'tools'],
        embeddingBatchSize: 4,
      })),
      routes: (['planner', 'thinker', 'learning'] as const).map((role) => ({
        role,
        orderedModelIds: [`fixture-${role}`],
        timeoutMs: 10000,
      })),
    },
    { 'fixture-provider': { action: 'set', value: 'private-key-canary' } },
  )
  store.set('settings', {
    ...store.settings,
    debounceMs: 0,
    maxWaitMs: 100,
    learningBatchMs: 60000,
  })
  const engine = new Engine(store)
  const scene = {
    id: 'fixture-group',
    kind: 'group' as const,
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'fixture-group',
  }
  store.saveScene({
    ...scene,
    name: 'Fixture group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  store.set('rules', [
    {
      id: 'global-rule',
      scope: 'global',
      content: 'Prefer concise replies.',
      enabled: true,
      nonOverridable: false,
    },
    {
      id: 'scene-rule',
      scope: 'scene',
      sceneId: scene.id,
      content: 'Use metric units.',
      enabled: true,
      nonOverridable: true,
    },
    {
      id: 'excluded-rule',
      scope: 'scene',
      sceneId: 'other-group',
      content: 'EXCLUDED_RULE_CANARY',
      enabled: true,
      nonOverridable: false,
    },
  ])
  let sent = 0
  engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent', messageId: `delivered-${++sent}` }),
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  engine.runtime.installTypingDelay({ speedFactor: 10 }, new AbortController().signal)
  const message: RuntimeMessage = {
    id: 'source',
    sceneId: scene.id,
    senderId: 'fixture-person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [
      { type: 'text', text: 'I prefer concise technical explanations. private-input-canary' },
    ],
  }
  try {
    engine.runtime.ingest(message, scene)
    for (
      let i = 0;
      i < 500 &&
      !store.logs.some(
        (row) =>
          row.stage === 'turn' && row.status !== 'started' && row.status !== 'cancel_requested',
      );
      i++
    )
      await new Promise((resolve) => setTimeout(resolve, 10))
    assert.equal(sent, 2)
    const turnRequests = captured.filter((request) =>
      ['fixture-planner', 'fixture-thinker'].includes(request.model),
    )
    assert.ok(turnRequests.length >= 3)
    for (const request of turnRequests) {
      const system = request.messages
        .filter((message) => message.role === 'system')
        .map((message) => message.content)
        .join('\n')
      assert.match(system, /Prefer concise replies/)
      assert.match(system, /Use metric units/)
      assert.doesNotMatch(system, /EXCLUDED_RULE_CANARY/)
    }
    await (engine.learning as unknown as { learn(id: string): Promise<void> }).learn(scene.id)
    const rows = store.logs.filter((row) => row.module === 'flow')
    const turn = rows.find((row) => row.stage === 'turn' && row.status === 'completed')
    assert.ok(turn)
    for (const stage of [
      'ingress',
      'routing',
      'queue',
      'context_prepare',
      'decision',
      'model_request',
      'model_attempt',
      'model_queue',
      'tool',
      'memory.search',
      'send',
      'send_segment',
      'typing_delay',
      'learning.admission',
      'learning.batch',
      'learning.extract',
      'learning.persist',
    ])
      assert.ok(
        rows.some((row) => row.stage === stage),
        stage,
      )
    const batch = rows.find((row) => row.stage === 'learning.batch' && row.status === 'completed')
    assert.ok(batch)
    assert.notEqual(batch.traceId, turn.traceId)
    assert.ok(batch.linkedTraceIds?.includes(turn.traceId!))
    assert.equal(batch.turnId, undefined)
    assert.ok((batch.counts?.savedCount ?? 0) > 0)
    assert.equal(
      rows.filter((row) => row.stage === 'send_segment' && row.status === 'completed').length,
      2,
    )
    assert.ok(
      rows.some(
        (row) =>
          row.stage === 'model_attempt' &&
          row.status === 'completed' &&
          row.role === 'replyer' &&
          row.counts?.totalTokens === 80,
      ),
    )
    for (const start of rows.filter((row) => row.status === 'started'))
      assert.equal(
        rows.filter(
          (row) =>
            row.spanId === start.spanId &&
            row.status !== 'started' &&
            row.status !== 'cancel_requested',
        ).length,
        1,
        `terminal: ${start.stage}`,
      )
    assert.doesNotMatch(
      JSON.stringify(rows),
      /private-(?:input|answer|followup|query|key|decision)-canary/,
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})
