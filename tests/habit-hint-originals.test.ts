import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { TurnContext } from '../src/runtime.ts'
import type { HabitRecord } from '../src/habits.ts'

test('hint source projection keeps first duplicate JSON ID without repeated array searches', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 'synthetic',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'synthetic',
    platformSceneId: 'synthetic',
  }
  store.saveScene({
    ...scene,
    name: 'synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  try {
    const add = (sqlId: string, sourceId: string) =>
      store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(
        sqlId,
        sqlId,
        JSON.stringify({
          id: 'duplicate',
          sceneId: scene.id,
          kind: 'sceneRule',
          content: 'Synthetic rule',
          evidenceVersion: 2,
          sources: [
            {
              sceneId: scene.id,
              messageId: sourceId,
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
    add('duplicate', 'first-source')
    add('different-sql-id', 'second-source')
    const adminList = engine.habits.adminList.bind(engine.habits)
    let originalReads = 0
    engine.habits.adminList = () => {
      originalReads++
      const originals = adminList()
      originals.find = () => {
        throw Error('repeated linear original lookup')
      }
      return originals
    }
    const context: TurnContext = {
      scene,
      mode: 'real',
      turnId: 'synthetic-turn',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const read = (
      engine as unknown as { learnedHints(context: TurnContext): HabitRecord[] }
    ).learnedHints.bind(engine)
    const hints = read(context)
    assert.deepEqual(
      hints.map((row) => row.id),
      ['duplicate', 'duplicate'],
    )
    assert.deepEqual([...context.dependencies], ['first-source'])
    assert.equal(originalReads, 2)
    assert.equal(
      context.validations.every((validate) => validate()),
      true,
    )
    store.recall(scene.id, 'first-source')
    assert.equal(
      context.validations.every((validate) => validate()),
      false,
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})
