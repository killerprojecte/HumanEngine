import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'

const scene: SceneRef = {
  id: 'exposure-handoff-scene',
  kind: 'group',
  adapterId: 'offline-fixture',
  accountId: 'offline-bot',
  platformSceneId: 'synthetic-group',
}
const otherScene: SceneRef = { ...scene, id: 'other-exposure-scene', accountId: 'other-bot' }
const originalContent = 'Original synthetic handoff premise.'
function fixture(learned = false) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, learningBatchMs: 300000 })
  for (const item of [scene, otherScene])
    store.saveScene({
      ...item,
      name: 'Synthetic handoff scene',
      participationEnabled: false,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const engine = new Engine(store)
  let sends = 0,
    indexes = 0
  engine.runtime.addAdapter(scene.adapterId, {
    send: async () => {
      sends++
      throw new Error('Unexpected synthetic transport invocation')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  engine.router = () => {
    throw new Error('Unexpected external model invocation')
  }
  engine.indexMemory = async () => {
    indexes++
  }
  const source = {
    sceneId: scene.id,
    messageId: 'parent-source',
    senderId: 'synthetic-person',
  }
  let parent: MemoryRecord
  if (learned) {
    const result = engine.memory.propose(
      {
        sceneId: scene.id,
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      {
        content: originalContent,
        kind: 'claim',
        personId: source.senderId,
        sources: [source],
      },
    )
    assert.equal(result.status, 'saved')
    if (result.status !== 'saved') throw new Error('parent_fixture_failed')
    parent = result.memory
  } else
    parent = engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id, personId: source.senderId },
      content: originalContent,
      kind: 'claim',
    })
  const extension = (
    engine.runtime as unknown as { extensions: RuntimeExtension[] }
  ).extensions.find((item) => item.tools.some((tool) => tool.name === 'memory.search'))!
  assert.ok(extension, 'exercise the extension actually registered by Engine')
  let turn = 0
  const context = (target = scene): TurnContext => ({
    scene: target,
    mode: 'real',
    turnId: `synthetic-handoff-${++turn}`,
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  })
  const search = async (ctx: TurnContext, query = 'handoff premise') =>
    (await extension.execute('memory.search', { query }, ctx)) as MemoryRecord[]
  const freshSource = (): RuntimeMessage => ({
    id: 'fresh-candidate-source',
    sceneId: scene.id,
    senderId: source.senderId,
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: 'Synthetic sourced child claim.' }],
  })
  return {
    store,
    engine,
    parent,
    source,
    extension,
    context,
    search,
    freshSource,
    sends: () => sends,
    indexes: () => indexes,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}

for (const mutation of [
  'content',
  'locked',
  'epistemic-and-date',
  'future',
  'delete',
  'recall',
  'scope',
] as const)
  test(`async search handoff drops a ${mutation}-changed snapshot rather than authorizing stale data or substituting new content`, async () => {
    const h = fixture(mutation === 'recall')
    const search = h.engine.memory.search.bind(h.engine.memory)
    let changed = false,
      retrievedOld = false,
      mutationError: unknown
    h.engine.memory.search = (...args) => {
      const result = search(...args)
      if (!changed) {
        retrievedOld = result.some(
          (item) => item.id === h.parent.id && item.content === originalContent,
        )
        queueMicrotask(() => {
          try {
            if (mutation === 'content')
              h.engine.memory.update(h.parent.id, {
                content: 'Corrected synthetic handoff premise.',
              })
            else if (mutation === 'locked') h.engine.memory.update(h.parent.id, { locked: true })
            else if (mutation === 'epistemic-and-date')
              h.engine.memory.update(h.parent.id, {
                epistemicKind: 'hypothesis',
                occurredAt: '2000-01-02T03:04:05Z',
              })
            else if (mutation === 'future')
              h.engine.memory.update(h.parent.id, { validFrom: '2100-01-01T00:00:00Z' })
            else if (mutation === 'delete') h.engine.memory.delete(h.parent.id)
            else if (mutation === 'recall') h.engine.memory.recall(scene.id, h.source.messageId)
            else {
              // Test-only storage-migration fault: no supported API permits moving a record's scope.
              const moved = { ...h.parent, scope: { ...h.parent.scope, sceneId: otherScene.id } }
              h.store.db
                .prepare('UPDATE memories SET scope_key=?, value=? WHERE id=?')
                .run(`scene:${otherScene.id}`, JSON.stringify(moved), h.parent.id)
            }
          } catch (error) {
            mutationError = error
          } finally {
            changed = true
          }
        })
      }
      return result
    }
    try {
      const staleTurn = h.context()
      const returned = await h.search(staleTurn)
      assert.ifError(mutationError)
      assert.equal(retrievedOld, true, 'the actual MemoryService search first found the old record')
      assert.equal(changed, true, 'the write must occur during the asynchronous return handoff')
      assert.deepEqual(returned, [], 'neither old metadata nor an unqueried replacement may leak')
      assert.equal(
        staleTurn.validations.length,
        1,
        'only the read-policy guard remains; dropped memory has no proof authority',
      )
      assert.ok(
        staleTurn.validations.every((valid) => valid()),
        'record drift does not affect the policy-only guard',
      )
      assert.deepEqual([...staleTurn.dependencies], [])

      const freshTurn = h.context()
      let fresh = await h.search(freshTurn)
      if (['delete', 'recall', 'scope', 'future'].includes(mutation)) assert.deepEqual(fresh, [])
      if (mutation === 'scope') {
        fresh = await h.search(h.context(otherScene))
        assert.equal(fresh.length, 1)
        assert.deepEqual(fresh[0]!.scope, {
          kind: 'scene',
          sceneId: otherScene.id,
          personId: h.source.senderId,
        })
      } else if (mutation === 'future') {
        h.engine.memory.update(h.parent.id, { validFrom: '2000-01-01T00:00:00Z' })
        fresh = await h.search(h.context())
        assert.equal(fresh.length, 1)
      } else if (!['delete', 'recall'].includes(mutation)) {
        assert.equal(fresh.length, 1, 'a new query must expose the current authorized version')
        assert.ok(freshTurn.validations.length > 0)
        assert.ok(freshTurn.validations.every((validate) => validate()))
        if (mutation === 'content')
          assert.equal(fresh[0]!.content, 'Corrected synthetic handoff premise.')
        else assert.equal(fresh[0]!.content, originalContent)
        if (mutation === 'locked') assert.equal(fresh[0]!.locked, true)
        if (mutation === 'epistemic-and-date') {
          assert.equal(fresh[0]!.epistemicKind, 'hypothesis')
          assert.equal(fresh[0]!.occurredAt, '2000-01-02T03:04:05Z')
        }
      }
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

test('unchanged scored search and person results remain exposed with current source dependencies', async () => {
  const h = fixture(true)
  try {
    const expected = h.engine.memory.search(
      { sceneId: scene.id, simulation: false },
      'handoff premise',
    )
    const turn = h.context()
    const results = await h.search(turn)
    assert.equal(
      results.length,
      1,
      'authority checking must not discard ordinary scored search results',
    )
    assert.deepEqual(results, expected)
    assert.equal(typeof results[0]!.score, 'number')
    assert.ok(results[0]!.score! > 0)
    assert.deepEqual([...turn.dependencies], [h.source.messageId])
    assert.ok(turn.validations.length > 0)
    assert.ok(turn.validations.every((validate) => validate()))
    const personTurn = h.context()
    const person = (await h.extension.execute(
      'memory.person',
      { personId: h.source.senderId },
      personTurn,
    )) as MemoryRecord[]
    assert.equal(person.length, 1)
    assert.equal(person[0]!.id, h.parent.id)
    assert.equal(person[0]!.score, undefined)
    assert.ok(personTurn.validations.every((validate) => validate()))
    assert.deepEqual([...personTurn.dependencies], [h.source.messageId])
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('unchanged records with reordered object keys retain their original retrieval ranking score', async () => {
  const h = fixture()
  const search = h.engine.memory.search.bind(h.engine.memory)
  h.engine.memory.search = (...args) =>
    search(...args).map(
      (record) => Object.fromEntries(Object.entries(record).reverse()) as MemoryRecord,
    )
  try {
    const turn = h.context()
    const results = await h.search(turn)
    assert.equal(results.length, 1)
    assert.equal(results[0]!.id, h.parent.id)
    assert.equal(
      results[0]!.score,
      search({ sceneId: scene.id, simulation: false }, 'handoff premise')[0]!.score,
    )
    assert.ok(turn.validations.every((validate) => validate()))
  } finally {
    await h.close()
  }
})

for (const mutation of ['content', 'locked'] as const)
  test(`a ${mutation} revision after successful exposure rejects the old turn, while a new query can learn`, async () => {
    const h = fixture()
    try {
      h.engine.runtime.ingest(h.freshSource(), scene)
      const staleTurn = h.context()
      assert.equal((await h.search(staleTurn)).length, 1)
      assert.ok(staleTurn.validations.every((validate) => validate()))
      const before = h.engine.memory.revisionState(h.parent.id)
      h.engine.memory.update(
        h.parent.id,
        mutation === 'content'
          ? { content: 'Corrected synthetic handoff premise.' }
          : { locked: true },
      )
      assert.ok(h.engine.memory.revisionState(h.parent.id).version > before.version)
      assert.ok(staleTurn.validations.some((validate) => !validate()))
      const candidate = {
        content: 'Synthetic sourced child claim.',
        kind: 'claim',
        personId: h.source.senderId,
        sourceMessageIds: [h.freshSource().id],
      }
      const rejected = (await h.extension.execute('learning.propose', candidate, staleTurn)) as {
        status: string
      }
      assert.equal(rejected.status, 'rejected')
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
      assert.equal(h.engine.memory.adminList().length, 1)
      const freshTurn = h.context()
      assert.equal((await h.search(freshTurn)).length, 1)
      const queued = (await h.extension.execute('learning.propose', candidate, freshTurn)) as {
        status: string
      }
      assert.equal(queued.status, 'queued')
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(h.engine.contextualLearning.queue.stats().completed, 1)
      assert.equal(h.indexes(), 1)
      const children = h.engine.memory
        .adminList()
        .filter((item) => item.content === candidate.content)
      assert.equal(children.length, 1)
      assert.deepEqual(
        children[0]!.sources.map((source) => source.messageId),
        [h.freshSource().id],
      )
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })
