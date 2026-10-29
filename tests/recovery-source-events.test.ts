import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'
import { Store } from '../src/store.ts'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { recordSourceEventMutation } from '../src/memory/recovery/source-events.ts'
import {
  parseConfigurationRecoveryEvent,
  configurationFlags,
} from '../src/memory/recovery/configuration-contract.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
const hash = (text: string) => createHash('sha256').update(text).digest('hex')
const identity = {
  sceneId: 'scene',
  adapterId: 'synthetic',
  accountId: 'account',
  messageId: 'message',
  senderId: 'person',
}
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-event-recovery-'))
  const store = new Store(join(root, 'live', 'humanengine.sqlite'))
  const authorityDirectory = metadata(store.db)!.authority
  const events = new SourceEventStore(store.db, {
    assertReady: () => store.assertRecoveryReady(),
    beforeWrite: (change) => recordSourceEventMutation(store.db, change),
  })
  await mkdir(join(root, 'media'))
  return {
    root,
    store,
    events,
    authorityDirectory,
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
const initial = () => ({
  kind: 'ingress' as const,
  identity,
  bodyHash: hash('SYNTHETIC ORIGINAL'),
  occurredAt: null,
})
const edit = () => ({
  kind: 'edit' as const,
  identity,
  bodyHash: hash('SYNTHETIC CORRECTED'),
  occurredAt: null,
  editId: 'edit-1',
  order: { kind: 'platform_sequence' as const, sequence: 1 },
})

test('source edit authority rejects old interpretation; current qualified snapshot restores without replaying body', async () => {
  const f = await fixture()
  try {
    assert.equal(metadata(f.store.db)!.minimumReader, 1)
    assert.equal(f.events.observe(initial()).status, 'new')
    assert.equal(metadata(f.store.db)!.minimumReader, 2)
    await f.snap('old')
    const changed = f.events.observe(edit())
    assert.equal(changed.status, 'edited')
    await assert.rejects(f.restore('old', 'denied'), /recovery_source_event_head_mismatch/)
    await assert.rejects(access(join(f.root, 'denied')))
    await f.snap('current')
    const result = await f.restore('current', 'restored')
    const restored = new Store(result.database)
    try {
      const events = new SourceEventStore(restored.db, {
        assertReady: () => restored.assertRecoveryReady(),
      })
      assert.equal(events.current(changed.ref!, identity).eligible, true)
      const authority = new RecoveryAuthority(f.authorityDirectory)
      try {
        const payload = JSON.stringify(authority.entries())
        assert.ok(payload.includes('source_edit_head'))
        assert.ok(!payload.includes('SYNTHETIC CORRECTED'))
        assert.ok(!payload.includes('SYNTHETIC ORIGINAL'))
      } finally {
        authority.close()
      }
    } finally {
      restored.close()
    }
  } finally {
    await f.close()
  }
})

test('later monotonic source recall erases every revision and discharges edit witness, retaining another source', async () => {
  const f = await fixture()
  try {
    f.events.observe(initial())
    const other = { ...identity, messageId: 'independent' }
    const kept = f.events.observe({ ...initial(), identity: other })
    await f.snap('old')
    f.events.observe(edit())
    f.store.db
      .transaction(() => {
        f.store.recall(identity.sceneId, identity.messageId)
        f.events.revokeAll(identity.sceneId, identity.messageId)
      })
      .immediate()
    const result = await f.restore('old', 'restored')
    const restored = new Store(result.database)
    try {
      assert.equal(restored.recalled(identity.sceneId, identity.messageId), true)
      assert.equal(
        restored.db
          .prepare('SELECT 1 FROM source_event_heads WHERE message_id=?')
          .get(identity.messageId),
        undefined,
      )
      assert.equal(
        (
          restored.db.prepare('SELECT count(*) AS n FROM source_event_revisions').get() as {
            n: number
          }
        ).n,
        1,
      )
      assert.equal(new SourceEventStore(restored.db).current(kept.ref!, other).eligible, true)
    } finally {
      restored.close()
    }
  } finally {
    await f.close()
  }
})

test('edit intent surviving local rollback blocks live access and cannot be applied as invented new source evidence', async () => {
  const f = await fixture()
  try {
    f.events.observe(initial())
    await f.snap('old')
    assert.throws(
      () =>
        f.store.db
          .transaction(() => {
            f.events.observe(edit())
            throw new Error('synthetic rollback')
          })
          .immediate(),
      /synthetic rollback/,
    )
    assert.throws(() => f.store.get('settings', {}), /head_changed/)
    await assert.rejects(f.restore('old', 'denied'), /recovery_source_event_head_mismatch/)
    await assert.rejects(access(join(f.root, 'denied')))
  } finally {
    await f.close()
  }
})

test('unknown source-event table format blocks recovery rather than ignoring a future revision domain', async () => {
  const f = await fixture()
  try {
    f.events.observe(initial())
    f.store.db.exec('CREATE TABLE source_event_future_v2(id TEXT)')
    await f.snap('future')
    await assert.rejects(f.restore('future', 'denied'), /recovery_unknown_source_event_schema/)
  } finally {
    await f.close()
  }
})

test('legacy configuration authority v1 remains strict and readable; v2 carries source enrollment flag', () => {
  const flags = configurationFlags({ sourceEventRevisionsEnabled: true })
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
    sourceEventRevisionsEnabled,
    memoryCommitmentsEnabled,
    memoryCommitmentsAnswerEnabled,
    memoryHistoricalReadEnabled,
    ...legacy
  } = flags
  assert.equal(sourceEventRevisionsEnabled, true)
  assert.deepEqual(
    parseConfigurationRecoveryEvent({
      kind: 'configuration_flags',
      schemaVersion: 1,
      flags: legacy,
    }).flags,
    legacy,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 1, flags }),
    /invalid/,
  )
  assert.deepEqual(
    parseConfigurationRecoveryEvent({
      kind: 'configuration_flags',
      schemaVersion: 2,
      flags: { ...legacy, sourceEventRevisionsEnabled },
    }).flags,
    { ...legacy, sourceEventRevisionsEnabled },
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 9, flags }),
    /invalid/,
  )
})
