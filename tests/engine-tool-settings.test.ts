import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { RuntimeExtension } from '../src/runtime.ts'

test('Engine tool definitions read settings once per access and immediately reflect configuration changes', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const original = Object.getOwnPropertyDescriptor(Store.prototype, 'settings')!.get!
  const extension = (
    engine.runtime as unknown as { extensions: RuntimeExtension[] }
  ).extensions.find((extension) =>
    extension.tools.some((tool) => tool.name === 'learning.propose'),
  )!
  const initial = store.settings
  const flags = {
    memoryGraphEnabled: true,
    memoryGraphAnswerEnabled: true,
    memoryKnowledgeEnabled: true,
    memoryExperienceEnabled: true,
    memorySemanticEnabled: true,
    memorySemanticAnswerEnabled: true,
    memoryWorkingSetEnabled: true,
    memoryEpisodesEnabled: true,
    memoryCommitmentsEnabled: true,
    memoryCommitmentsAnswerEnabled: true,
    memoryHistoricalReadEnabled: true,
  }
  let reads = 0
  Object.defineProperty(store, 'settings', {
    configurable: true,
    get() {
      reads++
      return original.call(store)
    },
  })
  const read = () => {
    reads = 0
    const tools = extension.tools
    assert.equal(
      reads,
      1,
      'One synchronous built-in tool definition snapshot should parse settings once',
    )
    return tools
  }
  const conditional = [
    'experience.outcomes',
    'experience.propose',
    'memory.summaries',
    'memory.workingSet',
    'memory.episodes',
    'memory.commitments',
    'memory.commitmentTimeline',
    'memory.commitmentHistory',
    'memory.history',
    'memory.relations',
  ]
  try {
    store.set('settings', { ...initial, ...flags })
    const enabled = read()
    for (const name of conditional)
      assert.ok(
        enabled.some((tool) => tool.name === name),
        name,
      )
    const learning = enabled.find((tool) => tool.name === 'learning.propose')!
    const properties = learning.inputSchema.properties as Record<string, unknown>
    for (const field of ['relations', 'commitment', 'knowledge'])
      assert.ok(properties[field], field)

    store.set('settings', {
      ...initial,
      ...Object.fromEntries(Object.keys(flags).map((key) => [key, false])),
    })
    const disabled = read()
    for (const name of conditional)
      assert.equal(
        disabled.some((tool) => tool.name === name),
        false,
        name,
      )
    const nextLearning = disabled.find((tool) => tool.name === 'learning.propose')!
    const nextProperties = nextLearning.inputSchema.properties as Record<string, unknown>
    for (const field of ['relations', 'commitment', 'knowledge'])
      assert.equal(nextProperties[field], undefined, field)
    assert.notEqual(nextLearning.description, learning.description)
    assert.notEqual(disabled, enabled)

    store.set('settings', { ...initial, ...flags })
    assert.deepEqual(
      read(),
      enabled,
      'A subsequent access must observe restored live settings without stale cross-call caching',
    )
  } finally {
    Reflect.deleteProperty(store, 'settings')
    await engine.dispose()
    store.close()
  }
})
