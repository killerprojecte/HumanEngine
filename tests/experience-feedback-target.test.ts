import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import { RecoveryAuthority, metadata } from '../src/memory/recovery/ledger.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'feedback-target-'))
  const store = new Store(join(root, 'data', 'synthetic.sqlite'))
  store.set('settings', { ...store.settings, memoryExperienceEnabled: true })
  const scene = {
    id: `ob:s:${Buffer.from(JSON.stringify(['offline', '123', 'group', '456'])).toString('base64url')}`,
    adapterId: 'offline',
    accountId: '123',
    kind: 'group' as const,
    platformSceneId: '456',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  engine.router = () => {
    throw new Error('model forbidden')
  }
  const internal = engine.runtime as any
  internal.scenes.set(scene.id, scene)
  const state = internal.state(scene.id)
  const at = new Date().toISOString()
  const message = (id: string, patch: Partial<RuntimeMessage> = {}): RuntimeMessage => ({
    id,
    sceneId: scene.id,
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: at,
    receivedAt: at,
    parts: [{ type: 'text', text: 'Synthetic history' }],
    ...patch,
  })
  state.messages = Array.from({ length: 500 }, (_, i) => message('m' + i))
  const target = message('m499', { senderId: 'bot' })
  const source = message('m498', {
    replyToId: target.id,
    parts: [{ type: 'text', text: '请先说结论' }],
  })
  state.messages[499] = target
  state.messages[498] = source
  return {
    store,
    engine,
    internal,
    state,
    scene,
    source,
    target,
    message,
    received: (input = source) => (engine as any).experienceRuntime.received(input),
    outcomes: () =>
      (
        store.db.prepare('SELECT count(*) AS n FROM memory_experience_outcomes').get() as {
          n: number
        }
      ).n,
    async close() {
      await engine.dispose()
      await store.closeLogs()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

for (const present of [false, true])
  test(`500-row quoted feedback projects only its exact target, present=${present}`, async () => {
    const f = fixture()
    const projected: string[] = []
    const original = f.store.presentMessage.bind(f.store)
    f.store.presentMessage = (message) => {
      projected.push(message.id)
      return original(message)
    }
    f.engine.runtime.preview = () => {
      throw new Error('whole-history preview forbidden')
    }
    try {
      const value = present ? f.source : { ...f.source, replyToId: 'missing' }
      assert.equal(f.received(value), undefined, 'feedback remains synchronous')
      assert.deepEqual(projected, present ? [f.target.id, f.source.id, f.target.id] : [])
      assert.equal(f.outcomes(), Number(present))
      assert.equal(f.state.messages.length, 500)
    } finally {
      await f.close()
    }
  })

test('plain feedback has no target, recovery or display reads', async () => {
  const f = fixture()
  f.engine.runtime.previewMessage = () => {
    throw new Error('unexpected target lookup')
  }
  const ready = f.store.assertRecoveryReady.bind(f.store)
  f.store.assertRecoveryReady = () => {
    throw new Error('unexpected recovery read')
  }
  try {
    const { replyToId: _ignored, ...plain } = f.source
    assert.equal(f.received(plain), undefined)
  } finally {
    f.store.assertRecoveryReady = ready
    await f.close()
  }
})

for (const revoked of ['durable', 'local'] as const)
  test(`single-target reads preserve ${revoked} recall placeholders and reject feedback`, async () => {
    const f = fixture()
    try {
      if (revoked === 'durable') f.store.recall(f.scene.id, f.target.id)
      else f.state.recalled.add(f.target.id)
      const expected = f.engine.runtime.preview(f.scene.id).find((row) => row.id === f.target.id)
      const actual = f.engine.runtime.previewMessage(f.scene.id, f.target.id)
      assert.deepEqual(actual, expected)
      assert.equal(actual?.status, 'recalled')
      assert.deepEqual(actual?.parts, [])
      f.received()
      assert.equal(f.outcomes(), 0)
      assert.equal(f.target.parts.length, 1, 'projection does not mutate retained evidence')
    } finally {
      await f.close()
    }
  })

test('target/source permission and recall checks remain fresh inside feedback admission', async () => {
  for (const deny of [
    'learning',
    'feature',
    'source-recall',
    'target-recall-during-lookup',
  ] as const) {
    const f = fixture()
    try {
      if (deny === 'learning')
        f.store.saveScene({ ...f.store.scene(f.scene.id)!, learningEnabled: false })
      if (deny === 'feature')
        f.store.set('settings', { ...f.store.settings, memoryExperienceEnabled: false })
      if (deny === 'source-recall') f.store.recall(f.scene.id, f.source.id)
      if (deny === 'target-recall-during-lookup') {
        const uses = f.engine.experience.targetUses.bind(f.engine.experience)
        f.engine.experience.targetUses = (...args) => {
          const value = uses(...args)
          f.store.recall(f.scene.id, f.target.id)
          return value
        }
      }
      if (deny === 'source-recall' || deny === 'target-recall-during-lookup')
        assert.throws(() => f.received(), /experience_feedback_not_authorized/)
      else f.received()
      assert.equal(f.outcomes(), 0, deny)
    } finally {
      await f.close()
    }
  }
})

test('single-target projection is scene-scoped and never searches another scene', async () => {
  const f = fixture()
  try {
    const foreign = { ...f.scene, id: 'foreign' }
    f.internal.scenes.set(foreign.id, foreign)
    f.internal.state(foreign.id).messages = [{ ...f.target, sceneId: foreign.id }]
    f.state.messages = f.state.messages.filter((row: RuntimeMessage) => row.id !== f.target.id)
    assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id), undefined)
    assert.equal(f.engine.runtime.previewMessage('unknown', f.target.id), undefined)
    f.received()
    assert.equal(f.outcomes(), 0)
    // Also fail closed if a trusted caller accidentally violates the retained-state invariant.
    f.state.messages.push({ ...f.target, sceneId: foreign.id })
    assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id), undefined)
    f.received()
    assert.equal(f.outcomes(), 0)
  } finally {
    await f.close()
  }
})

