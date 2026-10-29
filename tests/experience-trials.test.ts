import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { ExperienceService, type ExperiencePorts } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import type { ExperiencePolicyCandidate, TrustedFeedback } from '../src/memory/domain/experience.ts'
import type { TrialMutation } from '../src/memory/experience/trial-contracts.ts'
const ctx = { sceneId: 'scene', simulation: false }
function fixture() {
  const db = new Database(':memory:')
  let seq = 0,
    now = Date.parse('2026-10-29T00:00:00Z'),
    learning = true
  const flags = {
    enabled: true,
    hints: true,
    autoActivate: true,
    feedbackTrials: true,
    revision: 1,
  }
  const revoked = new Set<string>(),
    mutations: TrialMutation[] = []
  const ports: ExperiencePorts = {
    settings: () => flags,
    learningEnabled: () => learning,
    sceneAvailable: (s) => s === ctx.sceneId,
    admissionSourceAvailable: () => true,
    targetAvailable: () => true,
    sourceRevoked: (_, id) => revoked.has(id),
    validate: validateExperiencePolicy,
    now: () => now,
    id: () => `id${++seq}`,
    beforeTrialWrite: (m) => {
      mutations.push(m)
    },
  }
  const service = new ExperienceService(db, ports)
  const input = (
    id: string,
    code: TrustedFeedback['code'] = 'request_exact_person_lookup',
    target = 'intro',
  ): TrustedFeedback => ({
    source: { messageId: id, senderId: 'alice' },
    target: { messageId: target },
    observedAt: new Date(now).toISOString(),
    code,
  })
  const start = (code: TrustedFeedback['code'] = 'request_exact_person_lookup') =>
    service.recordFeedback(ctx, input('instruction', code))!.candidate!
  const applied = (p: ExperiencePolicyCandidate, target: string, turn = target) =>
    service.recordUse(ctx, {
      turnId: turn,
      policyRef: { id: p.id, version: p.version },
      kind: 'applied',
      operation:
        p.action === 'exact_person_before_semantic' ? 'memory_person' : 'plan_qualifiers_checked',
      targetMessageId: target,
    })
  const report = (p: ExperiencePolicyCandidate, id: string, target: string, sender = 'alice') =>
    service.recordFeedback(ctx, {
      ...input(id, 'user_reported_helpful', target),
      source: { messageId: id, senderId: sender },
      policyRef: { id: p.id, version: p.version },
    })!
  return {
    db,
    ports,
    service,
    flags,
    revoked,
    mutations,
    input,
    start,
    applied,
    report,
    advance: () => (now += 8 * 86400000),
    learn: (v: boolean) => (learning = v),
  }
}
test('fresh explicit operation instruction starts bounded trial; two exact independent applied reports promote by separate CAS', () => {
  const f = fixture()
  try {
    const p = f.start()
    assert.equal(p.status, 'validated')
    assert.equal(p.version, 2)
    const hint = f.service.hints(ctx, {
      requesterId: 'alice',
      personId: 'alice',
      personQuestion: true,
    })[0]!
    assert.equal(hint.eligibilityBasis, 'explicit_instruction_trial')
    assert.equal(hint.reportedHelpfulCount, 0)
    assert.equal(
      f.service.hints(ctx, { requesterId: 'bob', personId: 'bob', personQuestion: true }).length,
      0,
    )
    f.applied(p, 'reply1')
    const one = f.report(p, 'help1', 'reply1')
    assert.equal(one.candidate!.version, p.version)
    assert.equal(f.service.trials.get(p.id)!.positives.length, 1)
    assert.equal(f.service.snapshotValid(ctx, hint), false)
    f.applied(p, 'reply2')
    const two = f.report(p, 'help2', 'reply2')
    assert.equal(two.candidate!.version, p.version + 1)
    assert.equal(two.candidate!.hash, p.hash)
    assert.equal(f.service.trials.get(p.id)!.evidencePolicyVersion, p.version)
    const final = f.service.hints(ctx, {
      requesterId: 'alice',
      personId: 'alice',
      personQuestion: true,
    })[0]!
    assert.equal(final.eligibilityBasis, 'user_reported_support')
    assert.equal(final.reportedHelpfulCount, 2)
    assert.equal(
      f.service.recordFeedback(ctx, {
        ...f.input('help2', 'user_reported_helpful', 'reply2'),
        policyRef: { id: p.id, version: p.version },
      })!.duplicate,
      true,
    )
    assert.throws(() => f.report(p, 'help3', 'reply2'), /version_conflict/)
    assert.deepEqual(
      f.mutations.map((m) => m.operation),
      ['start', 'collect', 'promote'],
    )
  } finally {
    f.db.close()
  }
})
test('ACK/exposure, other person, same target, same root and same turn cannot qualify promotion', () => {
  const f = fixture()
  try {
    const p = f.start('request_verify_plan_qualifiers')
    f.service.recordUse(ctx, {
      turnId: 'exposed',
      policyRef: p,
      kind: 'exposed',
      targetMessageId: 'ack',
    })
    assert.throws(() => f.report(p, 'ack-help', 'ack'), /unverified_policy_use/)
    f.applied(p, 'r1', 'turn1')
    assert.throws(() => f.report(p, 'wrong-person', 'r1', 'bob'), /trial_ineligible/)
    f.report(p, 'one', 'r1')
    assert.throws(() => f.report(p, 'two', 'r1'), /not_independent/)
    f.applied(p, 'r2', 'turn1')
    assert.throws(() => f.report(p, 'three', 'r2'), /not_independent/)
    f.applied(p, 'r3', 'turn3')
    assert.throws(() => f.report(p, 'one', 'r3'), /not_independent/)
    assert.equal(f.service.trials.get(p.id)!.positives.length, 1)
    assert.equal(
      f.service.hints(ctx, { requesterId: 'bob', personId: 'bob', planQuestion: true }).length,
      0,
    )
  } finally {
    f.db.close()
  }
})
test('negative report suspends exact operation trial and old version cannot suspend administrator new policy', () => {
  const f = fixture()
  try {
    const p = f.start()
    f.applied(p, 'r1')
    const result = f.service.recordFeedback(ctx, {
      ...f.input('negative', 'counterexample', 'r1'),
      policyRef: p,
    })!
    assert.equal(result.candidate!.status, 'suspended')
    assert.equal(f.service.trials.get(p.id)!.phase, 'suspended')
    assert.equal(
      f.service.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
        .length,
      0,
    )
    assert.throws(() => f.report(p, 'later', 'r1'), /version_conflict/)
  } finally {
    f.db.close()
  }
})
test('new flag OFF and learning OFF do not start trials; OFF ABA invalidates old trials without changing independent admin grants', () => {
  const f = fixture()
  try {
    f.flags.feedbackTrials = false
    const old = f.start()
    assert.equal(old.status, 'candidate')
    assert.equal(f.service.trials.get(old.id), undefined)
    f.flags.feedbackTrials = true
    f.flags.revision++
    assert.equal(
      f.service.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
        .length,
      0,
    )
    const fresh = f.service.recordFeedback(ctx, f.input('fresh'))!.candidate!
    assert.equal(
      f.service.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
        .length,
      1,
    )
    f.flags.feedbackTrials = false
    f.flags.revision++
    assert.equal(f.service.snapshotValid(ctx, fresh), false)
    f.flags.feedbackTrials = true
    f.flags.revision++
    assert.equal(f.service.snapshotValid(ctx, fresh), false)
    const admin = f.service.review(ctx, fresh.id, fresh.version, 'active')
    f.flags.feedbackTrials = false
    assert.equal(f.service.snapshotValid(ctx, admin), true)
    f.learn(false)
    assert.equal(f.service.recordFeedback(ctx, f.input('no-learn')), undefined)
  } finally {
    f.db.close()
  }
})
test('expiry retains receipt as expired; restart never auto activates old instructions', () => {
  const f = fixture()
  try {
    const p = f.start()
    const restarted = new ExperienceService(f.db, f.ports)
    assert.equal(
      restarted.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
        .length,
      1,
    )
    f.advance()
    assert.equal(
      restarted.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
        .length,
      0,
    )
    assert.equal(restarted.trials.get(p.id)!.phase, 'expired')
    assert.equal(restarted.store.policy(p.id)!.status, 'validated')
  } finally {
    f.db.close()
  }
})
test('positive source or bot target recall physically clears trial evidence but preserves independent original instruction', () => {
  for (const recalled of ['positive', 'r1']) {
    const f = fixture()
    try {
      const p = f.start()
      f.applied(p, 'r1')
      f.report(p, 'positive', 'r1')
      f.revoked.add(recalled)
      f.service.revokeSource(ctx.sceneId, recalled)
      assert.equal(f.service.trials.get(p.id), undefined)
      assert.equal(f.service.store.sourceOutcomes(ctx.sceneId, 'positive').length, 0)
      assert.equal(f.service.store.policy(p.id)!.status, 'validated')
      assert.equal(
        f.service.hints(ctx, { requesterId: 'alice', personId: 'alice', personQuestion: true })
          .length,
        0,
      )
      assert.equal(f.service.store.sourceOutcomes(ctx.sceneId, 'instruction').length, 1)
    } finally {
      f.db.close()
    }
  }
})
test('no-ref new admission after enrollment fails; later enrollment invalidates original untracked source instead of upgrading snapshot', () => {
  const f = fixture()
  try {
    const p = f.start()
    const events = new SourceEventStore(f.db, { now: () => '2026-10-29T00:00:00Z' })
    const identity = {
      sceneId: 'scene',
      adapterId: 'adapter',
      accountId: 'account',
      messageId: 'instruction',
      senderId: 'alice',
    }
    f.db.transaction(() =>
      events.observe({ kind: 'ingress', identity, bodyHash: 'a'.repeat(64), occurredAt: null }),
    )()
    assert.equal(f.service.snapshotValid(ctx, p), false)
    assert.throws(() => f.service.recordFeedback(ctx, f.input('instruction')), /snapshot_stale/)
  } finally {
    f.db.close()
  }
})
test('hook failure rolls back trial and ordinary outcome; simulation leaves no trial table', () => {
  const f = fixture()
  try {
    assert.equal(f.service.recordFeedback({ ...ctx, simulation: true }, f.input('sim')), undefined)
    assert.equal(
      f.db.prepare("SELECT 1 FROM sqlite_master WHERE name='memory_experience_trials'").get(),
      undefined,
    )
    f.ports.beforeTrialWrite = () => {
      throw new Error('unused replaced closure')
    }
    const failing = new ExperienceService(f.db, {
      ...f.ports,
      beforeTrialWrite: () => {
        throw new Error('test_hold')
      },
    })
    assert.throws(() => failing.recordFeedback(ctx, f.input('hold')), /test_hold/)
    assert.equal(failing.store.sourceOutcomes(ctx.sceneId, 'hold').length, 0)
  } finally {
    f.db.close()
  }
})
test('operation instruction withdrawal requires matching exposed action and requester, not applied success', () => {
  for (const [instruction, withdrawal] of [
    ['request_exact_person_lookup', 'withdraw_person_instruction'],
    ['request_verify_plan_qualifiers', 'withdraw_plan_instruction'],
  ] as const) {
    const f = fixture()
    try {
      const p = f.start(instruction)
      f.service.recordUse(ctx, {
        turnId: 'exposure',
        policyRef: p,
        kind: 'exposed',
        targetMessageId: 'reply',
      })
      const wrong = {
        ...f.input('wrong', withdrawal, 'reply'),
        source: { messageId: 'wrong', senderId: 'bob' },
        policyRef: p,
      }
      assert.throws(() => f.service.recordFeedback(ctx, wrong), /person_mismatch/)
      assert.throws(
        () =>
          f.service.recordFeedback(ctx, {
            ...f.input(
              'wrong-action',
              withdrawal === 'withdraw_person_instruction'
                ? 'withdraw_plan_instruction'
                : 'withdraw_person_instruction',
              'reply',
            ),
            policyRef: p,
          }),
        /instruction_mismatch/,
      )
      const result = f.service.recordFeedback(ctx, {
        ...f.input('withdraw', withdrawal, 'reply'),
        policyRef: p,
      })!
      assert.equal(result.candidate!.status, 'suspended')
      assert.equal(f.service.trials.get(p.id)!.reason, 'instruction_withdrawn')
      assert.equal(f.service.trials.get(p.id)!.positives.length, 0)
    } finally {
      f.db.close()
    }
  }
})
test('administrator state change explicitly suspends trial receipt without revoking independent admin authorization', () => {
  const f = fixture()
  try {
    const p = f.start()
    const admin = f.service.review(ctx, p.id, p.version, 'active')
    assert.equal(f.service.trials.get(p.id)!.phase, 'suspended')
    assert.equal(f.service.trials.get(p.id)!.reason, 'qualification_changed')
    f.flags.feedbackTrials = false
    assert.equal(f.service.snapshotValid(ctx, admin), true)
    assert.equal(f.mutations.at(-1)!.operation, 'suspend')
  } finally {
    f.db.close()
  }
})
test('receipt capacity refuses new grants before intent but leaves all existing revocations available; stored BLOB fails closed', () => {
  const f = fixture()
  try {
    const p = f.start(),
      sample = f.service.trials.get(p.id)!
    const insert = f.db.prepare('INSERT INTO memory_experience_trials VALUES(?,?,?,?)')
    f.db.transaction(() => {
      for (let n = 1; n < 4096; n++) {
        const clone = { ...sample, policyId: `capacity-${n}` }
        insert.run(clone.policyId, clone.sceneId, clone.version, JSON.stringify(clone))
      }
    })()
    const intents = f.mutations.length
    assert.throws(() => f.service.recordFeedback(ctx, f.input('capacity-new')), /trial_capacity/)
    assert.equal(f.mutations.length, intents)
    assert.equal(f.service.store.sourceOutcomes(ctx.sceneId, 'capacity-new').length, 0)
    f.service.delete(ctx, p.id, p.version)
    assert.equal(f.service.trials.get(p.id), undefined)
    f.db
      .prepare('UPDATE memory_experience_trials SET value=CAST(value AS BLOB) WHERE policy=?')
      .run('capacity-1')
    assert.throws(() => f.service.trials.get('capacity-1'), /trial_corrupt/)
  } finally {
    f.db.close()
  }
})
test('exact newly enrolled source snapshot is accepted, but an edit invalidates old evidence without deleting retained receipt', () => {
  const f = fixture()
  try {
    const events = new SourceEventStore(f.db, { now: () => '2026-10-29T00:00:00Z' })
    const identity = {
      sceneId: 'scene',
      adapterId: 'adapter',
      accountId: 'account',
      messageId: 'enrolled',
      senderId: 'alice',
    }
    const first = f.db.transaction(() =>
      events.observe({ kind: 'ingress', identity, bodyHash: 'b'.repeat(64), occurredAt: null }),
    )()
    const input = f.input('enrolled')
    input.source.eventRef = first.ref!
    const p = f.service.recordFeedback(ctx, input)!.candidate!
    assert.equal(f.service.snapshotValid(ctx, p), true)
    f.db.transaction(() =>
      events.observe({
        kind: 'edit',
        identity,
        bodyHash: 'c'.repeat(64),
        occurredAt: null,
        editId: 'e1',
        order: { kind: 'observed_cas', expectedBase: first.ref! },
      }),
    )()
    assert.equal(f.service.snapshotValid(ctx, p), false)
    assert.ok(f.service.trials.get(p.id))
  } finally {
    f.db.close()
  }
})
