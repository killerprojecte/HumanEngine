import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setTimeout as delay } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import type { Message, SceneRef } from '../src/onebot.ts'
import type { ChatMessage, ToolCall } from '../src/models.ts'
import type { ReplyPlan } from '../src/runtime/reply-plan.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Reproduces the identifier-class confusion with new synthetic data, not captured host responses.
// The real native OpenAI-chat adapter is used. fetch never delegates to a socket or real provider.
type NativeMessage = { role: string; content: string; tool_call_id?: string }
type NativeRequest = {
  messages: NativeMessage[]
  tools: Array<{ function: { name: string } }>
}
type Receipt = {
  id: string
  toolName: string
  value: Array<{ id: string; content: string }>
  coverage: { status: 'complete' | 'limited'; omittedRecords: number }
}
type RepairHint = {
  error: { code: string; retryable: boolean; remainingRepairs: number }
  memorySelectionOptions: Array<{
    evidenceId: string
    memoryIds: string[]
    coverage: 'complete' | 'limited'
  }>
  coverage: 'complete' | 'limited'
}
const call = (name: string, args: Record<string, unknown>, id = name): ToolCall => ({
  id,
  name,
  arguments: args,
})
const response = (calls: ToolCall[] = []) =>
  new Response(
    JSON.stringify({
      choices: [
        {
          finish_reason: calls.length ? 'tool_calls' : 'stop',
          message: {
            role: 'assistant',
            content: '',
            tool_calls: calls.map((item, index) => ({
              id: `${item.id}-${index}`,
              type: 'function',
              function: {
                name: item.name.replaceAll('.', '__'),
                arguments: JSON.stringify(item.arguments),
              },
            })),
          },
        },
      ],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    }),
    { headers: { 'content-type': 'application/json' } },
  )
function receipts(body: NativeRequest): Receipt[] {
  return body.messages
    .filter((m) => m.role === 'tool')
    .map((m) => JSON.parse(m.content))
    .filter((r) => r.toolName)
}
function hint(body: NativeRequest): RepairHint | undefined {
  const last = body.messages.at(-1)
  if (last?.role !== 'tool') return
  const value = JSON.parse(last.content)
  return value.error?.remainingRepairs === 1 ? value : undefined
}
async function bounded<T>(work: Promise<T>, code: string, ms = 6000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      work,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(Error(code)), ms)
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
}

