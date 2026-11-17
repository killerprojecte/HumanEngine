import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { RuntimeExtension, TurnContext } from '../src/runtime.ts'
import { buildReplyPlan } from '../src/runtime/reply-plan.ts'
import type { Completion } from '../src/models.ts'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'

test('accumulated learned hints are not read or serialized during initial context preparation, but remain available on demand', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 's',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'bot',
    platformSceneId: 's',
  }
  store.saveScene({
    ...scene,
    name: 's',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  try {
    const count = 600
    const insert = store.db.prepare('INSERT INTO habits VALUES(?,?,?)')
    for (let i = 0; i < count; i++) {
      const id = `hint-${i}`
      insert.run(
        id,
        id,
        JSON.stringify({
          id,
          sceneId: scene.id,
          kind: 'sceneRule',
          content: `HINT_CANARY_${i} ${'context'.repeat(100)}`,
          ...(i === 599 ? { context: 'technical' } : {}),
          evidenceVersion: 2,
          sources: [
            {
              sceneId: scene.id,
              messageId: id,
              senderId: 'speaker',
              direction: 1,
              timestamp: Date.now(),
            },
          ],
          weight: 0,
          confidence: 0,
          status: 'active',
          locked: false,
          learnedAt: new Date().toISOString(),
        }),
      )
    }
    const context: TurnContext = {
      scene,
      mode: 'real',
      turnId: 'turn',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
      deferMemoryExposure: true,
    }
    const extensions = (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions
    const extension = extensions.find((e) => e.tools.some((t) => t.name === 'rules.list'))!
    const original = engine.habits.adminList.bind(engine.habits)
    let reads = 0
    engine.habits.adminList = () => {
      reads++
      return original()
    }
    // Same extension preparation used by Runtime, without requiring any model/network call.
    const learnedData = extensions.map((e) => e.contextData?.(context)).filter(Boolean)
    const initial = [
      {
        role: 'user' as const,
        content: JSON.stringify({
          evidence: learnedData.map((value, index) => ({ id: `context:${index}`, value })),
        }),
      },
    ]
    assert.equal(reads, 0)
    assert.equal(JSON.stringify(initial).includes('HINT_CANARY'), false)
    const budget = new PlannerEvidenceBudget()
    assert.doesNotThrow(() => budget.assertWithin(initial))
    const result = (await extension.execute('rules.list', {}, context)) as {
      learned: Array<{ content: string }>
    }
    assert.ok(reads > 0)
    assert.equal(result.learned.length, count)
    assert.match(result.learned[0]!.content, /HINT_CANARY_0/)
    // The old eager payload would exhaust the unchanged initial Planner budget.
    assert.throws(
      () => budget.assertWithin([{ role: 'user', content: JSON.stringify(result) }]),
      /planner_evidence_budget_exhausted/,
    )
    // Oversized results are withheld as a whole and do not authorize hidden sources.
    const omitted = budget.receipt(
      initial,
      { id: 'rules', toolName: 'rules.list', value: result },
      'call',
    )
    assert.match(omitted.message.content, /limited/)
    assert.doesNotMatch(omitted.message.content, /HINT_CANARY/)
    extension.delivered!('rules.list', omitted.receipt.value, context)
    assert.equal(context.dependencies.size, 0)
    const general = (await extension.execute(
      'rules.list',
      { query: 'HINT_CANARY_599', context: 'general' },
      context,
    )) as { learned: unknown[] }
    assert.deepEqual(general.learned, [])
    const narrow = (await extension.execute(
      'rules.list',
      { query: 'HINT_CANARY_599', context: 'technical' },
      context,
    )) as { learned: Array<{ content: string }> }
    assert.equal(narrow.learned.length, 1)
    assert.equal(context.dependencies.size, 0)
    const delivered = budget.receipt(
      initial,
      { id: 'narrow', toolName: 'rules.list', value: narrow },
      'narrow-call',
    )
    extension.delivered!('rules.list', delivered.receipt.value, context)
    assert.deepEqual([...context.dependencies], ['hint-599'])
    assert.equal(
      context.validations.every((validate) => validate()),
      true,
    )
    const retryContext = {
      ...context,
      dependencies: new Set<string>(),
      validations: [] as Array<() => boolean>,
    }
    let rounds = 0
    await buildReplyPlan({
      messages: initial,
      evidence: [],
      targetMessageIds: [],
      contextMessageIds: [],
      actionTools: [],
      readTools: extension.tools,
      signal: context.signal,
      eligible: () => true,
      execute: (name, args) => extension.execute(name, args, retryContext),
      deliver: (name, value, args) => extension.delivered!(name, value, retryContext, args),
      complete: async (messages): Promise<Completion> => {
        rounds++
        const receipts = messages
          .filter((message) => message.role === 'tool')
          .map((message) => JSON.parse(message.content))
        if (receipts.length === 1) {
          assert.equal(receipts[0].value.coverage.status, 'limited')
          assert.equal(retryContext.dependencies.size, 0)
        }
        if (receipts.length === 2) assert.equal(receipts[1].value.learned.length, 1)
        const toolCalls = [
          {
            id: `round-${rounds}`,
            name: rounds < 3 ? 'rules.list' : 'reply.plan',
            arguments:
              rounds === 1
                ? {}
                : rounds === 2
                  ? { query: 'HINT_CANARY_599', context: 'technical' }
                  : {
                      action: 'noReply',
                      targetMessageIds: [],
                      contextMessageIds: [],
                      strategy: 'No response needed.',
                      constraints: [],
                      facts: [],
                      assets: [],
                      emoji: [],
                      actions: [],
                    },
          },
        ]
        return {
          text: '',
          toolCalls,
          modelId: 'fixture',
          usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
          assistant: { role: 'assistant', content: '', toolCalls },
        }
      },
    })
    assert.equal(
      rounds,
      3,
      'oversized read can narrow and finish, rather than failing the planner turn',
    )
    assert.deepEqual([...retryContext.dependencies], ['hint-599'])
    store.recall(scene.id, 'hint-599')
    assert.equal(
      context.validations.every((validate) => validate()),
      false,
    )
    assert.match(extension.tools.find((t) => t.name === 'rules.list')!.description, /on demand/)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('rules receipts are withheld whole when cumulative Planner space, not record count, is exhausted', () => {
  const budget = new PlannerEvidenceBudget(2000)
  const value = { manual: [], learned: [{ content: 'x'.repeat(1200) }] }
  const messages = [{ role: 'user' as const, content: 'context'.repeat(150) }]
  const output = budget.receipt(messages, { id: 'rules', toolName: 'rules.list', value }, 'call')
  assert.match(output.message.content, /limited/)
  assert.doesNotMatch(output.message.content, /x{30}/)
  assert.ok(budget.measure([...messages, output.message]) <= 2000)
})
