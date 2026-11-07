import assert from 'node:assert/strict'
import test from 'node:test'
import { Runtime } from '../src/runtime.ts'
import { Store } from '../src/store.ts'
import type { Router, Completion } from '../src/models.ts'
import { fixturePlanCompletion } from './helpers/reply-plan.ts'

const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})

test('one synchronous tool outcome uses one eligibility result but the next boundary rechecks', async () => {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 5, maxWaitMs: 100 })
  let afterTool = false,
    postToolChecks = 0,
    replyerCalls = 0,
    executions = 0,
    projectionReads = 0
  const output = {
    status: 'sent',
    get projectionCanary() {
      projectionReads++
      return 'synthetic'
    },
  }
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (role, messages) => {
          const plan = fixturePlanCompletion(role, messages, { deliveryTool: 'fixture.send' })
          if (plan) return plan
          if (role === 'planner')
            return completion([
              {
                id: 'decision',
                name: 'participation.decide',
                arguments: { action: 'reply', reasonCode: 'fixture' },
              },
            ])
          replyerCalls++
          return completion([{ id: 'send', name: 'fixture.send', arguments: {} }])
        },
      }) as Router,
  )
  runtime.addExtension({
    contextData(context) {
      context.validations.push(() => !afterTool || ++postToolChecks === 1)
      return { fixture: true }
    },
    tools: [
      {
        name: 'fixture.send',
        description: 'Synthetic send receipt',
        effect: 'send',
        inputSchema: { type: 'object', properties: {}, additionalProperties: false },
      },
    ],
    async execute() {
      executions++
      afterTool = true
      return output
    },
  })
  const scene = {
    id: 's',
    kind: 'group' as const,
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 's',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent', messageId: 'unused' }),
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    const at = new Date().toISOString()
    runtime.ingest(
      {
        id: 'incoming',
        sceneId: 's',
        senderId: 'person',
        origin: 'live',
        status: 'available',
        sentAt: at,
        receivedAt: at,
        parts: [{ type: 'text', text: 'Synthetic question' }],
      },
      scene,
    )
    for (
      let i = 0;
      i < 200 &&
      !store.logs.some(
        (row) =>
          row.stage === 'turn' && ['completed', 'failed', 'cancelled'].includes(row.status ?? ''),
      );
      i++
    )
      await new Promise((done) => setTimeout(done, 5))
    assert.ok(
      store.logs.some(
        (row) =>
          row.stage === 'turn' && ['completed', 'failed', 'cancelled'].includes(row.status ?? ''),
      ),
    )
    const tool = store.logs.find((row) => row.stage === 'tool' && row.status !== 'started')
    assert.ok(tool)
    assert.equal(executions, 1)
    assert.equal(tool.status, 'completed')
    assert.equal(tool.reason, 'tool_returned')
    assert.equal(tool.nextStep, 'replyer')
    assert.ok(postToolChecks >= 2, 'the next use freshly observes revocation')
    assert.equal(projectionReads, 0, 'the immediate next gate rejects revoked tool output')
    assert.equal(replyerCalls, 1, 'fresh denial prevents another model request')
  } finally {
    runtime.dispose()
    store.close()
  }
})
