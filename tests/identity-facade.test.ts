import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { createIdentityFacade } from '../src/memory/identities/facade.ts'
import type { IdentityAlias } from '../src/memory/identities/contracts.ts'
const endpoint = (personId: string) => ({ adapterId: 'adapter', accountId: 'account', personId })
test('actual Store/Memory identity facade preserves authorized original people and scene-local aliases', () => {
  const store = new Store(':memory:')
  try {
    for (const id of ['s', 'private'])
      store.saveScene({
        id,
        kind: 'group',
        adapterId: 'adapter',
        accountId: 'account',
        platformSceneId: id,
        name: id,
        participationEnabled: true,
        learningEnabled: false,
        proactiveEnabled: false,
      })
    const memory = new MemoryService(store)
    const a = memory.create({
      scope: { kind: 'scene', sceneId: 's', personId: 'a' },
      content: 'A authorized fact',
      kind: 'fact',
    })
    const b = memory.create({
      scope: { kind: 'scene', sceneId: 'private', personId: 'b' },
      content: 'B private fact',
      kind: 'fact',
    })
    const facade = createIdentityFacade(store, memory),
      ctx = { sceneId: 's', simulation: false }
    store.db.transaction(() =>
      facade.adminLink(
        { operationId: 'link', expectedRevision: 0, adminConfirmed: true },
        endpoint('a'),
        endpoint('b'),
      ),
    )()
    assert.deepEqual(facade.query(ctx, 'a').snapshot.members, [endpoint('a')])
    assert.deepEqual(
      memory.list(ctx).map((m) => m.id),
      [a.id],
    )
    assert.equal(
      memory.list(ctx).some((m) => m.id === b.id),
      false,
    )
    store.db.transaction(() =>
      facade.putManualAlias(
        { operationId: 'alias', expectedRevision: 1, adminConfirmed: true },
        {
          id: 'alias',
          scopeSceneId: 'private',
          endpoint: endpoint('a'),
          label: 'PRIVATE_ALIAS',
          validFrom: null,
          validUntil: null,
        },
        'actual-admin',
      ),
    )()
    assert.deepEqual(facade.query(ctx, 'a').aliases[0]!.values, [])
    assert.deepEqual(facade.query({ ...ctx, simulation: true }, 'a').snapshot.members, [])
  } finally {
    store.close()
  }
})
test('trusted observed aliases bind current source and root recall removes label within caller transaction', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'adapter',
      accountId: 'account',
      platformSceneId: 's',
      name: 's',
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store),
      facade = createIdentityFacade(store, memory)
    memory.create({
      scope: { kind: 'scene', sceneId: 's', personId: 'a' },
      content: 'A',
      kind: 'fact',
    })
    const ctx = {
      sceneId: 's',
      simulation: false,
      sourceEvidence: [
        { sceneId: 's', messageId: 'm', senderId: 'a', available: true, isBot: false },
      ],
    }
    const alias: IdentityAlias = {
      id: 'obs',
      scopeSceneId: 's',
      endpoint: endpoint('a'),
      label: 'PRIVATE_OBS',
      validFrom: null,
      validUntil: null,
      source: { kind: 'observation', sceneId: 's', messageId: 'm', senderId: 'a' },
    }
    const cmd = { operationId: 'obs', expectedRevision: 0, adminConfirmed: true as const }
    assert.throws(
      () =>
        store.db.transaction(() =>
          facade.observeAlias({ ...ctx, sourceEvidence: [] }, cmd, alias),
        )(),
      /untrusted/,
    )
    store.db.transaction(() => facade.observeAlias(ctx, cmd, alias))()
    const snapshot = facade.query(ctx, 'a')
    assert.equal(snapshot.aliases[0]!.values.length, 1)
    store.db.transaction(() => {
      store.recall('s', 'm')
      facade.sourceRecalled('s', 'm')
    })()
    assert.equal(facade.validate(ctx, snapshot), false)
    assert.equal(facade.adminList(true).aliases.length, 0)
    assert.throws(
      () =>
        store.db.transaction(() =>
          facade.observeAlias(ctx, { ...cmd, operationId: 'readd', expectedRevision: 2 }, alias),
        )(),
      /untrusted/,
    )
    const revision = facade.witness().revision
    store.db.transaction(() => facade.sourceRecalled('s', 'm'))()
    assert.equal(facade.witness().revision, revision)
  } finally {
    store.close()
  }
})

test('new enrollment never authorizes unbound old alias observations or keeps their current view alive', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'adapter',
      accountId: 'account',
      platformSceneId: 's',
      name: 's',
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store),
      facade = createIdentityFacade(store, memory)
    memory.create({
      scope: { kind: 'scene', sceneId: 's', personId: 'a' },
      content: 'A',
      kind: 'fact',
    })
    const ctx = {
      sceneId: 's',
      simulation: false,
      sourceEvidence: [
        { sceneId: 's', messageId: 'm', senderId: 'a', available: true, isBot: false },
      ],
    }
    const alias: IdentityAlias = {
      id: 'old',
      scopeSceneId: 's',
      endpoint: endpoint('a'),
      label: 'OLD_OBSERVATION_CANARY',
      validFrom: null,
      validUntil: null,
      source: { kind: 'observation', sceneId: 's', messageId: 'm', senderId: 'a' },
    }
    store.db.transaction(() =>
      facade.observeAlias(
        ctx,
        { operationId: 'old', expectedRevision: 0, adminConfirmed: true },
        alias,
      ),
    )()
    const oldView = facade.query(ctx, 'a')
    const observed = store.sourceEvents.observe({
      kind: 'ingress',
      identity: {
        sceneId: 's',
        adapterId: 'adapter',
        accountId: 'account',
        messageId: 'm',
        senderId: 'a',
      },
      bodyHash: 'b'.repeat(64),
      occurredAt: null,
    })
    assert.ok(observed.ref)
    assert.equal(facade.query(ctx, 'a').aliases[0]!.values.length, 0)
    assert.equal(facade.validate(ctx, oldView), false)
    assert.throws(
      () =>
        store.db.transaction(() =>
          facade.observeAlias(
            ctx,
            { operationId: 'late', expectedRevision: 1, adminConfirmed: true },
            { ...alias, id: 'late' },
          ),
        )(),
      /untrusted/,
    )
    const fresh = {
      ...ctx,
      sourceEvidence: [{ ...ctx.sourceEvidence[0]!, eventRef: observed.ref! }],
    }
    store.db.transaction(() =>
      facade.observeAlias(
        fresh,
        { operationId: 'fresh', expectedRevision: 1, adminConfirmed: true },
        {
          ...alias,
          id: 'fresh',
          label: 'FRESH',
          source: {
            ...alias.source,
            kind: 'observation',
            sceneId: 's',
            messageId: 'm',
            senderId: 'a',
            eventRef: observed.ref!,
          },
        },
      ),
    )()
    assert.deepEqual(
      facade.query(ctx, 'a').aliases[0]!.values.map((a) => a.label),
      ['FRESH'],
    )
  } finally {
    store.close()
  }
})
