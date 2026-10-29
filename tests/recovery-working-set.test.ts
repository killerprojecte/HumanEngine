import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
import {
  metadata,
  requireWorkingSetReader,
  RecoveryAuthority,
  saveGate,
  revoke,
} from '../src/memory/recovery/ledger.ts'
import { readRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import {
  configurationFlags,
  parseConfigurationRecoveryEvent,
} from '../src/memory/recovery/configuration-contract.ts'
import {
  recordWorkingSetPin,
  createRecoveryWorkingSetPort,
} from '../src/memory/recovery/working-set.ts'
import { WorkingSetStorage } from '../src/memory/working-set/storage.ts'
import {
  WORKING_SET_TABLES,
  type WorkingSetPinPolicy,
} from '../src/memory/working-set/contracts.ts'

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-working-recovery-'))
  const store = new Store(join(root, 'live', 'humanengine.sqlite'))
  await mkdir(join(root, 'media'))
  return {
    root,
    store,
    snap: (name: string) =>
      backup({
        database: store.db.name,
        mediaRoot: join(root, 'media'),
        destination: join(root, name),
      }),
    restore: (name: string, target: string) =>
      restore({
        backupRoot: join(root, name),
        destination: join(root, target),
        authorityDirectory: metadata(store.db)!.authority,
      }),
    close: async () => {
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}
function seed(store: Store) {
  store.db
    .exec(`CREATE TABLE memory_working_set_items(scene_id TEXT,kind TEXT,entity_id TEXT,value TEXT,PRIMARY KEY(scene_id,kind,entity_id));
 CREATE TABLE memory_working_set_jobs(scene_id TEXT,kind TEXT,entity_id TEXT,value TEXT,PRIMARY KEY(scene_id,kind,entity_id));
 CREATE TABLE memory_working_set_receipts(receipt_key TEXT PRIMARY KEY,selected_at INTEGER,value TEXT);
 CREATE TABLE memory_working_set_pins(scene_id TEXT,kind TEXT,entity_id TEXT,value TEXT,PRIMARY KEY(scene_id,kind,entity_id));`)
  const selection = {
    ref: { kind: 'assertion', id: 'synthetic', version: 1, deletionEpoch: 0 },
    dependencyKey: 'a'.repeat(64),
  }
  const snapshot = { ...selection, sceneId: 'scene', recordedAt: 1, protection: null }
  store.db
    .transaction(() => {
      requireWorkingSetReader(store.db)
      store.db.prepare('INSERT INTO memory_working_set_items VALUES(?,?,?,?)').run(
        'scene',
        'assertion',
        'synthetic',
        JSON.stringify({
          schemaVersion: 1,
          snapshot,
          metadataRevision: 1,
          contextSelectedCount: 7,
          lastSelectedAt: 2,
          projectedAt: 2,
        }),
      )
      store.db.prepare('INSERT INTO memory_working_set_jobs VALUES(?,?,?,?)').run(
        'scene',
        'assertion',
        'synthetic',
        JSON.stringify({
          schemaVersion: 1,
          snapshot,
          generation: '11111111-1111-4111-8111-111111111111',
          enqueuedAt: 2,
          lease: null,
        }),
      )
      store.db.prepare('INSERT INTO memory_working_set_receipts VALUES(?,?,?)').run(
        'receipt',
        2,
        JSON.stringify({
          schemaVersion: 1,
          sceneId: 'scene',
          turnId: 'turn',
          selection,
          selectedAt: 2,
        }),
      )
    })
    .immediate()
}
test('real old and populated backups reset soft attention state but preserve recall and restore model hold', async () => {
  const f = await fixture()
  try {
    await f.snap('old')
    seed(f.store)
    f.store.recall('scene', 'deleted-source')
    await f.snap('soft')
    for (const name of ['old', 'soft']) {
      const restored = await f.restore(name, name + '-restored')
      const reader = new Store(restored.database)
      try {
        assert.equal(reader.recalled('scene', 'deleted-source'), true)
        assert.equal(readRestoredModelHold(reader.db)?.status, 'held')
        for (const table of Object.keys(WORKING_SET_TABLES)) {
          const exists = reader.db.prepare('SELECT 1 FROM sqlite_master WHERE name=?').get(table)
          if (exists)
            assert.equal(
              (reader.db.prepare(`SELECT count(*) n FROM ${table}`).get() as { n: number }).n,
              0,
            )
        }
        if (name === 'soft') assert.equal(metadata(reader.db)!.minimumReader, 4)
      } finally {
        reader.close()
      }
    }
  } finally {
    await f.close()
  }
})
test('unknown soft table, column, and row schema version quarantine instead of silently deleting', async () => {
  for (const mutation of ['table', 'column', 'version']) {
    const f = await fixture()
    try {
      seed(f.store)
      if (mutation === 'table')
        f.store.db.exec('CREATE TABLE memory_working_set_future(value TEXT)')
      if (mutation === 'column')
        f.store.db.exec('ALTER TABLE memory_working_set_items ADD COLUMN authority TEXT')
      if (mutation === 'version')
        f.store.db.exec(
          "UPDATE memory_working_set_items SET value=json_set(value,'$.schemaVersion',2)",
        )
      await f.snap('unknown')
      await assert.rejects(f.restore('unknown', 'denied'), /recovery_unknown_working_set_schema/)
    } finally {
      await f.close()
    }
  }
})
test('configuration v4 records both default-off soft feature flags without widening legacy versions', () => {
  const {
    memoryExperienceFeedbackEnabled,
    memorySemanticEnabled,
    memorySemanticAutoAdmissionEnabled,
    memorySemanticAnswerEnabled,
    memoryKnowledgeEnabled,
    memoryKnowledgeAutoAdmissionEnabled,
    memoryKnowledgeAnswerEnabled,
    topicAdmissionEnabled,
    ...flags
  } = configurationFlags({})
  assert.equal(flags.memoryWorkingSetEnabled, false)
  assert.equal(flags.memoryEpisodesEnabled, false)
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 4, flags }).flags,
    flags,
  )
  const { memoryWorkingSetEnabled, memoryEpisodesEnabled, ...v3 } = flags
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 3, flags: v3 })
      .flags,
    v3,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 3, flags }),
    /invalid/,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 8, flags }),
    /invalid/,
  )
})

