import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { SceneRef } from '../src/onebot.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const result = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const decision = (action: 'reply' | 'silent' = 'reply') =>
  result([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'fixture', targetMessageIds: ['source'] },
    },
  ])
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  result([{ id: 'plan', name: 'reply.plan', arguments: fixtureReplyPlan(messages, patch)! }])
const send = (operationId: string) => ({
  id: operationId,
  name: 'messages.send',
  arguments: {
    operationId,
    segments: [{ replyToId: 'source', parts: [{ type: 'text', text: 'A concise response.' }] }],
  },
})
const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
const scene: SceneRef = {
  id: 'planner-runtime',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
function setup(
  complete: (
    role: string,
    messages: ChatMessage[],
    tools: ToolDefinition[],
    signal?: AbortSignal,
  ) => Promise<Completion>,
) {
  const store = new Store(':memory:')
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    proactiveEnabled: true,
    learningEnabled: false,
  })
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  let sends = 0
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: `sent-${sends}` }
    },
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    history: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  const ingest = (id = 'source') => {
    const now = new Date().toISOString()
    const message: RuntimeMessage = {
      id,
      sceneId: scene.id,
      senderId: 'user',
      sentAt: now,
      receivedAt: now,
      origin: 'live',
      status: 'available',
      parts: [
        {
          type: 'text',
          text: id === 'source' ? 'Please respond.' : 'Correction: do not send that answer.',
        },
      ],
    }
    runtime.ingest(message, scene, false)
  }
  ingest()
  return {
    store,
    runtime,
    ingest,
    sends: () => sends,
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

for (const outcome of ['noReply', 'missing', 'invalid'] as const) {
  test(`runtime ${outcome} plan never invokes dialogue or sends`, async () => {
    let dialogueCalls = 0
    const fixture = setup(async (role, messages, tools) => {
      if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
      if (role === 'thinker') {
        dialogueCalls++
        return result([send('must-not-send')])
      }
      if (outcome === 'noReply') return plan(messages, { action: 'noReply' })
      if (outcome === 'missing') return result([])
      return result([
        {
          id: 'bad',
          name: 'reply.plan',
          arguments: { action: 'reply', strategy: 'No evidence or context supplied.' },
        },
      ])
    })
    try {
      await fixture.runtime.proactive(scene.id)
      assert.equal(dialogueCalls, 0)
      assert.equal(fixture.sends(), 0)
      assert.equal(fixture.runtime.activeTasks, 0)
      assert.ok(
        fixture.store.logs.some(
          (log) =>
            log.stage === 'turn' && log.status === (outcome === 'noReply' ? 'skipped' : 'failed'),
        ),
      )
    } finally {
      fixture.close()
    }
  })
}

for (const invalidation of ['cancel', 'recall'] as const) {
  test(`pending planner cannot execute returned tools after ${invalidation}`, async () => {
    const started = deferred(),
      release = deferred()
    let reads = 0,
      dialogueCalls = 0
    const fixture = setup(async (role, _messages, tools) => {
      if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
      if (role === 'thinker') {
        dialogueCalls++
        return result([send('must-not-send')])
      }
      started.resolve()
      await release.promise
      return result([{ id: 'read', name: 'fixture.read', arguments: {} }])
    })
    fixture.runtime.addExtension({
      tools: [
        {
          name: 'fixture.read',
          effect: 'read',
          description: 'Fixture read',
          inputSchema: { type: 'object', properties: {} },
        },
      ],
      execute: async () => {
        reads++
        return { ok: true }
      },
    })
    try {
      const running = fixture.runtime.proactive(scene.id)
      await started.promise
      if (invalidation === 'recall') fixture.runtime.recall(scene.id, 'source')
      else fixture.runtime.cancel(scene.id)
      release.resolve()
      await running
      assert.equal(reads, 0)
      assert.equal(dialogueCalls, 0)
      assert.equal(fixture.sends(), 0)
      assert.equal(fixture.runtime.activeTasks, 0)
    } finally {
      release.resolve()
      fixture.close()
    }
  })
}

test('recall during a pending planner read prevents late evidence reaching dialogue', async () => {
  const started = deferred(),
    release = deferred()
  let researchCalls = 0,
    dialogueCalls = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
    if (role === 'thinker') {
      dialogueCalls++
      return result([send('must-not-send')])
    }
    if (++researchCalls === 1) return result([{ id: 'read', name: 'fixture.read', arguments: {} }])
    return plan(messages)
  })
  fixture.runtime.addExtension({
    tools: [
      {
        name: 'fixture.read',
        effect: 'read',
        description: 'Fixture read',
        inputSchema: { type: 'object', properties: {} },
      },
    ],
    execute: async () => {
      started.resolve()
      await release.promise
      return { secret: 'LATE_EVIDENCE_CANARY' }
    },
  })
  try {
    const running = fixture.runtime.proactive(scene.id)
    await started.promise
    fixture.runtime.recall(scene.id, 'source')
    release.resolve()
    await running
    assert.equal(researchCalls, 1)
    assert.equal(dialogueCalls, 0)
    assert.equal(fixture.sends(), 0)
    assert.doesNotMatch(JSON.stringify(fixture.store.logs), /LATE_EVIDENCE_CANARY/)
  } finally {
    release.resolve()
    fixture.close()
  }
})