function fixture(
  t: TestContext,
  options: { secondWrong?: boolean; omitted?: boolean; hold?: boolean } = {},
) {
  const root = mkdtempSync(join(tmpdir(), 'he-planner-repair-'))
  const store = new Store(join(root, 'fixture.sqlite'))
  store.set('settings', {
    ...store.settings,
    debounceMs: 0,
    maxWaitMs: 100,
    ...(options.omitted ? { plannerEvidenceMaxBytes: 16384 } : {}),
  })
  store.saveModels(
    {
      providers: [
        {
          id: 'synthetic',
          name: 'In-process native mock',
          protocol: 'openai-chat',
          baseUrl: 'https://native-fixture.invalid/v1',
          credentialEnv: '',
          retryCount: 0,
          timeoutMs: 4000,
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
      routes: (['planner', 'replyer'] as const).map((role) => ({
        role,
        orderedModelIds: ['synthetic'],
        timeoutMs: 5000,
      })),
    },
    { synthetic: { action: 'set', value: 'synthetic-nonsecret-placeholder' } },
  )
  const diagnostics: Array<{ code: string; detail: Record<string, unknown> }> = []
  let finished!: () => void
  const done = new Promise<void>((resolve) => {
    finished = resolve
  })
  const writeLog = store.log.bind(store)
  store.log = (level, module, code, detail = {}) => {
    const logged = writeLog(level, module, code, detail)
    diagnostics.push({ code, detail })
    if (detail.stage === 'turn_cleanup' && detail.status === 'completed') finished()
    return logged
  }
  const engine = new Engine(store)
  let adapter!: AdapterContext,
    scene!: SceneRef,
    sends = 0,
    plannerCalls = 0,
    replyerCalls = 0
  const registration = installAdapter(
    'repair-fixture',
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
  store.saveScene({
    ...scene,
    name: 'Synthetic repair group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const records = ['Alice prefers jasmine tea.', 'Alice requests no sugar.'].map(
    (content, index) => {
      const source = {
        sceneId: scene.id,
        messageId: adapter.messageId(scene, `memory-source-${index}`),
        senderId: 'alice',
      }
      const value = engine.memory.propose(
        {
          sceneId: scene.id,
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        {
          content: content + (options.omitted ? ' Qualified synthetic detail.'.repeat(400) : ''),
          kind: 'fact',
          epistemicKind: 'explicit',
          subject: { kind: 'self', sourceMessageId: source.messageId },
          sources: [source],
        },
      )
      assert.equal(value.status, 'saved')
      if (value.status !== 'saved') throw Error('fixture_memory_not_saved')
      return value.memory
    },
  )
  store.saveScene({ ...store.scene(scene.id)!, learningEnabled: false })
  engine.sceneChanged(store.scene(scene.id)!)
  const requests: NativeRequest[] = [],
    repairRequests: NativeRequest[] = [],
    fixtureErrors: unknown[] = []
  let entered!: () => void, release!: () => void
  const repairEntered = new Promise<void>((resolve) => {
    entered = resolve
  })
  const releaseRepair = new Promise<void>((resolve) => {
    release = resolve
  })
  let originalPlan: ReplyPlan | undefined
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    try {
      assert.equal(String(url), 'https://native-fixture.invalid/v1/chat/completions')
      const body = JSON.parse(String(init?.body)) as NativeRequest
      requests.push(structuredClone(body))
      const tools = body.tools.map((tool) => tool.function.name)
      if (tools.includes('participation__decide')) {
        const input = JSON.parse(body.messages.find((m) => m.role === 'user')!.content)
        return response([
          call('participation.decide', {
            action: 'reply',
            reasonCode: 'synthetic_saved_preference',
            targetMessageIds: [input.messages.at(-1).id],
          }),
        ])
      }
      if (tools.includes('reply__plan')) {
        plannerCalls++
        const repair = hint(body)
        if (repair) {
          repairRequests.push(structuredClone(body))
          assert.deepEqual(tools, ['reply__plan'], 'repair cannot acquire new read/action tools')
          assert.equal(repair.error.retryable, true)
          assert.equal(repair.error.remainingRepairs, 1)
          entered()
          if (options.hold) await releaseRepair
          assert.ok(originalPlan)
          if (options.secondWrong)
            return response([
              call('reply.plan', originalPlan as unknown as Record<string, unknown>),
            ])
          const delivered = receipts(body).find((r) => r.toolName === 'memory.person')!
          const record = delivered.value.find((r) =>
            r.content.startsWith('Alice prefers jasmine tea.'),
          )
          const selection = repair.memorySelectionOptions.find(
            (item) => item.evidenceId === delivered.id,
          )!
          assert.ok(selection)
          if (!options.omitted) assert.ok(record && selection.memoryIds.includes(record.id))
          else {
            assert.deepEqual(delivered.value, [])
            assert.equal(repair.coverage, 'limited')
            assert.ok(
              repair.memorySelectionOptions.every(
                (item) => item.memoryIds.length === 0 && item.coverage === 'limited',
              ),
            )
          }
          return response([
            call('reply.plan', {
              ...originalPlan,
              facts: [
                { text: 'The saved declaration says jasmine tea.', evidenceIds: [delivered.id] },
              ],
              memorySelections: [
                { evidenceId: delivered.id, memoryIds: [record?.id ?? records[0]!.id] },
              ],
            }),
          ])
        }
        if (plannerCalls === 1)
          return response([
            call('memory.search', { query: 'Alice jasmine tea sugar' }),
            call('memory.person', { personId: 'alice' }),
          ])
        const delivered = receipts(body),
          search = delivered.find((r) => r.toolName === 'memory.search')!,
          person = delivered.find((r) => r.toolName === 'memory.person')!
        assert.ok(search && person)
        assert.notEqual(search.id, person.id)
        const selected = person.value.find((r) =>
          r.content.startsWith('Alice prefers jasmine tea.'),
        )
        if (!options.omitted) {
          assert.ok(selected)
          assert.ok(search.value.some((record) => record.id === selected.id))
          assert.notEqual(selected.id, search.id)
          assert.notEqual(selected.id, person.id)
        } else {
          assert.deepEqual(search.value, [])
          assert.deepEqual(person.value, [])
          assert.equal(person.coverage.status, 'limited')
        }
        const wrongOuter = selected?.id ?? records[0]!.id
        originalPlan = fixtureReplyPlan(body.messages as unknown as ChatMessage[], {
          facts: [{ text: 'The saved declaration says jasmine tea.', evidenceIds: [wrongOuter] }],
          memorySelections: [{ evidenceId: wrongOuter, memoryIds: [wrongOuter] }],
        })!
        return response([call('reply.plan', originalPlan as unknown as Record<string, unknown>)])
      }
      assert.deepEqual(tools, ['messages__send'])
      replyerCalls++
      if (replyerCalls > 1) return response()
      const user = JSON.parse(body.messages.find((m) => m.role === 'user')!.content)
      assert.ok(
        user.evidence.some(
          (item: { toolName?: string; memories?: Array<{ content: string }> }) =>
            item.toolName === 'memory.person' &&
            item.memories?.some((record) => record.content === records[0]!.content),
        ),
      )
      return response([
        call('messages.send', {
          operationId: 'synthetic-repaired-reply',
          segments: [
            { parts: [{ type: 'text', text: 'The saved declaration says jasmine tea.' }] },
          ],
        }),
      ])
    } catch (error) {
      fixtureErrors.push(error)
      throw error
    }
  })
  return {
    engine,
    store,
    scene,
    records,
    requests,
    repairRequests,
    diagnostics,
    get counts() {
      return { plannerCalls, replyerCalls, sends }
    },
    repairEntered,
    release,
    async run() {
      const now = new Date().toISOString()
      adapter.receive(
        {
          id: adapter.messageId(scene, 'question'),
          sceneId: scene.id,
          senderId: 'alice',
          senderDisplayName: 'Alice',
          origin: 'live',
          status: 'available',
          sentAt: now,
          receivedAt: now,
          parts: [
            { type: 'text', text: 'Please inspect the saved drink preference rather than guess.' },
          ],
        } as Message,
        scene,
        false,
      )
      await bounded(done, 'fixture_turn_timeout')
      assert.deepEqual(fixtureErrors, [])
      assert.equal(engine.runtime.activeTasks, 0)
    },
    readABA() {
      for (const enabled of [false, true]) {
        const next = { ...store.scene(scene.id)!, memoryReadEnabled: enabled }
        store.saveScene(next)
        engine.sceneChanged(next)
      }
    },
    async close() {
      release()
      registration.dispose()
      try {
        await engine.dispose()
      } finally {
        store.close()
        rmSync(root, { recursive: true, force: true })
      }
    },
  }
}

test('native Engine repairs an inner-record/outer-receipt confusion once and delivers the corrected qualified memory to a local adapter', async (t) => {
  const h = fixture(t)
  try {
    await h.run()
    assert.equal(h.repairRequests.length, 1)
    assert.equal(hint(h.repairRequests[0]!)!.error.code, 'planner_unknown_memory_evidence')
    assert.deepEqual(h.counts, { plannerCalls: 3, replyerCalls: 1, sends: 1 })
  } finally {
    await h.close()
  }
})

test('native Engine stops after the second invalid plan without a third repair or any send', async (t) => {
  const h = fixture(t, { secondWrong: true })
  try {
    await h.run()
    assert.equal(h.repairRequests.length, 1)
    assert.deepEqual(h.counts, { plannerCalls: 3, replyerCalls: 0, sends: 0 })
    assert.ok(
      h.diagnostics.some((item) =>
        JSON.stringify(item.detail).includes('planner_unknown_memory_evidence'),
      ),
    )
  } finally {
    await h.close()
  }
})

for (const change of ['source-recall', 'read-aba', 'same-body-version'] as const)
  test(`native pending plan repair cannot outlive ${change} or silently reacquire latest authority`, async (t) => {
    const h = fixture(t, { hold: true })
    try {
      const done = h.run()
      await bounded(h.repairEntered, 'fixture_repair_not_reached')
      if (change === 'source-recall')
        h.engine.runtime.recall(h.scene.id, h.records[0]!.sources[0]!.messageId)
      else if (change === 'read-aba') h.readABA()
      else {
        const before = h.engine.memory.revisionState(h.records[0]!.id)!
        h.engine.memory.update(h.records[0]!.id, { locked: true })
        assert.ok(h.engine.memory.revisionState(h.records[0]!.id)!.version > before.version)
        assert.equal(
          h.engine.memory.adminList().find((r) => r.id === h.records[0]!.id)!.content,
          h.records[0]!.content,
        )
      }
      h.release()
      await done
      await delay(10)
      assert.equal(h.repairRequests.length, 1)
      assert.deepEqual(h.counts, { plannerCalls: 3, replyerCalls: 0, sends: 0 })
    } finally {
      await h.close()
    }
  })

test('budget-omitted records are not reintroduced by the native repair hint or authorized by a copied hidden ID', async (t) => {
  const h = fixture(t, { omitted: true })
  try {
    await h.run()
    assert.equal(h.repairRequests.length, 1)
    const repair = hint(h.repairRequests[0]!)!
    assert.equal(repair.coverage, 'limited')
    assert.ok(repair.memorySelectionOptions.every((item) => item.memoryIds.length === 0))
    assert.ok(!JSON.stringify(repair).includes(h.records[0]!.id))
    assert.deepEqual(h.counts, { plannerCalls: 3, replyerCalls: 0, sends: 0 })
  } finally {
    await h.close()
  }
})
