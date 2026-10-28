import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm, writeFile, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { MemeService, type MemeContext, type MemeTags } from '../src/memes.ts'
import type { Router } from '../src/models.ts'
const tags: MemeTags = {
  appearance: ['cartoon'],
  style: [],
  subject: [],
  emotion: ['happy'],
  intent: ['greeting'],
  tone: ['playful'],
  intensity: ['low'],
  memeMeaning: ['hello'],
  ocr: '',
}
const selectionContext = {
  persona: 'fixture',
  preferences: {},
  sourceContext: {},
  sourceContextKnown: true,
}
function model(fn: () => unknown | Promise<unknown>): Pick<Router, 'complete'> {
  return {
    complete: async () => ({
      text: JSON.stringify(await fn()),
      toolCalls: [],
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      modelId: 'fixture',
      assistant: { role: 'assistant', content: '' },
    }),
  }
}
const answer = () => ({
  ...tags,
  selection: {
    usable: true,
    shareable: false,
    confidence: 1,
    containsPersonalData: false,
    containsRealPerson: false,
    sourceSpecific: false,
  },
})
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'known-memes-'))
  const store = new Store(':memory:')
  const memes = new MemeService(store, root)
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      name: id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: '1',
      platformSceneId: id,
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  const bytes = await sharp({ create: { width: 8, height: 8, channels: 3, background: '#f80' } })
    .png()
    .toBuffer()
  const ctx = (sceneId = 'a', messageId = 'm1'): MemeContext => ({
    sceneId,
    simulation: false,
    sourceEvidence: [{ sceneId, messageId, senderId: 'user', available: true, isBot: false }],
  })
  const source = (context: MemeContext) => ({
    sceneId: context.sceneId,
    messageId: context.sourceEvidence![0]!.messageId,
    senderId: 'user',
  })
  const learning = (id: string, enabled: boolean) =>
    store.saveScene({ ...store.scene(id)!, learningEnabled: enabled })
  return {
    root,
    store,
    memes,
    bytes,
    ctx,
    source,
    learning,
    close: async () => {
      await memes.dispose()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('learning off admits visible known emotion once, never sendable; active library is not downgraded', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    let calls = 0
    const router = model(() => {
      calls++
      return answer()
    })
    const first = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), router, {
      selectionContext,
    }))!
    assert.equal(first.knownOnly, true)
    assert.equal(first.available, false)
    assert.equal(first.analyzed, true)
    assert.deepEqual(first.tags.emotion, ['happy'])
    assert.equal(f.memes.adminPage({ state: 'known' }).total, 1)
    const again = f.ctx('a', 'm2')
    const second = (await f.memes.observeBytes(
      again,
      Buffer.from(f.bytes),
      f.source(again),
      router,
    ))!
    assert.equal(second.id, first.id)
    assert.equal(calls, 1)
    assert.equal(f.memes.adminGet(first.id)!.sources.length, 2)
    assert.equal((await f.memes.recognizeBytes(ctx, f.bytes))!.id, first.id)
    assert.deepEqual(f.memes.search(ctx, ''), [])
    assert.throws(() => f.memes.original(ctx, first.id), /meme_not_available/)
    assert.throws(() => f.memes.update(first.id, { available: true }), /known_meme_requires_review/)
    assert.throws(
      () => f.memes.update(first.id, { shared: true, privacyReviewed: true }),
      /known_meme_requires_review/,
    )
    assert.throws(() => f.memes.promoteKnown(ctx, first.id), /learning_disabled/)
    f.learning('a', true)
    await f.memes.observeBytes(again, f.bytes, f.source(again), router, { selectionContext })
    assert.equal(
      f.memes.adminGet(first.id)!.available,
      false,
      'enabling learning does not silently activate analyzed known item',
    )
    await f.memes.analyze(ctx, first.id, router, { force: true, selectionContext })
    assert.equal(
      f.memes.adminGet(first.id)!.available,
      false,
      'reanalyzing known meaning is not approval to send',
    )
    assert.equal(f.memes.adminGet(first.id)!.knownOnly, true)
    assert.equal(f.memes.adminGet(first.id)!.shared, false)
    f.memes.promoteKnown(ctx, first.id)
    f.learning('a', false)
    await f.memes.observeBytes(again, f.bytes, f.source(again), router)
    assert.equal(f.memes.adminGet(first.id)!.available, true)
    assert.equal(f.memes.adminGet(first.id)!.knownOnly, false)
  } finally {
    await f.close()
  }
})