test('new correction during planning requires exact-send revalidation and rejects stale output', async () => {
  const started = deferred(),
    release = deferred()
  let decisions = 0,
    validations = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((tool) => tool.name === 'participation.decide'))
      return decision(++decisions === 1 ? 'reply' : 'silent')
    if (tools.some((tool) => tool.name === 'reply.validate')) {
      validations++
      const data = JSON.parse(messages.find((message) => message.role === 'user')!.content)
      assert.ok(data.newMessages.some((message: { id: string }) => message.id === 'correction'))
      assert.deepEqual(data.originalTarget, ['source'])
      return result([{ id: 'validation', name: 'reply.validate', arguments: { valid: false } }])
    }
    if (role === 'thinker') return result([send('stale-send')])
    started.resolve()
    await release.promise
    return plan(messages)
  })
  try {
    const running = fixture.runtime.proactive(scene.id)
    await started.promise
    fixture.ingest('correction')
    release.resolve()
    await running
    await new Promise((resolve) => setTimeout(resolve, 10))
    assert.equal(validations, 1)
    assert.equal(fixture.sends(), 0)
    assert.equal(fixture.runtime.activeTasks, 0)
  } finally {
    release.resolve()
    fixture.close()
  }
})

test('two send calls with different operation IDs in one dialogue completion emit only once', async () => {
  let dialogueCalls = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
    if (role === 'planner') return plan(messages)
    dialogueCalls++
    assert.deepEqual(
      tools.map((tool) => tool.name),
      ['messages.send'],
    )
    return result([send('first'), send('different-operation')])
  })
  try {
    await fixture.runtime.proactive(scene.id)
    assert.equal(dialogueCalls, 1)
    assert.equal(fixture.sends(), 1)
    assert.equal(
      fixture.runtime.preview(scene.id).filter((message) => message.senderId === 'bot').length,
      1,
    )
  } finally {
    fixture.close()
  }
})

for (const stalledRole of ['planner', 'thinker'] as const) {
  test(
    `${stalledRole} stage timeout bounds an uncooperative completion without retry or send`,
    { timeout: 3000 },
    async () => {
      let plannerCalls = 0,
        dialogueCalls = 0
      const fixture = setup(async (role, messages, tools) => {
        if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
        if (role === 'planner') {
          plannerCalls++
          if (stalledRole === 'planner') return new Promise<Completion>(() => {})
          return plan(messages)
        }
        dialogueCalls++
        return new Promise<Completion>(() => {})
      })
      fixture.store.saveModels({
        providers: [],
        models: [],
        routes: [
          {
            role: 'planner',
            orderedModelIds: [],
            timeoutMs: stalledRole === 'planner' ? 100 : 1000,
          },
          {
            role: 'thinker',
            orderedModelIds: [],
            timeoutMs: stalledRole === 'thinker' ? 100 : 1000,
          },
        ],
      })
      // AbortSignal.timeout is unref'ed; preserve the event loop while testing its completion.
      const keepalive = setInterval(() => {}, 100)
      try {
        await fixture.runtime.proactive(scene.id)
        assert.equal(plannerCalls, 1)
        assert.equal(dialogueCalls, stalledRole === 'thinker' ? 1 : 0)
        assert.equal(fixture.sends(), 0)
        assert.equal(fixture.runtime.activeTasks, 0)
      } finally {
        clearInterval(keepalive)
        fixture.close()
      }
    },
  )
}

