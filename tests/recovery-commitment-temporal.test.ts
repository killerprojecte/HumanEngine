import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
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

test('actual privacy temporal marker replays its original recorded time without recovered private evidence', async () => {
  const f = await commitmentFixture()
  try {
    const plan = f.add('proposal-a', 'a', { kind: 'propose', terms: f.terms('18:00') })
    f.add('confirm-b', 'b', { kind: 'confirm', target: plan.ref })
    const before = f.memory.commitments.snapshot(plan.ref.id)!
    assert.ok(before.timeline)
    await f.snap('confirmed')
    f.memory.recall('s', 'confirm-b')
    const live = f.memory.commitments.snapshot(plan.ref.id)!
    const marker = live.timeline!.transitions.at(-1)!
    assert.equal(marker.states, null)
    assert.ok(['purge', 'requalify'].includes(marker.reason))
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    try {
      const events = authority
        .entries()
        .map((e) => e.revocation)
        .filter((e) => e.kind === 'commitment_head')
      const event = events.at(-1)!
      assert.equal(event.schemaVersion, 2)
      assert.equal(event.privacyMarker?.recordedAt, marker.recordedAt)
      assert.equal(JSON.stringify(event).includes('Synthetic meeting'), false)
    } finally {
      authority.close()
    }
    const result = await f.restore('confirmed', 'restored'),
      store = new Store(result.database)
    try {
      const { MemoryService } = await import('../src/memory.ts')
      const memory = new MemoryService(store)
      assert.deepEqual(memory.commitments.snapshot(plan.ref.id), live)
      const stored = store.db.prepare('SELECT value FROM memory_commitment_transitions').all()
      assert.equal(JSON.stringify(stored).includes('confirm-b'), false)
      assert.equal(metadata(store.db)!.minimumReader, 10)
      memory.dispose()
    } finally {
      store.close()
    }
  } finally {
    await f.close()
  }
})

test('missing newly confirmed temporal material is quarantined rather than projected from current time', async () => {
  const f = await commitmentFixture()
  try {
    const plan = f.add('proposal-a', 'a', { kind: 'propose', terms: f.terms('18:00') })
    await f.snap('before-confirm')
    f.add('confirm-b', 'b', { kind: 'confirm', target: plan.ref })
    await assert.rejects(f.restore('before-confirm', 'denied'), /recovery_commitment_head_mismatch/)
  } finally {
    await f.close()
  }
})
