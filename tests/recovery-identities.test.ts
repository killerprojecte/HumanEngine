import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { IdentityRegistry } from '../src/memory/identities/service.ts'
import {
  recordIdentityMutation,
  createRecoveryIdentityPort,
} from '../src/memory/recovery/identities.ts'
import { parseIdentityRecoveryEvent } from '../src/memory/recovery/identity-contract.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
const a = { adapterId: 'offline', accountId: 'bot', personId: 'a' },
  b = { ...a, personId: 'b' },
  c = { ...a, personId: 'c' }
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-identity-recovery-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database),
    opened: Store[] = []
  const registry = new IdentityRegistry(store.db, {
    assertReady: () => store.assertRecoveryReady(),
    beforeWrite: (m) => recordIdentityMutation(store.db, m),
    authorizeObservation: () => true,
    sourceAvailable: () => true,
  })
  let n = 0
  const command = () => ({
    operationId: `operation-${++n}`,
    expectedRevision: registry.witness().revision,
    adminConfirmed: true as const,
  })
  return {
    root,
    store,
    registry,
    command,
    write: (f: () => unknown) => store.db.transaction(f).immediate(),
    snapshot: async (name = 'snapshot') => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (snapshot: string, name = 'restored') => {
      const result = await restore({
        backupRoot: snapshot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const restored = new Store(result.database)
      opened.push(restored)
      return {
        store: restored,
        registry: new IdentityRegistry(restored.db, {
          assertReady: () => restored.assertRecoveryReady(),
        }),
      }
    },
    close: async () => {
      for (const s of opened) s.close()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('restrictive unlink/split/alias removal replays actual core and exactly matches latest state', async () => {
  const f = await fixture()
  try {
    f.write(() => f.registry.link(f.command(), a, b))
    f.write(() => f.registry.link(f.command(), b, c))
    f.write(() => f.registry.link(f.command(), a, c))
    f.write(() =>
      f.registry.putAlias(f.command(), {
        id: 'alias',
        scopeSceneId: 'scene',
        endpoint: a,
        label: 'PRIVATE_ALIAS_CANARY',
        source: { kind: 'manual', administratorId: 'admin' },
        validFrom: null,
        validUntil: null,
      }),
    )
    const snapshot = await f.snapshot()
    f.write(() => f.registry.unlink(f.command(), a, b))
    f.write(() => f.registry.split(f.command(), [a]))
    f.write(() => f.registry.removeAlias(f.command(), 'alias'))
    const restored = await f.recover(snapshot)
    assert.deepEqual(restored.registry.adminList(true), f.registry.adminList(true))
    assert.deepEqual(restored.registry.witness(), f.registry.witness())
    assert.equal(metadata(restored.store.db)!.minimumReader, 6)
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    try {
      assert.equal(JSON.stringify(authority.entries()).includes('PRIVATE_ALIAS_CANARY'), false)
    } finally {
      authority.close()
    }
    const again = await f.recover(snapshot, 'restored-again')
    assert.deepEqual(again.registry.witness(), f.registry.witness())
  } finally {
    await f.close()
  }
})

test('new link and alias grants require trusted new material; newest snapshot is accepted', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot('old')
    f.write(() => f.registry.link(f.command(), a, b))
    await assert.rejects(f.recover(old), /recovery_identity_material_missing/)
    await assert.rejects(access(join(f.root, 'restored')))
    const current = await f.snapshot('current')
    assert.deepEqual((await f.recover(current, 'latest')).registry.witness(), f.registry.witness())
    f.write(() =>
      f.registry.putAlias(f.command(), {
        id: 'alias',
        scopeSceneId: 'scene',
        endpoint: a,
        label: 'Synthetic label',
        source: { kind: 'manual', administratorId: 'admin' },
        validFrom: null,
        validUntil: null,
      }),
    )
    await assert.rejects(f.recover(current, 'missing-alias'), /recovery_identity_material_missing/)
  } finally {
    await f.close()
  }
})

test('source recall uses exact typed cleanup; root-only interrupted cleanup stays quarantined', async () => {
  const f = await fixture()
  try {
    f.write(() =>
      f.registry.putAlias(f.command(), {
        id: 'observed',
        scopeSceneId: 'scene',
        endpoint: a,
        label: 'Synthetic observation',
        source: { kind: 'observation', sceneId: 'scene', messageId: 'message', senderId: 'a' },
        validFrom: null,
        validUntil: null,
      }),
    )
    const old = await f.snapshot()
    f.write(() => {
      f.store.recall('scene', 'message')
      f.registry.recallSource(f.command(), 'scene', 'message')
    })
    const restored = await f.recover(old)
    assert.equal(restored.registry.adminList(true).aliases.length, 0)
    assert.deepEqual(restored.registry.witness(), f.registry.witness())
  } finally {
    await f.close()
  }
  const g = await fixture()
  try {
    g.write(() =>
      g.registry.putAlias(g.command(), {
        id: 'observed',
        scopeSceneId: 'scene',
        endpoint: a,
        label: 'Synthetic observation',
        source: { kind: 'observation', sceneId: 'scene', messageId: 'message', senderId: 'a' },
        validFrom: null,
        validUntil: null,
      }),
    )
    const old = await g.snapshot()
    g.store.recall('scene', 'message')
    const authority = new RecoveryAuthority(metadata(g.store.db)!.authority)
    try {
      const head = authority.head()
      await assert.rejects(g.recover(old), /recovery_identity_source_cleanup_missing/)
      assert.deepEqual(
        authority.head(),
        head,
        'offline lifecycle must not invent an authority operation',
      )
      await assert.rejects(access(join(g.root, 'restored')))
      await assert.rejects(access(join(g.root, 'restored.quarantine')))
    } finally {
      authority.close()
    }
  } finally {
    await g.close()
  }
})

test('identity schema and ledger reject unknown formats, raw aliases and non-text authority material', async () => {
  const f = await fixture()
  try {
    f.write(() => f.registry.link(f.command(), a, b))
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    try {
      const event = authority
        .entries()
        .find((e) => e.revocation.kind === 'identity_mutation')!.revocation
      assert.throws(
        () => parseIdentityRecoveryEvent({ ...event, label: 'not allowed' }),
        /recovery_identity_event_invalid/,
      )
      assert.throws(
        () => parseIdentityRecoveryEvent({ ...event, schemaVersion: 2 }),
        /recovery_identity_event_invalid/,
      )
    } finally {
      authority.close()
    }
    const port = createRecoveryIdentityPort(f.store)
    f.store.db.exec('CREATE TABLE memory_identity_future (value TEXT)')
    assert.throws(() => port.assertSupported(), /recovery_unknown_identity_schema/)
    f.store.db.exec('DROP TABLE memory_identity_future')
    f.store.db.exec('UPDATE memory_identity_registry SET value=CAST(value AS BLOB)')
    assert.throws(() => port.assertSupported(), /recovery_identity_material_invalid/)
  } finally {
    await f.close()
  }
})

test('external identity intent survives local rollback and blocks old local reads/restoration', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot()
    assert.throws(
      () =>
        f.write(() => {
          f.registry.link(f.command(), a, b)
          throw new Error('synthetic rollback')
        }),
      /synthetic rollback/,
    )
    assert.throws(() => f.registry.witness(), /recovery_authority_head_changed/)
    await assert.rejects(f.recover(old), /recovery_identity_material_missing/)
  } finally {
    await f.close()
  }
})
