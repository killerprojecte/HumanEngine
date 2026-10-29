import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import {
  configurationFlags,
  parseConfigurationRecoveryEvent,
} from '../src/memory/recovery/configuration-contract.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

test('feedback configuration v8 is separate and legacy v7 cannot smuggle the new flag', () => {
  const flags = configurationFlags({}),
    { memoryExperienceFeedbackEnabled, ...old } = flags
  assert.equal(memoryExperienceFeedbackEnabled, false)
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 7, flags: old })
      .flags,
    old,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 7, flags }),
    /recovery_configuration_invalid/,
  )
  assert.deepEqual(
    parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 8, flags }).flags,
    flags,
  )
  assert.throws(
    () => parseConfigurationRecoveryEvent({ kind: 'configuration_flags', schemaVersion: 9, flags }),
    /recovery_configuration_invalid/,
  )
})

test('old snapshot replays feedback OFF and ABA with the independent monotonic policy revision', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-trial-recovery-'))
  const database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  let reopened: Store | undefined
  try {
    const set = (enabled: boolean) =>
      store.set('settings', { ...store.settings, memoryExperienceFeedbackEnabled: enabled })
    assert.equal(store.get('memory-experience-feedback-revision', 0), 0)
    set(true)
    assert.equal(store.get('memory-experience-feedback-revision', 0), 1)
    const backupRoot = join(root, 'backup')
    await backup({ database, mediaRoot, destination: backupRoot })
    set(false)
    set(true)
    set(true)
    set(false)
    assert.equal(store.get('memory-experience-feedback-revision', 0), 4)
    const restored = await restore({
      backupRoot,
      destination: join(root, 'restored'),
      authorityDirectory: metadata(store.db)!.authority,
    })
    reopened = new Store(restored.database)
    assert.equal(
      reopened.get<Record<string, unknown>>('settings', {}).memoryExperienceFeedbackEnabled,
      false,
    )
    assert.equal(reopened.get('memory-experience-feedback-revision', 0), 4)
  } finally {
    reopened?.close()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

async function trialFixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-trial-live-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database),
    opened: Store[] = []
  store.saveScene({
    id: 'scene',
    adapterId: 'synthetic',
    accountId: 'account',
    platformSceneId: 'scene',
    kind: 'group',
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  store.set('settings', {
    ...store.settings,
    memoryExperienceEnabled: true,
    memoryExperienceHintsEnabled: true,
    memoryExperienceFeedbackEnabled: true,
  })
  const { ExperienceService } = await import('../src/memory/experience/service.ts')
  const { validateExperiencePolicy } = await import('../src/memory/experience/validation.ts')
  let now = Date.parse('2026-10-29T12:00:00Z')
  const ctx = { sceneId: 'scene', simulation: false }
  const create = (s: Store) =>
    new ExperienceService(s.db, {
      settings: () => ({
        enabled: true,
        autoActivate: false,
        hints: true,
        feedbackTrials: s.settings.memoryExperienceFeedbackEnabled,
        revision: s.get('memory-experience-feedback-revision', 0),
      }),
      learningEnabled: () => true,
      sceneAvailable: (s) => s === 'scene',
      admissionSourceAvailable: () => true,
      targetAvailable: () => true,
      sourceRevoked: (scene, message) => s.recalled(scene, message),
      validate: validateExperiencePolicy,
      now: () => now,
    })
  const service = create(store)
  const start = () =>
    service.recordFeedback(ctx, {
      source: { messageId: 'instruction', senderId: 'person' },
      target: { messageId: 'first-bot' },
      code: 'request_exact_person_lookup',
      observedAt: new Date(now).toISOString(),
    })!.candidate!
  const positive = (policy: { id: string; version: number }, n: number) => {
    assert.equal(
      service.recordUse(ctx, {
        turnId: 'turn-' + n,
        policyRef: policy,
        kind: 'applied',
        operation: 'memory_person',
        targetMessageId: 'bot-' + n,
      }),
      true,
    )
    return service.recordFeedback(ctx, {
      source: { messageId: 'feedback-' + n, senderId: 'person' },
      target: { messageId: 'bot-' + n, turnId: 'turn-' + n },
      code: 'user_reported_helpful',
      policyRef: policy,
      observedAt: new Date(now).toISOString(),
    })!
  }
  return {
    root,
    store,
    service,
    advance: () => {
      now += 8 * 86400_000
    },
    start,
    positive,
    ctx,
    create,
    snapshot: async (name: string) => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (backupRoot: string, name = 'restored') => {
      const result = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const s = new Store(result.database)
      opened.push(s)
      return s
    },
    close: async () => {
      for (const s of opened) s.close()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('actual trial and two reported supports survive current snapshot; old material cannot invent promotion', async () => {
  const f = await trialFixture()
  try {
    const before = await f.snapshot('before'),
      policy = f.start()
    assert.equal(f.service.trials.get(policy.id)?.phase, 'trial')
    await assert.rejects(f.recover(before, 'missing-start'), /recovery_experience_trial_/)
    const trial = await f.snapshot('trial')
    f.positive(policy, 1)
    assert.equal(f.service.store.policy(policy.id)!.version, policy.version)
    assert.equal(f.service.trials.get(policy.id)!.positives.length, 1)
    f.positive(policy, 2)
    assert.equal(f.service.trials.get(policy.id)!.phase, 'promoted')
    assert.equal(f.service.store.policy(policy.id)!.version, policy.version + 1)
    await assert.rejects(f.recover(trial, 'missing-positive'), /recovery_experience_trial_/)
    const restored = await f.recover(await f.snapshot('current'))
    const service = f.create(restored)
    assert.deepEqual(service.trials.get(policy.id), f.service.trials.get(policy.id))
    assert.equal(
      service.hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true })
        .length,
      1,
    )
  } finally {
    await f.close()
  }
})

for (const recalled of ['feedback-1', 'bot-1'])
  test(`actual positive source or target ${recalled} recall erases trial authorization and restores without resurrecting feedback`, async () => {
    const f = await trialFixture()
    try {
      const policy = f.start()
      f.positive(policy, 1)
      f.positive(policy, 2)
      const old = await f.snapshot('old')
      f.store.db
        .transaction(() => {
          f.store.recall('scene', recalled)
          f.service.revokeSource('scene', recalled)
        })
        .immediate()
      assert.equal(f.service.trials.get(policy.id), undefined)
      assert.equal(
        f.service.hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true })
          .length,
        0,
      )
      const restored = await f.recover(old)
      const service = f.create(restored)
      assert.equal(service.trials.get(policy.id), undefined)
      assert.equal(
        service.hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true })
          .length,
        0,
      )
      assert.deepEqual(service.store.sourceOutcomes('scene', recalled), [])
    } finally {
      await f.close()
    }
  })

