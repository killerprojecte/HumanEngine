import assert from 'node:assert/strict'
import test from 'node:test'
import { toolOutcome } from '../src/diagnostics/runtime-results.ts'

test('rejected learning is not reported as successfully completed or copies private reasons', () => {
  const outcome = toolOutcome({ status: 'rejected', reason: 'PRIVATE_LEARNING_REASON' })
  assert.equal(outcome.status, 'failed')
  assert.equal(outcome.details.reason, 'tool_rejected')
  assert.equal(JSON.stringify(outcome).includes('PRIVATE_LEARNING_REASON'), false)
})

for (const status of ['queued', 'duplicate'])
  test(`${status} records its receipt state rather than implying a saved memory`, () => {
    const outcome = toolOutcome({ status, jobId: 'PRIVATE_JOB' })
    assert.equal(outcome.details.receiptState, status)
    assert.equal(JSON.stringify(outcome).includes('PRIVATE_JOB'), false)
  })
