import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { currentFlowContext, startFlow, withFlowContext } from '../src/diagnostics/flow.ts'
import { stdoutLog } from '../src/diagnostics/format.ts'

test('flow spans have one terminal, readable summaries, safe structural errors and isolated concurrent contexts', async () => {
  const store = new Store(':memory:')
  try {
    await Promise.all(
      ['fixture-a', 'fixture-b'].map((traceId) =>
        withFlowContext(
          { traceId, turnId: traceId },
          async () => {
            const span = startFlow(store, 'model_attempt', {
              role: 'thinker',
              apiModel: 'fixture-model',
            })
            await span.run(async () => {
              await new Promise((resolve) => setTimeout(resolve, traceId === 'fixture-a' ? 5 : 1))
              assert.equal(currentFlowContext().traceId, traceId)
              store.log('info', 'fixture', 'inside')
            })
            span.finish('completed', {
              counts: { inputTokens: 10, outputTokens: 4, toolCalls: 1 },
              outputKind: 'tool_calls',
              nextStep: 'tools',
            })
            span.finish('failed', { reason: 'duplicate' })
          },
          store,
        ),
      ),
    )
    for (const traceId of ['fixture-a', 'fixture-b']) {
      const rows = store.logs.filter((row) => row.traceId === traceId)
      assert.equal(rows.filter((row) => row.status === 'started').length, 1)
      assert.equal(rows.filter((row) => row.status === 'completed').length, 1)
      assert.equal(rows.filter((row) => row.status === 'failed').length, 0)
      const end = rows.find((row) => row.status === 'completed')!
      assert.match(end.message!, /用途=.*输入 tokens=10.*工具呼叫数=1/)
      assert.match(stdoutLog(end), /完成: 呼叫模型.*下一步.*\| \{/)
      assert.equal(rows.find((row) => row.code === 'inside')!.turnId, traceId)
    }
    const failed = startFlow(store, 'send_segment')
    failed.finish(
      'failed',
      { reason: 'failed_receipt' },
      new Error('private-message-canary secret-key-canary https://credential.invalid/private'),
    )
    assert.doesNotMatch(
      JSON.stringify(store.logs),
      /private-message-canary|secret-key-canary|credential.invalid/,
    )
  } finally {
    store.close()
  }
})
test('failed logging sinks never change workflow behavior', () => {
  const span = startFlow(
    {
      log() {
        throw new Error('fixture sink failed')
      },
    },
    'turn',
  )
  assert.equal(
    span.run(() => 42),
    42,
  )
  assert.doesNotThrow(() => span.finish('completed'))
  assert.equal(span.ended, true)
})