const pinPolicy = (pinned = true, policyRevision = 1): WorkingSetPinPolicy => ({
  schemaVersion: 1,
  sceneId: 'scene',
  selection: {
    ref: { kind: 'assertion', id: 'synthetic', version: 1, deletionEpoch: 0 },
    dependencyKey: 'a'.repeat(64),
  },
  policyRevision,
  pinned,
  terminal: false,
})
test('external latest pin/unpin folds across old backups, while missing pinned material blocks restoration', async () => {
  const f = await fixture()
  try {
    await f.snap('before-pin')
    f.store.db.transaction(() => recordWorkingSetPin(f.store.db, pinPolicy())).immediate()
    await assert.rejects(
      f.restore('before-pin', 'missing-pin-target'),
      /working_set_pin_material_missing/,
    )
    f.store.db.transaction(() => recordWorkingSetPin(f.store.db, pinPolicy(false, 2))).immediate()
    const restored = await f.restore('before-pin', 'after-unpin')
    const reader = new Store(restored.database)
    try {
      const policy = new WorkingSetStorage(reader.db).pin('scene', {
        kind: 'assertion',
        id: 'synthetic',
      })
      assert.equal(policy?.pinned, false)
      assert.equal(policy?.policyRevision, 2)
      assert.equal(readRestoredModelHold(reader.db)?.status, 'held')
    } finally {
      reader.close()
    }
  } finally {
    await f.close()
  }
})
test('verified current or stale pin intent survives reset without granting source authority', async () => {
  for (const state of ['current', 'stale'] as const) {
    const f = await fixture()
    let maintenance: Store | undefined, authority: RecoveryAuthority | undefined
    try {
      seed(f.store)
      f.store.db.transaction(() => recordWorkingSetPin(f.store.db, pinPolicy())).immediate()
      const gate = metadata(f.store.db)!
      saveGate(f.store.db, { ...gate, state: 'quarantine' })
      authority = new RecoveryAuthority(gate.authority)
      maintenance = new Store(f.store.db.name, { authority })
      let checks = 0
      const port = createRecoveryWorkingSetPort(maintenance, {
        resolvePinTarget: (p) => {
          checks++
          assert.deepEqual(p, pinPolicy())
          return state
        },
      })
      maintenance.db
        .transaction(() => {
          port.verifyPins(authority!.entries().map((e) => e.revocation))
          port.reset()
        })
        .immediate()
      const policy = new WorkingSetStorage(maintenance.db).pin('scene', {
        kind: 'assertion',
        id: 'synthetic',
      })
      assert.deepEqual(policy, pinPolicy())
      assert.equal(checks, 1)
      assert.equal(
        (
          maintenance.db.prepare('SELECT count(*) n FROM memory_working_set_items').get() as {
            n: number
          }
        ).n,
        0,
      )
      assert.equal(
        maintenance.db.prepare("SELECT 1 FROM sqlite_master WHERE name='memories'").get(),
        undefined,
      )
    } finally {
      maintenance?.close()
      authority?.close()
      await f.close()
    }
  }
})