test('private identical content has isolated ids and emotion; reviewed shared content can be recognized', async () => {
  const f = await fixture()
  try {
    const a = f.ctx(),
      b = f.ctx('b')
    const first = (await f.memes.observeBytes(a, f.bytes, f.source(a), model(answer)))!
    assert.equal(await f.memes.recognizeBytes(b, f.bytes), undefined)
    const second = await f.memes.admitKnown(b, f.bytes, f.source(b))
    assert.notEqual(second.id, first.id)
    assert.equal(second.contentHash, first.contentHash)
    assert.equal(second.analyzed, false)
    assert.deepEqual(second.tags.emotion, [])
    assert.equal((await f.memes.recognizeBytes(a, f.bytes))!.id, first.id)
    f.learning('a', true)
    f.memes.promoteKnown(a, first.id)
    f.memes.update(first.id, { shared: true, privacyReviewed: true })
    const c = { ...b, sceneId: 'c', sourceEvidence: [] }
    assert.equal((await f.memes.recognizeBytes(c, f.bytes))!.id, first.id)
    f.store.recall('a', 'm1')
    assert.equal(
      await f.memes.recognizeBytes(a, f.bytes),
      undefined,
      'another private scene source cannot revive recalled local semantics',
    )
  } finally {
    await f.close()
  }
})

test('no route records known only; unknown photos admit=false do not persist; on-path analysis retains existing review policy', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    assert.equal(
      await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), undefined, { admit: false }),
      undefined,
    )
    assert.equal(f.memes.adminList().length, 0)
    const known = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx)))!
    assert.equal(known.analyzed, false)
    assert.equal(known.knownOnly, true)
    f.learning('a', true)
    const analyzed = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(answer), {
      selectionContext,
    }))!
    assert.equal(analyzed.available, true)
    assert.equal(analyzed.knownOnly, false)
  } finally {
    await f.close()
  }
})

test('same-source concurrent observers share analysis; one cancelled waiter does not cancel another', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    let calls = 0,
      release!: () => void
    const router = model(async () => {
      calls++
      await new Promise<void>((resolve) => {
        release = resolve
      })
      return answer()
    })
    const controller = new AbortController()
    const first = f.memes.observeBytes(ctx, f.bytes, f.source(ctx), router, {
      signal: controller.signal,
    })
    const failure = assert.rejects(first, /analysis_cancelled/)
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    const other = f.ctx('a', 'm2')
    const second = f.memes.observeBytes(other, f.bytes, f.source(other), router)
    await new Promise((resolve) => setTimeout(resolve, 20))
    controller.abort()
    await failure
    release()
    assert.equal((await second)!.analyzed, true)
    assert.equal(calls, 1)
  } finally {
    await f.close()
  }
})

test('failed same source does not immediately rebill; new source can retry; learning toggle and recall prevent promotion', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    let calls = 0
    const bad = model(() => {
      calls++
      throw new Error('fixture_failure')
    })
    await assert.rejects(f.memes.observeBytes(ctx, f.bytes, f.source(ctx), bad))
    await assert.rejects(
      f.memes.observeBytes(ctx, f.bytes, f.source(ctx), bad),
      /analysis_failed_recently/,
    )
    assert.equal(calls, 1)
    f.learning('a', true)
    const next = f.ctx('a', 'new')
    const result = await f.memes.observeBytes(
      next,
      f.bytes,
      f.source(next),
      model(() => {
        calls++
        f.learning('a', false)
        return answer()
      }),
      { selectionContext },
    )
    assert.equal(result!.available, false)
    assert.equal(result!.knownOnly, true)
    assert.equal(calls, 2)
    const before = f.memes.adminList().length
    const recalled = f.ctx('b', 'gone')
    f.store.recall('b', 'gone')
    await assert.rejects(
      f.memes.admitKnown(recalled, f.bytes, f.source(recalled)),
      /invalid_source_evidence/,
    )
    assert.equal(f.memes.adminList().length, before)
  } finally {
    await f.close()
  }
})

