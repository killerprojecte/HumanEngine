import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer, type ServerResponse } from 'node:http'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { ChatMessage, Completion } from '../src/models.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { ReplyPlan } from '../src/runtime/reply-plan.ts'
import type { Settings } from '../src/schema.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const scene: SceneRef = {
  id: 'evidence-budget-scene',
  kind: 'group',
  adapterId: 'offline-evidence',
  accountId: 'synthetic-bot',
  platformSceneId: 'synthetic-group',
}
const tail = '但是并未确认；仅本次临时适用，不是永久偏好，不可据此断言已成功。'
const longMemory = `TailCandidate ${'细节 "引用" \\ 路径\n'.repeat(220)}${tail}`
const memoryFact = 'MEMORY_DEPENDENT_CONCLUSION'
const mixedFact = 'MIXED_WEB_CONCLUSION_ONLY_IF_MEMORY_CONDITION'
const webFact = 'WEB_ONLY_FACT'
const messageFact = 'MESSAGE_ONLY_FACT'
const draft = 'MEMORY_RESTATEMENT_DRAFT'
type Call = Completion['toolCalls'][number]
type NativeMessage = {
  role: string
  content: string
  tool_call_id?: string
  tool_calls?: unknown[]
}
type NativeRequest = {
  messages: NativeMessage[]
  tools: Array<{ function: { name: string } }>
}
type Receipt = {
  id: string
  toolName: string
  value: Array<{ id: string; content: string }> | Record<string, unknown>
  coverage?: {
    status: string
    returnedRecords: number
    omittedRecords: number
    reason?: string
  }
}
const call = (name: string, args: Record<string, unknown> = {}, id = name): Call => ({
  id,
  name,
  arguments: args,
})
const receipts = (body: NativeRequest): Receipt[] =>
  body.messages
    .filter((message) => message.role === 'tool')
    .map((message) => JSON.parse(message.content))
const planCall = (body: NativeRequest, patch: Partial<ReplyPlan> = {}) =>
  call('reply.plan', fixtureReplyPlan(body.messages as ChatMessage[], patch)!, 'final-plan')
function respond(response: ServerResponse, calls: Call[], content = '') {
  response.setHeader('Content-Type', 'application/json')
  response.end(
    JSON.stringify({
      choices: [
        {
          finish_reason: calls.length ? 'tool_calls' : 'stop',
          message: {
            content,
            ...(calls.length
              ? {
                  tool_calls: calls.map((tool) => ({
                    id: tool.id,
                    type: 'function',
                    function: {
                      name: tool.name.replaceAll('.', '__'),
                      arguments: JSON.stringify(tool.arguments),
                    },
                  })),
                }
              : {}),
          },
        },
      ],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    }),
  )
}

