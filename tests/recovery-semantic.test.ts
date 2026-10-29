import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { SemanticStorage } from '../src/memory/semantic/storage.ts'
import {
  recordSemanticMutation,
  createRecoverySemanticPort,
} from '../src/memory/recovery/semantic.ts'
import { parseSemanticRecoveryEvent } from '../src/memory/recovery/semantic-contract.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import {
  configurationFlags,
  parseConfigurationRecoveryEvent,
} from '../src/memory/recovery/configuration-contract.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-semantic-recovery-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database),
    opened: Store[] = []
  return {
    root,
    store,
    database,
    mediaRoot,
    snapshot: async (name = 'snapshot') => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (backupRoot: string, name = 'restored') => {
      const result = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const s = new Store(result.database)
      opened.push(s)
      return s
    },
    close: async () => {
      for (const s of opened) s.close()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('semantic journal accepts only bounded state witnesses, never candidate text or skipped versions', () => {
  const value = {
    kind: 'semantic_mutation',
    schemaVersion: 1,
    operation: 'review',
    id: 'candidate',
    sceneId: 'scene',
    previousVersion: 1,
    nextVersion: 2,
    beforeHash: 'a'.repeat(64),
    afterHash: 'b'.repeat(64),
    nextState: { status: 'rejected', admission: 'administrator_review' },
  }
  assert.deepEqual(parseSemanticRecoveryEvent(value), value)
  for (const patch of [
    { text: 'PRIVATE_BODY' },
    { nextVersion: 4 },
    { nextState: { status: 'candidate', admission: 'administrator_review' } },
    { schemaVersion: 2 },
  ])
    assert.throws(
      () => parseSemanticRecoveryEvent({ ...value, ...patch }),
      /recovery_semantic_event_invalid/,
    )
})

test('attempt witnesses retain dedup metadata and older snapshots cannot pretend no attempt occurred', async () => {
  const f = await fixture()
  try {
    const storage = new SemanticStorage(
      f.store.db,
      () => f.store.assertRecoveryReady(),
      (m) => recordSemanticMutation(f.store.db, m),
    )
    const old = await f.snapshot('before')
    const attempt = {
      schemaVersion: 1 as const,
      sceneId: 'scene',
      inputKey: 'a'.repeat(64),
      status: 'abandoned' as const,
      version: 1,
      parents: [],
      roots: [],
    }
    f.store.db.transaction(() => storage.writeAttempt(attempt, undefined)).immediate()
    assert.equal(metadata(f.store.db)!.minimumReader, 8)
    await assert.rejects(f.recover(old, 'missing'), /recovery_semantic_attempt_material_missing/)
    await assert.rejects(access(join(f.root, 'missing')))
    const current = await f.snapshot('current'),
      restored = await f.recover(current)
    assert.deepEqual(
      new SemanticStorage(restored.db, () => restored.assertRecoveryReady()).attempts(),
      [attempt],
    )
  } finally {
    await f.close()
  }
})

test('semantic precommit survives local rollback and blocks stale live access', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot()
    const storage = new SemanticStorage(
      f.store.db,
      () => f.store.assertRecoveryReady(),
      (m) => recordSemanticMutation(f.store.db, m),
    )
    assert.throws(
      () =>
        f.store.db
          .transaction(() => {
            storage.writeAttempt(
              {
                schemaVersion: 1,
                sceneId: 'scene',
                inputKey: 'b'.repeat(64),
                status: 'abandoned',
                version: 1,
                parents: [],
                roots: [],
              },
              undefined,
            )
            throw new Error('synthetic rollback')
          })
          .immediate(),
      /synthetic rollback/,
    )
    assert.throws(() => f.store.settings, /recovery_authority_head_changed/)
    await assert.rejects(f.recover(old), /recovery_semantic_attempt_material_missing/)
  } finally {
    await f.close()
  }
})

test('semantic recovery rejects unknown table versions and nontext attempt material', () => {
  const store = new Store(':memory:')
  try {
    const port = createRecoverySemanticPort(store, () => {
      throw new Error('unused service')
    })
    store.db.exec('CREATE TABLE memory_semantic_future(value TEXT)')
    assert.throws(() => port.assertSupported(), /recovery_unknown_semantic_schema/)
    store.db.exec(
      'DROP TABLE memory_semantic_future; CREATE TABLE memory_semantic_attempts(scene_id TEXT,input_key TEXT,value TEXT)',
    )
    store.db
      .prepare('INSERT INTO memory_semantic_attempts VALUES(?,?,?)')
      .run('s', 'a'.repeat(64), Buffer.from('{}'))
    assert.throws(() => port.assertSupported(), /recovery_semantic_material_invalid/)
  } finally {
    store.close()
  }
})

