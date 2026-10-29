import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { IdentityRegistry } from '../src/memory/identities/service.ts'
import type {
  IdentityAlias,
  IdentityCommand,
  IdentityEndpoint,
  IdentityScope,
} from '../src/memory/identities/contracts.ts'

const endpoint = (personId: string, accountId = 'account'): IdentityEndpoint => ({
  adapterId: 'adapter',
  accountId,
  personId,
})
const scope: IdentityScope = {
  sceneId: 'scene',
  adapterId: 'adapter',
  accountId: 'account',
  allowedPersonIds: ['a', 'b', 'c'],
}
const command = (
  expectedRevision: number,
  operationId = `op-${expectedRevision}`,
): IdentityCommand => ({ expectedRevision, operationId, adminConfirmed: true })
function fixture() {
  const db = new Database(':memory:')
  let ready = true,
    source = true
  const registry = new IdentityRegistry(db, {
    assertReady: () => {
      if (!ready) throw new Error('recovery_quarantined')
    },
    authorizeObservation: (alias) =>
      alias.source.kind === 'observation' && alias.source.senderId === alias.endpoint.personId,
    sourceAvailable: () => source,
    now: () => Date.parse('2026-10-29T09:00:00Z'),
  })
  return {
    db,
    registry,
    ready: (value: boolean) => (ready = value),
    source: (value: boolean) => (source = value),
    run: <T>(fn: () => T): T => db.transaction(fn)(),
  }
}
test('explicit stable links never use nickname or grant scene/account access, snapshots detect ABA', () => {
  const f = fixture()
  try {
    const original = f.registry.resolveMembers(scope, 'a')
    f.run(() => f.registry.link(command(0), endpoint('a'), endpoint('b')))
    assert.deepEqual(
      f.registry.resolveMembers(scope, 'a').members.map((e) => e.personId),
      ['a', 'b'],
    )
    assert.equal(f.registry.validateSnapshot(scope, original), false)
    f.run(() => f.registry.link(command(1), endpoint('b'), endpoint('foreign', 'other')))
    assert.deepEqual(
      f.registry.resolveMembers(scope, 'a').members.map((e) => e.personId),
      ['a', 'b'],
    )
    assert.deepEqual(
      f.registry.resolveMembers({ ...scope, allowedPersonIds: ['a'] }, 'a').members,
      [endpoint('a')],
    )
    const snapshot = f.registry.resolveMembers(scope, 'a')
    f.run(() => f.registry.unlink(command(2), endpoint('a'), endpoint('b')))
    f.run(() => f.registry.link(command(3), endpoint('a'), endpoint('b')))
    assert.equal(f.registry.validateSnapshot(scope, snapshot), false)
    assert.equal(
      f.registry.validateSnapshot(
        { ...scope, sceneId: 'other' },
        f.registry.resolveMembers(scope, 'a'),
      ),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('operation replay is exact, revision CAS strict, and explicit split cuts alternate paths', () => {
  const f = fixture()
  try {
    f.run(() => f.registry.link(command(0), endpoint('a'), endpoint('b')))
    assert.equal(
      f.run(() => f.registry.link(command(0), endpoint('a'), endpoint('b'))).duplicate,
      true,
    )
    assert.throws(
      () => f.run(() => f.registry.link(command(0), endpoint('a'), endpoint('c'))),
      /operation_conflict/,
    )
    assert.throws(
      () => f.run(() => f.registry.link(command(0, 'new'), endpoint('a'), endpoint('c'))),
      /revision_conflict/,
    )
    f.run(() => f.registry.link(command(1), endpoint('b'), endpoint('c')))
    f.run(() => f.registry.link(command(2), endpoint('c'), endpoint('a')))
    f.run(() => f.registry.split(command(3), [endpoint('a')]))
    assert.deepEqual(f.registry.resolveMembers(scope, 'a').members, [endpoint('a')])
    assert.deepEqual(
      f.registry.resolveMembers(scope, 'b').members.map((e) => e.personId),
      ['b', 'c'],
    )
  } finally {
    f.db.close()
  }
})
test('alias provenance and exact validity do not imply identity; recall physically removes observation labels', () => {
  const f = fixture()
  try {
    const alias: IdentityAlias = {
      id: 'alias',
      endpoint: endpoint('a'),
      label: 'PRIVATE_ALIAS_CANARY',
      scopeSceneId: 'scene',
      source: { kind: 'observation', sceneId: 'scene', messageId: 'message', senderId: 'a' },
      validFrom: null,
      validUntil: null,
    }
    f.run(() => f.registry.putAlias(command(0), alias))
    assert.equal(f.registry.aliases(scope, 'a').length, 1)
    assert.equal(f.registry.aliases({ ...scope, sceneId: 'other' }, 'a').length, 0)
    f.source(false)
    assert.equal(f.registry.aliases(scope, 'a').length, 0)
    f.source(true)
    assert.throws(
      () =>
        f.run(() =>
          f.registry.putAlias(command(1), {
            ...alias,
            id: 'bad',
            source: {
              ...alias.source,
              kind: 'observation',
              sceneId: 'scene',
              messageId: 'other',
              senderId: 'b',
            },
          }),
        ),
      /untrusted/,
    )
    f.run(() =>
      f.registry.putAlias(command(1), {
        ...alias,
        id: 'expired',
        label: 'same-name',
        validUntil: '2026-10-28T09:00:00Z',
      }),
    )
    assert.equal(f.registry.aliases(scope, 'a').length, 1)
    assert.deepEqual(f.registry.resolveMembers(scope, 'a').members, [endpoint('a')])
    f.run(() => f.registry.recallSource(command(2), 'scene', 'message'))
    const row = f.db.prepare('SELECT value FROM memory_identity_registry').get() as {
      value: string
    }
    assert.equal(row.value.includes('PRIVATE_ALIAS_CANARY'), false)
    assert.equal(row.value.includes('"messageId"'), false)
  } finally {
    f.db.close()
  }
})
test('reads have ready gates, simulation is zero write, mutation needs caller transaction', () => {
  const f = fixture()
  try {
    assert.deepEqual(f.registry.resolveMembers({ ...scope, simulation: true }, 'a').members, [])
    assert.equal(
      f.db
        .prepare("SELECT count(*) n FROM sqlite_master WHERE name='memory_identity_registry'")
        .get() &&
        (
          f.db
            .prepare("SELECT count(*) n FROM sqlite_master WHERE name='memory_identity_registry'")
            .get() as { n: number }
        ).n,
      0,
    )
    assert.throws(
      () =>
        f.run(() =>
          f.registry.link({ ...command(0), simulation: true }, endpoint('a'), endpoint('b')),
        ),
      /simulation/,
    )
    assert.throws(() => f.registry.link(command(0), endpoint('a'), endpoint('b')), /transaction/)
    f.ready(false)
    assert.throws(() => f.registry.resolveMembers(scope, 'a'), /quarantined/)
    assert.throws(() => f.registry.adminList(true), /quarantined/)
    assert.throws(() => f.registry.aliases(scope, 'a'), /quarantined/)
  } finally {
    f.db.close()
  }
})
test('prewrite intent is before any local write; throw and async hooks cannot commit', () => {
  for (const asyncHook of [false, true]) {
    const db = new Database(':memory:')
    const registry = new IdentityRegistry(db, {
      assertReady() {},
      beforeWrite() {
        assert.equal(
          (
            db
              .prepare("SELECT count(*) n FROM sqlite_master WHERE name='memory_identity_registry'")
              .get() as { n: number }
          ).n,
          0,
        )
        if (!asyncHook) throw new Error('denied')
        return Promise.resolve()
      },
    })
    try {
      assert.throws(
        () => db.transaction(() => registry.link(command(0), endpoint('a'), endpoint('b')))(),
        asyncHook ? /async_hook/ : /denied/,
      )
      assert.equal(registry.adminList(true).revision, 0)
    } finally {
      db.close()
    }
  }
})
test('component bound never evicts established identity and malformed persistent data fails closed', () => {
  const f = fixture()
  try {
    for (let i = 1; i < 32; i++)
      f.run(() => f.registry.link(command(i - 1), endpoint('p0'), endpoint('p' + i)))
    assert.throws(
      () => f.run(() => f.registry.link(command(31), endpoint('p0'), endpoint('p32'))),
      /component_capacity/,
    )
    assert.equal(f.registry.adminList(true).revision, 31)
    f.db
      .prepare('UPDATE memory_identity_registry SET value=?')
      .run(JSON.stringify({ format: 2, revision: 31, edges: [], aliases: [], operations: [] }))
    assert.throws(() => f.registry.adminList(true), /corrupt/)
  } finally {
    f.db.close()
  }
})

test('metadata-only witnesses are reproducible and revoked observations cannot be re-admitted', () => {
  const db = new Database(':memory:')
  const mutations: unknown[] = []
  let available = true
  const registry = new IdentityRegistry(db, {
    assertReady() {},
    authorizeObservation: () => true,
    sourceAvailable: () => available,
    beforeWrite: (mutation) => {
      mutations.push(mutation)
      assert.equal(registry.witness().qualificationHash, mutation.beforeHash)
    },
  })
  const alias: IdentityAlias = {
    id: 'private-alias',
    endpoint: endpoint('a'),
    label: 'PRIVATE_ALIAS_WITNESS_CANARY',
    scopeSceneId: 'scene',
    source: {
      kind: 'observation',
      sceneId: 'scene',
      messageId: 'message',
      senderId: 'a',
      eventRef: { eventId: 'event', revision: 1, deletionEpoch: 0 },
    },
    validFrom: null,
    validUntil: null,
  }
  try {
    db.transaction(() => registry.putAlias(command(0), alias))()
    const receipt = mutations[0] as { qualificationHash: string; targets: { aliasIds: string[] } }
    assert.equal(registry.witness().qualificationHash, receipt.qualificationHash)
    assert.deepEqual(receipt.targets.aliasIds, ['private-alias'])
    assert.equal(JSON.stringify(receipt).includes('PRIVATE_ALIAS_WITNESS_CANARY'), false)
    available = false
    assert.throws(
      () => db.transaction(() => registry.putAlias(command(1), { ...alias, id: 'readded' }))(),
      /untrusted/,
    )
    db.transaction(() => registry.recallSource(command(1), 'scene', 'message'))()
    assert.equal(registry.adminList(true).aliases.length, 0)
    const state = JSON.parse(
      (db.prepare('SELECT value FROM memory_identity_registry').get() as { value: string }).value,
    )
    state.operations[0].revision = 2
    db.prepare('UPDATE memory_identity_registry SET value=?').run(JSON.stringify(state))
    assert.throws(() => registry.witness(), /corrupt/)
  } finally {
    db.close()
  }
})

test('manual alias is scene-local and unknown time is not fabricated', () => {
  const f = fixture()
  try {
    const alias: IdentityAlias = {
      id: 'manual',
      endpoint: endpoint('a'),
      label: 'PRIVATE_MANUAL_ALIAS',
      scopeSceneId: 'private-scene',
      source: { kind: 'manual', administratorId: 'verified-admin' },
      validFrom: null,
      validUntil: null,
    }
    f.run(() => f.registry.putAlias(command(0), alias))
    assert.equal(f.registry.aliases(scope, 'a').length, 0)
    assert.deepEqual(f.registry.aliases({ ...scope, sceneId: 'private-scene' }, 'a'), [alias])
    assert.throws(
      () =>
        f.run(() =>
          f.registry.putAlias(command(1), {
            ...alias,
            id: 'bad',
            source: { kind: 'observation', sceneId: 'other', messageId: 'm', senderId: 'a' },
          }),
        ),
      /scope_mismatch/,
    )
    const snapshot = f.registry.resolveMembers(scope, 'a')
    f.run(() => f.registry.removeAlias(command(1), 'manual'))
    assert.equal(f.registry.validateSnapshot(scope, snapshot), false)
  } finally {
    f.db.close()
  }
})

test('new registry instance observes committed links and rolled-back edits without caching authority', () => {
  const f = fixture()
  try {
    f.run(() => f.registry.link(command(0), endpoint('a'), endpoint('b')))
    const reopened = new IdentityRegistry(f.db, { assertReady() {} })
    assert.equal(reopened.resolveMembers(scope, 'a').members.length, 2)
    const snapshot = reopened.resolveMembers(scope, 'a')
    assert.throws(
      () =>
        f.run(() => {
          f.registry.unlink(command(1), endpoint('a'), endpoint('b'))
          throw new Error('rollback')
        }),
      /rollback/,
    )
    assert.equal(reopened.validateSnapshot(scope, snapshot), true)
    f.run(() => f.registry.unlink(command(1), endpoint('a'), endpoint('b')))
    assert.equal(reopened.validateSnapshot(scope, snapshot), false)
  } finally {
    f.db.close()
  }
})

test('opaque Unicode identities have exact canonical ordering before durable intent; BLOB JSON is rejected', () => {
  const db = new Database(':memory:')
  let intents = 0
  const registry = new IdentityRegistry(db, {
    assertReady() {},
    beforeWrite() {
      intents++
    },
  })
  try {
    const a = endpoint('é'),
      b = endpoint('e\u0301')
    db.transaction(() => registry.link(command(0), a, b))()
    db.transaction(() => registry.link(command(1), b, a))()
    assert.equal(intents, 2)
    assert.equal(registry.adminList(true).edges.length, 1)
    const stored = db.prepare('SELECT value FROM memory_identity_registry').get() as {
      value: string
    }
    db.prepare('UPDATE memory_identity_registry SET value=?').run(Buffer.from(stored.value))
    assert.throws(() => registry.adminList(true), /corrupt/)
    assert.throws(() => db.transaction(() => registry.unlink(command(2), a, b))(), /corrupt/)
    assert.equal(intents, 2)
  } finally {
    db.close()
  }
})

test('reserved operation slots reject grants before intent but permit all existing authorizations to be revoked', () => {
  const db = new Database(':memory:')
  let intents = 0
  const registry = new IdentityRegistry(db, {
    assertReady() {},
    beforeWrite() {
      intents++
    },
  })
  try {
    db.transaction(() => registry.link(command(0), endpoint('a'), endpoint('b')))()
    const state = JSON.parse(
      (db.prepare('SELECT value FROM memory_identity_registry').get() as { value: string }).value,
    )
    state.revision = 16382
    state.operations = Array.from({ length: 16382 }, (_, i) => ({
      id: 'seed-' + i,
      hash: 'a'.repeat(64),
      revision: i + 1,
    }))
    state.aliases = [
      {
        id: 'alias',
        scopeSceneId: 's',
        endpoint: endpoint('a'),
        label: 'private',
        source: { kind: 'manual', administratorId: 'admin' },
        validFrom: null,
        validUntil: null,
      },
    ]
    db.prepare('UPDATE memory_identity_registry SET value=?').run(JSON.stringify(state))
    intents = 0
    assert.throws(
      () =>
        db.transaction(() =>
          registry.link(command(16382, 'newgrant'), endpoint('b'), endpoint('c')),
        )(),
      /reserve_capacity/,
    )
    assert.throws(
      () =>
        db.transaction(() =>
          registry.unlink(command(16382, 'noop'), endpoint('a'), endpoint('c')),
        )(),
      /reserve_capacity/,
    )
    assert.equal(intents, 0)
    db.transaction(() =>
      registry.unlink(command(16382, 'revoke-edge'), endpoint('a'), endpoint('b')),
    )()
    db.transaction(() => registry.removeAlias(command(16383, 'revoke-alias'), 'alias'))()
    assert.equal(intents, 2)
    const final = registry.adminList(true)
    assert.equal(final.revision, 16384)
    assert.deepEqual(final.edges, [])
    assert.deepEqual(final.aliases, [])
  } finally {
    db.close()
  }
})
