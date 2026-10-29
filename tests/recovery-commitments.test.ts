import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { parseCommitmentRecoveryEvent } from '../src/memory/recovery/commitment-contract.ts'
import {
  recordCommitmentMutation,
  createRecoveryCommitmentPort,
} from '../src/memory/recovery/commitments.ts'
import {
  metadata,
  saveGate,
  RecoveryAuthority,
  requireCommitmentReader,
  requireSourceEventReader,
  captureRecoveryEffects,
  revoke,
} from '../src/memory/recovery/ledger.ts'
import {
  configurationFlags,
  parseConfigurationRecoveryEvent,
} from '../src/memory/recovery/configuration-contract.ts'
const head = {
  id: 'synthetic-plan',
  sceneId: 'scene',
  latestVersion: 2,
  activeVersion: 2,
  everActivatedVersion: 2,
  everTerminal: null,
  deletionEpoch: 0,
  mutationVersion: 4,
  deleted: false,
}

test('commitment authority payload is strict metadata and never accepts terms or unversioned state', () => {
  const value = {
    kind: 'commitment_head',
    schemaVersion: 1,
    sceneId: 'scene',
    id: 'synthetic-plan',
    termsVersion: 2,
    mutationVersion: 4,
    deletionEpoch: 0,
    qualificationHash: 'a'.repeat(64),
  }
  assert.deepEqual(parseCommitmentRecoveryEvent(value), value)
  assert.throws(
    () => parseCommitmentRecoveryEvent({ ...value, terms: 'RAW_TERMS_NOT_ALLOWED' }),
    /invalid/,
  )
  assert.throws(() => parseCommitmentRecoveryEvent({ ...value, mutationVersion: 0 }), /invalid/)
  assert.throws(() => parseCommitmentRecoveryEvent({ ...value, schemaVersion: 2 }), /invalid/)
  assert.equal(
    parseCommitmentRecoveryEvent({
      kind: 'commitment_delete',
      schemaVersion: 1,
      sceneId: 'scene',
      id: 'synthetic-plan',
      deletionEpoch: 1,
    }).kind,
    'commitment_delete',
  )
})

