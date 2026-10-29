import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import {
  recordKnowledgeWrite,
  createRecoveryKnowledgePort,
} from '../src/memory/recovery/knowledge.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

test('knowledge reader barrier is transaction-bound, lazy, and rollback preserves old reader', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-knowledge-reader-'))
  const store = new Store(join(root, 'live', 'db.sqlite'))
  try {
    assert.equal(metadata(store.db)!.minimumReader, 1)
    createRecoveryKnowledgePort(store).assertSupported()
    assert.equal(metadata(store.db)!.minimumReader, 1)
    assert.throws(() => recordKnowledgeWrite(store.db), /recovery_transaction_required/)
    assert.throws(
      () =>
        store.db
          .transaction(() => {
            recordKnowledgeWrite(store.db)
            throw new Error('rollback')
          })
          .immediate(),
      /rollback/,
    )
    assert.equal(metadata(store.db)!.minimumReader, 1)
    store.db.transaction(() => recordKnowledgeWrite(store.db)).immediate()
    assert.equal(metadata(store.db)!.minimumReader, 7)
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('actual restore refuses future knowledge tables and leaves no published target', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-knowledge-restore-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  try {
    store.db.exec('CREATE TABLE memory_knowledge_future_v2(value TEXT)')
    await backup({ database, mediaRoot, destination: join(root, 'backup') })
    await assert.rejects(
      restore({
        backupRoot: join(root, 'backup'),
        destination: join(root, 'restored'),
        authorityDirectory: metadata(store.db)!.authority,
      }),
      /recovery_unknown_knowledge_schema/,
    )
    await assert.rejects(access(join(root, 'restored')))
    await assert.rejects(access(join(root, 'restored.quarantine')))
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('knowledge recovery strictly rejects future columns and non-text or unknown registry material', () => {
  const store = new Store(':memory:')
  try {
    const port = createRecoveryKnowledgePort(store)
    store.db.exec(
      'CREATE TABLE memory_knowledge_edges(id TEXT PRIMARY KEY,scene_id TEXT,assertion_id TEXT,admission_key TEXT,value TEXT, future TEXT)',
    )
    assert.throws(() => port.assertSupported(), /recovery_unknown_knowledge_schema/)
    store.db.exec(
      'DROP TABLE memory_knowledge_edges; CREATE TABLE memory_knowledge_edges(id TEXT PRIMARY KEY,scene_id TEXT,assertion_id TEXT,admission_key TEXT,value TEXT)',
    )
    store.db
      .prepare('INSERT INTO memory_knowledge_edges VALUES(?,?,?,?,?)')
      .run('edge', 'scene', 'assertion', 'key', Buffer.from('{}'))
    assert.throws(() => port.assertSupported(), /recovery_knowledge_material_invalid/)
    store.db
      .prepare('UPDATE memory_knowledge_edges SET value=?')
      .run(JSON.stringify({ registryVersion: 999 }))
    assert.throws(() => port.assertSupported(), /recovery_knowledge_material_invalid/)
  } finally {
    store.close()
  }
})

test('configuration v6 adds independent knowledge flags without reinterpreting legacy v5', async () => {
  const { configurationFlags, parseConfigurationRecoveryEvent } =
    await import('../src/memory/recovery/configuration-contract.ts')
  const {
    memoryExperienceFeedbackEnabled,
    memorySemanticEnabled,
    memorySemanticAutoAdmissionEnabled,
    memorySemanticAnswerEnabled,
    ...flags
  } = configurationFlags({
    memoryGraphEnabled: true,
    memoryGraphAutoAdmissionEnabled: true,
    memoryGraphAnswerEnabled: true,
  })
  assert.equal(flags.memoryKnowledgeEnabled, false)
  assert.equal(flags.memoryKnowledgeAutoAdmissionEnabled, false)
  assert.equal(flags.memoryKnowledgeAnswerEnabled, false)
  const {
    memoryKnowledgeEnabled: _enabled,
    memoryKnowledgeAutoAdmissionEnabled: _auto,
    memoryKnowledgeAnswerEnabled: _answer,
    ...old
  } = flags
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 5, flags: old })
      .flags,
    old,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 5, flags }),
    /recovery_configuration_invalid/,
  )
  assert.throws(
    () =>
      parseConfigurationRecoveryEvent({
        kind: 'configuration_flags',
        schemaVersion: 6,
        flags: old,
      }),
    /recovery_configuration_invalid/,
  )
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 6, flags }).flags,
    flags,
  )
})