test('dirty known availability cannot bypass send/search/shared lookup; on-disk bytes must match actual hash', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    const asset = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(answer)))!
    const row = f.memes.adminGet(asset.id)!
    f.store.db
      .prepare('UPDATE meme_assets SET value=? WHERE id=?')
      .run(
        JSON.stringify({ ...row, available: true, shared: true, privacyReviewed: true }),
        asset.id,
      )
    assert.deepEqual(f.memes.search(ctx, ''), [])
    assert.throws(() => f.memes.original(ctx, asset.id), /meme_not_available/)
    assert.equal(await f.memes.recognizeBytes(f.ctx('b'), f.bytes), undefined)
    await writeFile(join(f.root, `${asset.id}.${asset.format}`), Buffer.from('tampered'))
    await assert.rejects(f.memes.admitKnown(ctx, f.bytes, f.source(ctx)), /media_hash_mismatch/)
  } finally {
    await f.close()
  }
})

test('force reanalysis of automatically rejected known meme cannot become send or sharing approval', async () => {
  const f = await fixture()
  try {
    f.learning('a', true)
    const ctx = f.ctx()
    const rejected = () => ({ ...answer(), selection: { ...answer().selection, usable: false } })
    const asset = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(rejected), {
      selectionContext,
    }))!
    assert.equal(asset.knownOnly, true)
    assert.equal(asset.analyzed, true)
    assert.equal(asset.selectionOrigin, 'automatic')
    const enthusiastic = () => ({
      ...answer(),
      selection: { ...answer().selection, shareable: true },
    })
    await f.memes.analyze(ctx, asset.id, model(enthusiastic), { force: true, selectionContext })
    const updated = f.memes.adminGet(asset.id)!
    assert.equal(updated.available, false)
    assert.equal(updated.knownOnly, true)
    assert.equal(updated.shared, false)
    assert.equal(updated.privacyReviewed, false)
    assert.throws(() => f.memes.original(ctx, asset.id), /meme_not_available/)
    f.memes.promoteKnown(ctx, asset.id)
    assert.equal(f.memes.adminGet(asset.id)!.available, true)
  } finally {
    await f.close()
  }
})

test('explicit admin inclusion/demotion works with learning off without changing tags or implicitly sharing', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    const asset = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(answer)))!
    assert.equal(f.store.scene('a')!.learningEnabled, false)
    const included = f.memes.adminSetIncluded(asset.id, true)
    assert.equal(included.available, true)
    assert.equal(included.knownOnly, false)
    assert.equal(included.shared, false)
    assert.deepEqual(included.tags, asset.tags)
    assert.equal(f.memes.search(ctx, '').length, 1)
    f.memes.original(ctx, asset.id)
    const known = f.memes.adminSetIncluded(asset.id, false)
    assert.equal(known.knownOnly, true)
    assert.equal(known.available, false)
    assert.deepEqual(known.tags, asset.tags)
    assert.equal(f.memes.search(ctx, '').length, 0)
    assert.throws(() => f.memes.original(ctx, asset.id), /meme_not_available/)
    assert.throws(() => f.memes.update(asset.id, { available: true }), /known_meme_requires_review/)
    f.memes.adminSetIncluded(asset.id, true)
    f.store.recall('a', 'm1')
    assert.throws(() => f.memes.adminSetIncluded(asset.id, true), /meme_not_available/)
  } finally {
    await f.close()
  }
})

test('admin demotion cancels in-flight analysis and cannot be resurrected by its late result', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    const asset = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(answer)))!
    f.memes.adminSetIncluded(asset.id, true)
    f.learning('a', true)
    let release!: () => void
    const pending = f.memes.analyze(
      ctx,
      asset.id,
      model(async () => {
        await new Promise<void>((resolve) => {
          release = resolve
        })
        return answer()
      }),
      { force: true, selectionContext },
    )
    const rejected = assert.rejects(pending, /analysis_cancelled/)
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    const known = f.memes.adminSetIncluded(asset.id, false)
    release()
    await rejected
    const final = f.memes.adminGet(asset.id)!
    assert.equal(final.knownOnly, true)
    assert.equal(final.available, false)
    assert.equal(final.shared, false)
    assert.deepEqual(final.tags, known.tags)
    assert.throws(() => f.memes.original(ctx, asset.id), /meme_not_available/)
  } finally {
    await f.close()
  }
})

