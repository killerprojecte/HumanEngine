import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { MemeService, type MemeContext } from '../src/memes.ts'

async function setup() {
  const root = await mkdtemp(join(tmpdir(), 'meme-reference-'))
  const store = new Store(join(root, 'state.sqlite'))
  const media = join(root, 'media')
  const service = new MemeService(store, media)
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      name: id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'account',
      platformSceneId: id,
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  const ctx = (sceneId = 'a', messageId = 'm1'): MemeContext => ({
    sceneId,
    simulation: false,
    sourceEvidence: [{ sceneId, messageId, senderId: 'user', available: true, isBot: false }],
  })
  const source = { sceneId: 'a', messageId: 'm1', senderId: 'user' }
  const bytes = await sharp({ create: { width: 8, height: 8, channels: 3, background: '#f80' } })
    .png()
    .toBuffer()
  return {
    root,
    store,
    media,
    service,
    ctx,
    source,
    bytes,
    close: async () => {
      await service.dispose()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}
const reference = 'synthetic-opaque-reference-with-private-locator'

test('verified exact media references persist across service restart and read known bytes without send permission', async () => {
  const f = await setup()
  try {
    const asset = await f.service.admitKnown(f.ctx(), f.bytes, f.source)
    f.service.bindMediaReference(f.ctx(), reference, asset.id, f.source, f.bytes)
    await f.service.dispose()
    f.store.close()
    const restoredStore = new Store(join(f.root, 'state.sqlite'))
    const restarted = new MemeService(restoredStore, f.media)
    assert.equal(restarted.lookupMediaReference(f.ctx(), reference, 'm1')?.id, asset.id)
    assert.equal(restarted.lookupMediaReference(f.ctx(), reference, 'm1')?.knownOnly, true)
    assert.equal(restarted.hasMediaReference(f.ctx(), reference, 'm1'), true)
    assert.equal(restarted.hasMediaReference(f.ctx(), reference, 'new-message'), false)
    assert.deepEqual(await restarted.readReference(f.ctx(), reference, 'm1'), f.bytes)
    assert.throws(() => restarted.original(f.ctx(), asset.id), /meme_not_available/)
    assert.equal(restarted.lookupMediaReference(f.ctx('b'), reference, 'm1'), undefined)
    assert.equal(
      restarted.lookupMediaReference({ ...f.ctx(), simulation: true }, reference, 'm1'),
      undefined,
    )
    assert.equal(restarted.lookupMediaReference(f.ctx(), reference + 'changed', 'm1'), undefined)
    const rows = restoredStore.db.prepare('SELECT * FROM meme_media_references').all()
    assert.equal(JSON.stringify(rows).includes(reference), false)
    restoredStore.saveScene({ ...restoredStore.scene('a')!, accountId: 'different' })
    assert.equal(restarted.hasMediaReference(f.ctx(), reference, 'm1'), false)
    await restarted.dispose()
    restoredStore.close()
  } finally {
    await f.close()
  }
})

test('reference bindings reject false bytes/source and invalidate on recall, disable, delete and disk corruption', async () => {
  const f = await setup()
  try {
    const asset = await f.service.admitKnown(f.ctx(), f.bytes, f.source)
    assert.throws(
      () =>
        f.service.bindMediaReference(f.ctx(), reference, asset.id, f.source, Buffer.from('wrong')),
      /media_content_hash_mismatch/,
    )
    assert.throws(
      () =>
        f.service.bindMediaReference(
          f.ctx('a', 'other'),
          reference,
          asset.id,
          { ...f.source, messageId: 'other' },
          f.bytes,
        ),
      /invalid_source_evidence/,
    )
    f.service.bindMediaReference(f.ctx(), reference, asset.id, f.source, f.bytes)
    f.service.update(asset.id, { disabled: true })
    assert.equal(f.service.hasMediaReference(f.ctx(), reference, 'm1'), true)
    assert.equal(f.service.hasMediaReference(f.ctx(), reference, 'new-message'), false)
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm1'), undefined)
    await assert.rejects(
      f.service.readReference(f.ctx(), reference, 'm1'),
      /image_source_unavailable/,
    )
    f.service.update(asset.id, { disabled: false })
    await writeFile(f.service.recognitionOriginal(f.ctx(), asset.id).path, Buffer.from('bad'))
    await assert.rejects(
      f.service.readReference(f.ctx(), reference, 'm1'),
      /media_content_hash_mismatch/,
    )
    await writeFile(f.service.recognitionOriginal(f.ctx(), asset.id).path, f.bytes)
    f.store.recall('a', 'm1')
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm1'), undefined)
    await f.service.adminDelete(asset.id)
    assert.equal(f.service.hasMediaReference(f.ctx(), reference, 'm1'), true)
    assert.equal(f.service.hasMediaReference(f.ctx(), reference, 'new-message'), false)
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm1'), undefined)
  } finally {
    await f.close()
  }
})

test('identical mutable locator used by different messages preserves independently verified content', async () => {
  const f = await setup()
  try {
    const first = await f.service.admitKnown(f.ctx(), f.bytes, f.source)
    f.service.bindMediaReference(f.ctx(), reference, first.id, f.source, f.bytes)
    const otherBytes = await sharp({
      create: { width: 8, height: 8, channels: 3, background: '#08f' },
    })
      .png()
      .toBuffer()
    const next = { ...f.source, messageId: 'm2' }
    const second = await f.service.admitKnown(f.ctx('a', 'm2'), otherBytes, next)
    assert.equal(f.service.hasMediaReference(f.ctx(), reference, 'm2'), false)
    f.service.bindMediaReference(f.ctx('a', 'm2'), reference, second.id, next, otherBytes)
    assert.notEqual(first.id, second.id)
    assert.deepEqual(await f.service.readReference(f.ctx(), reference, 'm1'), f.bytes)
    assert.deepEqual(await f.service.readReference(f.ctx(), reference, 'm2'), otherBytes)
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference), undefined)
    f.store.recall('a', 'm1')
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm1'), undefined)
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm2')?.id, second.id)
  } finally {
    await f.close()
  }
})