test('configuration v7 independently defaults semantic flags off and retains exact legacy v6', () => {
  const flags = configurationFlags({ memoryKnowledgeEnabled: true }),
    {
      memoryExperienceFeedbackEnabled,
      memorySemanticEnabled,
      memorySemanticAutoAdmissionEnabled,
      memorySemanticAnswerEnabled,
      ...legacy
    } = flags
  assert.deepEqual(
    [memorySemanticEnabled, memorySemanticAutoAdmissionEnabled, memorySemanticAnswerEnabled],
    [false, false, false],
  )
  assert.deepEqual(
    parseConfigurationRecoveryEvent({
      kind: 'configuration_flags',
      schemaVersion: 6,
      flags: legacy,
    }).flags,
    legacy,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 6, flags }),
    /recovery_configuration_invalid/,
  )
  const { memoryExperienceFeedbackEnabled: omittedFeedback, ...v7 } = flags
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 7, flags: v7 })
      .flags,
    v7,
  )
})

async function liveFixture() {
  const f = await fixture(),
    { MemoryService } = await import('../src/memory.ts'),
    memory = new MemoryService(f.store)
  f.store.saveScene({
    id: 's',
    adapterId: 'offline',
    accountId: 'bot',
    platformSceneId: 'group',
    kind: 'group',
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  f.store.set('settings', {
    ...f.store.settings,
    memorySemanticEnabled: true,
    memorySemanticAutoAdmissionEnabled: false,
    memorySemanticAnswerEnabled: true,
  })
  const ctx = { sceneId: 's', simulation: false, provenance: 'source-only' as const }
  const propose = (messageId: string, content: string) =>
    memory.propose(
      {
        ...ctx,
        sourceEvidence: [
          {
            sceneId: 's',
            messageId,
            senderId: 'person',
            sourceText: content,
            available: true,
            isBot: false,
          },
        ],
      },
      {
        kind: 'fact',
        content,
        sources: [{ sceneId: 's', messageId, senderId: 'person' }],
        subject: { kind: 'self', sourceMessageId: messageId },
        epistemicKind: 'explicit',
      },
    )
  const selection = (id: string) => {
    const state = memory.revisionState(id),
      ref = { id, version: state.version, deletionEpoch: state.deletionEpoch },
      current = memory.semanticCurrent(ctx, ref)
    assert.ok(current)
    return { ref, dependencyKey: current.dependencyKey }
  }
  const admit = (ids: string[], text = 'PRIVATE_SYNTHETIC_SUMMARY') =>
    f.store.db
      .transaction(() =>
        memory.semantic.admit(ctx, ids.map(selection), [
          { kind: 'summary_candidate', selectedParentIds: ids, text, hypothesis: false },
        ]),
      )
      .immediate()
  return {
    ...f,
    memory,
    ctx,
    propose,
    selection,
    admit,
    close: async () => {
      memory.dispose()
      await f.close()
    },
  }
}
function savedId(result: ReturnType<Awaited<ReturnType<typeof liveFixture>>['propose']>) {
  assert.equal(result.status, 'saved')
  if (result.status !== 'saved') throw new Error('fixture_assertion_failed')
  return result.memory.id
}

test('real administrator review reject and admit transitions restore in order without journaled text', async () => {
  const f = await liveFixture()
  try {
    const ids = [
      savedId(f.propose('a', 'I use a synthetic red instrument.')),
      savedId(f.propose('b', 'I use a synthetic blue instrument.')),
    ]
    const admitted = f.admit(ids)
    assert.deepEqual(admitted.rejected, [])
    const candidate = admitted.created[0]!
    const old = await f.snapshot()
    f.store.db
      .transaction(() => f.memory.semantic.review(f.ctx, candidate.id, 1, 'admitted'))
      .immediate()
    f.store.db
      .transaction(() => f.memory.semantic.review(f.ctx, candidate.id, 2, 'rejected'))
      .immediate()
    f.store.db
      .transaction(() => f.memory.semantic.review(f.ctx, candidate.id, 3, 'admitted'))
      .immediate()
    const restored = await f.recover(old)
    const actual = new SemanticStorage(restored.db, () => restored.assertRecoveryReady()).get(
      candidate.id,
    )
    assert.deepEqual(actual, f.memory.semantic.storage.get(candidate.id))
    assert.equal(actual!.status, 'admitted')
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    try {
      assert.equal(JSON.stringify(authority.entries()).includes('PRIVATE_SYNTHETIC_SUMMARY'), false)
    } finally {
      authority.close()
    }
  } finally {
    await f.close()
  }
})

test('real candidate grant after an old snapshot requires its trusted material', async () => {
  const f = await liveFixture()
  try {
    const ids = [
      savedId(f.propose('a', 'I use a synthetic red instrument.')),
      savedId(f.propose('b', 'I use a synthetic blue instrument.')),
    ]
    const old = await f.snapshot()
    assert.equal(f.admit(ids).created.length, 1)
    await assert.rejects(f.recover(old), /recovery_semantic_material_missing/)
  } finally {
    await f.close()
  }
})

test('real source OR recall restores exact semantic candidate and qualified parent state', async () => {
  const f = await liveFixture()
  try {
    const first = savedId(f.propose('a', 'I use the same synthetic shared instrument.'))
    assert.equal(
      savedId(f.propose('independent', 'I use the same synthetic shared instrument.')),
      first,
    )
    const second = savedId(f.propose('b', 'I use an independent synthetic blue instrument.'))
    const admitted = f.admit([first, second])
    assert.deepEqual(admitted.rejected, [])
    const candidate = admitted.created[0]!
    const old = await f.snapshot()
    f.memory.recall('s', 'a')
    assert.ok(f.memory.semantic.current(f.ctx, candidate.id))
    const restored = await f.recover(old),
      { MemoryService } = await import('../src/memory.ts'),
      memory = new MemoryService(restored)
    try {
      assert.deepEqual(
        memory.semantic.storage.get(candidate.id),
        f.memory.semantic.storage.get(candidate.id),
      )
      assert.deepEqual(
        memory.semantic.current(f.ctx, candidate.id),
        f.memory.semantic.current(f.ctx, candidate.id),
      )
      assert.equal(
        JSON.stringify(memory.semantic.storage.get(candidate.id)).includes('"messageId":"a"'),
        false,
      )
    } finally {
      memory.dispose()
    }
  } finally {
    await f.close()
  }
})

test('real terminal parent deletion restores candidate tombstone without resurrecting body or dedup key', async () => {
  const f = await liveFixture()
  try {
    const ids = [
      savedId(f.propose('a', 'I use a synthetic red instrument.')),
      savedId(f.propose('b', 'I use a synthetic blue instrument.')),
    ]
    const candidate = f.admit(ids).created[0]!,
      old = await f.snapshot()
    f.memory.delete(ids[0]!)
    const restored = await f.recover(old),
      storage = new SemanticStorage(restored.db, () => restored.assertRecoveryReady())
    assert.equal(storage.get(candidate.id), undefined)
    assert.equal(storage.seen(candidate.inputKey), true)
    const rows = restored.db.prepare('SELECT value FROM memory_semantic_candidates').all()
    assert.equal(JSON.stringify(rows).includes('PRIVATE_SYNTHETIC_SUMMARY'), false)
    assert.equal(
      f.memory.list(f.ctx).some((m) => m.id === ids[1]),
      true,
    )
  } finally {
    await f.close()
  }
})

test('indirect private parent recall clears a candidate captured before an independent OR branch existed', async () => {
  const f = await liveFixture()
  try {
    const parent = savedId(f.propose('private-root', 'A private synthetic premise.')),
      state = f.memory.revisionState(parent),
      parentRef = { id: parent, version: state.version, deletionEpoch: state.deletionEpoch }
    const text = 'I use a shared synthetic calibration instrument.',
      source = { sceneId: 's', messageId: 'dependent-a', senderId: 'person' }
    const a = f.memory.propose(
      {
        ...f.ctx,
        provenance: 'contextual',
        dependencyMemoryIds: [parent],
        dependencyParentReferences: [parentRef],
        sourceEvidence: [{ ...source, sourceText: text, available: true, isBot: false }],
      },
      {
        kind: 'fact',
        content: text,
        sources: [source],
        subject: { kind: 'self', sourceMessageId: source.messageId },
        epistemicKind: 'explicit',
      },
    )
    const aid = savedId(a),
      other = savedId(f.propose('other', 'I retain an independent synthetic declaration.'))
    const admitted = f.admit([aid, other], 'INDIRECT_PRIVATE_SEMANTIC_CANARY')
    assert.deepEqual(admitted.rejected, [])
    const candidate = admitted.created[0]!
    assert.ok(
      candidate.parents
        .find((p) => p.ref.id === aid)!
        .proofs.every((p) => p.parents.includes(parent)),
    )
    assert.equal(savedId(f.propose('independent-b', text)), aid)
    const old = await f.snapshot()
    f.memory.recall('s', 'private-root')
    assert.ok(f.memory.list(f.ctx).some((m) => m.id === aid))
    assert.equal(f.memory.semantic.storage.get(candidate.id), undefined)
    const restored = await f.recover(old),
      storage = new SemanticStorage(restored.db, () => restored.assertRecoveryReady())
    assert.equal(storage.get(candidate.id), undefined)
    assert.equal(storage.seen(candidate.inputKey), true)
    assert.equal(
      JSON.stringify(
        restored.db.prepare('SELECT value FROM memory_semantic_candidates').all(),
      ).includes('INDIRECT_PRIVATE_SEMANTIC_CANARY'),
      false,
    )
    const { MemoryService } = await import('../src/memory.ts'),
      memory = new MemoryService(restored)
    try {
      assert.ok(memory.list(f.ctx).some((m) => m.id === aid))
      assert.ok(memory.list(f.ctx).some((m) => m.id === other))
    } finally {
      memory.dispose()
    }
  } finally {
    await f.close()
  }
})

test('old backup replays semantic feature revocation independently of structured knowledge', async () => {
  const f = await fixture()
  try {
    f.store.set('settings', {
      ...f.store.settings,
      memoryKnowledgeEnabled: true,
      memorySemanticEnabled: true,
      memorySemanticAutoAdmissionEnabled: true,
      memorySemanticAnswerEnabled: true,
    })
    const old = await f.snapshot()
    f.store.set('settings', {
      ...f.store.settings,
      memorySemanticEnabled: false,
      memorySemanticAutoAdmissionEnabled: false,
      memorySemanticAnswerEnabled: false,
    })
    const restored = await f.recover(old)
    assert.equal(restored.settings.memorySemanticEnabled, false)
    assert.equal(restored.settings.memorySemanticAutoAdmissionEnabled, false)
    assert.equal(restored.settings.memorySemanticAnswerEnabled, false)
    assert.equal(restored.settings.memoryKnowledgeEnabled, true)
  } finally {
    await f.close()
  }
})

test('actual semantic attempt started receipt survives restore and refuses the same input without model replay', async () => {
  const f = await liveFixture()
  try {
    const ids = [
        savedId(f.propose('a', 'I use a synthetic red instrument.')),
        savedId(f.propose('b', 'I use a synthetic blue instrument.')),
      ],
      input = { inputKey: 'c'.repeat(64), parents: ids.map(f.selection) }
    assert.equal(
      f.store.db.transaction(() => f.memory.semantic.beginAttempt(f.ctx, input)).immediate(),
      true,
    )
    const current = await f.snapshot(),
      restored = await f.recover(current),
      { MemoryService } = await import('../src/memory.ts'),
      memory = new MemoryService(restored)
    try {
      assert.equal(
        restored.db.transaction(() => memory.semantic.beginAttempt(f.ctx, input)).immediate(),
        false,
      )
      assert.equal(memory.semantic.storage.attempts()[0]!.status, 'started')
      assert.equal(memory.semantic.storage.attempts()[0]!.version, 1)
    } finally {
      memory.dispose()
    }
  } finally {
    await f.close()
  }
})

async function episodeFixture() {
  const f = await liveFixture()
  f.store.set('settings', {
    ...f.store.settings,
    memoryKnowledgeEnabled: true,
    memoryKnowledgeAutoAdmissionEnabled: true,
    memoryKnowledgeAnswerEnabled: true,
  })
  const textA = 'I plan a synthetic joint activity.',
    textB = 'I may participate conditionally.'
  const a = savedId(f.propose('ordinary-a', textA)),
    b = savedId(f.propose('ordinary-b', textB))
  const reference = (id: string) => {
    const state = f.memory.revisionState(id)
    return { id, version: state.version, deletionEpoch: state.deletionEpoch }
  }
  const evidence = (messageId: string, text: string) => ({
    sceneId: 's',
    messageId,
    senderId: 'person',
    sourceText: text,
    parts: [{ type: 'text' as const, text }],
    available: true,
    isBot: false,
  })
  const markerText = 'I supplied a synthetic structural declaration.'
  const marker = savedId(
    f.memory.propose(
      {
        ...f.ctx,
        internalDeclaration: 'knowledge_declaration',
        sourceEvidence: [evidence('marker-source', markerText)],
      },
      {
        kind: 'claim',
        content: markerText,
        sources: [{ sceneId: 's', messageId: 'marker-source', senderId: 'person' }],
        subject: { kind: 'self', sourceMessageId: 'marker-source' },
        epistemicKind: 'explicit',
      },
    ),
  )
  const relation = (
    messageId: string,
    text: string,
    object: import('../src/memory/knowledge/contracts.ts').KnowledgeProposal['object'],
  ) => ({
    registryVersion: 1 as const,
    predicate: 'participates_in' as const,
    subject: { kind: 'self' as const, personId: 'person' },
    object,
    polarity: 'positive' as const,
    modality: 'asserted' as const,
    temporalScope: 'current' as const,
    conditions: [],
    sourceSpans: [{ messageId, partIndex: 0, start: 0, end: text.length, stance: 'self' as const }],
  })
  f.store.db
    .transaction(() =>
      f.memory.knowledge.admit(
        { ...f.ctx, sourceEvidence: [evidence('ordinary-a', textA)] },
        reference(a),
        [
          relation('ordinary-a', textA, {
            kind: 'new',
            entityKind: 'event',
            title: 'Synthetic event identity',
          }),
        ],
      ),
    )
    .immediate()
  const edge = f.memory.knowledge.storage.rows()[0]!
  assert.equal(edge.object.kind, 'entity')
  if (edge.object.kind !== 'entity') throw Error('fixture_entity_missing')
  const entity = edge.object.entity
  f.store.db
    .transaction(() =>
      f.memory.knowledge.admit(
        {
          ...f.ctx,
          provenance: 'contextual',
          exposedEntities: [{ entityId: entity.id, backingRef: entity.backing }],
          exposedAssertions: [reference(b)],
          sourceEvidence: [evidence('marker-source', markerText)],
        },
        reference(marker),
        [relation('marker-source', markerText, { kind: 'existing', entityId: entity.id })],
      ),
    )
    .immediate()
  const groups = f.memory.semanticEpisodes.discover(f.ctx, reference(a)).groups
  assert.equal(groups.length, 1)
  const group = groups[0]!
  assert.ok(group.anchorDependencies.some((d) => d.ref.id === marker))
  assert.equal(
    group.parents.some((d) => d.ref.id === marker),
    false,
  )
  const createEpisode = () =>
    f.store.db
      .transaction(() =>
        f.memory.semantic.admit(
          f.ctx,
          group.parents,
          [
            {
              kind: 'episode_candidate',
              selectedParentIds: group.parents.map((p) => p.ref.id),
              text: 'PRIVATE_EPISODE_MARKER_CANARY',
              hypothesis: false,
              episodeAnchor: group.anchor,
            },
          ],
          {
            anchor: group.anchor,
            membershipKey: group.anchorMembershipKey,
            anchorDependencies: group.anchorDependencies,
            anchorQualifications: group.anchorQualifications,
          },
        ),
      )
      .immediate()
  return { ...f, a, b, marker, createEpisode }
}

for (const operation of ['source', 'terminal'] as const)
  test(`actual episode anchor ${operation} privacy restores tombstone while ordinary declarations and independent summary survive`, async () => {
    const f = await episodeFixture()
    const { Runtime } = await import('../src/runtime.ts')
    const runtime = new Runtime(f.store)
    runtime.addExtension({
      tools: [],
      execute: async () => undefined,
      prepareRecall: (sceneId, messageId) => f.memory.prepareRecall(sceneId, messageId),
    })
    try {
      const summary = f.admit([f.a, f.b], 'INDEPENDENT_SUMMARY').created[0]!
      const admitted = f.createEpisode()
      assert.deepEqual(admitted.rejected, [])
      const episode = admitted.created[0]!
      assert.ok(episode)
      const old = await f.snapshot()
      if (operation === 'source') runtime.recall('s', 'marker-source')
      else f.memory.delete(f.marker)
      assert.equal(f.memory.semantic.storage.get(episode.id), undefined)
      assert.ok(f.memory.semantic.storage.get(summary.id))
      const restored = await f.recover(old)
      const storage = new SemanticStorage(restored.db, () => restored.assertRecoveryReady())
      assert.deepEqual(storage.rows(), f.memory.semantic.storage.rows())
      assert.equal(storage.get(episode.id), undefined)
      assert.equal(storage.seen(episode.inputKey), true)
      assert.equal(
        JSON.stringify(
          restored.db.prepare('SELECT value FROM memory_semantic_candidates').all(),
        ).includes('PRIVATE_EPISODE_MARKER_CANARY'),
        false,
      )
      const { MemoryService } = await import('../src/memory.ts')
      const memory = new MemoryService(restored)
      try {
        assert.ok(memory.list(f.ctx).some((r) => r.id === f.a))
        assert.ok(memory.list(f.ctx).some((r) => r.id === f.b))
      } finally {
        memory.dispose()
      }
    } finally {
      runtime.dispose()
      await f.close()
    }
  })

test('new episode membership and anchor proof material cannot be reconstructed from an old backup journal', async () => {
  const f = await episodeFixture()
  try {
    const old = await f.snapshot()
    assert.equal(f.createEpisode().created.length, 1)
    await assert.rejects(f.recover(old), /recovery_semantic_material_missing/)
  } finally {
    await f.close()
  }
})

test('untouched summary snapshot preserves its exact journal witness across strict decoding', async () => {
  const f = await liveFixture()
  try {
    const ids = [
      savedId(f.propose('plain-a', 'A synthetic first declaration.')),
      savedId(f.propose('plain-b', 'A synthetic second declaration.')),
    ]
    const candidate = f.admit(ids).created[0]!
    const snapshot = await f.snapshot()
    const restored = await f.recover(snapshot)
    assert.deepEqual(
      new SemanticStorage(restored.db, () => restored.assertRecoveryReady()).get(candidate.id),
      f.memory.semantic.storage.get(candidate.id),
    )
  } finally {
    await f.close()
  }
})

test('attempt write canonicalizes reordered keys before its durable witness and rejects invalid input before intent', async () => {
  const f = await fixture()
  try {
    const storage = new SemanticStorage(
      f.store.db,
      () => f.store.assertRecoveryReady(),
      (m) => recordSemanticMutation(f.store.db, m),
    )
    const canonical = {
      schemaVersion: 1 as const,
      sceneId: 's',
      inputKey: 'd'.repeat(64),
      status: 'started' as const,
      version: 1,
      parents: [],
      roots: [],
    }
    const reordered = {
      roots: canonical.roots,
      parents: canonical.parents,
      version: canonical.version,
      status: canonical.status,
      inputKey: canonical.inputKey,
      sceneId: canonical.sceneId,
      schemaVersion: canonical.schemaVersion,
    }
    f.store.db.transaction(() => storage.writeAttempt(reordered, undefined)).immediate()
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    try {
      const { semanticHash } = await import('../src/memory/semantic/contracts.ts')
      const event = authority.entries().at(-1)!.revocation
      assert.equal(event.kind, 'semantic_mutation')
      if (event.kind !== 'semantic_mutation') throw Error('fixture_event_missing')
      assert.equal(event.afterHash, semanticHash(storage.attempts()[0]))
      const head = authority.head()
      assert.throws(() =>
        f.store.db
          .transaction(() => storage.writeAttempt({ ...reordered, version: 0 }, undefined))
          .immediate(),
      )
      assert.deepEqual(authority.head(), head)
    } finally {
      authority.close()
    }
    const restored = await f.recover(await f.snapshot())
    assert.deepEqual(
      new SemanticStorage(restored.db, () => restored.assertRecoveryReady()).attempts(),
      [canonical],
    )
  } finally {
    await f.close()
  }
})
