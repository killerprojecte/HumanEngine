import test from 'node:test'
import assert from 'node:assert/strict'
import { KnowledgeExposure } from '../src/memory/read/knowledge-exposure.ts'
import { SemanticExposure } from '../src/memory/read/semantic-exposure.ts'
import type { KnowledgeQuery, KnowledgeQueryResult } from '../src/memory/knowledge/contracts.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'

const context = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
function fixture(kind: 'knowledge' | 'semantic') {
  let calls = 0,
    allowed = true,
    revision = 1
  let hook: (() => void) | undefined
  const coverage = new Map<string, 'complete' | 'limited'>()
  const knowledge = new KnowledgeExposure({
    allowed: () => allowed,
    policyIdentity: () => revision,
    query: (_ctx, input): KnowledgeQueryResult => {
      calls++
      const key = input.anchor.kind === 'person' ? input.anchor.personId : 'other'
      const result: KnowledgeQueryResult = {
        edges: [],
        paths: [],
        supportClosure: { edges: [], records: [] },
        coverage: coverage.get(key) ?? 'complete',
        interpretation: 'attributed_claims_not_transitive_truth',
      }
      hook?.()
      return result
    },
  })
  const semantic = new SemanticExposure({
    allowed: () => allowed,
    policyIdentity: () => revision,
    query: () => {
      calls++
      const result = { items: [], coverage: coverage.get('all') ?? 'complete' }
      hook?.()
      return result
    },
  })
  return {
    query(c: ModelMemoryContext, key = 'p') {
      const input: KnowledgeQuery = {
        anchor: { kind: 'person', personId: key },
        predicates: ['uses_tool'],
        depth: 1,
      }
      return kind === 'knowledge' ? knowledge.query(c, input) : semantic.query(c)
    },
    deliver(c: ModelMemoryContext, values: readonly unknown[]) {
      return kind === 'knowledge' ? knowledge.deliver(c, values) : semantic.deliver(c, values)
    },
    guard(c: ModelMemoryContext) {
      return kind === 'knowledge' ? knowledge.captureGuard(c) : semantic.captureGuard(c)
    },
    count() {
      return calls
    },
    reset() {
      calls = 0
    },
    revoke() {
      allowed = false
    },
    changePolicy() {
      revision++
    },
    change(key: string) {
      coverage.set(kind === 'knowledge' ? key : 'all', 'limited')
    },
    hook(value: (() => void) | undefined) {
      hook = value
    },
  }
}
for (const kind of ['knowledge', 'semantic'] as const) {
  test(`${kind}: twelve delivered receipts have linear captured guard and bounded turn checks`, () => {
    const f = fixture(kind),
      c = context()
    for (let n = 1; n <= 12; n++) {
      const values = f.query(c)
      assert.equal(f.deliver(c, values).length, 1)
      f.reset()
      assert.equal(f.guard(c)!(), true)
      assert.equal(f.count(), 2 * n - 1)
      f.reset()
      assert.equal(
        c.validations.every((g) => g()),
        true,
      )
      assert.equal(f.count(), n * n)
      f.reset()
      assert.equal(f.guard(c)!(), true)
      assert.equal(f.count(), 2 * n - 1)
    }
    f.revoke()
    assert.equal(f.guard(c)!(), false)
  })
  test(`${kind}: unselected tail and out-of-order first delivery do not drop authority`, () => {
    const f = fixture(kind),
      c = context()
    const a = f.query(c, 'a'),
      b = f.query(c, 'b')
    assert.equal(f.guard(c), undefined)
    assert.equal(f.deliver(c, b).length, 1)
    const original = f.guard(c)!
    assert.equal(f.deliver(c, a).length, 1)
    f.query(c, 'unselected')
    f.reset()
    assert.equal(f.guard(c)!(), true)
    assert.equal(f.count(), 3)
    f.reset()
    assert.equal(original(), true)
    assert.equal(f.count(), 1)
    assert.equal(f.deliver(c, b).length, 1)
    f.reset()
    assert.equal(f.guard(c)!(), true)
    assert.equal(f.count(), 3)
    f.change('b')
    assert.equal(f.guard(c)!(), false)
    assert.equal(f.deliver(c, f.query(c, 'fresh')).length, 0)
  })
  test(`${kind}: policy ABA and changed snapshots remain invalid after await`, async () => {
    const f = fixture(kind),
      c = context()
    f.deliver(c, f.query(c))
    const guard = f.guard(c)!
    await Promise.resolve()
    f.changePolicy()
    assert.equal(guard(), false)
    assert.equal(f.guard(c)!(), false)
    assert.equal(f.deliver(context(), f.query(c)).length, 0)
  })
  test(`${kind}: a later read callback changing an earlier snapshot fails closed`, () => {
    const f = fixture(kind),
      c = context()
    f.deliver(c, f.query(c, 'early'))
    f.deliver(c, f.query(c, 'late'))
    f.hook(() => {
      f.hook(undefined)
      f.change('early')
    })
    assert.equal(f.guard(c)!(), false)
  })
  test(`${kind}: an earlier read callback changing the newest snapshot fails closed`, () => {
    const f = fixture(kind),
      c = context()
    f.deliver(c, f.query(c, 'early'))
    f.deliver(c, f.query(c, 'late'))
    let reads = 0
    f.hook(() => {
      if (++reads === 2) {
        f.hook(undefined)
        f.change('late')
      }
    })
    assert.equal(f.guard(c)!(), false)
  })
  test(`${kind}: callback revocation is rechecked after the newest final read`, () => {
    const f = fixture(kind),
      c = context()
    f.deliver(c, f.query(c))
    let reads = 0
    f.hook(() => {
      if (++reads === 1) {
        f.hook(undefined)
        f.revoke()
      }
    })
    assert.equal(f.guard(c)!(), false)
  })
  test(`${kind}: nested delivery cannot replace the chain during qualification`, () => {
    const f = fixture(kind),
      c = context()
    f.deliver(c, f.query(c, 'first'))
    const outer = f.query(c, 'outer'),
      nested = f.query(c, 'nested')
    f.hook(() => {
      f.hook(undefined)
      assert.equal(f.deliver(c, nested).length, 0)
    })
    f.deliver(c, outer)
    assert.equal(
      c.validations.every((g) => g()),
      false,
    )
    f.change('first')
    assert.equal(f.guard(c)!(), false)
  })
}

test('backing anchor qualification survives delivery rebinding and callback changes', () => {
  let anchor = true,
    mutate = false
  const page = (): KnowledgeQueryResult => ({
    edges: [],
    paths: [],
    supportClosure: { edges: [], records: [] },
    coverage: 'complete',
    interpretation: 'attributed_claims_not_transitive_truth',
  })
  const e = new KnowledgeExposure({
    allowed: () => true,
    query: page,
    discover: () => {
      const result = page()
      if (mutate) anchor = false
      return result
    },
  })
  const c = context()
  const first = e.queryFromBacking(
    c,
    { id: 'a', version: 1, deletionEpoch: 0 },
    { predicates: ['uses_tool'], depth: 1 },
    () => anchor,
  )
  const sibling = e.query(c, {
    anchor: { kind: 'person', personId: 'p' },
    predicates: ['uses_tool'],
    depth: 1,
  })
  assert.equal(e.deliver(c, sibling).length, 1)
  assert.equal(e.deliver(c, first).length, 1)
  const guard = e.captureGuard(c)!
  assert.equal(guard(), true)
  mutate = true
  assert.equal(guard(), false)
  assert.equal(e.captureGuard(c)!(), false)
})
