import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { Settings } from '../src/schema.ts'
import type { WorkingSetItem, WorkingSetJob } from '../src/memory/working-set/contracts.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'

// Actual Engine.start and committed lifecycle callbacks; no workingSet read or direct drain.
// Router/transport sentinels establish that metadata maintenance cannot initiate model or chat work.
const primary = 'maintenance-first',
  other = 'maintenance-other'
const context = (sceneId = primary) => ({ sceneId, simulation: false })
const body = 'MAINTENANCE_BODY_CANARY: synthetic, unconfirmed, only this occasion.'

function fixture(t: TestContext, initial: Partial<Settings> = {}) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T01:00:00Z'),
  })
  const root = mkdtempSync(join(tmpdir(), 'he-workspace-maintenance-')),
    path = join(root, 'synthetic.sqlite')
  const previousPlugins = process.env.HUMANENGINE_PLUGINS
  process.env.HUMANENGINE_PLUGINS = ''
  let store: Store,
    engine: Engine,
    disposed = false
  const stats = { router: 0, network: 0, sends: 0, batches: 0 }
  t.mock.method(globalThis, 'fetch', async () => {
    stats.network++
    throw Error('Unexpected network')
  })
  const open = () => {
    store = new Store(path)
    if (!store.scene(primary)) {
      store.set('settings', {
        ...store.settings,
        memoryWorkingSetEnabled: true,
        memoryEpisodesEnabled: true,
        ...initial,
      })
      for (const id of [primary, other])
        store.saveScene({
          id,
          kind: 'group',
          adapterId: 'offline-maintenance',
          accountId: 'bot',
          platformSceneId: id,
          name: 'Synthetic maintenance',
          participationEnabled: false,
          learningEnabled: false,
          proactiveEnabled: false,
        })
    }
    engine = new Engine(store)
    disposed = false
    engine.router = () => {
      stats.router++
      throw Error('Unexpected router invocation')
    }
    engine.runtime.addAdapter('offline-maintenance', {
      send: async () => {
        stats.sends++
        throw Error('Unexpected send')
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    const batch = engine.memory.workspace.maintenanceBatch.bind(engine.memory.workspace)
    engine.memory.workspace.maintenanceBatch = () => {
      stats.batches++
      return batch()
    }
  }
  open()
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    stats,
    async start() {
      await engine.start()
      assert.deepEqual(
        engine.plugins.list().map((p) => [p.id, p.enabled]),
        [
          ['snowluma', false],
          ['typing-delay', false],
          ['web-search', false],
          ['web-read', false],
        ],
      )
    },
    create(id: string, sceneId = primary) {
      return engine.memory.create({
        scope: { kind: 'scene', sceneId, personId: 'synthetic-person' },
        kind: 'claim',
        epistemicKind: 'hypothesis',
        content: `${body} ${id}`,
      })
    },
    settings(patch: Partial<Settings>) {
      store.set('settings', { ...store.settings, ...patch })
      engine.settingsChanged()
    },
    rows<T>(suffix: 'items' | 'jobs' | 'receipts'): T[] {
      const name = `memory_working_set_${suffix}`
      if (!store.db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(name))
        return []
      return (
        store.db.prepare(`SELECT value FROM ${name} ORDER BY rowid`).all() as { value: string }[]
      ).map((row) => JSON.parse(row.value) as T)
    },
    items() {
      return this.rows<WorkingSetItem>('items')
    },
    jobs() {
      return this.rows<WorkingSetJob>('jobs')
    },
    zeroTraffic() {
      assert.deepEqual(
        { router: stats.router, network: stats.network, sends: stats.sends },
        { router: 0, network: 0, sends: 0 },
      )
    },
    async stop() {
      if (!disposed) {
        await engine.dispose()
        disposed = true
      }
    },
    async reopen() {
      await this.stop()
      store.close()
      open()
    },
    async close() {
      try {
        await this.stop()
      } finally {
        store.close()
        rmSync(root, { recursive: true, force: true })
        if (previousPlugins === undefined) delete process.env.HUMANENGINE_PLUGINS
        else process.env.HUMANENGINE_PLUGINS = previousPlugins
      }
      this.zeroTraffic()
    },
  }
}

test('real Engine startup consumes committed create/update notifications without any read-page trigger or idle polling', async (t) => {
  const h = fixture(t)
  try {
    await h.start()
    t.mock.timers.tick(250)
    assert.equal(h.stats.batches, 1)
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, 1, 'an empty startup check does not install periodic scans')
    const record = h.create('initial')
    assert.equal(h.jobs().length, 1)
    assert.equal(h.items().length, 0)
    t.mock.timers.tick(249)
    assert.equal(h.items().length, 0)
    t.mock.timers.tick(1)
    assert.equal(h.jobs().length, 0)
    assert.equal(h.items()[0]?.snapshot.ref.id, record.id)
    assert.equal(h.items()[0]?.contextSelectedCount, 0)
    assert.equal(h.rows('receipts').length, 0)
    const previous = h.items()[0]!
    h.engine.memory.update(record.id, { content: `${body} updated once` })
    h.engine.memory.update(record.id, { locked: true })
    const current = h.jobs()[0]!
    assert.equal(h.jobs().length, 1, 'notifications coalesce while the pending ref advances')
    assert.ok(current.snapshot.ref.version > previous.snapshot.ref.version)
    const truth = h.store.db.prepare('SELECT * FROM memories').all()
    t.mock.timers.tick(250)
    assert.deepEqual(h.items()[0]?.snapshot, current.snapshot)
    assert.equal(h.jobs().length, 0)
    assert.deepEqual(h.store.db.prepare('SELECT * FROM memories').all(), truth)
    assert.ok(!JSON.stringify(h.items()).includes(body), 'projection stores no source/body text')
    const calls = h.stats.batches
    t.mock.timers.tick(20_000)
    assert.equal(h.stats.batches, calls)
  } finally {
    await h.close()
  }
})

