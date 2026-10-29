import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { HabitService, type HabitContext } from '../src/habits.ts'
import { MemeService, type MemeContext } from '../src/memes.ts'
import { ExperienceService } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import { domainSourceCurrent } from '../src/memory/read/domain-source-events.ts'
const source = (messageId = 'm1', senderId = 'person') => ({
  sceneId: 'scene',
  messageId,
  senderId,
})
function setup() {
  const store = new Store(':memory:')
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'adapter',
    accountId: 'account',
    platformSceneId: 'scene',
    name: 'Synthetic',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  return store
}
function observe(store: Store, messageId = 'm1', senderId = 'person') {
  return store.sourceEvents.observe({
    kind: 'ingress',
    identity: { ...source(messageId, senderId), adapterId: 'adapter', accountId: 'account' },
    bodyHash: 'a'.repeat(64),
    occurredAt: null,
  }).ref!
}
function edit(store: Store, messageId = 'm1', senderId = 'person') {
  const identity = { ...source(messageId, senderId), adapterId: 'adapter', accountId: 'account' }
  const ref = store.sourceEvents.headMetadata(identity)!.ref
  return store.sourceEvents.observe({
    kind: 'edit',
    identity,
    bodyHash: 'b'.repeat(64),
    occurredAt: null,
    editId: 'edit',
    order: { kind: 'observed_cas', expectedBase: ref },
  }).ref!
}
test('habit source editing changes current hints without deleting history; independent evidence remains', () => {
  const store = setup()
  const habits = new HabitService(store)
  const first = observe(store)
  const ctx = (messageId: string, senderId: string): HabitContext => ({
    sceneId: 'scene',
    simulation: false,
    sourceEvidence: [
      {
        ...source(messageId, senderId),
        available: true,
        isBot: false,
        origin: 'live',
        text: 'Bot, from now on prefer concise replies and fewer emoji.',
        timestamp: Date.now(),
      },
    ],
  })
  const one = habits.propose(ctx('m1', 'person'), {
    kind: 'botExpression',
    preference: 'emoji',
    direction: -1,
    sources: [source()],
  })
  assert.equal(one.status, 'saved')
  assert.equal(habits.list(ctx('m1', 'person')).length, 1)
  const raw = JSON.stringify(store.db.prepare('SELECT value FROM habits').all())
  edit(store)
  assert.equal(habits.list(ctx('m1', 'person')).length, 0)
  assert.equal(JSON.stringify(store.db.prepare('SELECT value FROM habits').all()), raw)
  assert.equal(domainSourceCurrent(store, { ...source(), eventRef: first }), false)
  assert.equal(domainSourceCurrent(store, source()), false)
  assert.equal(
    habits.propose(ctx('m1', 'person'), {
      kind: 'botExpression',
      preference: 'emoji',
      direction: -1,
      sources: [source()],
    }).status,
    'rejected',
  )
  const other = habits.propose(ctx('m2', 'other'), {
    kind: 'botExpression',
    preference: 'emoji',
    direction: -1,
    sources: [source('m2', 'other')],
  })
  assert.equal(other.status, 'saved')
  assert.equal(habits.list(ctx('m2', 'other')).length, 1)
  store.close()
})
test('meme current read rejects edited source while independent same-content source remains', async () => {
  const store = setup()
  const root = await mkdtemp(join(tmpdir(), 'event-meme-'))
  const memes = new MemeService(store, root)
  const ctx = (messageId: string): MemeContext => ({
    sceneId: 'scene',
    simulation: false,
    sourceEvidence: [{ ...source(messageId), available: true, isBot: false }],
  })
  try {
    observe(store)
    const bytes = await sharp({ create: { width: 8, height: 8, channels: 3, background: '#f80' } })
      .png()
      .toBuffer()
    const asset = await memes.admitKnown(ctx('m1'), bytes, source())
    assert.ok(memes.recognitionOriginal(ctx('m1'), asset.id))
    const before = JSON.stringify(memes.adminGet(asset.id))
    edit(store)
    assert.throws(() => memes.recognitionOriginal(ctx('m1'), asset.id), /meme_not_available/)
    assert.equal(JSON.stringify(memes.adminGet(asset.id)), before)
    await assert.rejects(memes.admitKnown(ctx('m1'), bytes, source()), /invalid_source/)
    const otherBytes = await sharp({
      create: { width: 8, height: 8, channels: 3, background: '#0f0' },
    })
      .png()
      .toBuffer()
    const independent = await memes.admitKnown(ctx('m2'), otherBytes, source('m2'))
    await memes.admitKnown(ctx('m3'), otherBytes, source('m3'))
    observe(store, 'm2')
    edit(store, 'm2')
    assert.ok(memes.recognitionOriginal(ctx('m3'), independent.id))
  } finally {
    await memes.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
test('experience edit disables hints and feedback admission, not stored policy or outcomes', () => {
  const store = setup()
  observe(store)
  const service = new ExperienceService(store.db, {
    settings: () => ({ enabled: true, autoActivate: true, hints: true }),
    learningEnabled: () => true,
    sceneAvailable: () => true,
    admissionSourceAvailable: () => true,
    targetAvailable: () => true,
    sourceRevoked: (scene, id) => store.recalled(scene, id),
    validate: validateExperiencePolicy,
  })
  const ctx = { sceneId: 'scene', simulation: false }
  const input = {
    source: { messageId: 'm1', senderId: 'person' },
    target: { messageId: 'bot' },
    code: 'request_conclusion_first' as const,
    observedAt: new Date().toISOString(),
  }
  const result = service.recordFeedback(ctx, input)!
  assert.equal(result.candidate?.status, 'active')
  assert.equal(service.hints(ctx, { personId: 'person' }).length, 1)
  const before = JSON.stringify(
    store.db.prepare('SELECT value FROM memory_experience_policies').all(),
  )
  edit(store)
  assert.equal(service.hints(ctx, { personId: 'person' }).length, 0)
  assert.throws(() => service.recordFeedback(ctx, input), /feedback_not_authorized/)
  assert.equal(
    JSON.stringify(store.db.prepare('SELECT value FROM memory_experience_policies').all()),
    before,
  )
  store.close()
})
