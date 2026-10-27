import assert from 'node:assert/strict'
import test from 'node:test'
import { ModelError } from '../src/models.ts'
import type { MemoryRecord } from '../src/memory.ts'
import { explicitDurableLearningSignal } from '../src/learning-policy.ts'
import {
  call,
  completed,
  epoch,
  future,
  harness,
  identifier,
  message,
  otherSceneId,
  proposal,
  sceneId,
  subjectId,
} from './helpers/learning-technical-fixture.ts'

test('metadata reconciliation finds an unindexed encoding setting beside an indexed window setting', async () => {
  const h = harness()
  try {
    const encoding = h.seed(
      'Device output encoding is hexadecimal.',
      message(101, 'Encoding specification.'),
      { personId: subjectId },
    )
    const window = h.seed(
      'Receive window contains sixteen frames.',
      message(102, 'Window specification.'),
      { personId: subjectId },
    )
    const locked = h.seed(
      'Comparator policy is immutable.',
      message(103, 'Comparator specification.'),
      { personId: subjectId, occurredAt: epoch, validUntil: future },
    )
    h.engine.memory.lock(locked.id, true)
    const manual = h.engine.memory.create({
      scope: { kind: 'scene', sceneId, personId: subjectId },
      kind: 'fact',
      content: 'Operator-managed parity configuration.',
    })
    const excluded = [
      h.seed('Isolated device policy.', message(104, 'Isolation check.', subjectId, otherSceneId), {
        personId: subjectId,
      }),
      h.seed('Different endpoint policy.', message(105, 'Endpoint check.', identifier(3)), {
        personId: identifier(3),
      }),
      h.seed('Quarantined parser policy.', message(106, 'Parser check.'), { personId: subjectId }),
      h.seed('Retracted framing policy.', message(107, 'Framing check.'), { personId: subjectId }),
    ]
    h.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...excluded[2], lineageStatus: 'quarantined' }), excluded[2]!.id)
    h.engine.memory.recall(sceneId, identifier(107))
    const spaceId = h.engine.memory.registerSpace({
      providerId: identifier(20),
      modelId: identifier(21),
      apiModelName: identifier(22),
      deployment: identifier(23),
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    })
    h.engine.memory.index(window.id, spaceId, [1, 0])
    h.access.searchMemory = async (ctx, query) =>
      h.engine.memory.search(ctx, query, undefined, { spaceId, vector: [1, 0] })
    const inputs = [
      message(201, '正式更正：编码输出改成二进制表示。'),
      message(202, '接收窗口现在改成八个数据帧。'),
    ]
    const visible = await h.access.searchMemory(
      { sceneId, simulation: false },
      inputs.map((item) => (item.parts[0]!.type === 'text' ? item.parts[0]!.text : '')).join(' '),
    )
    assert.ok(visible.some((item) => item.id === window.id))
    assert.ok(!visible.some((item) => item.id === encoding.id))
    const updates = [
      proposal('Device output encoding is binary.', inputs[0]!, true),
      proposal('Receive window contains eight frames.', inputs[1]!, true),
    ]
    for (const update of updates) update.arguments.occurredAt = epoch
    let compared = 0
    h.install(async (_role, prompts, tools) => {
      if (tools?.some((item) => item.name === 'learning.reconcile')) {
        compared++
        const payload = JSON.parse(prompts[1]!.content)
        const targets = payload.relatedMemories as Array<MemoryRecord & { canRevise: boolean }>
        for (const record of [encoding, window, locked, manual])
          assert.ok(targets.some((item) => item.id === record.id))
        assert.ok(targets.every((item) => !excluded.some((record) => record.id === item.id)))
        assert.equal(targets.find((item) => item.id === manual.id)!.origin, 'manual')
        for (const record of [locked, manual])
          assert.equal(targets.find((item) => item.id === record.id)!.canRevise, false)
        const temporal = targets.find((item) => item.id === locked.id)!
        assert.equal(temporal.occurredAt, epoch)
        assert.equal(temporal.validUntil, future)
        assert.equal(temporal.learnedAt, locked.learnedAt)
        assert.equal(typeof payload.now, 'string')
        assert.equal(payload.timezone, h.store.settings.timezone)
        assert.deepEqual(payload.targetLimits, {
          perCandidate: 4,
          perGroup: 8,
          total: 16,
          semantic: 8,
        })
        return completed([
          call(
            'learning.reconcile',
            { candidateIndex: 0, targetId: locked.id, action: 'supersede' },
            801,
          ),
          call(
            'learning.reconcile',
            { candidateIndex: 0, targetId: encoding.id, action: 'supersede' },
            802,
          ),
          call(
            'learning.reconcile',
            { candidateIndex: 1, targetId: window.id, action: 'supersede' },
            803,
          ),
        ])
      }
      assert.equal(JSON.stringify(prompts).includes(encoding.content), false)
      return completed(updates)
    })
    await h.run(inputs)
    assert.equal(compared, 1)
    assert.equal(h.engine.learningStatus().failed, 0)
    const records = h.engine.memory.adminList()
    for (const record of [encoding, window])
      assert.equal(records.find((item) => item.id === record.id)!.status, 'superseded')
    for (const update of updates) {
      const saved = records.find((item) => item.content === update.arguments.content)!
      assert.ok(saved)
      assert.equal(saved.status, 'active')
      assert.equal(saved.occurredAt, update.arguments.occurredAt)
    }
    assert.equal(records.find((item) => item.id === locked.id)!.status, 'active')
  } finally {
    await h.close()
  }
})

