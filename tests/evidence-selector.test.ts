import assert from 'node:assert/strict'
import test from 'node:test'
import {
  selectEvidenceGroups,
  type EvidenceGroup,
  type JsonValue,
} from '../src/runtime/evidence-selector.ts'
const bytes = (s: string) => new TextEncoder().encode(s).length
const serialize = (values: readonly JsonValue[]) =>
  JSON.stringify({
    now: '2026-10-28T00:00:00Z',
    evidence: values,
    constraints: ['不可猜测'],
  })
const group = (id: string, value: JsonValue, rank = 1): EvidenceGroup<JsonValue> => ({
  id,
  value,
  rank,
  complete: true,
})
const run = (candidates: EvidenceGroup<JsonValue>[], limit = 10000) =>
  selectEvidenceGroups({
    candidates,
    budget: { mode: 'bytes', limit },
    serialize,
  })
test('Chinese tail negation, time, conflict and AND provenance stay whole or are omitted whole', () => {
  const value = {
    text: '中文'.repeat(100) + '但并未确认，不是永久偏好',
    sources: ['s1', 's2'],
    validUntil: '2026-12-01',
    conflict: { other: '相反声明', status: 'unresolved' },
  }
  const g = group('a', value)
  const exact = bytes(serialize([value]))
  const fit = run([g], exact)
  assert.equal(fit.status, 'selected')
  assert.deepEqual(fit.groups[0]!.value, value)
  assert.equal(fit.usage?.count, exact)
  const omit = run([g], exact - 1)
  assert.equal(omit.status, 'insufficient')
  assert.equal(omit.groups.length, 0)
  assert.equal(omit.serialized, undefined)
  assert.deepEqual(omit.omitted, [{ id: 'a', reason: 'budget' }])
})
test('UTF8 counts final JSON escaping, emoji and full envelope overhead exactly', () => {
  const value = { text: '😀\n"\\中文' }
  const result = run([group('a', value)], bytes(serialize([value])))
  assert.equal(result.usage?.count, bytes(result.serialized!))
  assert.equal(result.usage?.unit, 'bytes')
  assert.ok(result.usage!.count > JSON.stringify(value).length)
  assert.equal(run([], bytes(serialize([])) - 1).reason, 'envelope_budget')
})
test('mandatory groups cannot be silently dropped for completeness, budget or count', () => {
  assert.equal(
    run([{ ...group('m', 'large'), mandatory: true }], bytes(serialize([]))).reason,
    'mandatory_budget',
  )
  assert.equal(
    run([{ ...group('m', 'x'), mandatory: true, complete: false }]).reason,
    'mandatory_incomplete',
  )
  assert.equal(
    run(
      Array.from({ length: 13 }, (_, i) => ({
        ...group(String(i), 'x'),
        mandatory: true,
      })),
    ).reason,
    'mandatory_group_limit',
  )
  assert.equal(
    run(Array.from({ length: 41 }, (_, i) => group(String(i), 'x'))).reason,
    'candidate_limit',
  )
  assert.equal(
    run([{ ...group('x', 'unqualified'), complete: false }]).reason,
    'no_complete_evidence',
  )
})
test('deterministic rank and Planner selections preserve input and freeze cloned outputs', () => {
  const input = [group('b', { text: 'b' }), group('a', { text: 'a' })]
  const before = JSON.stringify(input)
  const a = run(input),
    b = run([...input].reverse())
  assert.equal(a.serialized, b.serialized)
  assert.equal(JSON.stringify(input), before)
  assert.notEqual(a.groups[0]!.value, input[1]!.value)
  assert.equal(Reflect.set(a.groups[0]!.value as object, 'text', 'changed'), false)
  assert.deepEqual(a.groups[0]!.value, { text: 'a' })
  const selected = selectEvidenceGroups({
    candidates: input,
    selectedIds: ['b'],
    budget: { mode: 'bytes', limit: 1000 },
    serialize,
  })
  assert.deepEqual(
    selected.groups.map((g) => g.id),
    ['b'],
  )
  assert.equal(selected.omitted[0]!.reason, 'not_selected')
  assert.equal(
    selectEvidenceGroups({
      candidates: input,
      selectedIds: ['unknown'],
      budget: { mode: 'bytes', limit: 1000 },
      serialize,
    }).status,
    'invalid',
  )
})
test('injected tokenizer records exact identity and rejects invalid counts, errors and instability', () => {
  const base = { candidates: [group('a', 'x')], serialize }
  const token = (count: (s: string) => number) =>
    selectEvidenceGroups({
      ...base,
      budget: {
        mode: 'tokens',
        limit: 1000,
        tokenizer: {
          verified: true,
          version: 'fixture-v1',
          modelIdentity: 'synthetic-model',
          count,
        },
      },
    })
  const result = token((s) => bytes(s))
  assert.equal(result.usage?.unit, 'tokens')
  assert.equal(result.usage?.modelIdentity, 'synthetic-model')
  for (const n of [NaN, -1, Infinity, 1.5, 0]) assert.equal(token(() => n).status, 'invalid')
  assert.equal(
    token(() => {
      throw new Error('private body must not escape')
    }).reason,
    'invalid_input_or_measurement',
  )
  let n = 1
  assert.equal(token(() => n++).status, 'invalid')
  let changes = 0
  assert.equal(
    selectEvidenceGroups({
      ...base,
      serialize: () => JSON.stringify({ n: changes++ }),
      budget: { mode: 'bytes', limit: 1000 },
    }).reason,
    'unstable_measurement',
  )
  assert.equal(
    selectEvidenceGroups({ ...base, budget: { mode: 'bytes', limit: NaN } }).status,
    'invalid',
  )
})
test('output <=12, complete envelopes always within actual byte budget without truncation', () => {
  for (let limit = bytes(serialize([])); limit < 1000; limit += 17) {
    const result = run(
      Array.from({ length: 30 }, (_, i) =>
        group(String(i), { text: '中文😀'.repeat(i + 1), condition: 'not permanent' }, 30 - i),
      ),
      limit,
    )
    if (result.status === 'selected') {
      assert.ok(bytes(result.serialized!) <= limit)
      assert.ok(result.groups.length <= 12)
      assert.equal(result.usage?.count, bytes(result.serialized!))
    }
  }
})

test('contradictory mandatory selection and duplicate IDs fail closed', () => {
  const candidate = { ...group('a', 'trusted complete group'), mandatory: true }
  assert.equal(
    selectEvidenceGroups({
      candidates: [candidate],
      selectedIds: [],
      budget: { mode: 'bytes', limit: 1000 },
      serialize,
    }).reason,
    'mandatory_not_selected',
  )
  assert.equal(run([candidate, candidate]).status, 'invalid')
})