test('negative feedback cannot be dropped by an old backup; current suspended receipt remains non-authoritative', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start(),
      old = await f.snapshot('pre-negative')
    assert.equal(
      f.service.recordUse(f.ctx, {
        turnId: 'negative-turn',
        policyRef: policy,
        kind: 'applied',
        operation: 'memory_person',
        targetMessageId: 'negative-target',
      }),
      true,
    )
    const result = f.service.recordFeedback(f.ctx, {
      source: { messageId: 'negative-source', senderId: 'person' },
      target: { messageId: 'negative-target', turnId: 'negative-turn' },
      code: 'counterexample',
      policyRef: policy,
      observedAt: '2026-10-29T12:00:00Z',
    })!
    assert.equal(result.candidate?.status, 'suspended')
    assert.equal(f.service.trials.get(policy.id)?.phase, 'suspended')
    await assert.rejects(
      f.recover(old, 'missing-negative'),
      /recovery_experience_qualification_missing/,
    )
    const restored = await f.recover(await f.snapshot('with-negative'))
    assert.equal(
      f
        .create(restored)
        .hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true }).length,
      0,
    )
    assert.deepEqual(f.create(restored).trials.get(policy.id), f.service.trials.get(policy.id))
  } finally {
    await f.close()
  }
})

test('trial expiry is a bounded restrictive replay without fabricating new evidence', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start(),
      old = await f.snapshot('before-expiry')
    f.advance()
    assert.equal(
      f.service.hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true })
        .length,
      0,
    )
    assert.equal(f.service.trials.get(policy.id)?.phase, 'expired')
    const restored = await f.recover(old)
    assert.deepEqual(f.create(restored).trials.get(policy.id), f.service.trials.get(policy.id))
  } finally {
    await f.close()
  }
})

test('restored feedback OFF then ON cannot renew the old trial settings revision', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start(),
      old = await f.snapshot('before-toggle')
    const before = f.service.trials.get(policy.id)!
    for (const enabled of [false, true])
      f.store.set('settings', { ...f.store.settings, memoryExperienceFeedbackEnabled: enabled })
    assert.equal(
      f.service.hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true })
        .length,
      0,
    )
    const restored = await f.recover(old)
    assert.equal(
      restored.get('memory-experience-feedback-revision', 0),
      before.settingsRevision + 2,
    )
    assert.equal(
      f
        .create(restored)
        .hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true }).length,
      0,
    )
    assert.deepEqual(f.create(restored).trials.get(policy.id), before)
  } finally {
    await f.close()
  }
})