test('metadata selection enforces trusted authors, scene-level privacy and declared caps', async () => {
  const h = harness()
  try {
    for (let i = 0; i < 12; i++)
      h.seed(`Endpoint register ${i} is allocated.`, message(300 + i, 'Register allocation.'), {
        personId: subjectId,
      })
    const shared = h.seed(
      'Transport supports framed messages.',
      message(320, 'Transport contract.'),
    )
    const evidence = {
      sceneId,
      messageId: identifier(321),
      senderId: subjectId,
      origin: 'live' as const,
      available: true,
      isBot: false,
    }
    const ctx = { sceneId, simulation: false, sourceEvidence: [evidence] }
    const candidate = {
      candidateIndex: 0,
      kind: 'claim' as const,
      content: 'Endpoint register layout changed.',
      personId: subjectId,
      sourceMessageIds: [evidence.messageId],
    }
    const lookup = h.engine.memory.reconciliationTargets(ctx, [candidate], [])
    assert.equal(lookup.coverage, 'capped')
    assert.equal(lookup.targets.length, 4)
    assert.ok(
      lookup.targets.every(
        ({ memory }) => memory.scope.kind === 'scene' && memory.scope.personId === subjectId,
      ),
    )
    assert.equal(
      h.engine.memory.reconciliationTargets(ctx, [{ ...candidate, personId: identifier(4) }], [])
        .targets.length,
      0,
    )
    assert.equal(
      h.engine.memory.reconciliationTargets(
        { ...ctx, sourceEvidence: [{ ...evidence, origin: 'history' }] },
        [candidate],
        [],
      ).targets.length,
      0,
    )
    assert.deepEqual(
      h.engine.memory
        .reconciliationTargets(ctx, [{ ...candidate, personId: undefined }], [])
        .targets.map(({ memory }) => memory.id),
      [shared.id],
    )
    h.store.recall(sceneId, evidence.messageId)
    assert.equal(h.engine.memory.reconciliationTargets(ctx, [candidate], []).targets.length, 0)
  } finally {
    await h.close()
  }
})

for (const mode of ['accept', 'empty', 'pronoun', 'wrong-source', 'temporary-error'] as const) {
  test(`empty extraction review uses one independent technical confirmation: ${mode}`, async () => {
    const h = harness()
    try {
      const fact = 'The frame checksum algorithm is CRC32C.'
      const first = message(401, 'Frame checksum specification.', identifier(3))
      const existing = h.seed(fact, first)
      const confirmation = message(
        402,
        'I independently confirm that the frame checksum algorithm is CRC32C.',
        identifier(4),
      )
      const incidental = message(403, 'The processing queue is idle.', identifier(5))
      let extraction = 0,
        review = 0,
        repair = 0
      h.install(async (_role, prompts, tools) => {
        if (tools?.some((item) => item.name === 'learning.repair')) {
          repair++
          return completed()
        }
        if (tools?.some((item) => item.name === 'learning.review_empty')) {
          review++
          const payload = JSON.parse(prompts[1]!.content)
          assert.deepEqual(payload.messages, [confirmation])
          assert.equal(Object.hasOwn(payload, 'relatedMemories'), false)
          assert.equal(h.engine.memory.adminList().length, 1)
          if (mode === 'temporary-error') throw new ModelError('fixture_unavailable', 503)
          if (mode === 'empty') return completed()
          return completed([
            call('learning.review_empty', {
              kind: 'fact',
              content:
                mode === 'pronoun' ? 'She verified the CRC32C frame checksum setting.' : fact,
              sourceMessageIds: [mode === 'wrong-source' ? first.id : confirmation.id],
            }),
          ])
        }
        if (tools?.some((item) => item.name === 'learning.reconcile')) return completed()
        extraction++
        return completed()
      })
      await h.run([incidental, confirmation])
      assert.equal(extraction, 1)
      assert.equal(review, 1)
      assert.equal(repair, 0)
      if (mode === 'temporary-error') assert.equal(h.engine.learningStatus().retried, 1)
      h.engine.memory.recall(sceneId, first.id)
      const records = h.engine.memory.list({ sceneId, simulation: false })
      assert.equal(records.length, mode === 'accept' ? 1 : 0)
      if (mode === 'accept') {
        assert.equal(records[0]!.id, existing.id)
        assert.deepEqual(
          records[0]!.sources.map((item) => item.messageId),
          [confirmation.id],
        )
      }
      assert.equal(h.engine.memory.list({ sceneId: otherSceneId, simulation: false }).length, 0)
    } finally {
      await h.close()
    }
  })
}

test('incidental diagnostics and quoted or conditional confirmations do not force extraction', async () => {
  for (const text of [
    'The scheduler is waiting.',
    'The console has no pending tasks.',
    'If I independently confirm the packet layout, then a review may begin.',
    'The log quotes "I independently confirm the packet layout is fixed."',
  ])
    assert.equal(explicitDurableLearningSignal(text), false)
  assert.equal(
    explicitDurableLearningSignal(
      'I independently confirm the packet layout uses fixed-width fields.',
    ),
    true,
  )
  const h = harness()
  try {
    let calls = 0
    h.install(async () => {
      calls++
      return completed()
    })
    await h.run([message(501, 'The scheduler is waiting.')])
    assert.equal(calls, 1)
    assert.equal(h.engine.memory.adminList().length, 0)
  } finally {
    await h.close()
  }
})
