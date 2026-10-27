import assert from 'node:assert/strict'
import test from 'node:test'
import { EmbeddingIndex } from '../src/embedding-index.ts'
import { MemoryService } from '../src/memory.ts'
import { Router, type RouterConfig } from '../src/models.ts'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'
import { Store } from '../src/store.ts'

process.env.HUMANENGINE_EMBEDDING_SYNTHETIC = 'local-fixture-not-a-real-key'

const config = (dimensions?: number): RouterConfig => ({
  providers: [
    {
      id: 'p',
      name: 'Local synthetic',
      protocol: 'fixture:embed',
      baseUrl: 'http://127.0.0.1/v1',
      credentialEnv: 'HUMANENGINE_EMBEDDING_SYNTHETIC',
    },
  ],
  models: [
    {
      id: 'm',
      displayName: 'Synthetic',
      providerId: 'p',
      apiModelName: 'local',
      capabilities: ['embedding'],
      embeddingBatchSize: 4,
      ...(dimensions ? { embeddingDimensions: dimensions } : {}),
    },
  ],
  routes: [{ role: 'embedding', orderedModelIds: ['m'], timeoutMs: 10000 }],
})
function fixture() {
  const store = new Store(':memory:'),
    memory = new MemoryService(store),
    protocols = new ModelProtocolRegistry()
  store.set('models', config(1024))
  let calls = 0
  let outputWidth: number | undefined
  let hook: (() => void | Promise<void>) | undefined
  protocols.register(
    'fixture:embed',
    'Local synthetic',
    {
      complete: async () => {
        throw new Error('unexpected_chat_replay')
      },
      embeddings: async (ctx, inputs) => {
        calls++
        await hook?.()
        return {
          vectors: inputs.map((text) =>
            Array.from({ length: outputWidth ?? ctx.model?.embeddingDimensions ?? 1024 }, (_, i) =>
              i === 0 ? text.length : 0,
            ),
          ),
          usage: { inputTokens: inputs.length, outputTokens: 0, totalTokens: inputs.length },
        }
      },
    },
    new AbortController().signal,
  )
  let clock = Date.now()
  const now = () => clock
  const index = new EmbeddingIndex(
    store,
    memory,
    (cfg) => new Router(cfg, undefined, undefined, protocols),
    { now },
  )
  const add = (text: string) =>
    memory.create({ scope: { kind: 'scene', sceneId: 's' }, kind: 'fact', content: text })
  return {
    store,
    memory,
    index,
    add,
    get calls() {
      return calls
    },
    setHook(fn?: () => void | Promise<void>) {
      hook = fn
    },
    setWidth(width?: number) {
      outputWidth = width
    },
    protocols,
    now,
    advance(ms: number) {
      clock += ms
    },
  }
}