/** Real Engine/Runtime/native HTTP path, but every receipt and provider response is synthetic. */
async function fixture(
  options: {
    contents?: string[]
    settings?: Partial<Settings>
    extras?: boolean
  } = {},
) {
  const bodies: NativeRequest[] = []
  const captures: Array<{ role: string; messages: ChatMessage[] }> = []
  const errors: unknown[] = []
  let planner = (_body: NativeRequest, _round: number): { calls: Call[]; content?: string } => ({
    calls: [],
  })
  let plannerCalls = 0
  const server = createServer(async (request, response) => {
    try {
      const chunks: Buffer[] = []
      for await (const chunk of request) chunks.push(Buffer.from(chunk))
      const body = JSON.parse(Buffer.concat(chunks).toString()) as NativeRequest
      bodies.push(body)
      const tools = body.tools.map((tool) => tool.function.name)
      if (tools.includes('participation__decide')) {
        respond(response, [
          call('participation.decide', {
            action: 'reply',
            reasonCode: 'synthetic_evidence_budget',
            targetMessageIds: ['source'],
          }),
        ])
      } else if (tools.includes('reply__plan')) {
        const result = planner(body, ++plannerCalls)
        respond(response, result.calls, result.content)
      } else {
        assert.deepEqual(tools, ['messages__send'])
        // Terminal text is deliberately not a send; no platform transport is invoked.
        respond(response, [], 'Synthetic inspection complete; do not deliver.')
      }
    } catch (error) {
      errors.push(error)
      response.statusCode = 500
      response.end('{}')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    debounceMs: 0,
    maxWaitMs: 100,
    replySuggestionsEnabled: true,
    ...options.settings,
  })
  store.saveModels(
    {
      providers: [
        {
          id: 'synthetic-provider',
          name: 'Loopback evidence fixture',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${(server.address() as { port: number }).port}`,
          credentialEnv: '',
          retryCount: 0,
          timeoutMs: 2000,
        },
      ],
      models: [
        {
          id: 'synthetic-model',
          displayName: 'Synthetic',
          providerId: 'synthetic-provider',
          apiModelName: 'local-evidence',
          capabilities: ['text', 'tools'],
          embeddingBatchSize: 4,
        },
      ],
      routes: (['planner', 'replyer'] as const).map((role) => ({
        role,
        orderedModelIds: ['synthetic-model'],
        timeoutMs: 5000,
      })),
    },
    { 'synthetic-provider': { action: 'set', value: 'synthetic-nonsecret-key' } },
  )
  store.saveScene({
    ...scene,
    name: 'Synthetic evidence scene',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  const originalRouter = engine.router.bind(engine)
  engine.router = (config) => {
    const router = originalRouter(config)
    const complete = router.complete.bind(router)
    router.complete = (role, messages, ...args) => {
      captures.push({ role, messages: structuredClone(messages) })
      return complete(role, messages, ...args)
    }
    return router
  }
  let sends = 0
  engine.runtime.addAdapter(scene.adapterId, {
    send: async () => {
      sends++
      throw new Error('Unexpected platform send')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  let extraReads = 0,
    actions = 0
  engine.runtime.addExtension({
    tools: [
      {
        name: 'web-read.read',
        effect: 'read',
        description: 'Synthetic local web receipt',
        inputSchema: { type: 'object', properties: {} },
      },
      {
        name: 'fixture.propose',
        effect: 'learningCandidate',
        description: 'Synthetic unknown action outcome',
        inputSchema: { type: 'object', properties: {} },
      },
    ],
    ...(options.extras === false
      ? {}
      : {
          contextData: () => ({
            trust: 'untrusted_image_descriptions',
            framePolicy: 'first_frame_only',
            images: [
              { messageId: 'source', status: 'unavailable', reason: 'synthetic_image_unconfirmed' },
            ],
          }),
        }),
    execute: async (name) => {
      if (name === 'fixture.propose') {
        actions++
        return { note: 'No confirmation is available.' }
      }
      extraReads++
      return {
        url: 'https://example.invalid/synthetic',
        title: 'Synthetic source',
        coverage: 'partial',
        body: 'RAW_BODY_NOT_FOR_REPLYER',
      }
    },
  })
  let records: Array<ReturnType<Engine['memory']['create']>>
  try {
    records = (options.contents ?? [longMemory]).map((content) =>
      engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id },
        kind: 'claim',
        epistemicKind: 'reported',
        content,
        occurredAt: '2026-10-01T00:00:00Z',
        validUntil: '2099-01-01T00:00:00Z',
      }),
    )
  } catch (error) {
    await engine.dispose()
    store.close()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    throw error
  }
  let resolveDone!: () => void
  const done = new Promise<void>((resolve) => {
    resolveDone = resolve
  })
  const log = store.log.bind(store)
  store.log = (...args) => {
    const result = log(...args)
    if (args[3]?.stage === 'turn_cleanup' && args[3]?.status === 'completed') resolveDone()
    return result
  }
  return {
    store,
    engine,
    bodies,
    captures,
    records,
    setPlanner(next: typeof planner) {
      planner = next
    },
    sends: () => sends,
    extraReads: () => extraReads,
    actions: () => actions,
    async run() {
      const now = new Date().toISOString()
      engine.runtime.ingest(
        {
          id: 'source',
          sceneId: scene.id,
          senderId: 'synthetic-person',
          origin: 'live',
          status: 'available',
          sentAt: now,
          receivedAt: now,
          parts: [
            { type: 'text', text: '请区分未确认与已确认。Literal "quotes", \\ path and 😀.' },
          ],
        },
        scene,
      )
      await done
      assert.deepEqual(errors, [])
      assert.equal(sends, 0)
    },
    reply() {
      const request = bodies.find(
        (body) => body.tools.length === 1 && body.tools[0]!.function.name === 'messages__send',
      )
      assert.ok(request, 'a Replyer request must actually reach the loopback provider')
      const content = request.messages.find((message) => message.role === 'user')!.content
      return { content, data: JSON.parse(content) }
    },
    async close() {
      await engine.dispose()
      store.close()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}

function ordinaryPlan(h: Awaited<ReturnType<typeof fixture>>, select = true) {
  h.setPlanner((body, round) => {
    if (round === 1)
      return {
        calls: [
          call('memory.search', { query: 'TailCandidate' }, 'memory-read'),
          call('web-read.read', {}, 'web-read'),
        ],
      }
    const [memory, web] = receipts(body)
    assert.ok(Array.isArray(memory!.value) && memory!.value.length)
    const source = JSON.parse(
      body.messages.find((message) => message.role === 'user')!.content,
    ).evidence.find((receipt: { value?: { parts?: unknown } }) =>
      Array.isArray(receipt.value?.parts),
    )
    return {
      calls: [
        planCall(body, {
          strategy: 'MEMORY_RESTATEMENT_STRATEGY',
          expressionGoals: ['MEMORY_RESTATEMENT_GOAL'],
          replySuggestion: draft,
          constraints: ['Do not describe unknown actions or unavailable images as confirmed.'],
          memorySelections: select
            ? [
                {
                  evidenceId: memory!.id,
                  memoryIds: [(memory!.value as Array<{ id: string }>)[0]!.id],
                },
              ]
            : [],
          facts: [
            { text: memoryFact, evidenceIds: [memory!.id] },
            { text: mixedFact, evidenceIds: [memory!.id, web!.id] },
            { text: webFact, evidenceIds: [web!.id] },
            { text: messageFact, evidenceIds: [source.id] },
          ],
          actions: [{ toolName: 'fixture.propose', arguments: {} }],
        }),
      ],
    }
  })
}

test(
  'real Runtime preserves complete long-memory qualifications and reports exact projected UTF8 bytes delivered to the provider',
  { timeout: 15000 },
  async () => {
    const h = await fixture()
    try {
      ordinaryPlan(h)
      await h.run()
      const { content, data } = h.reply()
      const receipt = data.evidence.find(
        (item: { toolName: string }) => item.toolName === 'memory.search',
      )
      assert.equal(receipt.memories[0].content, longMemory)
      assert.ok(receipt.memories[0].content.length > 1200)
      assert.ok(receipt.memories[0].content.endsWith(tail))
      assert.equal(receipt.memories[0].epistemicKind, 'reported')
      assert.equal(receipt.memories[0].occurredAt, '2026-10-01T00:00:00Z')
      assert.equal(receipt.memories[0].validUntil, '2099-01-01T00:00:00Z')
      assert.equal(data.memoryEvidenceCoverage.status, 'complete')
      assert.equal(data.plan.replySuggestion, draft)
      const captured = h.captures
        .find((capture) => capture.role === 'replyer')!
        .messages.find((message) => message.role === 'user')!.content
      assert.equal(content, captured, 'native transport must receive the exact projected string')
      const measured = h.store.logs.find(
        (log) => log.stage === 'memory.handoff' && log.status === 'completed',
      )!.counts!
      assert.equal(measured.serializedBytes, Buffer.byteLength(content, 'utf8'))
      assert.ok(measured.serializedBytes! > content.length)
      assert.ok(measured.serializedBytes! <= h.store.settings.replyEvidenceMaxBytes)
      assert.doesNotMatch(JSON.stringify(measured), /tokens|tokenizer|exactTokens/i)
      assert.doesNotMatch(content, /contentTruncated|RAW_BODY_NOT_FOR_REPLYER/)
    } finally {
      await h.close()
    }
  },
)

for (const reason of ['budget', 'missing-selection'] as const)
  test(
    `real Replyer ${reason} omits the entire memory and dependent mixed-source conclusions but retains unrelated evidence and cautions`,
    { timeout: 15000 },
    async () => {
      const h = await fixture({
        settings: reason === 'budget' ? { replyEvidenceMaxBytes: 4096 } : {},
      })
      try {
        ordinaryPlan(h, reason !== 'missing-selection')
        await h.run()
        const { content, data } = h.reply()
        assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
        assert.equal(data.memoryEvidenceCoverage.omitted.length, 1)
        if (reason === 'missing-selection')
          assert.equal(data.memoryEvidenceCoverage.omitted[0].reason, 'missing_memory_selection')
        assert.deepEqual(
          data.plan.facts.map((fact: { text: string }) => fact.text),
          [webFact, messageFact],
        )
        assert.doesNotMatch(
          content,
          /TailCandidate|MEMORY_DEPENDENT_CONCLUSION|MIXED_WEB_CONCLUSION|MEMORY_RESTATEMENT|RAW_BODY_NOT_FOR_REPLYER/,
        )
        assert.equal(data.plan.replySuggestion, undefined)
        assert.deepEqual(data.plan.expressionGoals, [])
        assert.equal(data.actionReceipts[0].status, 'unconfirmed')
        assert.equal(h.actions(), 1)
        assert.match(content, /first_frame_only|untrusted_image_descriptions/)
        assert.match(content, /synthetic_image_unconfirmed/)
        assert.match(content, /Do not describe unknown actions/)
        assert.match(content, /https:\/\/example.invalid\/synthetic/)
        assert.ok(Buffer.byteLength(content, 'utf8') <= h.store.settings.replyEvidenceMaxBytes)
        const stats = h.store.logs.find(
          (log) => log.stage === 'memory.handoff' && log.status === 'completed',
        )!.counts!
        assert.equal(stats.serializedBytes, Buffer.byteLength(content, 'utf8'))
        assert.equal(stats.omittedCount, 1)
      } finally {
        await h.close()
      }
    },
  )

test(
  'actual cumulative Planner omits a broad conflicting receipt atomically with coverage, then a narrow query can restore a complete record',
  { timeout: 15000 },
  async () => {
    const narrow = 'TailCandidate narrowtoken: only this occasion, still unconfirmed.'
    const h = await fixture({
      contents: [
        narrow,
        `TailCandidate contrary correction ${'中文 "\\\n'.repeat(2500)}not permanent.`,
      ],
      extras: false,
      settings: { plannerEvidenceMaxBytes: 32768 },
    })
    try {
      h.setPlanner((body, round) => {
        if (round === 1)
          return { calls: [call('memory.search', { query: 'TailCandidate' }, 'broad-read')] }
        const seen = receipts(body)
        assert.deepEqual(seen[0]!.value, [])
        assert.deepEqual(seen[0]!.coverage, {
          status: 'limited',
          returnedRecords: 0,
          omittedRecords: 2,
          reason: 'planner_evidence_budget',
        })
        if (round === 2)
          return { calls: [call('memory.search', { query: 'narrowtoken' }, 'narrow-read')] }
        const restored = seen[1]!
        assert.ok(Array.isArray(restored.value))
        assert.equal(restored.value.length, 1)
        assert.equal(restored.value[0]!.content, narrow)
        assert.equal(restored.coverage?.status, 'complete')
        return {
          calls: [
            planCall(body, {
              memorySelections: [{ evidenceId: restored.id, memoryIds: [restored.value[0]!.id] }],
            }),
          ],
        }
      })
      await h.run()
      assert.match(h.reply().content, /only this occasion, still unconfirmed/)
      assert.doesNotMatch(h.reply().content, /contrary correction/)
      const plannerRequests = h.bodies.filter((body) =>
        body.tools.some((tool) => tool.function.name === 'reply__plan'),
      )
      const canonical = h.captures.filter((capture) =>
        capture.messages.some(
          (message) => message.role === 'user' && message.content.includes('planContract'),
        ),
      )
      assert.equal(canonical.length, 3)
      assert.equal(plannerRequests.length, 3)
      for (const [index, request] of canonical.entries()) {
        const bytes = Buffer.byteLength(JSON.stringify(request.messages), 'utf8')
        assert.ok(
          bytes <= 32768,
          `request ${index} must include system/user/assistant/tool envelopes within its cap`,
        )
        assert.deepEqual(
          plannerRequests[index]!.messages.map((message) => message.content),
          request.messages.map((message) => message.content),
        )
        if (index > 0) {
          assert.ok(
            request.messages.some(
              (message) => message.role === 'assistant' && message.toolCalls?.length,
            ),
          )
          assert.ok(request.messages.some((message) => message.role === 'tool'))
          assert.ok(
            bytes >
              request.messages.reduce(
                (sum, message) => sum + Buffer.byteLength(message.content),
                0,
              ),
            'JSON escaping and assistant/tool structure cannot be counted as content only',
          )
        }
      }
      assert.ok(
        Buffer.byteLength(JSON.stringify(canonical[2]!.messages)) >
          Buffer.byteLength(JSON.stringify(canonical[1]!.messages)),
      )
    } finally {
      await h.close()
    }
  },
)

test(
  'actual Planner member validation rejects a previously seen ID when cited through a later omitted memory receipt',
  { timeout: 15000 },
  async () => {
    const h = await fixture({
      contents: [
        'TailCandidate narrowtoken: safe small record.',
        `TailCandidate ${'中文 "\\\n'.repeat(2500)}opposite condition.`,
      ],
      extras: false,
      settings: { plannerEvidenceMaxBytes: 32768 },
    })
    try {
      h.setPlanner((body, round) => {
        if (round === 1)
          return { calls: [call('memory.search', { query: 'narrowtoken' }, 'first-read')] }
        if (round === 2)
          return { calls: [call('memory.search', { query: 'TailCandidate' }, 'second-read')] }
        const seen = receipts(body)
        assert.ok(Array.isArray(seen[0]!.value) && seen[0]!.value.length === 1)
        assert.deepEqual(seen[1]!.value, [])
        assert.equal(seen[1]!.coverage?.omittedRecords, 2)
        return {
          calls: [
            planCall(body, {
              memorySelections: [{ evidenceId: seen[1]!.id, memoryIds: [seen[0]!.value[0]!.id] }],
            }),
          ],
        }
      })
      await h.run()
      assert.equal(h.captures.filter((capture) => capture.role === 'replyer').length, 0)
      // The diagnostic sanitizer intentionally withholds unregistered exception text.
      assert.ok(h.store.logs.some((log) => log.stage === 'planner' && log.status === 'failed'))
    } finally {
      await h.close()
    }
  },
)

test(
  'actual Planner counts an escaped assistant tool-call message before executing reads or sending the next model request',
  { timeout: 15000 },
  async () => {
    const h = await fixture({
      contents: [],
      extras: false,
      settings: { plannerEvidenceMaxBytes: 32768 },
    })
    try {
      h.setPlanner(() => ({
        calls: [call('web-read.read', {}, 'never-execute')],
        content: '😀 "\\\n'.repeat(7000),
      }))
      await h.run()
      assert.equal(h.extraReads(), 0)
      assert.equal(
        h.bodies.filter((body) => body.tools.some((tool) => tool.function.name === 'reply__plan'))
          .length,
        1,
      )
      assert.equal(h.captures.filter((capture) => capture.role === 'replyer').length, 0)
      assert.ok(h.store.logs.some((log) => log.errorCode === 'planner_evidence_budget_exhausted'))
    } finally {
      await h.close()
    }
  },
)

test(
  'upstream Planner budget omissions remain visible in the final wire even without memory selections or facts',
  { timeout: 15000 },
  async () => {
    const h = await fixture({
      contents: [`TailCandidate ${'中文 "\\\n'.repeat(2500)}unconfirmed.`],
      extras: false,
      settings: { plannerEvidenceMaxBytes: 32768 },
    })
    try {
      h.setPlanner((body, round) => {
        if (round === 1)
          return { calls: [call('memory.search', { query: 'TailCandidate' }, 'broad-read')] }
        const receipt = receipts(body)[0]!
        assert.deepEqual(receipt.value, [])
        assert.equal(receipt.coverage?.omittedRecords, 1)
        return {
          calls: [
            planCall(body, {
              facts: [],
              memorySelections: [],
              strategy: 'UNSUPPORTED_EMPTY_LIBRARY_STRATEGY',
              expressionGoals: ['UNSUPPORTED_EMPTY_LIBRARY_GOAL'],
              replySuggestion: 'UNSUPPORTED_EMPTY_LIBRARY_DRAFT: There are no remembered plans.',
            }),
          ],
        }
      })
      await h.run()
      const { content, data } = h.reply()
      assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
      assert.equal(data.memoryEvidenceCoverage.omitted.length, 1)
      assert.equal(data.memoryEvidenceCoverage.omitted[0].reason, 'planner_evidence_budget')
      assert.equal(data.memoryEvidenceCoverage.omitted[0].omittedRecords, 1)
      assert.doesNotMatch(content, /UNSUPPORTED_EMPTY_LIBRARY|There are no remembered plans/)
      assert.equal(data.plan.replySuggestion, undefined)
      assert.deepEqual(data.plan.expressionGoals, [])
    } finally {
      await h.close()
    }
  },
)

for (const delivered of [false, true])
  test(
    `actual ${delivered ? 'delivered' : 'budget-omitted'} memory ${delivered ? 'retains' : 'does not create'} an exposure cancellation dependency`,
    { timeout: 15000 },
    async () => {
      const h = await fixture({
        contents: [
          delivered
            ? 'TailCandidate small complete qualification.'
            : `TailCandidate ${'中文 "\\\n'.repeat(2500)}unconfirmed.`,
        ],
        extras: false,
        settings: { plannerEvidenceMaxBytes: 32768 },
      })
      try {
        let changed = false
        h.setPlanner((body, round) => {
          if (round === 1)
            return {
              calls: [call('memory.search', { query: 'TailCandidate' }, 'read-before-change')],
            }
          const receipt = receipts(body)[0]!
          assert.ok(Array.isArray(receipt.value))
          assert.equal(receipt.value.length, delivered ? 1 : 0)
          assert.equal(receipt.coverage?.status, delivered ? 'complete' : 'limited')
          h.engine.memory.update(h.records[0]!.id, { locked: true })
          changed = true
          return { calls: [planCall(body)] }
        })
        await h.run()
        assert.equal(changed, true)
        const replyCalls = h.captures.filter((capture) => capture.role === 'replyer').length
        assert.equal(replyCalls, delivered ? 0 : 1)
        if (!delivered) assert.equal(h.reply().data.memoryEvidenceCoverage.status, 'omitted')
        else
          assert.ok(
            h.store.logs.some(
              (log) =>
                log.stage === 'planner' && (log.status === 'failed' || log.status === 'cancelled'),
            ),
          )
      } finally {
        await h.close()
      }
    },
  )
