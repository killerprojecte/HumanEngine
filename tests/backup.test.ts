import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { MemeService } from '../src/memes.ts'
import { backup, digest } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

test('Online backup restores config, memory, recall tombstones, and hashed media only into a fresh destination', async () => {
  const root = await mkdtemp(join(tmpdir(), 'humanengine-backup-'))
  const database = join(root, 'source.sqlite'),
    mediaRoot = join(root, 'source-media'),
    destination = join(root, 'backup')
  const store = new Store(database)
  let restored: Store | undefined
  try {
    store.set('example', { enabled: true, credentialEnv: 'EXAMPLE_API_KEY' })
    store.saveScene({
      id: 'scene',
      kind: 'group',
      adapterId: 'snow',
      accountId: '42',
      platformSceneId: '1',
      name: 'group',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const record = memory.create({
      scope: { kind: 'scene', sceneId: 'scene' },
      content: 'A durable fact',
      kind: 'fact',
    })
    store.recall('scene', 'old-message')
    const memes = new MemeService(store, mediaRoot)
    const bytes = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#f00' } })
      .png()
      .toBuffer()
    const source = { sceneId: 'scene', messageId: 'media-message', senderId: 'person' }
    const imported = await memes.import(
      {
        sceneId: 'scene',
        simulation: false,
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      bytes,
      source,
    )
    assert.notEqual(imported.id, digest(bytes), 'new scoped id is not the raw byte hash')
    assert.equal(imported.contentHash, digest(bytes))
    const legacyId = digest(bytes)
    const legacy = {
      ...memes.adminGet(imported.id)!,
      id: legacyId,
      contentHash: undefined,
      sources: [],
    }
    store.db.prepare('INSERT INTO meme_assets VALUES(?,?)').run(legacyId, JSON.stringify(legacy))
    await writeFile(join(mediaRoot, `${legacyId}.png`), bytes)
    await writeFile(join(root, '.env'), 'EXAMPLE_API_KEY=not-for-backup')
    const manifest = await backup({ database, mediaRoot, destination })
    assert.equal(manifest.files.length, 3)
    assert.ok(!JSON.stringify(manifest).includes('.env'))
    assert.equal(
      digest(await readFile(join(destination, 'humanengine.sqlite'))),
      manifest.files[0]!.sha256,
    )
    // Source remains open and usable: the helper did not require a shutdown or raw WAL copying.
    store.set('after-backup', true)
    const fresh = join(root, 'restored')
    const result = await restore({ backupRoot: destination, destination: fresh })
    assert.ok(result.vectorVersion)
    restored = new Store(result.database)
    assert.deepEqual(restored.get('example', null), {
      enabled: true,
      credentialEnv: 'EXAMPLE_API_KEY',
    })
    assert.equal(restored.get('after-backup', false), false)
    assert.equal(new MemoryService(restored).adminList()[0]?.id, record.id)
    assert.equal(restored.recalled('scene', 'old-message'), true)
    const media = manifest.files.find((f) => f.path.startsWith('media/'))!
    assert.deepEqual(await readFile(join(fresh, media.path)), bytes)
    for (const entry of manifest.files.filter((file) => file.path.startsWith('media/'))) {
      assert.equal(entry.sha256, digest(bytes))
      assert.deepEqual(await readFile(join(fresh, entry.path)), bytes)
    }
    await assert.rejects(restore({ backupRoot: destination, destination: fresh }))
    assert.equal(restored.get('example', null) !== null, true)
    const active = join(root, 'existing')
    await mkdir(active)
    await writeFile(join(active, 'sentinel'), 'keep')
    await assert.rejects(backup({ database, mediaRoot, destination: active }))
    assert.equal(await readFile(join(active, 'sentinel'), 'utf8'), 'keep')
    await writeFile(join(destination, media.path), 'tampered')
    await assert.rejects(
      restore({ backupRoot: destination, destination: join(root, 'corrupt-restore') }),
      /backup_hash_mismatch/,
    )
    await assert.rejects(readFile(join(root, 'corrupt-restore', 'humanengine.sqlite')))
    // Re-signing a tampered file in the manifest must not bypass the snapshot's content hash.
    media.sha256 = digest(Buffer.from('tampered'))
    media.bytes = Buffer.byteLength('tampered')
    await writeFile(join(destination, 'manifest.json'), JSON.stringify(manifest))
    await assert.rejects(
      restore({ backupRoot: destination, destination: join(root, 'wrong-content-restore') }),
      /media_content_hash_mismatch/,
    )
    await assert.rejects(readFile(join(root, 'wrong-content-restore', 'humanengine.sqlite')))
  } finally {
    restored?.close()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