test('reader capabilities advance only on actual corresponding mutations; authority chain format stays one', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-commit-reader-'))
  const path = join(root, 'live', 'humanengine.sqlite'),
    store = new Store(path)
  try {
    assert.equal(metadata(store.db)!.minimumReader, 1)
    store.db.transaction(() => requireSourceEventReader(store.db)).immediate()
    assert.equal(metadata(store.db)!.minimumReader, 2)
    store.db.transaction(() => requireCommitmentReader(store.db)).immediate()
    assert.equal(metadata(store.db)!.minimumReader, 3)
    const authority = new RecoveryAuthority(metadata(store.db)!.authority)
    try {
      assert.equal(
        (authority.db.prepare('SELECT format FROM identity').get() as { format: number }).format,
        1,
      )
    } finally {
      authority.close()
    }
    const other = new Store(path)
    other.close()
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('commitment precommit intent survives caller rollback and blocks the old live snapshot', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-commit-intent-')),
    store = new Store(join(root, 'live', 'humanengine.sqlite'))
  try {
    const before = metadata(store.db)!
    assert.throws(
      () =>
        store.db
          .transaction(() => {
            recordCommitmentMutation(store.db, {
              kind: 'confirm',
              head,
              qualificationHash: 'a'.repeat(64),
            })
            throw new Error('synthetic failed local commit')
          })
          .immediate(),
      /synthetic failed/,
    )
    assert.equal(metadata(store.db)!.head.sequence, before.head.sequence)
    assert.throws(() => store.get('settings', {}), /head_changed/)
    const authority = new RecoveryAuthority(before.authority)
    try {
      const entry = authority.entries().at(-1)!.revocation
      assert.equal(entry.kind, 'commitment_head')
      assert.ok(!JSON.stringify(entry).includes('termsText'))
    } finally {
      authority.close()
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('future commitment schema is rejected before any recovery service construction', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-commit-schema-')),
    path = join(root, 'live', 'humanengine.sqlite')
  const live = new Store(path),
    gate = metadata(live.db)!
  live.db.exec('CREATE TABLE memory_commitment_future_v2(id TEXT)')
  saveGate(live.db, { ...gate, state: 'quarantine' })
  live.close()
  const authority = new RecoveryAuthority(gate.authority),
    store = new Store(path, { authority })
  try {
    let constructed = false
    const port = createRecoveryCommitmentPort(store, () => {
      constructed = true
      throw new Error('must not construct')
    })
    assert.throws(() => port.assertSupported(), /unknown_commitment_schema/)
    assert.equal(constructed, false)
  } finally {
    store.close()
    authority.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('configuration authority preserves exact v1/v2 and adds only three opt-in v3 flags', () => {
  const {
    memoryExperienceFeedbackEnabled,
    memorySemanticEnabled,
    memorySemanticAutoAdmissionEnabled,
    memorySemanticAnswerEnabled,
    memoryKnowledgeEnabled,
    memoryKnowledgeAutoAdmissionEnabled,
    memoryKnowledgeAnswerEnabled,
    topicAdmissionEnabled,
    memoryWorkingSetEnabled,
    memoryEpisodesEnabled,
    ...flags
  } = configurationFlags({
    memoryCommitmentsEnabled: true,
    memoryCommitmentsAnswerEnabled: true,
    memoryHistoricalReadEnabled: true,
  })
  const {
    memoryCommitmentsEnabled,
    memoryCommitmentsAnswerEnabled,
    memoryHistoricalReadEnabled,
    ...v2
  } = flags
  const { sourceEventRevisionsEnabled, ...v1 } = v2
  assert.deepEqual(
    [memoryCommitmentsEnabled, memoryCommitmentsAnswerEnabled, memoryHistoricalReadEnabled],
    [true, true, true],
  )
  assert.equal(sourceEventRevisionsEnabled, false)
  for (const [schemaVersion, versionFlags, count] of [
    [1, v1, 10],
    [2, v2, 11],
    [3, flags, 14],
  ] as const) {
    assert.equal(Object.keys(versionFlags).length, count)
    assert.deepEqual(
      parseConfigurationRecoveryEvent({
        kind: 'configuration_flags',
        schemaVersion,
        flags: versionFlags,
      }).flags,
      versionFlags,
    )
  }
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 2, flags }),
    /invalid/,
  )
})

// These fixtures use the actual MemoryService candidate/proof and privacy lifecycle, not a permissive recovery authority.
async function commitmentFixture() {
  const { MemoryService } = await import('../src/memory.ts')
  const { mkdir } = await import('node:fs/promises')
  const root = await mkdtemp(join(tmpdir(), 'he-real-commit-recovery-'))
  const store = new Store(join(root, 'live', 'humanengine.sqlite'))
  store.set('settings', { ...store.settings, memoryCommitmentsEnabled: true })
  store.saveScene({
    id: 's',
    kind: 'group',
    adapterId: 'synthetic',
    accountId: 'a',
    platformSceneId: 's',
    name: 'Synthetic',
    learningEnabled: true,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  const memory = new MemoryService(store)
  await mkdir(join(root, 'media'))
  const { backup } = await import('../scripts/backup.ts'),
    { restore } = await import('../scripts/restore.ts')
  const authorityDirectory = metadata(store.db)!.authority
  const terms = (time: string) => ({
    kind: 'plan' as const,
    title: 'Synthetic meeting',
    participantIds: ['a', 'b'],
    temporalScope: 'future' as const,
    polarity: 'positive' as const,
    modality: 'asserted' as const,
    conditions: [],
    timeQualification: time,
  })
  const add = (
    messageId: string,
    personId: string,
    command: import('../src/memory/domain/commitments.ts').CommitmentCommand,
    content = `Synthetic explicit declaration ${messageId}`,
  ) => {
    const target = 'target' in command ? command.target : undefined
    const source = { sceneId: 's', messageId, senderId: personId }
    const result = memory.propose(
      {
        sceneId: 's',
        simulation: false,
        provenance: target ? 'contextual' : 'source-only',
        dependencyMemoryIds: [],
        sourceEvidence: [
          {
            ...source,
            sourceText: content,
            available: true,
            isBot: false,
            mentions: [{ partIndex: 0, userId: personId === 'a' ? 'b' : 'a' }],
          },
        ],
        exposedCommitments: target
          ? [
              {
                ...target,
                mutationVersion: memory.commitments.storage.head(target.id)!.mutationVersion,
              },
            ]
          : [],
      },
      {
        kind: 'fact',
        content,
        sources: [source],
        subject: { kind: 'self', sourceMessageId: messageId },
        epistemicKind: 'explicit',
        commitment: command,
      },
    )
    assert.equal(result.status, 'saved')
    if (
      result.status !== 'saved' ||
      !result.commitment ||
      !('item' in result.commitment) ||
      !result.commitment.item
    )
      throw new Error(`synthetic_commitment_not_saved:${JSON.stringify(result)}`)
    return result.commitment.item
  }
  return {
    root,
    store,
    memory,
    terms,
    add,
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
        authorityDirectory,
      }),
    close: async () => {
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('real MemoryService recall never restores old confirmed 18 after activated 19 loses a participant', async () => {
  const f = await commitmentFixture()
  try {
    const first = f.add('a18', 'a', { kind: 'propose', terms: f.terms('18:00') })
    f.add('b18', 'b', { kind: 'confirm', target: first.ref })
    await f.snap('eighteen')
    const next = f.add('a19', 'a', { kind: 'amend', target: first.ref, terms: f.terms('19:00') })
    f.add('b19', 'b', { kind: 'confirm', target: next.ref })
    await f.snap('nineteen')
    f.memory.recall('s', 'b19')
    const expected = f.memory.commitments.snapshot(first.ref.id)!
    assert.equal(expected.head.everActivatedVersion, 2)
    const ctx = { sceneId: 's', simulation: false, provenance: 'source-only' as const }
    const live = f.memory.commitments.list(ctx).items[0]!
    assert.equal(live.ref.version, 2)
    assert.equal(live.confirmation, 'incomplete')
    await assert.rejects(
      f.restore('eighteen', 'denied'),
      /recovery_commitment_(head_mismatch|material_missing)/,
    )
    const result = await f.restore('nineteen', 'restored')
    const restored = new Store(result.database)
    try {
      const { MemoryService } = await import('../src/memory.ts'),
        memory = new MemoryService(restored)
      assert.equal(
        memory.commitments.snapshot(first.ref.id)!.qualificationHash,
        expected.qualificationHash,
      )
      const view = memory.commitments.list(ctx).items[0]!
      assert.equal(view.ref.version, 2)
      assert.equal(view.confirmation, 'incomplete')
      assert.notEqual(view.terms?.timeQualification, '18:00')
    } finally {
      restored.close()
    }
  } finally {
    await f.close()
  }
})

test('real terminal commitment delete clears retained terms and commands without deleting independent plan', async () => {
  const f = await commitmentFixture()
  try {
    const first = f.add('first-a', 'a', { kind: 'propose', terms: f.terms('18:00') })
    const independent = f.add('second-a', 'a', { kind: 'propose', terms: f.terms('20:00') })
    await f.snap('before-delete')
    f.store.db
      .transaction(() =>
        f.memory.commitments.erase(
          { sceneId: 's', simulation: false, provenance: 'source-only' },
          first.ref,
        ),
      )
      .immediate()
    const result = await f.restore('before-delete', 'restored')
    const restored = new Store(result.database)
    try {
      const { MemoryService } = await import('../src/memory.ts'),
        memory = new MemoryService(restored)
      assert.equal(memory.commitments.snapshot(first.ref.id)!.head.deleted, true)
      assert.equal(memory.commitments.snapshot(first.ref.id)!.revisions.length, 0)
      assert.equal(
        restored.db
          .prepare('SELECT 1 FROM memory_commitment_commands WHERE id=?')
          .get(first.ref.id),
        undefined,
      )
      assert.equal(memory.commitments.snapshot(independent.ref.id)!.head.deleted, false)
      assert.equal(memory.commitments.snapshot(independent.ref.id)!.revisions.length, 1)
    } finally {
      restored.close()
    }
  } finally {
    await f.close()
  }
})

test('real independent OR recall advances exact backing qualification and restores identical confirmed snapshot', async () => {
  const f = await commitmentFixture()
  try {
    const content = 'Synthetic self commitment with two independently retained declarations'
    let backingId = ''
    for (const messageId of ['first-or', 'independent-or']) {
      const source = { sceneId: 's', messageId, senderId: 'a' }
      const saved = f.memory.propose(
        {
          sceneId: 's',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [{ ...source, sourceText: content, available: true, isBot: false }],
        },
        {
          kind: 'fact',
          content,
          sources: [source],
          subject: { kind: 'self', sourceMessageId: messageId },
          epistemicKind: 'explicit',
        },
      )
      assert.equal(saved.status, 'saved')
      if (saved.status !== 'saved') throw new Error('synthetic_or_assertion_not_saved')
      if (backingId) assert.equal(saved.memory.id, backingId)
      backingId = saved.memory.id
    }
    // Exercise the actual core authority against an existing OR-backed assertion.
    // The model-facing marker admission path intentionally creates one-source backing records.
    const state = f.store.db
      .prepare('SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?')
      .get(backingId) as { version: number; deletion_epoch: number }
    const admitted = f.store.db
      .transaction(() =>
        f.memory.commitments.admit(
          {
            sceneId: 's',
            simulation: false,
            provenance: 'source-only',
            sourceEvidence: [
              { sceneId: 's', messageId: 'first-or', senderId: 'a', available: true, isBot: false },
            ],
          } as import('../src/memory/domain/contracts.ts').MemoryContext & {
            provenance: 'source-only'
          },
          { id: backingId, version: state.version, deletionEpoch: state.deletion_epoch },
          { kind: 'propose', terms: { ...f.terms('18:00'), participantIds: ['a'] } },
        ),
      )
      .immediate()
    assert.equal(admitted.status, 'saved')
    if (!('item' in admitted) || !admitted.item)
      throw new Error('synthetic_or_commitment_not_saved')
    const plan = admitted.item
    assert.equal(plan.confirmation, 'confirmed')
    const before = f.memory.commitments.snapshot(plan.ref.id)!
    assert.equal(before.revisions[0]!.evidence[0]!.proofs.length, 2)
    await f.snap('before-or-recall')
    f.memory.recall('s', 'first-or')
    const after = f.memory.commitments.snapshot(plan.ref.id)!
    const evidence = after.revisions[0]!.evidence[0]!
    assert.deepEqual(evidence.backing, before.revisions[0]!.evidence[0]!.backing)
    assert.ok(evidence.qualificationRef)
    assert.notDeepEqual(evidence.qualificationRef, evidence.backing)
    assert.equal(evidence.proofs.length, 1)
    assert.equal(evidence.proofs[0]!.sources[0]!.messageId, 'independent-or')
    const ctx = { sceneId: 's', simulation: false, provenance: 'source-only' as const }
    assert.equal(f.memory.commitments.list(ctx).items[0]!.confirmation, 'confirmed')
    const result = await f.restore('before-or-recall', 'restored-or')
    const restored = new Store(result.database)
    try {
      const { MemoryService } = await import('../src/memory.ts'),
        memory = new MemoryService(restored)
      assert.equal(
        memory.commitments.snapshot(plan.ref.id)!.qualificationHash,
        after.qualificationHash,
      )
      assert.deepEqual(
        restored.db
          .prepare(
            'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
          )
          .get(evidence.backing.id),
        f.store.db
          .prepare(
            'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
          )
          .get(evidence.backing.id),
        'restored backing authority must equal the live post-recall qualification, not only the stored commitment hash',
      )
      assert.equal(memory.commitments.list(ctx).items[0]!.confirmation, 'confirmed')
    } finally {
      restored.close()
    }
    const again = await f.restore('before-or-recall', 'restored-or-again')
    const repeated = new Store(again.database)
    try {
      const { MemoryService } = await import('../src/memory.ts')
      const memory = new MemoryService(repeated)
      assert.equal(
        memory.commitments.snapshot(plan.ref.id)!.qualificationHash,
        after.qualificationHash,
      )
      assert.equal(memory.commitments.list(ctx).items[0]!.confirmation, 'confirmed')
      assert.deepEqual(
        repeated.db
          .prepare(
            'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
          )
          .get(evidence.backing.id),
        f.store.db
          .prepare(
            'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
          )
          .get(evidence.backing.id),
      )
    } finally {
      repeated.close()
    }
  } finally {
    await f.close()
  }
})

test('causal recovery receipts are maintenance-only and never escape a failed transaction or async callback', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-replay-receipts-'))
  const path = join(root, 'live', 'humanengine.sqlite'),
    live = new Store(path)
  const gate = metadata(live.db)!
  assert.throws(() => captureRecoveryEffects(live.db, () => undefined), /maintenance_required/)
  live.db.exec('CREATE TABLE synthetic_capture_probe(value TEXT)')
  saveGate(live.db, { ...gate, state: 'quarantine' })
  live.close()
  const authority = new RecoveryAuthority(gate.authority),
    store = new Store(path, { authority })
  try {
    assert.throws(
      () =>
        captureRecoveryEffects(store.db, () => {
          revoke(store.db, { kind: 'assertion', id: 'failed', throughVersion: 2, terminal: false })
          store.db.prepare('INSERT INTO synthetic_capture_probe VALUES(?)').run('must rollback')
          throw new Error('synthetic failed lifecycle')
        }),
      /synthetic failed lifecycle/,
    )
    assert.equal(store.db.prepare('SELECT 1 FROM synthetic_capture_probe').get(), undefined)
    const valid = {
      kind: 'assertion' as const,
      id: 'successful',
      throughVersion: 7,
      terminal: false,
    }
    assert.deepEqual(
      captureRecoveryEffects(store.db, () => revoke(store.db, valid)),
      [valid],
    )
    assert.throws(
      () => captureRecoveryEffects(store.db, () => Promise.resolve()),
      /async_capture_denied/,
    )
    assert.deepEqual(
      captureRecoveryEffects(store.db, () => undefined),
      [],
    )
    assert.equal(authority.head().sequence, gate.head.sequence)
  } finally {
    store.close()
    authority.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('privacy recall of original proposal removes dependent amended terms during real restore', async () => {
  const f = await commitmentFixture()
  try {
    const first = f.add('privacy-a18', 'a', { kind: 'propose', terms: f.terms('18:00') })
    f.add('privacy-b18', 'b', { kind: 'confirm', target: first.ref })
    const next = f.add('privacy-a19', 'a', {
      kind: 'amend',
      target: first.ref,
      terms: f.terms('19:00'),
    })
    f.add('privacy-b19', 'b', { kind: 'confirm', target: next.ref })
    await f.snap('before-proposal-recall')
    f.memory.recall('s', 'privacy-a18')
    const expected = f.memory.commitments.snapshot(first.ref.id)!
    assert.equal(expected.head.everActivatedVersion, 2)
    assert.equal(expected.revisions.length, 0)
    const result = await f.restore('before-proposal-recall', 'restored-privacy')
    const restored = new Store(result.database)
    try {
      const { MemoryService } = await import('../src/memory.ts'),
        memory = new MemoryService(restored)
      const snapshot = memory.commitments.snapshot(first.ref.id)!
      assert.equal(snapshot.qualificationHash, expected.qualificationHash)
      assert.equal(snapshot.revisions.length, 0)
      assert.equal(
        restored.db
          .prepare('SELECT 1 FROM memory_commitment_revisions WHERE id=?')
          .get(first.ref.id),
        undefined,
      )
      assert.equal(
        restored.db
          .prepare('SELECT 1 FROM memory_commitment_commands WHERE id=?')
          .get(first.ref.id),
        undefined,
      )
      const view = memory.commitments.list({
        sceneId: 's',
        simulation: false,
        provenance: 'source-only',
      }).items[0]!
      assert.equal(view.ref.version, 2)
      assert.equal(view.confirmation, 'incomplete')
      assert.equal(view.terms, undefined)
    } finally {
      restored.close()
    }
  } finally {
    await f.close()
  }
})
