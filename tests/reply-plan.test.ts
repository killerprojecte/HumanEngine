import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildReplyPlan,
  validateReplyPlan,
  plannerReadTools,
  plannerActionTools,
  PLANNER_MAX_ROUNDS,
  PLANNER_MAX_TOOL_CALLS,
  type ReplyPlan,
} from '../src/runtime/reply-plan.ts'
import type { Completion, ToolDefinition } from '../src/models.ts'

const tool = (name: string): ToolDefinition => ({ name, description: 'Fixture', inputSchema: {} })
const plan = (patch: Partial<ReplyPlan> = {}): ReplyPlan => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: ['message-1'],
  contextMessageIds: ['message-1'],
  strategy: 'Answer the current question briefly.',
  constraints: ['Use only the supplied evidence.'],
  facts: [],
  assets: [],
  emoji: [],
  draftGoals: ['Give a concise answer.'],
  actions: [],
  ...patch,
})
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (...toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const scope = () => ({
  targetMessageIds: ['message-1'],
  contextMessageIds: ['message-1', 'message-2'],
  evidence: [{ id: 'message:1', value: 'The user supplied this fact.' }],
  actionTools: [tool('memes.feedback')],
})
const input = (
  patch: Partial<Parameters<typeof buildReplyPlan>[0]> = {},
): Parameters<typeof buildReplyPlan>[0] => ({
  ...scope(),
  messages: [{ role: 'user', content: 'Fixture context' }],
  readTools: [tool('memory.search')],
  signal: new AbortController().signal,
  eligible: () => true,
  complete: async () => completion(call('reply.plan', plan())),
  execute: async () => {
    throw Error('Unexpected execution')
  },
  ...patch,
})

// Production callers must pass only declared stage capabilities, never all extension tools.
test('planner capability selection separates read, effect and send tools', () => {
  const tools = [
    tool('messages.get'),
    tool('memory.search'),
    tool('memes.search'),
    tool('rules.list'),
    { ...tool('web-search.search'), effect: 'read' as const },
    tool('memes.feedback'),
    tool('habits.feedback'),
    tool('habits.propose'),
    tool('learning.propose'),
    { ...tool('fixture.propose'), effect: 'learningCandidate' as const },
    tool('messages.send'),
    { ...tool('fixture.send'), effect: 'send' as const },
    tool('fixture.unknown'),
  ]
  assert.deepEqual(
    plannerReadTools(tools).map((t) => t.name),
    ['messages.get', 'memory.search', 'memes.search', 'rules.list', 'web-search.search'],
  )
  assert.deepEqual(
    plannerActionTools(tools).map((t) => t.name),
    ['memes.feedback', 'habits.feedback', 'habits.propose', 'learning.propose', 'fixture.propose'],
  )
})

test('reply plans reject malformed structure and hidden explanation fields', () => {
  for (const invalid of [
    null,
    {},
    { ...plan(), hiddenReasoning: 'Do not retain this' },
    { ...plan(), facts: [{ text: 'Unsupported', evidenceIds: [] }] },
    { ...plan(), action: 'send' },
    { ...plan(), strategy: 'x'.repeat(1001) },
  ])
    assert.throws(() => validateReplyPlan(invalid, scope()), /planner_invalid_plan/)
})

test('reply plans bind recipient, context and facts to known identifiers', () => {
  for (const patch of [
    { targetMessageIds: ['foreign'] },
    { contextMessageIds: ['foreign'] },
    { contextMessageIds: ['message-2'] },
  ])
    assert.throws(() => validateReplyPlan(plan(patch), scope()), /planner_invalid_target/)
  assert.throws(
    () =>
      validateReplyPlan(plan({ facts: [{ text: 'A claim', evidenceIds: ['invented'] }] }), scope()),
    /planner_unknown_evidence/,
  )
  assert.equal(
    validateReplyPlan(
      plan({ facts: [{ text: 'The user supplied this fact.', evidenceIds: ['message:1'] }] }),
      scope(),
    ).facts.length,
    1,
  )
})

test('meme selection must reference the exact asset in an actual meme-search receipt', () => {
  const selected = plan({ assets: [{ assetId: 'asset-1', evidenceId: 'tool:1' }] })
  for (const evidence of [
    [],
    [{ id: 'tool:1', toolName: 'memory.search', value: { items: [{ id: 'asset-1' }] } }],
    [{ id: 'tool:1', toolName: 'memes.search', value: { items: [{ id: 'asset-2' }] } }],
  ])
    assert.throws(
      () => validateReplyPlan(selected, { ...scope(), evidence }),
      /planner_unknown_asset/,
    )
  const valid = validateReplyPlan(selected, {
    ...scope(),
    evidence: [{ id: 'tool:1', toolName: 'memes.search', value: { items: [{ id: 'asset-1' }] } }],
  })
  assert.equal(valid.assets[0]!.assetId, 'asset-1')
})

test('noReply is terminal and cannot authorize actions or assets', async () => {
  let executions = 0,
    completions = 0
  const outcome = await buildReplyPlan(
    input({
      complete: async () => {
        completions++
        return completion(call('reply.plan', plan({ action: 'noReply' })))
      },
      execute: async () => {
        executions++
      },
    }),
  )
  assert.equal(outcome.plan.action, 'noReply')
  assert.equal(completions, 1)
  assert.equal(executions, 0)
  assert.throws(
    () =>
      validateReplyPlan(
        plan({ action: 'noReply', actions: [{ toolName: 'memes.feedback', arguments: {} }] }),
        scope(),
      ),
    /planner_invalid_no_reply/,
  )
  assert.throws(
    () =>
      validateReplyPlan(plan({ actions: [{ toolName: 'messages.send', arguments: {} }] }), scope()),
    /planner_forbidden_action/,
  )
})

test('planner really executes a read, retains its authoritative receipt, and cites it in the final plan', async () => {
  let calls = 0
  const original = input()
  const receipt = {
    items: [{ id: 'memory-1', content: 'Verified fixture fact' }],
    coverage: 'partial',
  }
  const executions: unknown[] = []
  const outcome = await buildReplyPlan(
    input({
      messages: original.messages,
      execute: async (name, args, signal) => {
        executions.push({ name, args, aborted: signal.aborted })
        return receipt
      },
      complete: async (messages, tools) => {
        assert.deepEqual(
          tools.map((t) => t.name),
          ['memory.search', 'reply.plan'],
        )
        if (++calls === 1) return completion(call('memory.search', { query: 'fixture' }, 'read-1'))
        const evidence = messages.find((m) => m.role === 'tool' && m.toolCallId === 'read-1')!
        assert.deepEqual(JSON.parse(evidence.content), {
          id: 'tool:1',
          toolName: 'memory.search',
          value: receipt,
        })
        return completion(
          call(
            'reply.plan',
            plan({ facts: [{ text: 'Verified fixture fact', evidenceIds: ['tool:1'] }] }),
          ),
        )
      },
    }),
  )
  assert.deepEqual(executions, [
    { name: 'memory.search', args: { query: 'fixture' }, aborted: false },
  ])
  assert.equal(outcome.rounds, 2)
  assert.equal(outcome.toolCalls, 1)
  assert.deepEqual(outcome.evidence.at(-1)!.value, receipt)
  assert.equal(original.messages.length, 1, 'planning must not mutate caller-owned transcript')
})

test('planner cannot execute undeclared sends or effects, nor combine a plan with another call', async () => {
  let executions = 0
  for (const name of ['messages.send', 'memes.feedback', 'fixture.unknown'])
    await assert.rejects(
      buildReplyPlan(
        input({
          complete: async () => completion(call(name)),
          execute: async () => {
            executions++
          },
        }),
      ),
      /planner_forbidden_tool/,
    )
  for (const calls of [
    [call('reply.plan', plan()), call('memory.search')],
    [call('reply.plan', plan()), call('reply.plan', plan(), 'second')],
  ])
    await assert.rejects(
      buildReplyPlan(input({ complete: async () => completion(...calls) })),
      /planner_ambiguous_plan/,
    )
  assert.equal(executions, 0)
  await assert.rejects(
    buildReplyPlan(input({ complete: async () => completion() })),
    /planner_missing_plan/,
  )
})

test('round and aggregate tool budgets are bounded independently', async () => {
  let rounds = 0,
    executions = 0
  await assert.rejects(
    buildReplyPlan(
      input({
        complete: async () => {
          rounds++
          return completion(call('memory.search'))
        },
        execute: async () => {
          executions++
          return []
        },
      }),
    ),
    /planner_round_budget_exhausted/,
  )
  assert.equal(rounds, PLANNER_MAX_ROUNDS)
  assert.equal(executions, PLANNER_MAX_ROUNDS)
  executions = 0
  await assert.rejects(
    buildReplyPlan(
      input({
        complete: async () =>
          completion(
            ...Array.from({ length: PLANNER_MAX_TOOL_CALLS + 1 }, (_, index) =>
              call('memory.search', {}, `r-${index}`),
            ),
          ),
        execute: async () => {
          executions++
        },
      }),
    ),
    /planner_tool_budget_exhausted/,
  )
  assert.equal(executions, 0, 'reject an oversized batch before executing any of it')
  rounds = 0
  const outcome = await buildReplyPlan(
    input({
      complete: async () =>
        ++rounds === 1
          ? completion(
              ...Array.from({ length: PLANNER_MAX_TOOL_CALLS }, (_, index) =>
                call('memory.search', {}, `r-${index}`),
              ),
            )
          : completion(call('reply.plan', plan())),
      execute: async () => [],
    }),
  )
  assert.equal(outcome.toolCalls, PLANNER_MAX_TOOL_CALLS)
  assert.equal(outcome.rounds, 2)
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

test('cancellation and invalidation after pending completion prevent tool execution', async () => {
  for (const kind of ['abort', 'ineligible'] as const) {
    const controller = new AbortController(),
      pending = deferred<Completion>(),
      entered = deferred<void>()
    let eligible = true,
      executions = 0
    const running = buildReplyPlan(
      input({
        signal: controller.signal,
        eligible: () => eligible,
        complete: async () => {
          entered.resolve()
          return pending.promise
        },
        execute: async () => {
          executions++
        },
      }),
    )
    await entered.promise
    if (kind === 'abort') controller.abort(new Error('fixture_cancelled'))
    else eligible = false
    pending.resolve(completion(call('memory.search')))
    await assert.rejects(
      running,
      kind === 'abort' ? /fixture_cancelled/ : /planner_context_ineligible/,
    )
    assert.equal(executions, 0)
  }
})

test('invalidation after a pending tool result prevents evidence handoff and further model calls', async () => {
  const pending = deferred<unknown>(),
    entered = deferred<void>()
  let eligible = true,
    completions = 0
  const running = buildReplyPlan(
    input({
      eligible: () => eligible,
      complete: async () => {
        completions++
        return completion(call('memory.search'))
      },
      execute: async () => {
        entered.resolve()
        return pending.promise
      },
    }),
  )
  await entered.promise
  eligible = false
  pending.resolve({ content: 'Must not reach dialogue after recall' })
  await assert.rejects(running, /planner_context_ineligible/)
  assert.equal(completions, 1)
})

test('noReply cannot smuggle a valid selected asset into delivery', () => {
  assert.throws(
    () =>
      validateReplyPlan(
        plan({ action: 'noReply', assets: [{ assetId: 'asset-1', evidenceId: 'tool:1' }] }),
        {
          ...scope(),
          evidence: [
            { id: 'tool:1', toolName: 'memes.search', value: { items: [{ id: 'asset-1' }] } },
          ],
        },
      ),
    /planner_invalid_no_reply/,
  )
})

test('aggregate tool budget includes calls from prior rounds', async () => {
  let rounds = 0,
    executions = 0
  await assert.rejects(
    buildReplyPlan(
      input({
        complete: async () =>
          completion(
            ...Array.from({ length: ++rounds === 1 ? 7 : 6 }, (_, index) =>
              call('memory.search', {}, `r-${rounds}-${index}`),
            ),
          ),
        execute: async () => {
          executions++
          return []
        },
      }),
    ),
    /planner_tool_budget_exhausted/,
  )
  assert.equal(rounds, 2)
  assert.equal(executions, 7)
})

test('pre-cancelled planning and failed reads never retry or synthesize a plan', async () => {
  const controller = new AbortController()
  controller.abort(new Error('already_cancelled'))
  let completions = 0,
    executions = 0
  await assert.rejects(
    buildReplyPlan(
      input({
        signal: controller.signal,
        complete: async () => {
          completions++
          return completion(call('reply.plan', plan()))
        },
      }),
    ),
    /already_cancelled/,
  )
  assert.equal(completions, 0)
  await assert.rejects(
    buildReplyPlan(
      input({
        complete: async () => {
          completions++
          return completion(call('memory.search'))
        },
        execute: async () => {
          executions++
          throw new Error('fixture_read_failed')
        },
      }),
    ),
    /fixture_read_failed/,
  )
  assert.equal(completions, 1)
  assert.equal(executions, 1)
})
