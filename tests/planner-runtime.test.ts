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
      if (role === 'replyer') {
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
      if (role === 'replyer') {
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
    if (role === 'replyer') {
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
    if (role === 'replyer') return result([send('stale-send')])
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
    const payload = JSON.parse(messages.find((message) => message.role === 'user')!.content)
    const keys = Object.keys(payload)
    assert.equal(typeof payload.now, 'string')
    assert.ok(Number.isFinite(Date.parse(payload.now)))
    if (tools.some((tool) => tool.name === 'participation.decide')) {
      assert.ok(keys.indexOf('messages') < keys.indexOf('now'))
      assert.ok(keys.indexOf('messages') < keys.indexOf('topics'))
    } else if (role === 'planner') {
      assert.ok(keys.indexOf('evidence') < keys.indexOf('decision'))
      assert.ok(keys.indexOf('evidence') < keys.indexOf('now'))
    } else {
      assert.equal(keys.at(-1), 'now')
      assert.ok(payload.messages.some((message: RuntimeMessage) => message.id === 'source'))
    }
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

for (const stalledRole of ['planner', 'replyer'] as const) {
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
            timeoutMs: stalledRole === 'replyer' ? 100 : 1000,
          },
        ],
      })
      // AbortSignal.timeout is unref'ed; preserve the event loop while testing its completion.
      const keepalive = setInterval(() => {}, 100)
      try {
        await fixture.runtime.proactive(scene.id)
        assert.equal(plannerCalls, 1)
        assert.equal(dialogueCalls, stalledRole === 'replyer' ? 1 : 0)
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

for (const enabled of [false, true]) {
  test(`actual Planner→Replyer optional suggestion switch ${enabled ? 'ON' : 'OFF'} keeps expression independent`, async () => {
    let composed = 0
    const fixture = setup(async (role, messages, tools) => {
      if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
      if (role === 'planner') {
        const schema = tools.find((tool) => tool.name === 'reply.plan')!.inputSchema as {
          properties: Record<string, unknown>
        }
        assert.equal('replySuggestion' in schema.properties, enabled)
        assert.doesNotMatch(messages[0]!.content, /Plan the exact response/)
        return plan(messages, enabled ? { replySuggestion: 'OPTIONAL_WORDING_CANARY' } : {})
      }
      assert.equal(role, 'replyer')
      composed++
      const input = JSON.parse(messages.find((message) => message.role === 'user')!.content)
      assert.equal(input.plan.replySuggestion, enabled ? 'OPTIONAL_WORDING_CANARY' : undefined)
      assert.equal(input.plan.actions, undefined, 'raw action arguments are not language input')
      assert.equal(input.plan.draftGoals, undefined)
      assert.match(messages[0]!.content, /Independently phrase/)
      assert.equal(tools.length, 1)
      return result([send('independently-worded')])
    })
    fixture.store.set('settings', { ...fixture.store.settings, replySuggestionsEnabled: enabled })
    try {
      await fixture.runtime.proactive(scene.id)
      assert.equal(composed, 1)
      assert.equal(fixture.sends(), 1)
    } finally {
      fixture.close()
    }
  })
}

test('disabled suggestions reject a planner-supplied draft before any Replyer invocation or send', async () => {
  let composed = 0
  const fixture = setup(async (role, messages, tools) => {
    if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
    if (role === 'planner') return plan(messages, { replySuggestion: 'FORBIDDEN_DRAFT' })
    composed++
    return result([send('must-not-send')])
  })
  try {
    await fixture.runtime.proactive(scene.id)
    assert.equal(composed, 0)
    assert.equal(fixture.sends(), 0)
    assert.ok(
      fixture.store.logs.some((log) => JSON.stringify(log).includes('planner_invalid_plan')),
    )
  } finally {
    fixture.close()
  }
})

for (const changeDuringReply of [false, true]) {
  test(
    `completed Replyer send call ${changeDuringReply ? 'waits at revalidation but terminates at remaining dialogue deadline' : 'reaches adapter without another model call when topic unchanged'}`,
    { timeout: 3000 },
    async () => {
      const validationStarted = deferred()
      let replyCompletions = 0,
        validations = 0
      const fixture = setup(async (role, messages, tools) => {
        if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
        if (tools.some((tool) => tool.name === 'reply.validate')) {
          validations++
          validationStarted.resolve()
          return new Promise<Completion>(() => {})
        }
        if (role === 'planner') return plan(messages)
        if (messages.some((message) => message.role === 'tool')) return result([])
        replyCompletions++
        if (changeDuringReply) fixture.ingest('correction-after-reply')
        return result([send('completed-reply')])
      })
      fixture.store.saveModels({
        providers: [],
        models: [],
        routes: [
          { role: 'planner', orderedModelIds: [], timeoutMs: 1000 },
          { role: 'replyer', orderedModelIds: [], timeoutMs: 100 },
        ],
      })
      const keepalive = setInterval(() => {}, 100)
      try {
        const running = fixture.runtime.proactive(scene.id)
        if (changeDuringReply) {
          await validationStarted.promise
          assert.equal(replyCompletions, 1)
          assert.equal(fixture.sends(), 0)
          const validation = fixture.store.logs.filter((row) => row.stage === 'send_validation')
          assert.deepEqual(
            validation.map((row) => row.status),
            ['started'],
          )
          assert.equal(fixture.store.logs.at(-1)?.stage, 'send_validation')
          assert.equal(fixture.store.logs.at(-1)?.nextStep, 'adapter_send')
          // This reproduces the reported visible snapshot after Replyer has returned.
          // It must NOT remain the final state once the existing stage deadline expires.
        }
        await running
        assert.equal(replyCompletions, 1)
        assert.equal(validations, changeDuringReply ? 1 : 0)
        assert.equal(fixture.sends(), changeDuringReply ? 0 : 1)
        assert.equal(fixture.runtime.activeTasks, 0)
        if (changeDuringReply) {
          assert(
            fixture.store.logs.some(
              (row) => row.stage === 'send_validation' && row.status === 'cancelled',
            ),
          )
          assert(
            fixture.store.logs.some(
              (row) => row.stage === 'turn_cleanup' && row.status === 'completed',
            ),
          )
          assert(!fixture.store.logs.some((row) => row.stage === 'adapter_send'))
        }
      } finally {
        clearInterval(keepalive)
        fixture.close()
      }
    },
  )
}

test(
  'real Router revalidation keeps send trace and deadline through protocol scheduling after completed Replyer',
  { timeout: 3000 },
  async () => {
    const { Router: RealRouter } = await import('../src/models.ts')
    const { ModelProtocolRegistry } = await import('../src/model-protocols.ts')
    const registry = new ModelProtocolRegistry()
    const validationStarted = deferred()
    let router!: InstanceType<typeof RealRouter>
    let replies = 0
    const fixture = setup((role, messages, tools, signal) =>
      router.complete(role as 'planner' | 'replyer', messages, tools, signal),
    )
    registry.register(
      'fixture:sendcheck',
      'Send validation fixture',
      {
        complete: async (_context, messages, tools) => {
          if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
          if (tools.some((tool) => tool.name === 'reply.plan')) return plan(messages)
          if (tools.some((tool) => tool.name === 'reply.validate')) {
            validationStarted.resolve()
            return new Promise<Completion>(() => {})
          }
          replies++
          fixture.ingest('correction-after-real-router-reply')
          return result([send('real-router-reply')])
        },
      },
      new AbortController().signal,
    )
    const config = {
      providers: [
        {
          id: 'p',
          name: 'fixture',
          protocol: 'fixture:sendcheck',
          credentialEnv: '',
          baseUrl: 'https://example.invalid',
          timeoutMs: 1000,
          retryCount: 0,
        },
      ],
      models: [
        {
          id: 'm',
          displayName: 'fixture',
          providerId: 'p',
          apiModelName: 'fixture',
          capabilities: ['text' as const, 'tools' as const],
        },
      ],
      routes: [
        { role: 'planner' as const, orderedModelIds: ['m'], timeoutMs: 1000 },
        { role: 'replyer' as const, orderedModelIds: ['m'], timeoutMs: 150 },
      ],
    }
    fixture.store.saveModels(config)
    router = new RealRouter(config, undefined, undefined, registry, undefined, fixture.store)
    const keepalive = setInterval(() => {}, 100)
    try {
      const running = fixture.runtime.proactive(scene.id)
      await validationStarted.promise
      assert.equal(replies, 1)
      assert.equal(fixture.sends(), 0)
      const validation = fixture.store.logs.findLast(
        (row) => row.stage === 'send_validation' && row.status === 'started',
      )!
      assert(validation)
      const following = fixture.store.logs.slice(fixture.store.logs.indexOf(validation) + 1)
      for (const stage of ['model_request', 'model_attempt', 'model_queue']) {
        const row = following.find((row) => row.stage === stage && row.status === 'started')
        assert(row, `missing ${stage}`)
        assert.equal(row.traceId, validation.traceId)
        assert.equal(row.turnId, validation.turnId)
      }
      await running
      assert.equal(fixture.runtime.activeTasks, 0)
      assert.equal(fixture.sends(), 0)
      assert(
        fixture.store.logs.some(
          (row) =>
            row.stage === 'turn_cleanup' &&
            row.status === 'completed' &&
            row.traceId === validation.traceId,
        ),
      )
    } finally {
      clearInterval(keepalive)
      fixture.close()
    }
  },
)

test('live participation window grows across turns then batch rolls, with fresh time and no recalled body', async () => {
  const seen: Array<{ messages: RuntimeMessage[]; now: string }> = []
  const fixture = setup(async (_role, messages, tools) => {
    assert.ok(tools.some((tool) => tool.name === 'participation.decide'))
    seen.push(JSON.parse(messages.find((message) => message.role === 'user')!.content))
    return decision('silent')
  })
  try {
    for (let i = 1; i < 30; i++) fixture.ingest(`m${i}`)
    await fixture.runtime.proactive(scene.id)
    for (let i = 30; i < 40; i++) fixture.ingest(`m${i}`)
    await fixture.runtime.proactive(scene.id)
    for (let i = 40; i < 55; i++) fixture.ingest(`m${i}`)
    await fixture.runtime.proactive(scene.id)
    assert.equal(seen[0]!.messages.length, 24)
    assert.equal(seen[1]!.messages.length, 34)
    assert.deepEqual(seen[1]!.messages.slice(0, 24), seen[0]!.messages)
    assert.equal(seen[2]!.messages.length, 24)
    fixture.runtime.recall(scene.id, 'm54')
    await fixture.runtime.proactive(scene.id)
    assert.ok(seen.at(-1)!.messages.every((message) => message.id !== 'm54'))
    assert.ok(seen.every((payload) => Number.isFinite(Date.parse(payload.now))))
  } finally {
    fixture.close()
  }
})

test('changed prompt policy during pending participation invalidates the old window turn', async () => {
  const started = deferred(),
    release = deferred()
  let laterCalls = 0
  const fixture = setup(async (_role, _messages, tools) => {
    if (tools.some((tool) => tool.name === 'participation.decide')) {
      started.resolve()
      await release.promise
      return decision()
    }
    laterCalls++
    return result([])
  })
  try {
    const pending = fixture.runtime.proactive(scene.id)
    await started.promise
    fixture.store.set('settings', {
      ...fixture.store.settings,
      persona: 'Changed policy immediately applies.',
    })
    release.resolve()
    await pending
    assert.equal(laterCalls, 0)
    assert.equal(fixture.sends(), 0)
  } finally {
    release.resolve()
    fixture.close()
  }
})