test('a rolled-back authoritative commit may notify but cannot produce a ghost job, projection or repeating timer', async (t) => {
  const h = fixture(t)
  try {
    await h.start()
    const valid = h.create('committed')
    t.mock.timers.tick(250)
    const items = h.items(),
      before = h.stats.batches
    let rolledBackId = ''
    assert.throws(
      () =>
        h.store.db
          .transaction(() => {
            rolledBackId = h.create('rolled-back').id
            assert.equal(h.jobs().length, 1, 'the actual callback notified inside the transaction')
            throw Error('synthetic outer rollback')
          })
          .immediate(),
      /synthetic outer rollback/,
    )
    assert.equal(h.jobs().length, 0)
    assert.equal(
      h.store.db.prepare('SELECT 1 FROM memories WHERE id=?').get(rolledBackId),
      undefined,
    )
    t.mock.timers.tick(250)
    assert.equal(h.stats.batches, before + 1)
    assert.deepEqual(h.items(), items)
    assert.equal(h.items()[0]?.snapshot.ref.id, valid.id)
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, before + 1)
  } finally {
    await h.close()
  }
})

test('OFF cancels pending metadata and does not backfill disabled-time writes or learning when re-enabled', async (t) => {
  const h = fixture(t)
  try {
    await h.start()
    const pending = h.create('pending-before-off')
    assert.equal(h.jobs().length, 1)
    h.settings({ memoryWorkingSetEnabled: false, memoryEpisodesEnabled: false })
    const disabled = h.create('written-while-off')
    assert.equal(h.jobs().length, 0)
    const before = h.stats.batches
    t.mock.timers.tick(20_000)
    assert.equal(h.stats.batches, before)
    assert.deepEqual(h.items(), [])
    h.settings({ memoryEpisodesEnabled: true })
    t.mock.timers.tick(250)
    assert.equal(h.stats.batches, before + 1)
    assert.deepEqual(h.items(), [])
    t.mock.timers.tick(20_000)
    assert.equal(h.stats.batches, before + 1)
    assert.equal(
      (
        h.store.db
          .prepare('SELECT count(*) AS n FROM memories WHERE id IN (?,?)')
          .get(pending.id, disabled.id) as { n: number }
      ).n,
      2,
    )
    h.engine.memory.update(pending.id, { locked: true })
    t.mock.timers.tick(250)
    assert.deepEqual(
      h.items().map((item) => item.snapshot.ref.id),
      [pending.id],
      'a new commit may enqueue; startup does not replay old bodies',
    )
  } finally {
    await h.close()
  }
})

