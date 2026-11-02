import test from 'node:test'
import assert from 'node:assert/strict'
import { Runtime, type RuntimeExtension, type TurnContext } from '../src/runtime.ts'
import { Store } from '../src/store.ts'
import { PluginManager } from '../src/plugins/manager.ts'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'

test('shared cache integrates plugin opt-in, oversized reads, planner delivery and later-turn isolation', async () => {
  const store = new Store(':memory:')
  const runtime = new Runtime(store)
  const extensions: RuntimeExtension[] = []
  const add = runtime.addExtension.bind(runtime)
  runtime.addExtension = (extension) => {
    extensions.push(extension)
    return add(extension)
  }
  let authorityValid = true
  let authorityOrigin: TurnContext | undefined
  runtime.addExtension({
    tools: [],
    execute: async () => null,
    captureToolResultGuards: (context) =>
      context === authorityOrigin ? [() => authorityValid] : [],
  })
  const manager = new PluginManager(store, runtime)
  let executions = 0
  const payload = 'large response '.repeat(100000)
  const context = (scene = 'scene', mode: 'real' | 'simulation' = 'real'): TurnContext => ({
    scene: {
      id: scene,
      kind: mode === 'real' ? 'group' : 'simulation',
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: scene,
    },
    mode,
    turnId: crypto.randomUUID(),
    signal: new AbortController().signal,
    dependencies: new Set(['source']),
    validations: [],
  })
  try {
    await manager.register({
      manifest: {
        id: 'cache-fixture',
        name: 'Fixture',
        version: '1',
        description: 'Fixture',
        capabilities: [],
        configSchema: { type: 'object' },
      },
      setup(plugin) {
        for (const name of ['large', 'explicit'])
          plugin.registerTool({
            name: `cache-fixture.${name}`,
            description: 'Fixture',
            effect: 'read',
            inputSchema: { type: 'object' },
            ...(name === 'explicit' ? { resultPreviewBytes: 16 } : {}),
            execute: async () => {
              executions++
              return { text: name === 'large' ? payload : 'short exact JSON' }
            },
          })
        plugin.registerTool({
          name: 'cache-fixture.stream',
          description: 'Fixture',
          effect: 'read',
          inputSchema: { type: 'object' },
          execute: async (_, turn) => ({
            status: 'ok',
            body: await turn.cacheResult(
              (async function* () {
                yield Buffer.from(payload)
              })(),
              { encoding: 'utf8', contentType: 'text/plain' },
            ),
          }),
        })
      },
    })
    await manager.enable('cache-fixture')
    const extension = extensions.find((entry) =>
      entry.tools.some((tool) => tool.name === 'cache-fixture.large'),
    )!
    const retrieval = extensions.find((entry) =>
      entry.tools.some((tool) => tool.name === 'tool-results.read'),
    )!
    const initial = context()
    authorityOrigin = initial
    const originalTurn = new AbortController()
    initial.signal = originalTurn.signal
    initial.validations.push(() => !originalTurn.signal.aborted)
    const receipt = (await extension.execute('cache-fixture.large', {}, initial)) as Record<
      string,
      unknown
    >
    assert.equal(receipt.complete, true)
    assert.equal(receipt.totalBytes, Buffer.byteLength(JSON.stringify({ text: payload })))
    assert.equal(receipt.truncated, true)
    assert.equal(executions, 1)
    originalTurn.abort('turn_finished')
    const later = context()
    const page = (await retrieval.execute(
      'tool-results.read',
      { resultId: receipt.resultId, offset: 1000000, length: 128, encoding: 'utf8' },
      later,
    )) as Record<string, any>
    assert.equal(page.content, JSON.stringify({ text: payload }).slice(1000000, 1000128))
    assert.ok(later.dependencies.has('source'))
    assert.equal(executions, 1, 'retrieval never re-executes original tool')
    const full = (await retrieval.execute(
      'tool-results.read',
      { resultId: receipt.resultId, full: true },
      context(),
    )) as Record<string, any>
    assert.equal(full.requestComplete, false)
    assert.ok(full.nextOffset > 0)
    const budget = new PlannerEvidenceBudget()
    const packed = budget.receipt(
      [],
      { id: 'e1', toolName: 'tool-results.read', value: full },
      'call1',
    )
    assert.ok(Buffer.byteLength(packed.message.content as string) < 65536)
    for (const other of [context('another'), context('scene', 'simulation')]) {
      const denied = (await retrieval.execute(
        'tool-results.read',
        { resultId: receipt.resultId },
        other,
      )) as Record<string, unknown>
      assert.equal(denied.error, 'tool_result_not_found_or_expired')
    }
    const explicit = (await extension.execute('cache-fixture.explicit', {}, context())) as Record<
      string,
      any
    >
    assert.equal(explicit.returnedRange.length, 16)
    const complete = (await retrieval.execute(
      'tool-results.read',
      { resultId: explicit.resultId, full: true },
      context(),
    )) as Record<string, any>
    assert.deepEqual(JSON.parse(complete.content), { text: 'short exact JSON' })
    assert.equal(complete.requestComplete, true)
    const stream = (await extension.execute('cache-fixture.stream', {}, context())) as Record<
      string,
      any
    >
    assert.equal(stream.status, 'ok')
    assert.equal(stream.body.totalBytes, Buffer.byteLength(payload))
    const derived = (await extension.execute('cache-fixture.explicit', {}, later)) as Record<
      string,
      any
    >
    authorityValid = false
    const revoked = (await retrieval.execute(
      'tool-results.read',
      { resultId: derived.resultId },
      context(),
    )) as Record<string, any>
    assert.equal(revoked.error, 'tool_result_invalidated')
    authorityValid = true
    retrieval.recalled?.('scene', 'source')
    const invalidated = (await retrieval.execute(
      'tool-results.read',
      { resultId: receipt.resultId },
      context(),
    )) as Record<string, any>
    assert.equal(invalidated.error, 'tool_result_not_found_or_expired')
    await manager.disable('cache-fixture')
    const disabled = (await retrieval.execute(
      'tool-results.read',
      { resultId: explicit.resultId },
      context(),
    )) as Record<string, any>
    assert.equal(disabled.error, 'tool_result_not_found_or_expired')
  } finally {
    await manager.dispose()
    runtime.dispose()
    store.close()
  }
})
