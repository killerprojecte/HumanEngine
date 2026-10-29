import test from 'node:test'
import assert from 'node:assert/strict'
import { IdentityExposure } from '../src/memory/read/identity-exposure.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
import { validWorkspaceState } from '../src/runtime/workspace-state-evidence.ts'
const ctx = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
const endpoint = { adapterId: 'adapter', accountId: 'account', personId: 'person' }
function fixture() {
  const data = {
    snapshot: { revision: 1, sceneId: 's', anchor: endpoint, members: [endpoint] },
    aliases: [
      {
        endpoint,
        values: [
          {
            id: 'alias',
            scopeSceneId: 's',
            endpoint,
            label: 'Synthetic alias',
            source: { kind: 'manual' as const, administratorId: 'PRIVATE_ADMIN' },
            validFrom: null,
            validUntil: null,
          },
        ],
      },
    ],
  }
  const exposure = new IdentityExposure({ query: () => structuredClone(data), allowed: () => true })
  return { data, exposure }
}
test('identity state is whole-delivery authority, never grants copied or omitted receipts', () => {
  const { exposure } = fixture(),
    c = ctx(),
    prepared = exposure.query(c, 'person')
  assert.equal(exposure.captureGuard(c), undefined)
  assert.deepEqual(exposure.deliver(c, structuredClone(prepared)), [])
  assert.deepEqual(exposure.deliver(c, []), [])
  assert.equal(exposure.captureGuard(c), undefined)
  assert.equal(exposure.deliver(c, prepared).length, 1)
  assert.equal(exposure.captureGuard(c)!(), true)
  assert.equal(validWorkspaceState('memory.identities', prepared[0]), true)
  assert.equal(JSON.stringify(prepared).includes('PRIVATE_ADMIN'), false)
})
test('identity aliases and registry ABA invalidate issued state without assertion revision changes', () => {
  const { exposure, data } = fixture(),
    c = ctx()
  const prepared = exposure.query(c, 'person')
  exposure.deliver(c, prepared)
  data.aliases[0]!.values = []
  assert.equal(
    c.validations.every((fn) => fn()),
    false,
  )
  assert.equal(exposure.captureGuard(c)!(), false)
  assert.deepEqual(exposure.deliver(c, prepared), [])
  data.snapshot.revision = 3
  const fresh = ctx()
  assert.equal(exposure.deliver(fresh, exposure.query(fresh, 'person')).length, 1)
  assert.equal(
    fresh.validations.every((fn) => fn()),
    true,
  )
})
