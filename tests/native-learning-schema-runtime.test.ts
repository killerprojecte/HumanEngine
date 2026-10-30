import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import type { Message, SceneRef } from '../src/onebot.ts'
import type { ChatMessage, ToolCall } from '../src/models.ts'
import {
  HabitProposalSchema,
  buildLearningCandidateSchema,
  definition,
} from '../src/memory/learning/contracts.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// All native fetch requests are intercepted in process, never forwarded to a socket.
// These scripted answers prove transport/schema/lifecycle contracts, not model extraction quality.
type Protocol = 'openai-chat' | 'openai-responses'
type NativeTool = { name: string; parameters: Record<string, unknown> }
type NativeRequest = {
  tools: Array<NativeTool | { function: NativeTool }>
  messages?: ChatMessage[]
  input?: Array<{ role?: ChatMessage['role']; content?: string; type?: string; output?: string }>
}
const content = 'Alice prefers jasmine tea.'
const call = (name: string, args: Record<string, unknown>): ToolCall => ({
  id: name,
  name,
  arguments: args,
})
function response(protocol: Protocol, calls: ToolCall[]): Response {
  const functions = calls.map((item, index) => ({
    id: `synthetic-${index}`,
    name: item.name.replaceAll('.', '__'),
    arguments: JSON.stringify(item.arguments),
  }))
  return new Response(
    JSON.stringify(
      protocol === 'openai-chat'
        ? {
            choices: [
              {
                finish_reason: calls.length ? 'tool_calls' : 'stop',
                message: {
                  role: 'assistant',
                  content: '',
                  tool_calls: functions.map((fn) => ({
                    id: fn.id,
                    type: 'function',
                    function: { name: fn.name, arguments: fn.arguments },
                  })),
                },
              },
            ],
            usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
          }
        : {
            status: 'completed',
            output: functions.map((fn) => ({
              type: 'function_call',
              call_id: fn.id,
              name: fn.name,
              arguments: fn.arguments,
            })),
            usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
          },
    ),
    { headers: { 'content-type': 'application/json' } },
  )
}
function messages(body: NativeRequest): ChatMessage[] {
  return (
    body.messages ??
    body.input!.flatMap((item) => {
      if (item.type === 'function_call_output')
        return [{ role: 'tool' as const, content: item.output! }]
      return item.role ? [{ role: item.role, content: item.content! }] : []
    })
  )
}
async function until(check: () => boolean, code: string) {
  const deadline = Date.now() + 5000
  while (!check()) {
    if (Date.now() > deadline) throw Error(code)
    await delay(10)
  }
}
function fixture(t: TestContext, protocol: Protocol, invalid = false) {
  const root = mkdtempSync(join(tmpdir(), 'he-native-learning-schema-'))
  const db = join(root, 'fixture.sqlite')
  let store!: Store, engine!: Engine, adapter!: AdapterContext, scene!: SceneRef
  let registration!: ReturnType<typeof installAdapter>
  const requests: NativeRequest[] = [],
    errors: unknown[] = []
  const learningTools: NativeTool[][] = []
  const logCodes: string[] = []
  let sends = 0,
    replyerCalls = 0,
    plannerCalls = 0,
    turnFinished = false
  const rawHabit = definition('habits.propose', '', HabitProposalSchema).inputSchema
  const rawMemory = definition(
    'learning.propose',
    '',
    buildLearningCandidateSchema(false, false, false, true),
  ).inputSchema
  assert.equal(Object.hasOwn(rawHabit, 'type'), false, 'the original union has no root type')
  assert.equal((rawHabit.oneOf as unknown[]).length, 2)
  const originals = structuredClone({ rawHabit, rawMemory })

  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    try {
      assert.equal(
        String(url),
        `https://native-schema.invalid/v1/${protocol === 'openai-chat' ? 'chat/completions' : 'responses'}`,
      )
      const body = JSON.parse(String(init?.body)) as NativeRequest
      requests.push(structuredClone(body))
      const tools = body.tools.map((tool) => ('function' in tool ? tool.function : tool))
      // Simulate the endpoint's object-root requirement for EVERY tool, not only the chosen call.
      if (tools.some((tool) => tool.parameters.type !== 'object'))
        return new Response(
          JSON.stringify({ error: { message: 'parameters must be JSON Schema type object' } }),
          { status: 400 },
        )
      const names = tools.map((tool) => tool.name)
      const prompts = messages(body)
      const input = JSON.parse(prompts.find((message) => message.role === 'user')!.content)
      if (names.includes('learning__propose')) {
        assert.deepEqual(names, ['learning__propose', 'habits__propose'])
        learningTools.push(structuredClone(tools))
        assert.deepEqual(
          tools.find((tool) => tool.name === 'habits__propose')!.parameters,
          { ...rawHabit, type: 'object' },
          'every original union branch, required key and strict constraint survives',
        )
        assert.deepEqual(
          tools.find((tool) => tool.name === 'learning__propose')!.parameters,
          rawMemory,
          'the ordinary object candidate schema is unchanged',
        )
        const source = input.messages[0] as { id: string }
        assert.ok(source.id)
        if (invalid)
          return response(protocol, [
            call('habits.propose', {
              kind: 'botExpression',
              preference: 'concise',
              direction: 1,
              sourceMessageIds: [source.id],
              content: 'Forbidden cross-branch field.',
            }),
            call('learning.propose', {
              content,
              kind: 'fact',
              epistemicKind: 'explicit',
              subject: { kind: 'self', sourceMessageId: source.id },
              sourceMessageIds: [source.id],
              unexpected: 'must not persist',
            }),
          ])
        return response(protocol, [
          call('learning.propose', {
            content,
            kind: 'fact',
            epistemicKind: 'explicit',
            subject: { kind: 'self', sourceMessageId: source.id },
            sourceMessageIds: [source.id],
          }),
        ])
      }
      if (names.includes('participation__decide'))
        return response(protocol, [
          call('participation.decide', {
            action: 'reply',
            reasonCode: 'synthetic_saved_preference',
            targetMessageIds: [input.messages.at(-1).id],
          }),
        ])
      if (names.includes('reply__plan')) {
        plannerCalls++
        const receipt = prompts
          .filter((message) => message.role === 'tool')
          .map((message) => JSON.parse(message.content))
          .find((item) => item.toolName === 'memory.search')
        if (!receipt) return response(protocol, [call('memory.search', { query: 'jasmine tea' })])
        const record = receipt.value.find((item: { content: string }) => item.content === content)
        assert.ok(record, 'the restarted Engine must deliver the learned stored record')
        assert.notEqual(receipt.id, record.id)
        const plan = fixtureReplyPlan(prompts, {
          facts: [{ text: content, evidenceIds: [receipt.id] }],
          memorySelections: [{ evidenceId: receipt.id, memoryIds: [record.id] }],
        })
        assert.ok(plan)
        return response(protocol, [call('reply.plan', plan as unknown as Record<string, unknown>)])
      }
      assert.deepEqual(names, ['messages__send'])
      replyerCalls++
      assert.ok(
        input.evidence.some((item: { memories?: Array<{ content: string }> }) =>
          item.memories?.some((record) => record.content === content),
        ),
      )
      return response(protocol, [
        call('messages.send', {
          operationId: 'synthetic-learned-answer',
          segments: [
            { parts: [{ type: 'text', text: 'The saved declaration says jasmine tea.' }] },
          ],
        }),
      ])
    } catch (error) {
      errors.push(error)
      throw error
    }
  })
  function open(first: boolean) {
    store = new Store(db)
    if (first) {
      store.set('settings', {
        ...store.settings,
        learningBatchMs: 100,
        learningMaxWaitMs: 300,
        learningMinIntervalMs: 100,
        debounceMs: 0,
        maxWaitMs: 100,
      })
      store.saveModels({
        providers: [
          {
            id: 'synthetic',
            name: 'In-process mock',
            protocol,
            baseUrl: 'https://native-schema.invalid/v1',
            credentialEnv: '',
            retryCount: 0,
            timeoutMs: 2000,
          },
        ],
        models: [
          {
            id: 'synthetic',
            displayName: 'Synthetic',
            providerId: 'synthetic',
            apiModelName: 'synthetic',
            capabilities: ['text', 'tools'],
          },
        ],
        routes: (['learning', 'planner', 'replyer'] as const).map((role) => ({
          role,
          orderedModelIds: ['synthetic'],
          timeoutMs: 3000,
        })),
      })
    }
    const log = store.log.bind(store)
    store.log = (level, module, code, detail = {}) => {
      const result = log(level, module, code, detail)
      logCodes.push(code)
      if (detail.stage === 'turn_cleanup' && detail.status === 'completed') turnFinished = true
      return result
    }
    engine = new Engine(store)
    registration = installAdapter(
      'native-schema-fixture',
      (context) => {
        adapter = context
        return {
          async send(_scene, segment) {
            sends++
            assert.deepEqual(segment.parts, [
              { type: 'text', text: 'The saved declaration says jasmine tea.' },
            ])
            return {
              status: 'sent' as const,
              messageId: context.messageId(scene, `local-ack-${sends}`),
            }
          },
          async history() {
            return { items: [], coverage: 'complete' as const }
          },
          async getMessage() {
            return { items: [], coverage: 'complete' as const }
          },
          dispose() {},
        }
      },
      engine.runtime,
      store,
      new AbortController().signal,
      {},
    )
    scene = adapter.scene('synthetic-account', 'group', 'synthetic-group')
    if (first)
      store.saveScene({
        ...scene,
        name: 'Synthetic native schema group',
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      })
  }
  function ingest(id: string, text: string) {
    const now = new Date().toISOString()
    adapter.receive(
      {
        id: adapter.messageId(scene, id),
        sceneId: scene.id,
        senderId: 'alice',
        senderDisplayName: 'Alice',
        origin: 'live',
        status: 'available',
        sentAt: now,
        receivedAt: now,
        parts: [{ type: 'text', text }],
      } as Message,
      scene,
      false,
    )
  }
  async function dispose() {
    registration.dispose()
    await engine.dispose()
    store.close()
  }
  open(true)
  return {
    get engine() {
      return engine
    },
    get store() {
      return store
    },
    get scene() {
      return scene
    },
    learningTools,
    logCodes,
    requests,
    async learn() {
      ingest('original-source', 'I prefer jasmine tea.')
      await until(
        () => engine.learningStatus().completed === 1 || engine.learningStatus().failed > 0,
        'learning_did_not_finish',
      )
      assert.deepEqual(errors, [])
      assert.equal(engine.learningStatus().failed, 0)
      assert.equal(engine.learningStatus().completed, 1)
      assert.equal(learningTools.length, 1)
      assert.equal(sends, 0)
      assert.deepEqual(
        { rawHabit, rawMemory },
        originals,
        'native serialization does not mutate shared schema definitions',
      )
    },
    async reopenAndReply() {
      store.saveScene({
        ...store.scene(scene.id)!,
        learningEnabled: false,
        participationEnabled: true,
      })
      engine.sceneChanged(store.scene(scene.id)!)
      await dispose()
      const before = requests.length
      open(false)
      await delay(150)
      assert.equal(requests.length, before, 'restart must not replay old extraction or chat turns')
      assert.equal(engine.learningStatus().queued, 0)
      assert.equal(engine.runtime.preview(scene.id).length, 0)
      ingest('fresh-question', 'What drink preference did I previously declare?')
      await until(() => turnFinished, 'reply_turn_did_not_finish')
      assert.deepEqual(errors, [])
      assert.equal(plannerCalls, 2)
      assert.equal(replyerCalls, 1)
      assert.equal(sends, 1)
      assert.equal(learningTools.length, 1, 'query with learning OFF does not extract again')
    },
    async close() {
      try {
        await dispose()
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    },
  }
}
for (const protocol of ['openai-chat', 'openai-responses'] as const) {
  test(`${protocol}: actual native Engine preserves the complete learning union, persists a sourced memory and replies after restart`, async (t) => {
    const h = fixture(t, protocol)
    try {
      await h.learn()
      const records = h.engine.memory.adminList()
      assert.equal(records.length, 1)
      assert.equal(records[0]!.content, content)
      assert.equal(records[0]!.scope.kind, 'scene')
      assert.equal(records[0]!.scope.personId, 'alice')
      assert.equal(records[0]!.sources[0]!.senderId, 'alice')
      assert.equal(records[0]!.sources[0]!.sceneId, h.scene.id)
      const id = records[0]!.id
      await h.reopenAndReply()
      assert.equal(h.engine.memory.adminList()[0]!.id, id)
    } finally {
      await h.close()
    }
  })
}
test('root object compatibility does not relax actual learning or discriminated-union argument validation', async (t) => {
  const h = fixture(t, 'openai-chat', true)
  try {
    await h.learn()
    assert.deepEqual(h.engine.memory.adminList(), [])
    assert.deepEqual(h.engine.habits.adminList(), [])
    assert.equal(
      h.logCodes.filter((code) => code === 'learning_candidate_invalid').length,
      2,
      'both malformed candidates reach and fail the unchanged local validators',
    )
    assert.equal(
      h.requests.length,
      1,
      'invalid arguments do not trigger provider fallback or automatic retries',
    )
  } finally {
    await h.close()
  }
})
