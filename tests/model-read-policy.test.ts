import assert from 'node:assert/strict'
import test from 'node:test'
import {
  ModelReadPolicy,
  ModelReadDenied,
  type ModelReadScene,
} from '../src/memory/read/model-read-policy.ts'
const context = (id = 's') => ({
  scene: { id },
  mode: 'real' as const,
  signal: new AbortController().signal,
  dependencies: new Set<string>(),
  validations: [] as Array<() => boolean>,
})
test('legacy defaults read independently from learning; missing/simulation denied', () => {
  const scenes = new Map<string, ModelReadScene>([['s', { id: 's' }]])
  const p = new ModelReadPolicy({ scene: (id) => scenes.get(id) })
  assert.equal(p.allowed('s'), true)
  assert.equal(p.personalizationAllowed('s'), true)
  assert.equal(p.allowed('missing'), false)
  assert.throws(() => p.begin({ ...context(), mode: 'simulation' }), ModelReadDenied)
  const c = context()
  const ticket = p.begin(c)
  assert.equal(p.used(c), true)
  p.begin(c)
  p.begin(c)
  assert.equal(
    c.validations.length,
    1,
    'one policy ticket validator per shared turn validation array',
  )
  assert.equal(c.validations[0]!(), true)
  assert.equal(p.valid({ ...ticket }), false, 'copying fields is not a new server ticket')
})
test('OFF then ON never renews old read/prefetch or context clone tickets', async () => {
  let scene: ModelReadScene = { id: 's', memoryReadRevision: 0 }
  const p = new ModelReadPolicy({ scene: () => scene }),
    c = context(),
    ticket = p.begin(c)
  scene = { ...scene, memoryReadEnabled: false, memoryReadRevision: 1 }
  await Promise.resolve()
  assert.equal(p.valid(ticket), false)
  assert.equal(c.validations[0]!(), false)
  assert.throws(() => p.assert(ticket), /memory_read_revoked/)
  scene = { ...scene, memoryReadEnabled: true, memoryReadRevision: 2 }
  assert.throws(() => p.begin({ ...c, validations: [] }), /memory_read_revoked/)
  assert.equal(p.validContext(c), false)
  assert.equal(p.valid(p.begin(context())), true)
})
test('personalization is narrower than read and revokes old generic tickets on change', () => {
  let scene: ModelReadScene = {
    id: 's',
    memoryReadEnabled: true,
    personalizationEnabled: false,
    memoryReadRevision: 4,
  }
  const p = new ModelReadPolicy({ scene: () => scene })
  assert.equal(p.allowed('s'), true)
  assert.equal(p.personalizationAllowed('s'), false)
  const c = context(),
    ticket = p.begin(c)
  assert.equal(ticket.personalizationEnabled, false)
  const clone = { ...c, validations: [] as Array<() => boolean> }
  assert.equal(p.begin(clone), ticket)
  assert.equal(clone.validations.length, 1)
  scene = { ...scene, personalizationEnabled: true, memoryReadRevision: 5 }
  assert.equal(clone.validations[0]!(), false)
  assert.throws(() => p.begin({ ...c, scene: { id: 'foreign' } }), /revoked/)
})
test('clean read-OFF context has no exposure and corrupt revisions fail closed', () => {
  let scene: ModelReadScene = { id: 's', memoryReadEnabled: false, memoryReadRevision: 2 }
  const p = new ModelReadPolicy({ scene: () => scene }),
    c = context()
  assert.equal(p.capture('s'), undefined)
  assert.throws(() => p.begin(c), /disabled/)
  assert.equal(p.used(c), false)
  assert.equal(p.validContext(c), true)
  scene = { ...scene, memoryReadRevision: NaN }
  assert.throws(() => p.capture('s'), /revoked/)
})

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const) {
  test(`malformed persisted ${key} never becomes permission`, () => {
    let scene: ModelReadScene = { id: 's' }
    const policy = new ModelReadPolicy({ scene: () => scene })
    const ticket = policy.require('s')
    for (const value of ['false', null, {}, 0, 1]) {
      scene = { id: 's', [key]: value } as ModelReadScene
      assert.throws(() => policy.require('s'), /memory_read_revoked/)
      assert.equal(policy.valid(ticket), false)
    }
  })
}