test('single-target projection preserves fresh identity and returns detached nested values', async () => {
  const f = fixture()
  try {
    f.target.sourceEventRef = { eventId: 'synthetic-event', revision: 1, deletionEpoch: 0 }
    f.store.set(`identity:${f.scene.id}:bot`, {
      senderDisplayName: 'Before',
      senderPlatformId: '789',
    })
    const first = f.engine.runtime.previewMessage(f.scene.id, f.target.id)!
    assert.deepEqual(
      first,
      f.engine.runtime.preview(f.scene.id).find((row) => row.id === f.target.id),
    )
    first.parts.length = 0
    first.sourceEventRef!.revision++
    f.store.set(`identity:${f.scene.id}:bot`, {
      senderDisplayName: 'After',
      senderPlatformId: '789',
    })
    const second = f.engine.runtime.previewMessage(f.scene.id, f.target.id)!
    assert.equal(second.senderDisplayName, 'After')
    assert.equal(second.parts.length, 1)
    assert.equal(second.sourceEventRef!.revision, 1)
    assert.equal(f.target.parts.length, 1)
    assert.equal(f.target.sourceEventRef.revision, 1)
  } finally {
    await f.close()
  }
})

test('single-target lookup keeps expiration, cache-disable and simulation trimming behavior', async () => {
  const f = fixture()
  try {
    f.target.receivedAt = '2000-01-01T00:00:00Z'
    assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id), undefined)
    assert.equal(
      f.state.messages.some((row: RuntimeMessage) => row.id === f.target.id),
      false,
    )
    f.store.set('settings', { ...f.store.settings, cacheEnabled: false })
    assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.source.id), undefined)
    assert.equal(f.state.messages.length, 0)
    const sim = f.engine.runtime.createSimulation()
    const simulation = { ...f.target, sceneId: sim, origin: 'simulation' as const }
    f.internal.state(sim).messages = [simulation]
    assert.deepEqual(
      f.engine.runtime.previewMessage(sim, simulation.id),
      f.engine.runtime.preview(sim)[0],
    )
    assert.equal(f.internal.state(sim).messages.length, 1)
  } finally {
    await f.close()
  }
})

test('single-target projection retains nested-transaction reads and fail-closed recovery checks', async () => {
  const f = fixture()
  const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
  try {
    f.store.db.transaction(() => {
      assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id)?.status, 'available')
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run(f.scene.id, f.target.id, new Date().toISOString())
      assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id)?.status, 'recalled')
      assert.equal(f.store.db.inTransaction, true)
    })()
    authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(
      () => f.engine.runtime.previewMessage(f.scene.id, f.target.id),
      /recovery_chain_invalid/,
    )
    assert.throws(
      () => f.engine.runtime.previewMessage(f.scene.id, 'missing'),
      /recovery_chain_invalid/,
    )
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    authority.close()
    await f.close()
  }
})

test('single-target projection denies writes within its scope and restores transaction state', async () => {
  const f = fixture()
  const original = f.store.presentMessage.bind(f.store)
  f.store.presentMessage = (message) => {
    f.store.db
      .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
      .run(f.scene.id, message.id, new Date().toISOString())
    return original(message)
  }
  try {
    assert.throws(() => f.engine.runtime.previewMessage(f.scene.id, f.target.id), /readonly/)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    f.store.presentMessage = original
    assert.equal(f.engine.runtime.previewMessage(f.scene.id, f.target.id)?.status, 'available')
  } finally {
    f.store.presentMessage = original
    await f.close()
  }
})
