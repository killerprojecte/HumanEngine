import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { ExperienceService, type ExperiencePorts } from '../src/memory/experience/service.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import type { ExperienceContext, TrustedFeedback } from '../src/memory/domain/experience.ts'
const ctx: ExperienceContext = { sceneId: 'scene', simulation: false }
function fixture(db = new Database(':memory:')) {
  const flags = { enabled: false, autoActivate: false, hints: false }
  let learning = true,
    live = true,
    time = Date.parse('2026-10-28T00:00:00Z'),
    sequence = 0
  const revoked = new Set<string>()
  const ports: ExperiencePorts = {
    settings: () => flags,
    learningEnabled: () => learning,
    sceneAvailable: (s) => s === 'scene',
    admissionSourceAvailable: () => live,
    targetAvailable: () => live,
    sourceRevoked: (_, id) => revoked.has(id),
    validate: validateExperiencePolicy,
    now: () => time,
    id: () => String(++sequence),
  }
  const service = new ExperienceService(db, ports)
  const input = (id = 'source'): TrustedFeedback => ({
    source: { messageId: id, senderId: 'person' },
    target: { messageId: 'bot-response' },
    code: 'request_conclusion_first',
    observedAt: '2026-10-28T00:00:00Z',
  })
  return {
    db,
    service,
    flags,
    ports,
    input,
    revoked,
    enable: () => Object.assign(flags, { enabled: true, autoActivate: true, hints: true }),
    learn: (v: boolean) => (learning = v),
    live: (v: boolean) => (live = v),
    advance: () => (time += 8 * 86400000),
  }
}
test('default OFF, learning OFF and simulation have no outcome writes; exact rooted feedback is idempotent', () => {
  const f = fixture()
  assert.equal(f.service.recordFeedback(ctx, f.input()), undefined)
  f.enable()
  f.learn(false)
  assert.equal(f.service.recordFeedback(ctx, f.input()), undefined)
  f.learn(true)
  assert.equal(f.service.recordFeedback({ ...ctx, simulation: true }, f.input()), undefined)
  const first = f.service.recordFeedback(ctx, f.input())!
  assert.equal(first.candidate?.status, 'active')
  const duplicate = f.service.recordFeedback(ctx, f.input())!
  assert.equal(duplicate.duplicate, true)
  assert.equal(duplicate.outcome.id, first.outcome.id)
  assert.equal(f.service.list(ctx).items.length, 1)
  assert.equal(f.service.listOutcomes(ctx).length, 1)
  assert.equal(f.service.hints(ctx, { personId: 'other' }).length, 0)
  assert.equal(f.service.hints(ctx, { personId: 'person' }).length, 1)
  f.db.close()
})
test('restart preserves admitted source without live preview; expiry and flags invalidate snapshots', () => {
  const f = fixture()
  f.enable()
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  f.live(false)
  const restored = new ExperienceService(f.db, f.ports)
  assert.equal(restored.hints(ctx, { personId: 'person' }).length, 1)
  assert.equal(restored.snapshotValid(ctx, p), true)
  f.flags.hints = false
  assert.equal(restored.snapshotValid(ctx, p), false)
  f.flags.hints = true
  f.advance()
  assert.equal(restored.hints(ctx, { personId: 'person' }).length, 0)
  assert.equal(restored.list(ctx).items[0]?.status, 'suspended')
  f.db.close()
})
test('mechanical receipt exact binding and CAS; no automatic activation of old candidate on toggle', () => {
  const f = fixture()
  f.flags.enabled = true
  f.flags.hints = true
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  assert.equal(p.status, 'candidate')
  f.flags.autoActivate = true
  assert.equal(f.service.hints(ctx, { personId: 'person' }).length, 0)
  const validated = f.service.validate(ctx, p.id, p.version)
  assert.equal(validated.status, 'validated')
  assert.throws(() => f.service.review(ctx, p.id, p.version, 'active'), /version_conflict/)
  const active = f.service.review(ctx, p.id, validated.version, 'active')
  assert.equal(active.status, 'active')
  f.db.close()
})
test('actual applied use anchors negative feedback; ACK or invented target cannot reward policy', () => {
  const f = fixture()
  f.enable()
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  assert.throws(
    () =>
      f.service.recordFeedback(ctx, {
        ...f.input('negative'),
        code: 'counterexample',
        policyRef: p,
      }),
    /unverified_policy_use/,
  )
  assert.throws(
    () =>
      f.service.recordUse(ctx, {
        turnId: 'turn',
        policyRef: p,
        kind: 'applied',
        operation: 'memory_person',
        targetMessageId: 'bot-response',
      }),
    /invalid_use/,
  )
  assert.equal(
    f.service.recordUse(ctx, {
      turnId: 'turn',
      policyRef: p,
      kind: 'applied',
      operation: 'conclusion_first_response',
      targetMessageId: 'bot-response',
    }),
    true,
  )
  const negative = f.service.recordFeedback(ctx, {
    ...f.input('negative'),
    code: 'counterexample',
    policyRef: p,
  })!
  assert.equal(negative.candidate?.status, 'suspended')
  assert.equal(f.service.hints(ctx, { personId: 'person' }).length, 0)
  assert.equal(f.service.snapshotValid(ctx, p), false)
  f.db.close()
})
test('withdrawal physically removes private refs and receipt, prevents source replay and stale exposure', () => {
  const f = fixture()
  f.enable()
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  f.service.revokeSource('scene', 'source')
  assert.equal(f.service.snapshotValid(ctx, p), false)
  assert.equal(f.service.listOutcomes(ctx).length, 0)
  assert.equal(f.service.list(ctx).items.length, 0)
  assert.equal(
    (f.db.prepare('SELECT count(*) AS n FROM memory_experience_validations').get() as { n: number })
      .n,
    0,
  )
  assert.throws(() => f.service.recordFeedback(ctx, f.input()), /source_revoked/)
  const tombs = f.db.prepare('SELECT * FROM memory_experience_tombstones').all()
  assert.ok(!JSON.stringify(tombs).includes('source'))
  f.db.close()
})
test('policy proposal requires exact exposed instruction and cannot invent action or cross scope', () => {
  const f = fixture()
  f.enable()
  const out = f.service.recordFeedback(ctx, f.input())!.outcome
  const proposal = {
    action: 'conclusion_before_detail' as const,
    condition: { kind: 'response_style' as const, personId: 'person' },
    outcomeRefs: [{ id: out.id, version: out.version }],
  }
  assert.throws(() => f.service.proposePolicy(ctx, proposal, []), /not_exposed/)
  assert.equal(f.service.proposePolicy(ctx, proposal, proposal.outcomeRefs).duplicate, true)
  assert.throws(
    () =>
      f.service.proposePolicy(
        ctx,
        { ...proposal, condition: { kind: 'response_style', personId: 'other' } },
        proposal.outcomeRefs,
      ),
    /instruction_mismatch/,
  )
  assert.throws(
    () =>
      f.service.proposePolicy(
        { sceneId: 'foreign', simulation: false },
        proposal,
        proposal.outcomeRefs,
      ),
    /learning_disabled/,
  )
  f.db.close()
})
test('failed holdout receipt cannot activate; transaction rollback leaves no journal', () => {
  const f = fixture()
  f.enable()
  f.ports.validate = (candidate) => ({
    ...validateExperiencePolicy(candidate),
    heldoutRoots: candidate.generationRoots,
  })
  assert.throws(() => f.service.recordFeedback(ctx, f.input()), /validation_failed/)
  assert.equal(f.service.listOutcomes(ctx).length, 0)
  f.db.close()
})

