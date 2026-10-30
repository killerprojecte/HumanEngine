import assert from 'node:assert/strict'
import test from 'node:test'
import { embeddingIndexActions } from '../app/utils/embedding-index-actions.ts'
import type { components } from '../app/types/api.d.ts'

type Status = components['schemas']['EmbeddingIndexStatus']
const status = (state: Status['state'] = 'needs_rebuild'): Status => ({
  state,
  ...(state === 'unconfigured'
    ? {}
    : { selected: { modelId: 'synthetic-encoder', encoderRevision: 'revision-current' } }),
  coverage: { eligible: 10, indexed: 0, pending: 10 },
  retained: { generations: 0, vectors: 0 },
})
const job = (state: NonNullable<Status['job']>['state']): NonNullable<Status['job']> => ({
  id: 'synthetic-job',
  encoderRevision: 'revision-current',
  modelId: 'synthetic-encoder',
  state,
  processed: 3,
  total: 10,
  requiresRetryConfirmation: false,
})

test('only explicit needs_rebuild exposes a new rebuild action', () => {
  assert.deepEqual(embeddingIndexActions(), [])
  assert.deepEqual(embeddingIndexActions(status('unconfigured')), [])
  assert.deepEqual(embeddingIndexActions(status('ready')), [])
  assert.deepEqual(embeddingIndexActions(status()), ['start'])
})

test('pending coverage and historical errors do not turn ready into needs_rebuild', () => {
  for (const state of ['cancelled', 'completed'] as const) {
    assert.deepEqual(
      embeddingIndexActions({ ...status('ready'), job: { ...job(state), error: 'old-error' } }),
      [],
    )
  }
})

test('running jobs expose controls without a duplicate start', () => {
  for (const state of ['needs_rebuild', 'ready', 'unconfigured'] as const) {
    assert.deepEqual(embeddingIndexActions({ ...status(state), job: job('running') }), [
      'pause',
      'cancel',
    ])
  }
})

test('paused and failed jobs retain resume and cancellation alongside deliberate restart', () => {
  for (const state of ['paused', 'failed'] as const) {
    assert.deepEqual(embeddingIndexActions({ ...status(), job: job(state) }), [
      'resume',
      'start',
      'cancel',
    ])
    assert.deepEqual(embeddingIndexActions({ ...status('ready'), job: job(state) }), [
      'resume',
      'cancel',
    ])
  }
})

test('an obsolete encoder cannot resume, but its old job can still be cancelled', () => {
  const obsolete = { ...job('paused'), encoderRevision: 'revision-old' }
  assert.deepEqual(embeddingIndexActions({ ...status(), job: obsolete }), ['start', 'cancel'])
  assert.deepEqual(embeddingIndexActions({ ...status('unconfigured'), job: obsolete }), ['cancel'])
})

test('unknown-request confirmation does not silently replace the existing resumable job', () => {
  assert.deepEqual(
    embeddingIndexActions({
      ...status(),
      job: { ...job('failed'), requiresRetryConfirmation: true },
    }),
    ['resume', 'start', 'cancel'],
  )
})