test('dispose cancels its timer; a fresh actual startup consumes only persisted jobs and clean restart does not rewrite metadata', async (t) => {
  const h = fixture(t, { memoryWorkingSetEnabled: false, memoryEpisodesEnabled: false })
  try {
    const unenrolled = h.create('no-metadata-job')
    h.settings({ memoryWorkingSetEnabled: true })
    await h.start()
    const pending = h.create('persistent-job')
    const before = h.stats.batches
    await h.stop()
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, before)
    assert.equal(h.jobs().length, 1)
    assert.deepEqual(h.items(), [])
    await h.reopen()
    assert.equal(h.jobs()[0]?.snapshot.ref.id, pending.id)
    await h.start()
    t.mock.timers.tick(250)
    assert.deepEqual(
      h.items().map((item) => item.snapshot.ref.id),
      [pending.id],
    )
    assert.equal(h.jobs().length, 0)
    assert.ok(h.store.db.prepare('SELECT 1 FROM memories WHERE id=?').get(unenrolled.id))
    const items = h.items()
    await h.reopen()
    await h.start()
    const clean = h.stats.batches
    t.mock.timers.tick(250)
    assert.equal(h.stats.batches, clean + 1)
    assert.deepEqual(h.items(), items)
    t.mock.timers.tick(20_000)
    assert.equal(h.stats.batches, clean + 1)
  } finally {
    await h.close()
  }
})

test('the actual timer drains at most forty jobs per 250ms batch and leaves the remaining five for the next tick', async (t) => {
  const h = fixture(t)
  try {
    const records = Array.from({ length: 45 }, (_, index) => h.create(`bounded-${index}`))
    assert.equal(h.jobs().length, 45)
    await h.start()
    t.mock.timers.tick(249)
    assert.equal(h.items().length, 0)
    t.mock.timers.tick(1)
    assert.equal(h.items().length, 40)
    assert.equal(h.jobs().length, 5)
    assert.equal(h.stats.batches, 1)
    t.mock.timers.tick(249)
    assert.equal(h.items().length, 40)
    t.mock.timers.tick(1)
    assert.equal(h.items().length, 45)
    assert.equal(h.jobs().length, 0)
    assert.equal(h.stats.batches, 2)
    assert.deepEqual(
      new Set(h.items().map((item) => item.snapshot.ref.id)),
      new Set(records.map((record) => record.id)),
    )
    assert.ok(
      h.items().every((item) => item.contextSelectedCount === 0 && item.lastSelectedAt === null),
    )
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, 2)
  } finally {
    await h.close()
  }
})

test('a leased oldest scene does not block another ready scene, and expired work is revalidated before projection', async (t) => {
  const h = fixture(t)
  try {
    const blocked = h.create('first-leased'),
      ready = h.create('second-ready', other)
    const lease = h.engine.memory.workspace.access.claim(context(), 1)[0]!
    assert.equal(lease.ref.id, blocked.id)
    await h.start()
    t.mock.timers.tick(250)
    assert.deepEqual(
      h.items().map((item) => item.snapshot.ref.id),
      [ready.id],
    )
    assert.equal(h.jobs()[0]?.snapshot.ref.id, blocked.id)
    assert.equal(h.jobs()[0]?.lease?.token, lease.token)
    t.mock.timers.tick(lease.expiresAt - Date.now())
    assert.equal(h.jobs().length, 0)
    assert.deepEqual(
      new Set(h.items().map((item) => item.snapshot.ref.id)),
      new Set([blocked.id, ready.id]),
    )
    assert.equal(
      h.engine.memory.workspace.access.complete(context(), [lease]).completed,
      0,
      'old lease cannot overwrite the finished projection',
    )
    const calls = h.stats.batches
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, calls)
  } finally {
    await h.close()
  }
})

test('external recovery-authority advancement stops a scheduled tick before metadata writes and never schedules retries', async (t) => {
  const h = fixture(t)
  try {
    await h.start()
    h.create('before-authority-gap')
    const jobs = h.jobs(),
      before = h.stats.batches
    const authority = new RecoveryAuthority(metadata(h.store.db)!.authority)
    try {
      authority.append(
        { kind: 'source', sceneId: primary, messageId: 'external-recall' },
        authority.head(),
      )
    } finally {
      authority.close()
    }
    t.mock.timers.tick(250)
    assert.equal(
      h.stats.batches,
      before,
      'the coordinator checks the real recovery gate before invoking its batch',
    )
    assert.deepEqual(h.jobs(), jobs)
    assert.deepEqual(h.items(), [])
    assert.throws(() => h.store.assertRecoveryReady(), /authority_head_changed/)
    t.mock.timers.tick(10_000)
    assert.equal(h.stats.batches, before)
    assert.deepEqual(h.jobs(), jobs)
  } finally {
    await h.close()
  }
})