test('explicit preference withdrawal needs matching exposed target and same person, not an applied quality claim', () => {
  const f = fixture()
  f.enable()
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  assert.equal(p.activationBasis, 'explicit_preference')
  f.service.recordUse(ctx, {
    turnId: 'turn',
    policyRef: p,
    kind: 'exposed',
    targetMessageId: 'bot-response',
  })
  assert.equal(f.service.targetUses(ctx, 'bot-response').length, 1)
  assert.throws(
    () =>
      f.service.recordFeedback(ctx, {
        ...f.input('withdraw-other'),
        source: { messageId: 'withdraw-other', senderId: 'other' },
        code: 'withdraw_instruction',
        policyRef: p,
      }),
    /person_mismatch/,
  )
  const result = f.service.recordFeedback(ctx, {
    ...f.input('withdraw'),
    code: 'withdraw_instruction',
    policyRef: p,
  })!
  assert.equal(result.candidate?.reason, 'explicit_instruction_withdrawn')
  assert.equal(result.candidate?.status, 'suspended')
  assert.equal(
    f.service.recordFeedback(ctx, {
      ...f.input('withdraw'),
      code: 'withdraw_instruction',
      policyRef: p,
    })?.duplicate,
    true,
  )
  assert.equal(f.service.list(ctx).items[0]?.version, result.candidate?.version)
  assert.equal(f.service.adminList(ctx).items[0]?.uses.applied, 0)
  f.db.close()
})