test('admin deletion removes known or included scoped bytes without touching equal-content private records', async () => {
  const f = await fixture()
  try {
    const a = f.ctx(),
      b = f.ctx('b')
    const first = (await f.memes.observeBytes(a, f.bytes, f.source(a), model(answer)))!
    const second = (await f.memes.observeBytes(b, f.bytes, f.source(b), model(answer)))!
    assert.notEqual(first.id, second.id)
    f.memes.adminSetIncluded(first.id, true)
    await f.memes.thumbnail(first.id)
    const deleting = f.memes.adminDelete(first.id)
    const delayed = f.ctx('a', 'delayed-background')
    const rejectedDuringDelete = assert.rejects(
      f.memes.admitKnown(delayed, f.bytes, f.source(delayed)),
      /media_deleted/,
    )
    assert.throws(
      () => f.memes.original(a, first.id),
      /meme_not_available/,
      'pending deletion immediately revokes current image reads',
    )
    assert.equal(await deleting, true)
    await rejectedDuringDelete
    assert.equal(f.memes.adminGet(first.id), undefined)
    await assert.rejects(readFile(join(f.root, `${first.id}.${first.format}`)), { code: 'ENOENT' })
    await assert.rejects(f.memes.thumbnail(first.id), /meme_not_found/)
    assert.deepEqual(await readFile(join(f.root, `${second.id}.${second.format}`)), f.bytes)
    assert.equal((await f.memes.recognizeBytes(b, f.bytes))!.id, second.id)
    assert.equal(await f.memes.adminDelete(first.id), false)
    assert.equal(second.knownOnly, true)
    assert.equal(await f.memes.adminDelete(second.id), true)
    await assert.rejects(readFile(join(f.root, `${second.id}.${second.format}`)), {
      code: 'ENOENT',
    })
  } finally {
    await f.close()
  }
})

test('admin delete cancels pending semantic analysis and pre-delete admission cannot restore the row or file', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    const asset = await f.memes.admitKnown(ctx, f.bytes, f.source(ctx))
    let release!: () => void
    const pending = f.memes.observeBytes(
      ctx,
      f.bytes,
      f.source(ctx),
      model(async () => {
        await new Promise<void>((resolve) => {
          release = resolve
        })
        return answer()
      }),
    )
    const rejected = assert.rejects(pending, /analysis_cancelled/)
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    const later = f.ctx('a', 'pending-admission')
    const admission = f.memes.admitKnown(later, f.bytes, f.source(later))
    const admissionRejected = assert.rejects(admission, /media_deleted/)
    await f.memes.adminDelete(asset.id)
    release()
    await rejected
    await admissionRejected
    assert.equal(f.memes.adminGet(asset.id), undefined)
    await assert.rejects(readFile(join(f.root, `${asset.id}.${asset.format}`)), { code: 'ENOENT' })
  } finally {
    await f.close()
  }
})

test('legacy raw-hash deletion cannot be escaped by allocating a fresh scoped surrogate', async () => {
  const f = await fixture()
  try {
    const ctx = f.ctx()
    const original = (await f.memes.observeBytes(ctx, f.bytes, f.source(ctx), model(answer)))!
    const row = f.memes.adminGet(original.id)!
    const legacyId = createHash('sha256').update(f.bytes).digest('hex')
    // Emulate a pre-migration database/file without changing the bytes or source authority.
    f.store.db.prepare('DELETE FROM meme_assets WHERE id=?').run(original.id)
    const legacy = { ...row, id: legacyId, contentHash: undefined }
    f.store.db.prepare('INSERT INTO meme_assets VALUES(?,?)').run(legacyId, JSON.stringify(legacy))
    for (const source of legacy.sources)
      f.store.db
        .prepare('INSERT INTO meme_sources VALUES(?,?,?,?)')
        .run(legacyId, source.sceneId, source.messageId, source.senderId)
    await writeFile(join(f.root, `${legacyId}.${legacy.format}`), f.bytes)
    let release!: () => void
    const lock = (
      f.memes as unknown as {
        withAssetWrite(id: string, action: () => Promise<void>): Promise<void>
      }
    ).withAssetWrite(
      legacyId,
      () =>
        new Promise<void>((resolve) => {
          release = resolve
        }),
    )
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    const deleting = f.memes.adminDelete(legacyId)
    await assert.rejects(f.memes.adminDelete(legacyId), /media_delete_in_progress/)
    await assert.rejects(f.memes.admitKnown(ctx, f.bytes, f.source(ctx)), /media_deleted/)
    release()
    await lock
    await deleting
    assert.equal(f.memes.adminList().length, 0)
    await assert.rejects(readFile(join(f.root, `${legacyId}.${legacy.format}`)), { code: 'ENOENT' })
  } finally {
    await f.close()
  }
})