test('old backup restores independent knowledge OFF flags without disabling legacy graph', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-knowledge-flags-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  let restored: Store | undefined
  try {
    store.set('settings', {
      ...store.settings,
      memoryGraphEnabled: true,
      memoryGraphAnswerEnabled: true,
      memoryKnowledgeEnabled: true,
      memoryKnowledgeAutoAdmissionEnabled: true,
      memoryKnowledgeAnswerEnabled: true,
    })
    await backup({ database, mediaRoot, destination: join(root, 'snapshot') })
    store.set('settings', {
      ...store.settings,
      memoryKnowledgeEnabled: false,
      memoryKnowledgeAutoAdmissionEnabled: false,
      memoryKnowledgeAnswerEnabled: false,
    })
    const result = await restore({
      backupRoot: join(root, 'snapshot'),
      destination: join(root, 'restored'),
      authorityDirectory: metadata(store.db)!.authority,
    })
    restored = new Store(result.database)
    assert.equal(restored.settings.memoryKnowledgeEnabled, false)
    assert.equal(restored.settings.memoryKnowledgeAutoAdmissionEnabled, false)
    assert.equal(restored.settings.memoryKnowledgeAnswerEnabled, false)
    assert.equal(restored.settings.memoryGraphEnabled, true)
    assert.equal(restored.settings.memoryGraphAnswerEnabled, true)
  } finally {
    restored?.close()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

async function knowledgeFixture() {
  const { MemoryService } = await import('../src/memory.ts')
  const root = await mkdtemp(join(tmpdir(), 'he-knowledge-live-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database),
    memory = new MemoryService(store),
    opened: { store: Store; memory: InstanceType<typeof MemoryService> }[] = []
  store.saveScene({
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
  store.set('settings', {
    ...store.settings,
    memoryKnowledgeEnabled: true,
    memoryKnowledgeAutoAdmissionEnabled: true,
    memoryKnowledgeAnswerEnabled: true,
  })
  const ctx = { sceneId: 's', simulation: false, provenance: 'source-only' as const }
  const evidence = (messageId: string, content: string) => ({
    sceneId: 's',
    messageId,
    senderId: 'person',
    sourceText: content,
    parts: [{ type: 'text' as const, text: content }],
    available: true,
    isBot: false,
  })
  const propose = (messageId: string, content: string) =>
    memory.propose(
      { ...ctx, sourceEvidence: [evidence(messageId, content)] },
      {
        kind: 'fact',
        content,
        sources: [{ sceneId: 's', messageId, senderId: 'person' }],
        subject: { kind: 'self', sourceMessageId: messageId },
        epistemicKind: 'explicit',
      },
    )
  const admit = (id: string, messages: string[], content: string, title = 'Synthetic tool') => {
    const state = store.db
      .prepare('SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?')
      .get(id) as { version: number; deletion_epoch: number }
    return store.db
      .transaction(() =>
        memory.knowledge.admit(
          { ...ctx, sourceEvidence: messages.map((m) => evidence(m, content)) },
          { id, version: state.version, deletionEpoch: state.deletion_epoch },
          [
            {
              registryVersion: 1,
              predicate: 'uses_tool',
              subject: { kind: 'self', personId: 'person' },
              object: { kind: 'new', entityKind: 'tool', title },
              polarity: 'positive',
              modality: 'asserted',
              temporalScope: 'current',
              conditions: [],
              sourceSpans: messages.map((messageId) => ({
                messageId,
                partIndex: 0,
                start: 0,
                end: content.length,
                stance: 'self' as const,
              })),
            },
          ],
        ),
      )
      .immediate()
  }
  const query = (m = memory) =>
    m.knowledge.query(ctx, {
      anchor: { kind: 'person', personId: 'person' },
      predicates: ['uses_tool'],
      depth: 1,
    })
  return {
    root,
    store,
    memory,
    ctx,
    propose,
    admit,
    query,
    snapshot: async (name = 'snapshot') => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (snapshot: string, name = 'restored') => {
      const output = await restore({
        backupRoot: snapshot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const s = new Store(output.database),
        m = new MemoryService(s)
      opened.push({ store: s, memory: m })
      return { store: s, memory: m }
    },
    close: async () => {
      for (const x of opened) {
        x.memory.dispose()
        x.store.close()
      }
      memory.dispose()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('real Memory source recall preserves exact independent OR and old backup restores the same qualified graph', async () => {
  const f = await knowledgeFixture()
  try {
    const content = 'I use the synthetic calibration tool.'
    const first = f.propose('first', content),
      second = f.propose('independent', content)
    assert.equal(first.status, 'saved')
    assert.equal(second.status, 'saved')
    if (first.status !== 'saved' || second.status !== 'saved')
      throw new Error('fixture_assertion_failed')
    assert.equal(first.memory.id, second.memory.id)
    const result = f.admit(first.memory.id, ['first', 'independent'], content)
    assert.deepEqual(result.rejected, [])
    assert.equal(result.created.length, 1)
    assert.equal(f.query().edges.length, 1)
    const snapshot = await f.snapshot()
    f.memory.recall('s', 'first')
    const live = f.query()
    assert.equal(
      f.memory.knowledge.storage.rows().length,
      1,
      'privacy recall must retain an independent OR graph row',
    )
    assert.equal(live.edges.length, 1, 'independent OR must stay currently readable')
    assert.equal(live.edges[0]!.proofs.length, 1)
    assert.equal(live.edges[0]!.proofs[0]!.sources[0]!.messageId, 'independent')
    const restored = await f.recover(snapshot)
    assert.deepEqual(f.query(restored.memory), live)
    assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
    assert.equal(
      JSON.stringify(restored.memory.knowledge.storage.rows()).includes('"messageId":"first"'),
      false,
    )
  } finally {
    await f.close()
  }
})

test('real terminal assertion deletion strips graph labels from an old backup while independent assertions survive', async () => {
  const f = await knowledgeFixture()
  try {
    const first = f.propose('removed', 'I use the removed synthetic calibration tool.'),
      other = f.propose('kept', 'I use a different independent synthetic instrument.')
    assert.equal(first.status, 'saved')
    assert.equal(other.status, 'saved')
    if (first.status !== 'saved' || other.status !== 'saved')
      throw new Error('fixture_assertion_failed')
    assert.equal(f.admit(first.memory.id, ['removed'], first.memory.content).created.length, 1)
    assert.equal(f.admit(other.memory.id, ['kept'], other.memory.content).created.length, 1)
    const snapshot = await f.snapshot()
    f.memory.delete(first.memory.id)
    const restored = await f.recover(snapshot)
    assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
    assert.equal(restored.memory.knowledge.storage.rows().length, 1)
    assert.equal(restored.memory.knowledge.storage.rows()[0]!.backing.id, other.memory.id)
    assert.equal(f.query(restored.memory).edges.length, 1)
  } finally {
    await f.close()
  }
})

test('old backup source withdrawal removes reverse-dependent entity labels but keeps unrelated knowledge', async () => {
  const f = await knowledgeFixture()
  try {
    const text = 'I use a synthetic platform tool.',
      first = f.propose('origin', text)
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') throw new Error('fixture_assertion_failed')
    const root = f.admit(first.memory.id, ['origin'], text).created[0]!
    assert.equal(root.object.kind, 'entity')
    if (root.object.kind !== 'entity') throw new Error('fixture_entity_failed')
    const childText = 'The synthetic platform tool version is release-A.',
      child = f.propose('child', childText)
    assert.equal(child.status, 'saved')
    if (child.status !== 'saved') throw new Error('fixture_assertion_failed')
    const state = f.store.db
      .prepare('SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?')
      .get(child.memory.id) as { version: number; deletion_epoch: number }
    const admitted = f.store.db
      .transaction(() =>
        f.memory.knowledge.admit(
          {
            ...f.ctx,
            provenance: 'contextual',
            exposedEntities: [
              {
                entityId: root.object.kind === 'entity' ? root.object.entity.id : '',
                backingRef: root.backing,
              },
            ],
            sourceEvidence: [
              {
                sceneId: 's',
                messageId: 'child',
                senderId: 'person',
                sourceText: childText,
                parts: [{ type: 'text', text: childText }],
                available: true,
                isBot: false,
              },
            ],
          },
          { id: child.memory.id, version: state.version, deletionEpoch: state.deletion_epoch },
          [
            {
              registryVersion: 1,
              predicate: 'has_property',
              property: 'version',
              subject: {
                kind: 'existing',
                entityId: root.object.kind === 'entity' ? root.object.entity.id : '',
              },
              object: { kind: 'literal', valueType: 'text', value: 'release-A' },
              polarity: 'positive',
              modality: 'asserted',
              temporalScope: 'current',
              conditions: [],
              sourceSpans: [
                {
                  messageId: 'child',
                  partIndex: 0,
                  start: 0,
                  end: childText.length,
                  stance: 'asserted',
                },
              ],
            },
          ],
        ),
      )
      .immediate()
    assert.deepEqual(admitted.rejected, [])
    assert.equal(admitted.created.length, 1)
    const other = f.propose('unrelated', 'I use an independent synthetic instrument.')
    assert.equal(other.status, 'saved')
    if (other.status !== 'saved') throw new Error('fixture_assertion_failed')
    assert.equal(f.admit(other.memory.id, ['unrelated'], other.memory.content).created.length, 1)
    const snapshot = await f.snapshot()
    f.memory.recall('s', 'origin')
    assert.equal(f.memory.knowledge.storage.rows().length, 1)
    const restored = await f.recover(snapshot)
    assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
    const body = JSON.stringify(restored.memory.knowledge.storage.rows())
    assert.equal(body.includes('release-A'), false)
    assert.equal(body.includes(root.id), false)
    assert.equal(restored.memory.knowledge.storage.rows()[0]!.backing.id, other.memory.id)
  } finally {
    await f.close()
  }
})

test('source edits never rebase a stored graph: old snapshot requires current witness, current snapshot stays unreadable', async () => {
  const f = await knowledgeFixture()
  try {
    const { createHash } = await import('node:crypto')
    const text = 'I use a synthetic editable tool.',
      identity = {
        sceneId: 's',
        adapterId: 'offline',
        accountId: 'bot',
        messageId: 'editable',
        senderId: 'person',
      }
    const observed = f.store.sourceEvents.observe({
      kind: 'ingress',
      identity,
      bodyHash: createHash('sha256').update(text).digest('hex'),
      occurredAt: null,
    })
    assert.equal(observed.status, 'new')
    if (!('ref' in observed) || !observed.ref) throw new Error('fixture_event_failed')
    const source = {
      sceneId: 's',
      messageId: 'editable',
      senderId: 'person',
      eventRef: observed.ref,
    }
    const context = {
      ...f.ctx,
      sourceEvidence: [
        {
          ...source,
          sourceText: text,
          parts: [{ type: 'text' as const, text }],
          available: true,
          isBot: false,
        },
      ],
    }
    const saved = f.memory.propose(context, {
      kind: 'fact',
      content: text,
      sources: [source],
      subject: { kind: 'self', sourceMessageId: 'editable' },
      epistemicKind: 'explicit',
      knowledge: [
        {
          registryVersion: 1,
          predicate: 'uses_tool',
          subject: { kind: 'self', personId: 'person' },
          object: { kind: 'new', entityKind: 'tool', title: 'Synthetic editable tool' },
          polarity: 'positive',
          modality: 'asserted',
          temporalScope: 'current',
          conditions: [],
          sourceSpans: [
            { messageId: 'editable', partIndex: 0, start: 0, end: text.length, stance: 'self' },
          ],
        },
      ],
    })
    assert.equal(saved.status, 'saved')
    assert.equal(f.query().edges.length, 1)
    const old = await f.snapshot('before-edit')
    f.store.sourceEvents.observe({
      kind: 'edit',
      identity,
      bodyHash: createHash('sha256').update('Different synthetic statement').digest('hex'),
      occurredAt: null,
      editId: 'edit-1',
      order: { kind: 'platform_sequence', sequence: 1 },
    })
    assert.equal(f.query().edges.length, 0)
    await assert.rejects(
      f.recover(old, 'stale'),
      /recovery_source_event_head_mismatch|recovery_source_event_material_missing/,
    )
    const current = await f.snapshot('after-edit')
    const restored = await f.recover(current, 'current')
    assert.equal(f.query(restored.memory).edges.length, 0)
    assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
  } finally {
    await f.close()
  }
})

for (const mode of ['ordinary', 'graph'] as const)
  test(`old backup clears all-new typed copies after ${mode} read dependency withdrawal`, async () => {
    const f = await knowledgeFixture()
    try {
      const original = f.propose('old-read', 'I use the private synthetic upstream instrument.')
      assert.equal(original.status, 'saved')
      if (original.status !== 'saved') throw new Error('fixture_assertion_failed')
      const state = f.store.db
        .prepare('SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?')
        .get(original.memory.id) as { version: number; deletion_epoch: number }
      const reference = {
        id: original.memory.id,
        version: state.version,
        deletionEpoch: state.deletion_epoch,
      }
      const root =
        mode === 'graph'
          ? f.admit(original.memory.id, ['old-read'], original.memory.content).created[0]!
          : undefined
      const text = 'I now refer to an independently named tool.',
        source = { sceneId: 's', messageId: 'typed-copy', senderId: 'person' }
      const copied = f.memory.propose(
        {
          ...f.ctx,
          provenance: 'contextual',
          ...(root
            ? {
                exposedKnowledgeEdges: [
                  { id: root.id, version: root.version, backing: root.backing },
                ],
              }
            : { dependencyMemoryIds: [reference.id], dependencyParentReferences: [reference] }),
          sourceEvidence: [
            {
              ...source,
              sourceText: text,
              parts: [{ type: 'text', text }],
              available: true,
              isBot: false,
            },
          ],
        },
        {
          kind: 'fact',
          content: 'Model-derived typed copy',
          sources: [source],
          subject: { kind: 'self', sourceMessageId: source.messageId },
          epistemicKind: 'explicit',
          knowledge: [
            {
              registryVersion: 1,
              predicate: 'uses_tool',
              subject: { kind: 'self', personId: 'person' },
              object: { kind: 'new', entityKind: 'tool', title: 'PRIVATE_TYPED_READ_COPY' },
              polarity: 'positive',
              modality: 'asserted',
              temporalScope: 'current',
              conditions: [],
              sourceSpans: [
                {
                  messageId: source.messageId,
                  partIndex: 0,
                  start: 0,
                  end: text.length,
                  stance: 'self',
                },
              ],
            },
          ],
        },
      )
      assert.equal(copied.status, 'saved')
      const child = f.memory.knowledge.storage
        .rows()
        .find(
          (e) => e.object.kind === 'entity' && e.object.entity.title === 'PRIVATE_TYPED_READ_COPY',
        )
      assert.ok(child)
      assert.equal(child.readDependencies.length, 1)
      assert.equal(
        child.dependencies.length,
        0,
        'all-new endpoints still inherit actual read lineage',
      )
      assert.equal(child.readDependencies[0]!.backing.id, reference.id)
      const independent = f.propose('retained', 'I use a separate independent public instrument.')
      assert.equal(independent.status, 'saved')
      if (independent.status !== 'saved') throw new Error('fixture_assertion_failed')
      assert.equal(
        f.admit(independent.memory.id, ['retained'], independent.memory.content).created.length,
        1,
      )
      const snapshot = await f.snapshot()
      if (mode === 'ordinary') f.memory.delete(original.memory.id)
      else f.memory.recall('s', 'old-read')
      assert.equal(f.memory.knowledge.storage.get(child.id), undefined)
      const restored = await f.recover(snapshot)
      assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
      const rows = restored.memory.knowledge.storage.rows()
      assert.equal(rows.length, 1)
      assert.equal(rows[0]!.backing.id, independent.memory.id)
      assert.equal(JSON.stringify(rows).includes('PRIVATE_TYPED_READ_COPY'), false)
      assert.equal(f.query(restored.memory).edges.length, 1)
    } finally {
      await f.close()
    }
  })

test('old backup prunes indirect parent privacy without upgrading a span-selected proof to an independent OR branch', async () => {
  const f = await knowledgeFixture()
  try {
    const parent = f.propose('private-parent', 'Synthetic private premise for a dependent claim.')
    assert.equal(parent.status, 'saved')
    if (parent.status !== 'saved') throw new Error('fixture_parent_failed')
    const parentState = f.memory.revisionState(parent.memory.id)
    const parentRef = {
      id: parent.memory.id,
      version: parentState.version,
      deletionEpoch: parentState.deletionEpoch,
    }
    const text = 'I use the same synthetic declared instrument.'
    const sourceA = { sceneId: 's', messageId: 'dependent-a', senderId: 'person' }
    const a = f.memory.propose(
      {
        ...f.ctx,
        provenance: 'contextual',
        dependencyMemoryIds: [parentRef.id],
        dependencyParentReferences: [parentRef],
        sourceEvidence: [
          {
            ...sourceA,
            sourceText: text,
            parts: [{ type: 'text', text }],
            available: true,
            isBot: false,
          },
        ],
      },
      {
        kind: 'fact',
        content: text,
        sources: [sourceA],
        subject: { kind: 'self', sourceMessageId: sourceA.messageId },
        epistemicKind: 'explicit',
      },
    )
    const b = f.propose('independent-b', text)
    assert.equal(a.status, 'saved')
    assert.equal(b.status, 'saved')
    if (a.status !== 'saved' || b.status !== 'saved') throw new Error('fixture_or_failed')
    assert.equal(a.memory.id, b.memory.id)
    const selected = f.admit(a.memory.id, ['dependent-a'], text, 'INDIRECT_PRIVATE_P_CANARY')
    assert.deepEqual(selected.rejected, [])
    assert.equal(selected.created.length, 1)
    assert.equal(selected.created[0]!.proofs.length, 1)
    assert.ok(selected.created[0]!.proofs[0]!.parents.includes(parentRef.id))
    const independent = f.admit(a.memory.id, ['independent-b'], text, 'Independent retained tool')
    assert.deepEqual(independent.rejected, [])
    assert.equal(independent.created.length, 1)
    assert.equal(independent.created[0]!.proofs[0]!.parents.includes(parentRef.id), false)
    const snapshot = await f.snapshot()
    f.memory.recall('s', 'private-parent')
    assert.ok(f.memory.list(f.ctx).some((record) => record.id === a.memory.id))
    assert.equal(f.memory.knowledge.storage.get(selected.created[0]!.id), undefined)
    assert.equal(f.query().edges.length, 1)
    assert.equal(f.query().edges[0]!.id, independent.created[0]!.id)
    const restored = await f.recover(snapshot)
    assert.ok(restored.memory.list(f.ctx).some((record) => record.id === a.memory.id))
    assert.deepEqual(restored.memory.knowledge.storage.rows(), f.memory.knowledge.storage.rows())
    assert.deepEqual(f.query(restored.memory), f.query())
    assert.equal(
      JSON.stringify(restored.memory.knowledge.storage.rows()).includes(
        'INDIRECT_PRIVATE_P_CANARY',
      ),
      false,
    )
    assert.equal(restored.memory.knowledge.storage.get(selected.created[0]!.id), undefined)
  } finally {
    await f.close()
  }
})