test('terminal assertion deletion overrides earlier pin without recreating the deleted entity', async () => {
  const f = await fixture()
  try {
    await f.snap('old')
    f.store.db
      .transaction(() => {
        recordWorkingSetPin(f.store.db, pinPolicy())
        revoke(f.store.db, {
          kind: 'assertion',
          id: 'synthetic',
          throughVersion: 1,
          terminal: true,
        })
      })
      .immediate()
    const restored = await f.restore('old', 'deleted')
    const reader = new Store(restored.database)
    try {
      const table = reader.db
        .prepare("SELECT 1 FROM sqlite_master WHERE name='memory_working_set_pins'")
        .get()
      if (table)
        assert.equal(reader.db.prepare('SELECT 1 FROM memory_working_set_pins').get(), undefined)
      assert.equal(
        reader.db.prepare("SELECT 1 FROM memories WHERE id='synthetic'").get(),
        undefined,
      )
      assert.equal(readRestoredModelHold(reader.db)?.status, 'held')
    } finally {
      reader.close()
    }
  } finally {
    await f.close()
  }
})

async function realWorkspaceFixture() {
  const f = await fixture()
  f.store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'synthetic',
    accountId: 'account',
    platformSceneId: 'room',
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  f.store.set('settings', { ...f.store.settings, memoryWorkingSetEnabled: true })
  const memory = new MemoryService(f.store),
    ctx = { sceneId: 'scene', simulation: false }
  const create = () =>
    memory.create({
      scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
      kind: 'fact',
      content: 'Synthetic preference one',
    })
  const select = (id: string) => {
    const item = memory.workspace
      .workingSet(ctx, { includeCold: true })
      .items.find((x) => x.kind === 'assertion' && x.snapshot.record.id === id)
    assert.ok(item)
    return { ref: item.accessibility.ref, dependencyKey: item.accessibility.dependencyKey }
  }
  return { ...f, memory, ctx, create, select }
}
test('real MemoryService pins remain qualified stale/hot across later content revision and restore, then unpin releases', async () => {
  const f = await realWorkspaceFixture()
  try {
    const record = f.create(),
      selection = f.select(record.id)
    await f.snap('before-pin')
    f.memory.workspace.access.setPinned(f.ctx, selection, { expectedRevision: 0, pinned: true })
    await f.snap('pinned')
    f.memory.update(record.id, { content: 'Synthetic preference corrected' })
    await f.snap('drifted')
    for (const name of ['before-pin', 'pinned', 'drifted']) {
      const restored = await f.restore(name, name + '-target'),
        reader = new Store(restored.database)
      try {
        const memory = new MemoryService(reader)
        const view = memory.workspace
          .workingSet(f.ctx, { includeCold: true })
          .items.find((x) => x.kind === 'assertion' && x.snapshot.record.id === record.id)
        assert.ok(view)
        assert.equal(view.accessibility.pinIntent, true)
        assert.equal(view.accessibility.state, 'hot')
        assert.equal(view.accessibility.pinStatus, name === 'drifted' ? 'stale' : 'current')
        assert.deepEqual(
          memory.workspace.access.storage.pin('scene', selection.ref)?.selection,
          selection,
        )
        assert.equal(readRestoredModelHold(reader.db)?.status, 'held')
      } finally {
        reader.close()
      }
    }
    f.memory.workspace.access.setPinned(f.ctx, selection, { expectedRevision: 1, pinned: false })
    const restored = await f.restore('pinned', 'unpinned-target'),
      reader = new Store(restored.database)
    try {
      assert.equal(new WorkingSetStorage(reader.db).pin('scene', selection.ref)?.pinned, false)
    } finally {
      reader.close()
    }
    f.memory.delete(record.id)
    const removed = await f.restore('pinned', 'deleted-target'),
      deleted = new Store(removed.database)
    try {
      assert.deepEqual(new MemoryService(deleted).list(f.ctx), [])
      assert.equal(new WorkingSetStorage(deleted.db).pin('scene', selection.ref), undefined)
    } finally {
      deleted.close()
    }
  } finally {
    await f.close()
  }
})
test('real old snapshot missing a newer pin anchor generation must quarantine, not mislabel stale', async () => {
  const f = await realWorkspaceFixture()
  try {
    const record = f.create()
    await f.snap('v1')
    f.memory.update(record.id, { content: 'Synthetic newer generation' })
    f.memory.workspace.access.setPinned(f.ctx, f.select(record.id), {
      expectedRevision: 0,
      pinned: true,
    })
    await assert.rejects(f.restore('v1', 'missing-v2'), /working_set_pin_material_missing/)
  } finally {
    await f.close()
  }
})
