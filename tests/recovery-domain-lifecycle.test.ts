import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { HabitService } from '../src/habits.ts'
import { MemeService, type MemeContext } from '../src/memes.ts'
import { ExperienceService } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { parseDomainRecoveryEvent } from '../src/memory/recovery/domain-contracts.ts'
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'domain-lifecycle-'))
  const store = new Store(join(root, 'data', 'live.sqlite')),
    media = join(root, 'media')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      name: id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: '1',
      platformSceneId: id,
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  const memes = new MemeService(store, media),
    habits = new HabitService(store)
  const experience = new ExperienceService(store.db, {
    settings: () => ({ enabled: true, autoActivate: true, hints: true }),
    learningEnabled: () => true,
    sceneAvailable: (scene) => !!store.scene(scene),
    admissionSourceAvailable: () => true,
    targetAvailable: () => true,
    sourceRevoked: (scene, id) => store.recalled(scene, id),
    validate: validateExperiencePolicy,
  })
  const bytes = await sharp({ create: { width: 4, height: 4, channels: 3, background: '#112233' } })
    .png()
    .toBuffer()
  const ctx = (sceneId: string, messageId: string): MemeContext => ({
    sceneId,
    simulation: false,
    sourceEvidence: [{ sceneId, messageId, senderId: 'user', available: true, isBot: false }],
  })
  const add = async (scene: string, message: string) => {
    const c = ctx(scene, message)
    return memes.admitKnown(c, bytes, { sceneId: scene, messageId: message, senderId: 'user' })
  }
  return {
    root,
    store,
    media,
    memes,
    habits,
    experience,
    bytes,
    ctx,
    add,
    close: async () => {
      await memes.dispose()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}
test('meme async delete keeps all domain reads blocked until bytes and scoped rows commit, not equal-content neighbors', async () => {
  const f = await fixture()
  try {
    const a = await f.add('a', 'a-source'),
      b = await f.add('b', 'b-source')
    f.memes.bindMediaReference(
      f.ctx('a', 'a-source'),
      'synthetic-reference',
      a.id,
      { sceneId: 'a', messageId: 'a-source', senderId: 'user' },
      f.bytes,
    )
    const deleting = f.memes.adminDelete(a.id)
    assert.throws(() => f.memes.adminPage(), /recovery_authority_head_changed/)
    assert.throws(() => f.habits.adminList(), /recovery_authority_head_changed/)
    assert.throws(
      () => f.experience.list({ sceneId: 'a', simulation: false }),
      /recovery_authority_head_changed/,
    )
    assert.equal(await deleting, true)
    assert.equal(f.memes.adminGet(a.id), undefined)
    assert.equal(
      f.memes.hasMediaReference(f.ctx('a', 'a-source'), 'synthetic-reference', 'a-source'),
      true,
    )
    assert.equal(
      f.memes.lookupMediaReference(f.ctx('a', 'a-source'), 'synthetic-reference', 'a-source'),
      undefined,
    )
    assert.equal(
      f.memes.hasMediaReference(f.ctx('a', 'new-source'), 'synthetic-reference', 'new-source'),
      false,
    )
    const denials = f.store.db.prepare('SELECT * FROM meme_reference_denials').all() as {
      key: string
    }[]
    assert.equal(denials.length, 1)
    assert.match(denials[0]!.key, /^[a-f0-9]{64}$/)
    assert.ok(!JSON.stringify(denials).includes('a-source'))
    assert.equal(
      f.store.db
        .prepare('SELECT count(*) AS n FROM meme_media_references WHERE asset_id=?')
        .get(a.id) &&
        (
          f.store.db
            .prepare('SELECT count(*) AS n FROM meme_media_references WHERE asset_id=?')
            .get(a.id) as { n: number }
        ).n,
      0,
    )
    assert.deepEqual(await readFile(join(f.media, `${b.id}.${b.format}`)), f.bytes)
    assert.equal(f.memes.adminGet(b.id)?.sources.length, 1)
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    const event = authority.entries().at(-1)!.revocation
    assert.equal(event.kind, 'meme_delete')
    assert.ok('qualificationHash' in event)
    authority.close()
    const restarted = new MemeService(f.store, f.media)
    assert.equal(
      restarted.hasMediaReference(f.ctx('a', 'a-source'), 'synthetic-reference', 'a-source'),
      true,
    )
    assert.equal(
      restarted.lookupMediaReference(f.ctx('a', 'a-source'), 'synthetic-reference', 'a-source'),
      undefined,
    )
    const replacement = await f.add('a', 'new-source')
    assert.equal(replacement.id, a.id)
    restarted.bindMediaReference(
      f.ctx('a', 'new-source'),
      'synthetic-reference',
      replacement.id,
      { sceneId: 'a', messageId: 'new-source', senderId: 'user' },
      f.bytes,
    )
    assert.deepEqual(
      await restarted.readReference(f.ctx('a', 'new-source'), 'synthetic-reference', 'new-source'),
      f.bytes,
    )
    assert.equal(
      restarted.lookupMediaReference(f.ctx('a', 'a-source'), 'synthetic-reference', 'a-source'),
      undefined,
    )
    await restarted.dispose()
  } finally {
    await f.close()
  }
})
test('admin final states journal deny and later regrant distinctly; source recall clears exact aliases only', async () => {
  const f = await fixture()
  try {
    const a = await f.add('a', 'first')
    await f.add('a', 'independent')
    f.memes.bindMediaReference(
      f.ctx('a', 'first'),
      'ref-one',
      a.id,
      { sceneId: 'a', messageId: 'first', senderId: 'user' },
      f.bytes,
    )
    f.memes.bindMediaReference(
      f.ctx('a', 'independent'),
      'ref-two',
      a.id,
      { sceneId: 'a', messageId: 'independent', senderId: 'user' },
      f.bytes,
    )
    f.memes.update(a.id, { disabled: true })
    f.memes.update(a.id, { disabled: false })
    f.memes.update(a.id, { disabled: true })
    const authority = new RecoveryAuthority(metadata(f.store.db)!.authority)
    const states = authority
      .entries()
      .filter((row) => row.revocation.kind === 'meme_state')
      .map((row) => row.revocation)
    assert.equal(states.length, 3)
    assert.deepEqual(
      states.map((row) => row.kind === 'meme_state' && row.state.disabled),
      [true, false, true],
    )
    authority.close()
    f.memes.recall('a', 'first')
    assert.deepEqual(
      f.memes.adminGet(a.id)?.sources.map((source) => source.messageId),
      ['independent'],
    )
    const refs = f.store.db
      .prepare('SELECT message_id FROM meme_media_references WHERE asset_id=?')
      .all(a.id) as { message_id: string }[]
    assert.deepEqual(refs, [{ message_id: 'independent' }])
  } finally {
    await f.close()
  }
})
test('domain event schema rejects unknown versions/keys and free-form reason payload', () => {
  assert.throws(() =>
    parseDomainRecoveryEvent({
      schemaVersion: 2,
      kind: 'habit_delete',
      id: 'a',
      fingerprint: 'a'.repeat(64),
    }),
  )
  assert.throws(() =>
    parseDomainRecoveryEvent({
      schemaVersion: 1,
      kind: 'meme_delete',
      id: '../file',
      contentHash: 'a'.repeat(64),
      qualificationHash: 'b'.repeat(64),
      format: 'png',
    }),
  )
  assert.throws(() =>
    parseDomainRecoveryEvent({
      schemaVersion: 1,
      kind: 'experience_state',
      sceneId: 's',
      id: 'p',
      qualificationHash: 'a'.repeat(64),
      version: 2,
      status: 'suspended',
      reason: 'private user body',
    }),
  )
})