test('trial metadata parser rejects fabricated versions, unknown reasons and copied feedback text', async () => {
  const { parseExperienceTrialRecoveryEvent } =
    await import('../src/memory/recovery/experience-trial-contract.ts')
  const event = {
    kind: 'experience_trial',
    schemaVersion: 1,
    operation: 'collect',
    policyId: 'policy',
    sceneId: 'scene',
    expectedPolicyVersion: 2,
    nextPolicyVersion: 2,
    expectedReceiptVersion: 1,
    nextReceiptVersion: 2,
    beforeHash: 'a'.repeat(64),
    afterHash: 'b'.repeat(64),
    nextPhase: 'trial',
  }
  assert.deepEqual(parseExperienceTrialRecoveryEvent(event), event)
  for (const patch of [
    { nextReceiptVersion: 4 },
    { nextPolicyVersion: 3 },
    { nextReason: 'model_says_success' },
    { text: 'PRIVATE_FEEDBACK' },
    { schemaVersion: 2 },
  ])
    assert.throws(
      () => parseExperienceTrialRecoveryEvent({ ...event, ...patch }),
      /recovery_experience_trial_event_invalid/,
    )
})

test('failed local trial transaction keeps durable intent and blocks live state and stale restore', async () => {
  const f = await trialFixture()
  try {
    const old = await f.snapshot('pre-rollback')
    assert.throws(
      () =>
        f.store.db
          .transaction(() => {
            f.start()
            throw new Error('synthetic_rollback')
          })
          .immediate(),
      /synthetic_rollback/,
    )
    assert.throws(() => f.store.settings, /recovery_authority_head_changed/)
    await assert.rejects(f.recover(old), /recovery_experience_trial_material_missing/)
  } finally {
    await f.close()
  }
})

test('trial restore rejects missing actual application evidence even with an intact receipt hash', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start()
    f.positive(policy, 1)
    f.positive(policy, 2)
    f.store.db.prepare('DELETE FROM memory_experience_uses WHERE target=?').run('bot-1')
    await assert.rejects(
      f.recover(await f.snapshot('incomplete-use')),
      /recovery_experience_trial_evidence_missing/,
    )
  } finally {
    await f.close()
  }
})

test('unknown trial columns and BLOB receipts never become accepted recovery authority', async () => {
  const { createRecoveryExperienceTrialsPort } =
    await import('../src/memory/recovery/experience-trials.ts')
  const store = new Store(':memory:')
  try {
    store.db.exec(
      'CREATE TABLE memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT,version INTEGER,value TEXT,future TEXT)',
    )
    const port = createRecoveryExperienceTrialsPort(store)
    assert.throws(() => port.assertSupported(), /recovery_unknown_experience_trial_schema/)
    store.db.exec(
      'DROP TABLE memory_experience_trials; CREATE TABLE memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT,version INTEGER,value TEXT)',
    )
    store.db
      .prepare('INSERT INTO memory_experience_trials VALUES(?,?,?,?)')
      .run('policy', 'scene', 1, Buffer.from('{}'))
    assert.throws(() => port.assertSupported(), /recovery_experience_trial_material_invalid/)
  } finally {
    store.close()
  }
})

test('explicit administrator transition suspends the trial receipt and restores its independent policy state', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start(),
      old = await f.snapshot('before-admin')
    f.service.review(f.ctx, policy.id, policy.version, 'suspended')
    assert.equal(f.service.trials.get(policy.id)?.phase, 'suspended')
    const restored = await f.recover(old)
    assert.deepEqual(f.create(restored).trials.get(policy.id), f.service.trials.get(policy.id))
    assert.deepEqual(f.create(restored).store.policy(policy.id), f.service.store.policy(policy.id))
    assert.equal(
      f
        .create(restored)
        .hints(f.ctx, { personId: 'person', requesterId: 'person', personQuestion: true }).length,
      0,
    )
  } finally {
    await f.close()
  }
})

test('direct deletion of a promoted policy restores exact positive outcome and use cleanup', async () => {
  const f = await trialFixture()
  try {
    const policy = f.start()
    f.positive(policy, 1)
    f.positive(policy, 2)
    const independent = f.service.recordFeedback(f.ctx, {
      source: { messageId: 'independent-instruction', senderId: 'other-person' },
      target: { messageId: 'independent-target' },
      code: 'request_exact_person_lookup',
      observedAt: '2026-10-29T12:00:00Z',
    })!.candidate!
    const old = await f.snapshot('before-direct-delete')
    const tables = [
      'memory_experience_policies',
      'memory_experience_trials',
      'memory_experience_outcomes',
      'memory_experience_uses',
      'memory_experience_tombstones',
    ]
    const rows = (store: Store) =>
      tables.map((table) => [table, store.db.prepare(`SELECT * FROM ${table} ORDER BY 1`).all()])
    f.service.delete(f.ctx, policy.id, f.service.store.policy(policy.id)!.version)
    const expected = rows(f.store)
    assert.equal(f.service.store.outcomes('scene', 100).length, 1)
    assert.ok(f.service.store.policy(independent.id))
    const restored = await f.recover(old)
    assert.deepEqual(rows(restored), expected)
  } finally {
    await f.close()
  }
})