test('a correction during participation remains stale through planning and is revalidated before send', async () => {
  const started = deferred(),
    release = deferred()
  let checks = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((t) => t.name === 'participation.decide')) {
      started.resolve()
      await release.promise
      return decision()
    }
    if (tools.some((t) => t.name === 'reply.validate')) {
      checks++
      return result([{ id: 'check', name: 'reply.validate', arguments: { valid: false } }])
    }
    if (role === 'planner') return plan(messages)
    return result([send('old-answer')])
  })
  try {
    const running = fixture.runtime.proactive(scene.id)
    await started.promise
    fixture.ingest('correction')
    release.resolve()
    await running
    assert.equal(checks, 1)
    assert.equal(fixture.sends(), 0)
  } finally {
    release.resolve()
    fixture.close()
  }
})

test('planner reads and planned learning retain one context identity and retrieved context reaches dialogue', async () => {
  let readContext: unknown,
    actionContext: unknown,
    selected = false,
    delivered = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((t) => t.name === 'participation.decide')) return decision()
    if (role === 'planner') {
      if (!messages.some((m) => m.role === 'tool'))
        return result([
          { id: 'read', name: 'messages.get', arguments: { ids: ['old'] } },
          { id: 'memory', name: 'fixture.memory', arguments: {} },
        ])
      return plan(messages, {
        targetMessageIds: ['old'],
        contextMessageIds: ['old'],
        actions: [{ toolName: 'fixture.learn', arguments: {} }],
      })
    }
    const data = JSON.parse(messages.find((m) => m.role === 'user')!.content)
    selected = data.messages.some(
      (m: RuntimeMessage) => m.id === 'old' && m.senderId === 'historical-person',
    )
    return result([
      {
        id: 'send',
        name: 'messages.send',
        arguments: {
          operationId: 'one',
          segments: [
            {
              replyToId: 'old',
              parts: [
                { type: 'mention', userId: 'historical-person' },
                { type: 'text', text: 'Acknowledged.' },
              ],
            },
          ],
        },
      },
    ])
  })
  // Historical ID is supplied through a trusted reply edge, not guessed by the model.
  fixture.runtime.ingest(
    {
      id: 'reference',
      sceneId: scene.id,
      senderId: 'user',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      origin: 'live',
      status: 'available',
      replyToId: 'old',
      parts: [{ type: 'text', text: 'Please inspect the earlier reply.' }],
    },
    scene,
    false,
  )
  fixture.runtime.addAdapter('fixture', {
    send: async () => {
      delivered++
      return { status: 'sent', messageId: 'done' }
    },
    getMessage: async () => ({
      items: [
        {
          id: 'old',
          sceneId: scene.id,
          senderId: 'historical-person',
          origin: 'history',
          status: 'available',
          sentAt: new Date().toISOString(),
          receivedAt: new Date().toISOString(),
          parts: [{ type: 'text', text: 'Earlier source.' }],
        },
      ],
      coverage: 'complete',
    }),
    history: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  fixture.runtime.addExtension({
    tools: [
      { name: 'fixture.memory', effect: 'read', description: '', inputSchema: {} },
      { name: 'fixture.learn', effect: 'learningCandidate', description: '', inputSchema: {} },
    ],
    execute: async (name, _input, context) => {
      if (name === 'fixture.memory') readContext = context
      else actionContext = context
      return { ok: true }
    },
  })
  try {
    await fixture.runtime.proactive(scene.id)
    assert.ok(readContext)
    assert.equal(actionContext, readContext)
    assert.equal(selected, true)
    assert.equal(delivered, 1)
  } finally {
    fixture.close()
  }
})

test('uncooperative pre-send hook is bounded and diagnosed as dialogue timeout without emission', async () => {
  const release = deferred()
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((t) => t.name === 'participation.decide')) return decision()
    if (role === 'planner') return plan(messages)
    return result([send('one')])
  })
  fixture.store.set('models', {
    providers: [],
    models: [],
    routes: [{ role: 'thinker', orderedModelIds: [], timeoutMs: 100 }],
  })
  fixture.runtime.beforeSend({
    id: 'slow',
    priority: 0,
    handler: async (segments) => {
      await release.promise
      return { action: 'continue', segments }
    },
  })
  try {
    await fixture.runtime.proactive(scene.id)
    assert.equal(fixture.sends(), 0)
    assert.equal(fixture.runtime.activeTasks, 0)
    const terminal = fixture.store.logs.find(
      (log) => log.stage === 'turn' && log.status === 'failed',
    )
    assert.ok(terminal)
    assert.match(JSON.stringify(terminal), /dialogue_timeout/)
  } finally {
    release.resolve()
    fixture.close()
  }
})