test('exact image lookups bound source qualification to id/hash candidates in a large library', async () => {
  const f = await setup()
  try {
    const asset = await f.service.admitKnown(f.ctx(), f.bytes, f.source)
    f.service.bindMediaReference(f.ctx(), reference, asset.id, f.source, f.bytes)
    const insertAsset = f.store.db.prepare('INSERT INTO meme_assets(id,value) VALUES(?,?)')
    const insertSource = f.store.db.prepare(
      'INSERT INTO meme_sources(asset_id,scene_id,message_id,sender_id) VALUES(?,?,?,?)',
    )
    f.store.db.transaction(() => {
      for (let index = 0; index < 2000; index++) {
        const id = `unrelated-${index}`
        const source = { ...f.source, messageId: id }
        insertAsset.run(id, JSON.stringify({ ...asset, id, contentHash: id, sources: [source] }))
        insertSource.run(id, source.sceneId, source.messageId, source.senderId)
      }
    })()
    const recalled = f.store.recalled.bind(f.store)
    const checked: string[] = []
    f.store.recalled = (scene, message) => {
      checked.push(message)
      return recalled(scene, message)
    }
    assert.equal((await f.service.recognizeBytes(f.ctx(), f.bytes))?.id, asset.id)
    assert.equal((await f.service.admitKnown(f.ctx(), f.bytes, f.source)).id, asset.id)
    assert.equal(f.service.lookupMediaReference(f.ctx(), reference, 'm1')?.id, asset.id)
    assert.deepEqual(await f.service.readReference(f.ctx(), reference, 'm1'), f.bytes)
    assert.ok(checked.length > 0)
    assert.equal(
      checked.some((id) => id.startsWith('unrelated-')),
      false,
    )
    assert.equal(await f.service.recognizeBytes(f.ctx('b'), f.bytes), undefined)
    assert.equal(
      await f.service.recognizeBytes({ ...f.ctx(), simulation: true }, f.bytes),
      undefined,
    )
    const plans = [
      ['id=?', asset.id, 'sqlite_autoindex_meme_assets_1'],
      [
        "COALESCE(json_extract(value,'$.contentHash'),id)=?",
        asset.contentHash!,
        'meme_content_hash_lookup',
      ],
    ]
    for (const [predicate, key, index] of plans) {
      const plan = f.store.db
        .prepare(
          `EXPLAIN QUERY PLAN SELECT value FROM meme_assets WHERE ${predicate} AND ((json_extract(value,'$.shared')=1 AND json_extract(value,'$.privacyReviewed')=1 AND COALESCE(json_extract(value,'$.knownOnly'),0)=0) OR id IN (SELECT asset_id FROM meme_sources WHERE scene_id=?))`,
        )
        .all(key!, 'a')
      assert.match(JSON.stringify(plan), new RegExp(index!))
    }
  } finally {
    await f.close()
  }
})

test('hash candidates preserve local preference, shared qualification, legacy ids and recall', async () => {
  const f = await setup()
  try {
    const a = await f.service.admitKnown(f.ctx(), f.bytes, f.source)
    const bSource = { ...f.source, sceneId: 'b', messageId: 'm2' }
    const b = await f.service.admitKnown(f.ctx('b', 'm2'), f.bytes, bSource)
    assert.notEqual(a.id, b.id)
    assert.equal((await f.service.recognizeBytes(f.ctx(), f.bytes))?.id, a.id)
    assert.equal((await f.service.recognizeBytes(f.ctx('b'), f.bytes))?.id, b.id)
    // Synthetic legacy shared record uses its hash as id and has no contentHash field.
    const { contentHash, ...legacy } = a
    f.store.db.prepare('INSERT INTO meme_assets(id,value) VALUES(?,?)').run(
      contentHash!,
      JSON.stringify({
        ...legacy,
        sources: [f.source],
        id: contentHash,
        shared: true,
        privacyReviewed: true,
        knownOnly: false,
      }),
    )
    assert.equal(
      (await f.service.recognizeBytes({ ...f.ctx(), simulation: true }, f.bytes))?.id,
      contentHash,
    )
    f.store.recalled = (scene, message) => scene === 'a' && message === 'm1'
    assert.equal(await f.service.recognizeBytes(f.ctx(), f.bytes), undefined)
    assert.equal(
      await f.service.recognizeBytes({ ...f.ctx(), simulation: true }, f.bytes),
      undefined,
    )
    assert.equal((await f.service.recognizeBytes(f.ctx('b'), f.bytes))?.id, b.id)
  } finally {
    await f.close()
  }
})