test('old delivered policy version cannot suspend a newly revalidated active version', () => {
  const f = fixture()
  f.enable()
  const original = f.service.recordFeedback(ctx, f.input())!.candidate!
  f.service.recordUse(ctx, {
    turnId: 'original-turn',
    policyRef: original,
    kind: 'exposed',
    targetMessageId: 'bot-response',
  })
  f.service.recordUse(ctx, {
    turnId: 'original-turn',
    policyRef: original,
    kind: 'applied',
    operation: 'conclusion_first_response',
    targetMessageId: 'bot-response',
  })
  const suspended = f.service.review(ctx, original.id, original.version, 'suspended')
  const validated = f.service.validate(ctx, suspended.id, suspended.version)
  const active = f.service.review(ctx, validated.id, validated.version, 'active')
  assert.equal(active.activationBasis, 'administrator_review')
  for (const code of ['withdraw_instruction', 'counterexample'] as const) {
    assert.throws(
      () => f.service.recordFeedback(ctx, { ...f.input(code), code, policyRef: original }),
      /version_conflict/,
    )
    assert.equal(f.service.list(ctx).items[0]?.version, active.version)
    assert.equal(f.service.list(ctx).items[0]?.status, 'active')
  }
  assert.equal(f.service.listOutcomes(ctx).length, 1)
  assert.equal(f.service.snapshotValid(ctx, active), true)
  f.db.close()
})

test('exposure counters count a turn once while retaining each exact delivered target', () => {
  const f = fixture()
  f.enable()
  const p = f.service.recordFeedback(ctx, f.input())!.candidate!
  for (const targetMessageId of [undefined, 'segment-one', 'segment-two'])
    f.service.recordUse(ctx, {
      turnId: 'turn-one',
      policyRef: p,
      kind: 'exposed',
      ...(targetMessageId ? { targetMessageId } : {}),
    })
  assert.equal(f.service.adminList(ctx).items[0]?.uses.exposed, 1)
  assert.equal(f.service.targetUses(ctx, 'segment-one').length, 1)
  assert.equal(f.service.targetUses(ctx, 'segment-two').length, 1)
  f.service.recordUse(ctx, { turnId: 'turn-two', policyRef: p, kind: 'exposed' })
  assert.equal(f.service.adminList(ctx).items[0]?.uses.exposed, 2)
  f.db.close()
})

test('style withdrawal cannot suspend person or plan policies but verified counterexamples remain applicable', () => {
  for (const code of ['request_exact_person_lookup', 'request_verify_plan_qualifiers'] as const) {
    const f = fixture()
    f.enable()
    const candidate = f.service.recordFeedback(ctx, { ...f.input(), code })!.candidate!
    const validated = f.service.validate(ctx, candidate.id, candidate.version)
    const active = f.service.review(ctx, validated.id, validated.version, 'active')
    f.service.recordUse(ctx, {
      turnId: 'turn',
      policyRef: active,
      kind: 'exposed',
      targetMessageId: 'bot-response',
    })
    assert.throws(
      () =>
        f.service.recordFeedback(ctx, {
          ...f.input('style-withdraw'),
          code: 'withdraw_instruction',
          policyRef: active,
        }),
      /instruction_mismatch/,
    )
    assert.equal(f.service.list(ctx).items[0]?.status, 'active')
    assert.equal(f.service.list(ctx).items[0]?.version, active.version)
    assert.equal(f.service.listOutcomes(ctx).length, 1)
    f.service.recordUse(ctx, {
      turnId: 'turn',
      policyRef: active,
      kind: 'applied',
      operation:
        code === 'request_exact_person_lookup' ? 'memory_person' : 'plan_qualifiers_checked',
      targetMessageId: 'bot-response',
    })
    const result = f.service.recordFeedback(ctx, {
      ...f.input('actual-counterexample'),
      code: 'counterexample',
      policyRef: active,
    })!
    assert.equal(result.candidate?.status, 'suspended')
    assert.equal(result.candidate?.reason, 'explicit_counterexample')
    f.db.close()
  }
})
