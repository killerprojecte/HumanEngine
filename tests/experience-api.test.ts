import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import { ExperienceService } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'

test('experience API authenticates strict CAS operations and exposes only scoped human safety/accounting projections', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const headers = {
    Authorization: 'Bearer synthetic-experience',
    'Content-Type': 'application/json',
  }
  for (const id of ['scene', 'foreign'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  let now = Date.parse('2026-10-28T00:00:00Z')
  const experience = new ExperienceService(store.db, {
    settings: () => ({
      enabled: store.settings.memoryExperienceEnabled,
      autoActivate: store.settings.memoryExperienceAutoActivateEnabled,
      hints: store.settings.memoryExperienceHintsEnabled,
    }),
    learningEnabled: (scene) => !!store.scene(scene)?.learningEnabled,
    sceneAvailable: (scene) => !!store.scene(scene),
    admissionSourceAvailable: (scene, message, sender) =>
      scene === 'scene' && message.startsWith('feedback-') && sender === 'person',
    targetAvailable: (scene, message) => scene === 'scene' && message === 'synthetic-bot-response',
    sourceRevoked: () => false,
    validate: validateExperiencePolicy,
    now: () => now,
  })
  Object.defineProperty(engine, 'experience', { value: experience, configurable: true })
  const app = createApp(store, { token: 'synthetic-experience', engine })
  const context = { sceneId: 'scene', simulation: false }
  const feedback = (messageId: string) =>
    experience.recordFeedback(context, {
      source: { messageId, senderId: 'person' },
      target: { messageId: 'synthetic-bot-response' },
      code: 'request_exact_person_lookup',
      observedAt: new Date(now).toISOString(),
    })!
  const get = async (scene = 'scene') =>
    (await app.request(`/api/memory-experience?sceneId=${scene}`, { headers })).json()
  const mutate = (id: string, version: number, operation: string, extra = {}, sceneId = 'scene') =>
    app.request(`/api/memory-experience/${id}${operation === 'delete' ? '' : '/' + operation}`, {
      method: operation === 'delete' ? 'DELETE' : 'POST',
      headers,
      body: JSON.stringify({ sceneId, expectedVersion: version, ...extra }),
    })
  try {
    for (const key of [
      'memoryExperienceEnabled',
      'memoryExperienceAutoActivateEnabled',
      'memoryExperienceHintsEnabled',
    ] as const)
      assert.equal(store.settings[key], false)
    assert.equal((await app.request('/api/memory-experience?sceneId=scene')).status, 401)
    assert.equal(
      (
        await app.request('/api/memory-experience/missing/validate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sceneId: 'scene', expectedVersion: 1 }),
        })
      ).status,
      401,
    )
    store.set('settings', {
      ...store.settings,
      memoryExperienceEnabled: true,
      memoryExperienceHintsEnabled: true,
    })
    store.set('identity:scene:person', {
      senderDisplayName: 'Synthetic person',
      senderPlatformId: 'PRIVATE_METADATA_CANARY',
    })
    const first = feedback('feedback-1').candidate!
    let rows = await get()
    assert.equal(rows.items.length, 1)
    assert.equal(rows.items[0].personName, 'Synthetic person')
    assert.equal(rows.items[0].sources[0].messageId, 'feedback-1')
    assert.deepEqual(rows.items[0].uses, { exposed: 0, applied: 0, declined: 0 })
    assert.doesNotMatch(
      JSON.stringify(rows),
      /PRIVATE_METADATA_CANARY|generationRoots|datasetHash|candidateHash|rootFamily/,
    )
    assert.deepEqual((await get('foreign')).items, [])
    assert.equal((await mutate(first.id, first.version, 'validate', {}, 'foreign')).status, 404)
    assert.equal((await mutate(first.id, first.version, 'validate', { passed: true })).status, 400)
    assert.equal(
      (await mutate(first.id, first.version, 'review', { status: 'active' })).status,
      409,
    )
    assert.equal((await mutate(first.id, first.version, 'validate')).status, 200)
    assert.equal((await mutate(first.id, first.version, 'validate')).status, 409)
    let policy = (await get()).items[0]
    assert.equal(policy.status, 'validated')
    assert.equal(policy.validation.tier, 'mechanical_only')
    assert.equal(policy.validation.passed, true)
    assert.equal(
      (await mutate(policy.id, policy.version, 'review', { status: 'active' })).status,
      200,
    )
    policy = (await get()).items[0]
    assert.equal(policy.activationBasis, 'administrator_review')
    experience.recordUse(context, {
      turnId: 'synthetic-turn',
      policyRef: { id: policy.id, version: policy.version },
      kind: 'exposed',
    })
    experience.recordUse(context, {
      turnId: 'synthetic-turn',
      policyRef: { id: policy.id, version: policy.version },
      kind: 'applied',
      operation: 'memory_person',
      targetMessageId: 'synthetic-bot-response',
    })
    assert.deepEqual((await get()).items[0].uses, { exposed: 1, applied: 1, declined: 0 })
    assert.equal(
      (await mutate(policy.id, policy.version, 'review', { status: 'suspended' })).status,
      200,
    )
    policy = (await get()).items[0]
    assert.equal(
      (await mutate(policy.id, policy.version, 'review', { status: 'retired' })).status,
      200,
    )
    policy = (await get()).items[0]
    assert.equal((await mutate(policy.id, policy.version, 'delete')).status, 200)
    assert.deepEqual((await get()).items, [])
    const second = feedback('feedback-2').candidate!
    now += 8 * 86400000
    assert.equal((await get()).items[0].expired, true)
    assert.equal((await mutate(second.id, second.version, 'validate')).status, 409)
    store.set('settings', { ...store.settings, memoryExperienceEnabled: false })
    assert.equal((await get()).enabled, false)
    assert.deepEqual((await get()).items, [])
    assert.equal((await mutate(second.id, second.version, 'delete')).status, 404)
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(
      (await app.request('/api/memory-experience?sceneId=simulation', { headers })).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
  } finally {
    engine.dispose()
    store.close()
  }
})
