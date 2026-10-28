import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'

const completed = (name: string, args: Record<string, unknown>, id = name): Completion => {
  const toolCalls = [{ id, name, arguments: args }]
  return {
    text: '',
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
}

test('authenticated manual memory is read by Planner while learning is OFF and reaches Replyer without prewritten answer or cited facts', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 'memory-scene',
    name: 'Memory fixture',
    kind: 'group' as const,
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'memory-scene',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  }
  store.saveScene(scene)
  store.saveScene({ ...scene, id: 'foreign', platformSceneId: 'foreign' })
  store.set('settings', { ...store.settings, replySuggestionsEnabled: false })
  const engine = new Engine(store)
  let sent = 0,
    replyerCalls = 0
  const toolNames: string[] = []
  let localMemoryId = ''
  engine.runtime.addAdapter('fixture', {
    send: async () => {
      sent++
      return { status: 'sent', messageId: 'delivered' }
    },
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  const complete = async (role: string, messages: ChatMessage[], tools: ToolDefinition[]) => {
    const input = JSON.parse(messages.find((item) => item.role === 'user')!.content)
    if (tools.some((tool) => tool.name === 'participation.decide'))
      return completed('participation.decide', {
        action: 'reply',
        reasonCode: 'fixture_memory',
        targetMessageIds: ['question'],
      })
    if (role === 'planner') {
      const receipts = messages
        .filter((item) => item.role === 'tool')
        .map((item) => JSON.parse(item.content))
      if (receipts.length === 0) {
        toolNames.push('memory.search')
        return completed('memory.search', { query: '他喜欢喝什么茶' }, 'search')
      }
      if (receipts.length === 1) {
        assert.equal(receipts[0].value.length, 1)
        assert.match(receipts[0].value[0].content, /乌龙茶/)
        assert.notEqual(
          receipts[0].value[0].id,
          localMemoryId,
          'canonical memory UUID is not exposed to model',
        )
        toolNames.push('memory.person')
        return completed('memory.person', { personId: 'person' }, 'person')
      }
      if (receipts.length === 2) {
        toolNames.push('memory.source')
        return completed('memory.source', { memoryId: receipts[0].value[0].id }, 'source')
      }
      assert.equal(receipts.length, 3)
      assert.deepEqual(
        receipts[2].value.sources,
        [],
        'manual memory does not invent original chat evidence',
      )
      assert.equal(
        input.replySuggestionsEnabled ?? input.responseSuggestionsEnabled ?? false,
        false,
      )
      return completed('reply.plan', {
        action: 'reply',
        deliveryTool: 'messages.send',
        ...input.planContract,
        strategy: 'Use the selected personal memory; do not guess missing history.',
        constraints: ['Do not turn a retrospective dated statement into an event happening today.'],
        facts: [],
        memorySelections: [{ evidenceId: receipts[0].id, memoryIds: [receipts[0].value[0].id] }],
        assets: [],
        emoji: [],
        expressionGoals: [],
        actions: [],
      })
    }
    assert.equal(role, 'replyer')
    replyerCalls++
    assert.match(JSON.stringify(input), /乌龙茶/)
    assert.match(JSON.stringify(input), /2024-03-02/)
    assert.doesNotMatch(JSON.stringify(input), /FOREIGN_PRIVATE_CANARY|UNSELECTED_LOCAL_CANARY/)
    assert.equal(
      input.plan.facts.length,
      0,
      'memory evidence is independently selected, not disguised as a prewritten fact answer',
    )
    assert.equal(input.plan.replySuggestion, undefined)
    assert.equal(input.plan.draftGoals, undefined)
    assert.ok(input.now)
    assert.ok(input.timezone)
    assert.equal(
      tools.some((tool) => tool.name === 'memory.search'),
      false,
      'Replyer composes rather than running memory research',
    )
    return completed('messages.send', {
      operationId: 'memory-answer',
      segments: [
        {
          replyToId: 'question',
          parts: [{ type: 'text', text: '你之前说喜欢乌龙茶；那条记录说的是 2024-03-02。' }],
        },
      ],
    })
  }
  engine.router = () => ({ complete }) as Router
  try {
    const app = createApp(store, { engine, token: 'fixture-admin-token' })
    const create = await app.request('/api/memories', {
      method: 'POST',
      headers: { Authorization: 'Bearer fixture-admin-token', 'Content-Type': 'application/json' },
      body: JSON.stringify({
        scope: { kind: 'scene', sceneId: scene.id, personId: 'person' },
        kind: 'fact',
        content: '2024-03-02，用户说喜欢喝乌龙茶，不喜欢咖啡。',
      }),
    })
    assert.equal(create.status, 200)
    const memory = await create.json()
    localMemoryId = memory.id
    assert.equal(memory.origin, 'manual')
    engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id, personId: 'person' },
      kind: 'fact',
      content: 'UNSELECTED_LOCAL_CANARY: unrelated project data.',
    })
    engine.memory.create({
      scope: { kind: 'scene', sceneId: 'foreign', personId: 'person' },
      kind: 'fact',
      content: 'FOREIGN_PRIVATE_CANARY 用户喜欢喝红茶',
    })
    const now = new Date().toISOString()
    const message: RuntimeMessage = {
      id: 'question',
      sceneId: scene.id,
      senderId: 'person',
      origin: 'live',
      status: 'available',
      sentAt: now,
      receivedAt: now,
      parts: [{ type: 'text', text: '还记得我之前喜欢喝什么茶吗？' }],
    }
    engine.runtime.ingest(message, scene, false)
    await engine.runtime.proactive(scene.id)
    assert.deepEqual(toolNames, ['memory.search', 'memory.person', 'memory.source'])
    assert.equal(replyerCalls, 1)
    assert.equal(sent, 1)
    assert.equal(store.scene(scene.id)!.learningEnabled, false)
    assert.equal(
      engine.memory.adminList().length,
      3,
      'ordinary incoming chat did not create learned memories while OFF',
    )
    assert.ok(
      store.logs.some(
        (log) =>
          log.stage === 'memory.search' &&
          log.action === 'lexical' &&
          log.reason === 'no_active_vector_index',
      ),
    )
    assert.ok(
      store.logs.some(
        (log) => log.stage === 'learning.admission' && log.reason === 'learning_disabled',
      ),
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})