test('1024 to 4096 rebuild isolates generations, retains old index, and switches atomically', async () => {
  const f = fixture()
  try {
    for (let i = 0; i < 9; i++) f.add(`Synthetic memory ${i}`)
    assert.equal(f.index.status().state, 'needs_rebuild')
    f.index.start()
    await f.index.settled()
    const old = f.index.status().active!
    assert.equal(old.dimensions, 1024)
    assert.equal(f.calls, 3)
    f.store.set('models', config(4096))
    const calls = f.calls
    assert.equal(f.index.status().state, 'needs_rebuild')
    assert.deepEqual(f.index.status().coverage, { eligible: 9, indexed: 0, pending: 9 })
    await f.index.search({ sceneId: 's', simulation: false }, 'Synthetic')
    assert.equal(f.calls, calls, 'stale encoder uses lexical, not a mismatched query')
    f.setHook(() => assert.equal(f.index.status().active?.generationId, old.generationId))
    f.index.start()
    await f.index.settled()
    const next = f.index.status().active!
    assert.equal(next.dimensions, 4096)
    assert.notEqual(next.spaceId, old.spaceId)
    assert.equal(f.index.status().job?.state, 'completed')
    assert.equal(
      (
        f.store.db
          .prepare('SELECT count(*) n FROM memory_vectors WHERE space_id=?')
          .get(old.spaceId) as { n: number }
      ).n,
      9,
    )
    assert.equal(f.index.status().state, 'ready')
    f.setHook()
    await f.index.search({ sceneId: 's', simulation: false }, 'Synthetic')
    assert.equal(f.calls, 7)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('changed content, deletes and recalled provenance during a batch cannot resurrect stale vectors', async () => {
  const f = fixture()
  try {
    const edited = f.add('old content'),
      removed = f.add('delete me')
    f.store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: 'a',
      platformSceneId: 's',
      name: 's',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    const proposed = f.memory.propose(
      {
        sceneId: 's',
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [
          { sceneId: 's', messageId: 'proof', senderId: 'person', available: true, isBot: false },
        ],
      },
      {
        content: 'synthetic learned fact',
        kind: 'fact',
        sources: [{ sceneId: 's', messageId: 'proof', senderId: 'person' }],
      },
    )
    assert.equal(proposed.status, 'saved')
    let changed = false
    f.setHook(() => {
      if (changed) return
      changed = true
      f.memory.update(edited.id, { content: 'new content' })
      f.memory.delete(removed.id)
      f.store.recall('s', 'proof')
      f.add('added during rebuild')
    })
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'completed')
    const rows = f.store.db.prepare('SELECT memory_id FROM memory_vectors').all() as {
      memory_id: string
    }[]
    assert.equal(rows.length, 2)
    assert.ok(rows.some((r) => r.memory_id === edited.id))
    assert.ok(!rows.some((r) => r.memory_id === removed.id))
    assert.equal(f.calls, 2)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('pause, restart and explicit uncertain-batch resume preserve completed checkpoints without automatic requests', async () => {
  const f = fixture()
  try {
    for (let i = 0; i < 5; i++) f.add(`entry ${i}`)
    let release!: () => void
    const blocked = new Promise<void>((resolve) => {
      release = resolve
    })
    let entered!: () => void
    const seen = new Promise<void>((resolve) => {
      entered = resolve
    })
    f.setHook(async () => {
      if (f.calls === 2) {
        entered()
        await blocked
      }
    })
    f.index.start()
    await seen
    f.index.pause()
    release()
    await f.index.settled()
    assert.equal(f.index.status().job?.processed, 4)
    assert.equal(f.index.status().job?.requiresRetryConfirmation, true)
    f.store.set('embedding.rebuild', { ...f.index.status().job!, state: 'running', leaseUntil: 0 })
    const restored = new EmbeddingIndex(
      f.store,
      f.memory,
      (cfg) => new Router(cfg, undefined, undefined, f.protocols),
      { now: f.now },
    )
    assert.equal(f.calls, 2)
    assert.throws(() => restored.resume(), /confirmation_required/)
    f.setHook()
    restored.resume(true)
    await restored.settled()
    assert.equal(f.calls, 3)
    assert.equal(restored.status().job?.state, 'completed')
    await restored.dispose()
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('cancel and mid-flight config change cannot publish, native empty rebuild does not issue a paid probe', async () => {
  const f = fixture()
  try {
    f.store.set('models', config())
    f.index.start()
    await f.index.settled()
    assert.equal(f.calls, 0)
    assert.equal(f.index.status().job?.error, 'embedding_empty_native_dimension_unknown')
    f.add('synthetic')
    f.store.set('models', config(1024))
    f.setHook(() => {
      f.index.cancel()
    })
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'cancelled')
    assert.equal(f.index.status().active, undefined)
    f.setHook(() => {
      f.store.set('models', config(4096))
    })
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'paused')
    assert.equal(f.index.status().active, undefined)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('fresh learning bootstraps once while legacy memories require explicit rebuild; credential rotation is not semantic drift', async () => {
  const f = fixture()
  try {
    const record = f.add('fresh synthetic fact')
    await f.index.index(record.id, record.content)
    assert.equal(f.index.status().state, 'ready')
    assert.equal(f.calls, 1)
    const old = f.index.status().active
    const changed = config(1024)
    changed.providers[0]!.credentialEnv = 'ROTATED_NAME_ONLY'
    f.store.set('models', changed)
    assert.equal(f.index.status().state, 'ready')
    assert.deepEqual(f.index.status().active, old)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
  const store = new Store(':memory:'),
    memory = new MemoryService(store)
  store.set('models', config(1024))
  const existing = memory.create({
    scope: { kind: 'scene', sceneId: 's' },
    content: 'legacy',
    kind: 'fact',
  })
  const index = new EmbeddingIndex(store, memory, () => {
    throw new Error('must_not_call')
  })
  await index.index(existing.id, existing.content)
  assert.equal(index.status().state, 'needs_rebuild')
  await index.dispose()
  store.db.close()
})

test('generation CAS rejects another active publisher and quarantined/unverified learned records are not rebuilt', async () => {
  const f = fixture()
  try {
    const valid = f.add('valid'),
      unverified = f.add('unverified legacy'),
      quarantined = f.add('quarantined')
    for (const [m, lineageStatus] of [
      [unverified, 'unverified'],
      [quarantined, 'quarantined'],
    ] as const)
      f.store.db
        .prepare('UPDATE memories SET value=? WHERE id=?')
        .run(JSON.stringify({ ...m, origin: 'learned', lineageStatus }), m.id)
    f.setHook(() => {
      f.store.set('embedding.active', {
        generationId: 'another-publisher',
        encoderRevision: 'other',
        modelId: 'other',
        dimensions: 1024,
        spaceId: 'other',
      })
    })
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().job?.error, 'embedding_active_generation_changed')
    assert.equal(f.index.status().active?.generationId, 'another-publisher')
    assert.equal(f.index.status().job?.total, 1)
    assert.deepEqual(
      (
        f.store.db.prepare('SELECT memory_id FROM memory_vectors').all() as { memory_id: string }[]
      ).map((r) => r.memory_id),
      [valid.id],
    )
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('sqlite-vec cosine accepts app storage ceiling 16384 and mandatory 4096 without truncation', () => {
  const f = fixture()
  try {
    const record = f.add('vector ceiling')
    for (const dimensions of [4096, 16384]) {
      const space = f.memory.registerSpace({
        providerId: 'p',
        modelId: 'm',
        apiModelName: 'local',
        deployment: 'local',
        dimensions,
        preprocessing: 'text-v1',
        normalized: false,
      })
      const vector = Array.from({ length: dimensions }, (_, i) => (i === 0 ? 1 : 0))
      f.memory.index(record.id, space, vector)
      assert.equal(
        f.memory.search({ sceneId: 's', simulation: false }, 'unmatched', undefined, {
          spaceId: space,
          vector,
        })[0]?.id,
        record.id,
      )
    }
  } finally {
    f.store.db.close()
  }
})

test('late configuration exposes incomplete coverage, restart before first vector requires explicit rebuild', async () => {
  const f = fixture()
  try {
    f.store.set('models', { providers: [], models: [], routes: [] })
    const missed = f.add('before model configured')
    await f.index.index(missed.id, missed.content)
    assert.equal(f.calls, 0)
    f.store.set('models', config(1024))
    const later = f.add('after model configured')
    await f.index.index(later.id, later.content)
    assert.equal(f.index.status().state, 'ready')
    assert.deepEqual(f.index.status().coverage, { eligible: 2, indexed: 1, pending: 1 })
    f.index.start()
    await f.index.settled()
    assert.deepEqual(f.index.status().coverage, { eligible: 2, indexed: 2, pending: 0 })
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
  const f2 = fixture()
  try {
    const missed = f2.add('crash before embedding')
    const restored = new EmbeddingIndex(f2.store, f2.memory, () => {
      throw new Error('no automatic replay')
    })
    await restored.index(missed.id, missed.content)
    assert.equal(restored.status().state, 'needs_rebuild')
    assert.deepEqual(restored.status().coverage, { eligible: 1, indexed: 0, pending: 1 })
    await restored.dispose()
  } finally {
    await f2.index.dispose()
    f2.store.db.close()
  }
})

test('native width drift marks index stale, does not compare dimensions, and stops repeated query charges', async () => {
  const f = fixture()
  try {
    f.store.set('models', config())
    const record = f.add('native synthetic fact')
    await f.index.index(record.id, record.content)
    assert.equal(f.index.status().active?.dimensions, 1024)
    f.setWidth(4096)
    await f.index.search({ sceneId: 's', simulation: false }, 'native')
    assert.equal(f.index.status().state, 'needs_rebuild')
    const calls = f.calls
    await f.index.search({ sceneId: 's', simulation: false }, 'native')
    assert.equal(f.calls, calls)
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().state, 'ready')
    assert.equal(f.index.status().active?.dimensions, 4096)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('restart attempt fencing prevents a late old worker response from changing the resumed checkpoint', async () => {
  const f = fixture()
  let restored: EmbeddingIndex | undefined
  let release!: () => void
  try {
    f.add('synthetic restart race')
    let entered!: () => void
    const seen = new Promise<void>((resolve) => {
      entered = resolve
    })
    const blocked = new Promise<void>((resolve) => {
      release = resolve
    })
    f.setHook(async () => {
      if (f.calls === 1) {
        entered()
        await blocked
      }
    })
    f.index.start()
    await seen
    const oldAttempt = f.index.status().job?.attemptId
    f.advance(30_001)
    restored = new EmbeddingIndex(
      f.store,
      f.memory,
      (cfg) => new Router(cfg, undefined, undefined, f.protocols),
      { now: f.now },
    )
    assert.equal(restored.status().job?.error, 'lease_expired_uncertain_request')
    restored.resume(true)
    await restored.settled()
    assert.notEqual(restored.status().job?.attemptId, oldAttempt)
    const final = restored.status()
    release()
    await f.index.settled()
    assert.deepEqual(restored.status(), final)
    assert.equal(final.job?.state, 'completed')
  } finally {
    release?.()
    await restored?.dispose()
    await f.index.dispose()
    f.store.db.close()
  }
})

test('explicit width mismatch latches stale and old deferred query cannot use or overwrite a new generation', async () => {
  const f = fixture()
  let release!: () => void
  try {
    const record = f.add('fixture fact')
    await f.index.index(record.id, record.content)
    f.setWidth(4096)
    await f.index.search({ sceneId: 's', simulation: false }, 'fixture')
    assert.equal(f.index.status().state, 'needs_rebuild')
    const paid = f.calls
    await f.index.search({ sceneId: 's', simulation: false }, 'fixture')
    assert.equal(f.calls, paid)
    f.store.set('models', config())
    f.setWidth(1024)
    f.index.start()
    await f.index.settled()
    const old = f.index.status().active!
    let entered!: () => void
    const seen = new Promise<void>((r) => {
      entered = r
    })
    const blocked = new Promise<void>((r) => {
      release = r
    })
    const queryCall = f.calls + 1
    f.setHook(async () => {
      if (f.calls === queryCall) {
        entered()
        await blocked
      }
    })
    const oldQuery = f.index.search({ sceneId: 's', simulation: false }, 'fixture')
    await seen
    f.setHook()
    f.setWidth(4096)
    f.index.start()
    await f.index.settled()
    const next = f.index.status().active!
    assert.notEqual(next.generationId, old.generationId)
    f.setWidth(1024)
    await f.index.search({ sceneId: 's', simulation: false }, 'fixture')
    assert.equal(f.store.get('embedding.incompatible', ''), next.generationId)
    f.setWidth(4096)
    release()
    await oldQuery
    assert.equal(f.store.get('embedding.incompatible', ''), next.generationId)
    assert.equal(f.index.status().state, 'needs_rebuild')
  } finally {
    release?.()
    await f.index.dispose()
    f.store.db.close()
  }
})

test('live peer cannot steal lease, expired lease needs consent, and old dispose cannot pause a new owner', async () => {
  const f = fixture()
  let peer: EmbeddingIndex | undefined
  let releaseFirst!: () => void
  let releaseSecond!: () => void
  try {
    f.add('owned fixture')
    let first!: () => void, second!: () => void
    const firstSeen = new Promise<void>((r) => {
        first = r
      }),
      secondSeen = new Promise<void>((r) => {
        second = r
      })
    const firstWait = new Promise<void>((r) => {
        releaseFirst = r
      }),
      secondWait = new Promise<void>((r) => {
        releaseSecond = r
      })
    f.setHook(async () => {
      if (f.calls === 1) {
        first()
        await firstWait
      } else if (f.calls === 2) {
        second()
        await secondWait
      }
    })
    f.index.start()
    await firstSeen
    peer = new EmbeddingIndex(
      f.store,
      f.memory,
      (cfg) => new Router(cfg, undefined, undefined, f.protocols),
      { now: f.now },
    )
    assert.equal(peer.status().job?.state, 'running')
    assert.throws(() => peer!.start(), /busy/)
    assert.throws(() => peer!.resume(true), /not_resumable/)
    assert.equal(f.calls, 1)
    f.advance(30_001)
    assert.equal(peer.status().job?.state, 'paused')
    assert.throws(() => peer!.resume(), /confirmation_required/)
    peer.resume(true)
    await secondSeen
    await f.index.dispose()
    assert.equal(peer.status().job?.state, 'running', 'old owner dispose cannot pause new owner')
    releaseFirst()
    releaseSecond()
    await peer.settled()
    assert.equal(peer.status().job?.state, 'completed')
  } finally {
    releaseFirst?.()
    releaseSecond?.()
    await peer?.dispose()
    await f.index.dispose()
    f.store.db.close()
  }
})

test('concurrent first vectors reuse one generation and conflicting width marks it stale', async () => {
  for (const conflicting of [false, true]) {
    const f = fixture()
    let release!: () => void
    try {
      f.store.set('models', config())
      const one = f.add('first scene'),
        two = f.add('second scene')
      let entered!: () => void
      const seen = new Promise<void>((r) => {
        entered = r
      })
      const blocked = new Promise<void>((r) => {
        release = r
      })
      f.setHook(async () => {
        if (f.calls === 1) {
          entered()
          await blocked
        }
      })
      const first = f.index.index(one.id, one.content)
      await seen
      await f.index.index(two.id, two.content)
      const generation = f.index.status().active!.generationId
      if (conflicting) f.setWidth(4096)
      release()
      await first
      assert.equal(f.index.status().active?.generationId, generation)
      assert.equal(f.index.status().retained.generations, 1)
      assert.equal(f.index.status().state, conflicting ? 'needs_rebuild' : 'ready')
      assert.equal(f.index.status().coverage.indexed, conflicting ? 0 : 2)
    } finally {
      release?.()
      await f.index.dispose()
      f.store.db.close()
    }
  }
})

test('retention is bounded to active, previous successful and current resumable staging without deleting memories', async () => {
  const f = fixture()
  try {
    f.add('retained fixture')
    for (let i = 0; i < 5; i++) {
      f.index.start()
      await f.index.settled()
      assert.ok(f.index.status().retained.generations <= 2)
    }
    const active = f.index.status().active!
    f.index.start()
    await f.index.settled()
    assert.ok(f.store.db.prepare('SELECT 1 FROM vector_spaces WHERE id=?').get(active.spaceId))
    assert.equal(f.memory.adminList().length, 1)
    assert.equal(f.index.status().retained.generations, 2)
    for (let i = 0; i < 5; i++) f.add(`staging fixture ${i}`)
    let entered!: () => void, release!: () => void
    const seen = new Promise<void>((r) => {
        entered = r
      }),
      blocked = new Promise<void>((r) => {
        release = r
      })
    const stopCall = f.calls + 2
    f.setHook(async () => {
      if (f.calls === stopCall) {
        entered()
        await blocked
      }
    })
    f.index.start()
    await seen
    const staging = f.index.status().job!.spaceId!
    assert.equal(f.index.status().retained.generations, 3)
    assert.ok(f.store.db.prepare('SELECT 1 FROM vector_spaces WHERE id=?').get(staging))
    f.index.pause()
    release()
    await f.index.settled()
    assert.ok(
      f.store.db.prepare('SELECT 1 FROM vector_spaces WHERE id=?').get(staging),
      'resumable staging retained',
    )
    f.setHook()
    f.index.resume(true)
    await f.index.settled()
    assert.equal(f.index.status().retained.generations, 2)
    assert.equal(f.memory.adminList().length, 6)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('stale action confirmations cannot start or mutate a replacement job', async () => {
  const f = fixture()
  try {
    f.add('action CAS fixture')
    const old = f.index.start(null)
    await f.index.settled()
    const current = f.index.start(old.id)
    await f.index.settled()
    const before = f.index.status(),
      calls = f.calls
    assert.throws(() => f.index.start(old.id), /embedding_job_changed/)
    assert.throws(() => f.index.start(null), /embedding_job_changed/)
    assert.throws(() => f.index.pause(old.id), /embedding_job_changed/)
    assert.throws(() => f.index.cancel(old.id), /embedding_job_changed/)
    assert.throws(() => f.index.resume(true, old.id), /embedding_job_changed/)
    assert.equal(f.index.status().job?.id, current.id)
    assert.deepEqual(f.index.status(), before)
    assert.equal(f.calls, calls)
    f.store.set('models', config(4096))
    assert.equal(f.index.status().selected?.requestedDimensions, 4096)
    assert.throws(
      () => f.index.start(current.id, before.selected!.encoderRevision),
      /embedding_config_changed/,
    )
    assert.equal(f.index.status().job?.id, current.id)
    assert.equal(f.calls, calls)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('transaction rollback does not poison resume with a nonexistent staging space', async () => {
  const f = fixture()
  try {
    f.add('rollback fixture')
    const original = f.memory.index.bind(f.memory)
    f.memory.index = () => {
      throw new Error('synthetic_write_failure')
    }
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'failed')
    assert.equal(f.index.status().job?.spaceId, undefined)
    assert.equal(f.index.status().active, undefined)
    f.memory.index = original
    f.index.resume(true)
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'completed')
    assert.equal(f.index.status().coverage.indexed, 1)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})

test('lease loss during final pointer write rolls back cutover and resumes without repeating completed embeddings', async () => {
  const f = fixture()
  try {
    f.add('lease deadline fixture')
    const original = f.store.set.bind(f.store)
    let expire = true
    f.store.set = (key, value) => {
      original(key, value)
      if (key === 'embedding.active' && expire) {
        expire = false
        f.advance(30_001)
      }
    }
    f.index.start()
    await f.index.settled()
    assert.equal(f.index.status().active, undefined)
    assert.equal(f.index.status().job?.state, 'paused')
    assert.equal(f.index.status().job?.requiresRetryConfirmation, false)
    const calls = f.calls
    f.index.resume()
    await f.index.settled()
    assert.equal(f.index.status().job?.state, 'completed')
    assert.equal(f.calls, calls)
  } finally {
    await f.index.dispose()
    f.store.db.close()
  }
})
