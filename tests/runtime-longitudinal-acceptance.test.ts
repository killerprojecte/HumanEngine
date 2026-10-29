import assert from 'node:assert/strict'
import test from 'node:test'
import {
  runRuntimeLongitudinal,
  longitudinalStageIds,
} from '../scripts/acceptance/runtime-longitudinal.ts'

test('actual Engine longitudinal ablations change inputs and flags, preserve qualifications and privacy through restart, and keep semantic gold pending', async (t) => {
  let network = 0
  t.mock.method(globalThis, 'fetch', async () => {
    network++
    throw Error('Unexpected external network')
  })
  const report = await runRuntimeLongitudinal({ runId: 'synthetic-test-only' })
  assert.equal(
    report.status,
    'passed',
    JSON.stringify(report.variants.map((v) => ({ id: v.id, failures: v.mechanicalFailures }))),
  )
  assert.equal(report.semanticGold, 'pending_independent_review')
  assert.equal(network, 0)
  assert.equal(report.sourceHash.length, 64)
  assert.equal(report.fixtureHash.length, 64)
  assert.equal(report.runnerHash.length, 64)
  assert.deepEqual(
    report.variants.map((v) => v.id),
    ['no-memory', 'ordinary', 'qualified-feature'],
  )
  assert.equal(new Set(report.variants.map((v) => JSON.stringify(v.configuration))).size, 3)
  for (const variant of report.variants) {
    assert.deepEqual(
      variant.stages.map((stage) => stage.id),
      [...longitudinalStageIds],
    )
    assert.ok(
      variant.stages.every(
        (stage) => stage.outcomes.length > 0 && stage.outcomes.every((check) => check.passed),
      ),
    )
    assert.equal(variant.callsByRole.sends, 0)
    assert.ok(variant.callsByRole.replyer > 0)
    assert.equal(variant.clock.receivedAtPolicy, 'actual_runtime_wall_clock')
  }
  assert.equal(report.variants[0]!.callsByRole.learning, 0)
  assert.ok(report.variants[1]!.callsByRole.learning > 0)
  assert.ok(report.variants[2]!.callsByRole.planner > report.variants[0]!.callsByRole.planner)
})

test('longitudinal acceptance rejects provider or credential injection before execution', async () => {
  await assert.rejects(
    () => runRuntimeLongitudinal({ provider: 'not-authorized' } as any),
    /longitudinal_invalid_options/,
  )
  await assert.rejects(
    () => runRuntimeLongitudinal({ runId: '../unsafe' }),
    /longitudinal_invalid_options/,
  )
})
