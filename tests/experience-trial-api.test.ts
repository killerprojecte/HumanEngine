import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { ExperienceService } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import { OpenAPIHono } from '@hono/zod-openapi'
import { installExperienceRoutes } from '../src/memory/interfaces/experience-routes.ts'
test('experience HTTP keeps validated policy distinct from trial/promotion and exposes no receipt hashes', async () => {
  const store = new Store(':memory:')
  let feedback = true,
    revision = 1
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'account',
    platformSceneId: 'scene',
    name: 'Synthetic',
    learningEnabled: true,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  const now = Date.parse('2026-10-29T00:00:00Z')
  const experience = new ExperienceService(store.db, {
    settings: () => ({
      enabled: true,
      autoActivate: false,
      hints: true,
      feedbackTrials: feedback,
      revision,
    }),
    learningEnabled: (s) => s === 'scene',
    sceneAvailable: (s) => s === 'scene',
    admissionSourceAvailable: (s) => s === 'scene',
    targetAvailable: (s) => s === 'scene',
    sourceRevoked: () => false,
    validate: validateExperiencePolicy,
    now: () => now,
  })
  const ctx = { sceneId: 'scene', simulation: false }
  const initial = experience.recordFeedback(ctx, {
    source: { messageId: 'instruction', senderId: 'person' },
    target: { messageId: 'first-bot' },
    code: 'request_exact_person_lookup',
    observedAt: new Date(now).toISOString(),
  })!.candidate!
  const app = new OpenAPIHono()
  app.use('/api/*', async (c, next) => {
    if (c.req.header('Authorization') !== 'Bearer synthetic')
      return c.json({ error: 'unauthorized' }, 401)
    await next()
  })
  installExperienceRoutes(app, { experience, store })
  const headers = { Authorization: 'Bearer synthetic', 'Content-Type': 'application/json' }
  const get = async () =>
    (await app.request('/api/memory-experience?sceneId=scene', { headers })).json()
  try {
    assert.equal((await app.request('/api/memory-experience?sceneId=scene')).status, 401)
    const first = (await get()).items[0]
    assert.equal(first.status, 'validated')
    assert.equal(first.activationBasis, undefined)
    assert.equal(first.trial.phase, 'trial')
    assert.equal(first.trialEligible, true)
    assert.equal(first.trial.positiveCount, 0)
    assert.equal(first.trial.policyVersion, initial.version)
    for (const n of [1, 2]) {
      assert.equal(
        experience.recordUse(ctx, {
          turnId: 'turn-' + n,
          policyRef: { id: initial.id, version: initial.version },
          kind: 'applied',
          operation: 'memory_person',
          targetMessageId: 'bot-' + n,
        }),
        true,
      )
      experience.recordFeedback(ctx, {
        source: { messageId: 'feedback-' + n, senderId: 'person' },
        target: { messageId: 'bot-' + n, turnId: 'turn-' + n },
        code: 'user_reported_helpful',
        policyRef: { id: initial.id, version: initial.version },
        observedAt: new Date(now).toISOString(),
      })
    }
    const promoted = (await get()).items[0]
    assert.equal(promoted.status, 'validated')
    assert.equal(promoted.trial.phase, 'promoted')
    assert.equal(promoted.trial.positiveCount, 2)
    assert.equal(promoted.trialEligible, true)
    assert.equal(promoted.activationBasis, undefined)
    assert.doesNotMatch(
      JSON.stringify(promoted.trial),
      /policyHash|sourceRoot|targetRoot|settingsRevision|turnId|[a-f0-9]{64}/,
    )
    const reject = await app.request('/api/memory-experience/' + initial.id + '/review', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        sceneId: 'scene',
        expectedVersion: promoted.version,
        status: 'active',
        passed: true,
      }),
    })
    assert.equal(reject.status, 400)
    assert.equal(
      experience.recordUse(ctx, {
        turnId: 'cancel-turn',
        policyRef: { id: initial.id, version: promoted.version },
        kind: 'exposed',
        targetMessageId: 'cancel-bot',
      }),
      true,
    )
    experience.recordFeedback(ctx, {
      source: { messageId: 'explicit-cancel', senderId: 'person' },
      target: { messageId: 'cancel-bot' },
      code: 'withdraw_person_instruction',
      policyRef: { id: initial.id, version: promoted.version },
      observedAt: new Date(now).toISOString(),
    })
    const cancelled = (await get()).items[0]
    assert.equal(cancelled.status, 'suspended')
    assert.equal(cancelled.trial.phase, 'suspended')
    assert.equal(cancelled.trial.reason, 'instruction_withdrawn')
    assert.equal(cancelled.trialEligible, false)
    feedback = false
    revision++
    const off = (await get()).items[0]
    assert.equal(off.trialEligible, false)
    assert.equal(off.trial.positiveCount, 2)
    assert.equal(
      experience.hints(ctx, { requesterId: 'person', personId: 'person', personQuestion: true })
        .length,
      0,
    )
  } finally {
    store.close()
  }
})
