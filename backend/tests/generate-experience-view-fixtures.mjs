/** Actual source projection and revalidation; synthetic already-authorized DTOs. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import {
  projectExperienceView,
  verifyExperienceView,
} from '../../src/memory/projections/experience-view.ts'
assert.equal(process.version, 'v24.22.1')
const clone = structuredClone
const ref = (id) => ({ id, version: 1, deletionEpoch: 0 })
const snapshot = (id = 'a', extra = {}) => ({
  ref: ref(id),
  qualificationKey: 'a'.repeat(64),
  record: {
    id,
    scope: { kind: 'scene', sceneId: 's' },
    kind: 'experience',
    content: `Synthetic ${id}`,
    learnedAt: '2000-01-01T00:00:00.000Z',
    sources: [],
    status: 'active',
    ...extra,
  },
})
const plan = (id = 'p', extra = {}) => ({
  ref: ref(id),
  sceneId: 's',
  mutationVersion: 1,
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  ...extra,
})
const input = (extra = {}) => ({
  sceneId: 's',
  simulation: false,
  assertions: [snapshot()],
  commitments: [plan()],
  upstreamCoverage: { assertions: 'complete', commitments: 'complete' },
  ...extra,
})
const projections = []
function project(name, v) {
  let result
  try {
    result = { value: projectExperienceView(v) }
  } catch (e) {
    result = { error: e.message }
  }
  projections.push({ name, input: v, ...result })
}
project('ordinary mixed order', input())
project('null limit default', input({ limit: null }))
project(
  'limited and capped',
  input({
    assertions: Array.from({ length: 14 }, (_, i) => snapshot(`a${i}`)),
    limit: 2,
    upstreamCoverage: { assertions: 'limited', commitments: 'complete' },
  }),
)
project(
  'extra upstream limited property',
  input({
    upstreamCoverage: { assertions: 'complete', commitments: 'complete', extra: 'limited' },
  }),
)
project(
  'forty candidates',
  input({ assertions: Array.from({ length: 39 }, (_, i) => snapshot(`a${i}`)) }),
)
project(
  'forty one invalid',
  input({ assertions: Array.from({ length: 40 }, (_, i) => snapshot(`a${i}`)) }),
)
project(
  'simulation skips item validation',
  input({
    simulation: true,
    assertions: [null],
    commitments: [null],
    upstreamCoverage: { assertions: 'limited', commitments: 'limited' },
  }),
)
project(
  'simulation still validates width',
  input({ simulation: true, assertions: Array(41).fill(null), commitments: [] }),
)
for (const value of [0, 13, 1.5, '2', false])
  project(`invalid limit ${JSON.stringify(value)}`, input({ limit: value }))
for (const [key, value] of [
  ['sceneId', ''],
  ['simulation', null],
  ['assertions', null],
  ['commitments', {}],
  ['upstreamCoverage', {}],
])
  project(`invalid ${key}`, input({ [key]: value }))
project('empty page', input({ assertions: [], commitments: [] }))
project(
  'view filters',
  input({
    assertions: [
      snapshot('shared', { scope: { kind: 'sharedPublicKnowledge' } }),
      snapshot('internal', { internalUse: 'working' }),
      snapshot('fact', { kind: 'fact' }),
      snapshot('inactive', { status: 'retracted' }),
      snapshot('disputed', {
        status: 'disputed',
        occurredAt: '1999-01-01',
        extra: { nested: [1, 2] },
      }),
    ],
  }),
)
project(
  'wrong scene',
  input({ assertions: [snapshot('a', { scope: { kind: 'scene', sceneId: 'other' } })] }),
)
project(
  'filter before wrong scene',
  input({
    assertions: [snapshot('a', { kind: 'fact', scope: { kind: 'scene', sceneId: 'other' } })],
  }),
)
project('duplicate assertion', input({ assertions: [snapshot(), snapshot()] }))
project(
  'duplicate commitment different mutation',
  input({ commitments: [plan(), plan('p', { mutationVersion: 2 })] }),
)
project('same id different kind', input({ commitments: [plan('a')] }))
for (const [key, value] of [
  ['version', 0],
  ['version', 1.5],
  ['deletionEpoch', -1],
  ['id', ''],
  ['version', 9007199254740992],
]) {
  let s = snapshot()
  s.ref[key] = value
  project(`invalid reference ${key} ${value}`, input({ assertions: [s] }))
}
for (const [key, value] of [
  ['qualificationKey', 'A'.repeat(64)],
  ['record', null],
  ['ref', null],
]) {
  let s = snapshot()
  s[key] = value
  project(`invalid snapshot ${key}`, input({ assertions: [s] }))
}
for (const [key, value] of [
  ['content', null],
  ['learnedAt', 4],
  ['sources', {}],
  ['scope', null],
  ['id', 'other'],
])
  project(`invalid record ${key}`, input({ assertions: [snapshot('a', { [key]: value })] }))
for (const [key, value] of [
  ['mutationVersion', 0],
  ['sceneId', 'other'],
  ['qualification', 'verified'],
  ['sourceBodyCoverage', 'complete'],
])
  project(`invalid plan ${key}`, input({ commitments: [plan('p', { [key]: value })] }))
for (const [name, key] of [
  ['singleton array', ['a'.repeat(64)]],
  ['nested array', [['a'.repeat(64)]]],
  ['trailing newline', 'a'.repeat(64) + '\n'],
  ['array null suffix', ['a'.repeat(64), null]],
  ['object', {}],
]) {
  const value = snapshot()
  value.qualificationKey = key
  project(`qualification coercion ${name}`, input({ assertions: [value] }))
}
project(
  'distinct array scenes',
  input({
    sceneId: [],
    assertions: [snapshot('a', { scope: { kind: 'scene', sceneId: [] } })],
    commitments: [],
  }),
)
project(
  'distinct object scenes',
  input({ sceneId: {}, assertions: [], commitments: [plan('p', { sceneId: {} })] }),
)
project(
  'numeric matching scenes',
  input({
    sceneId: 1,
    assertions: [snapshot('a', { scope: { kind: 'scene', sceneId: 1 } })],
    commitments: [],
  }),
)
const verifications = []
function verify(name, page, current, extra = {}) {
  const calls = []
  const sceneId = extra.sceneId ?? 's'
  const valid = verifyExperienceView(sceneId, page, {
    resolveCurrentBatch(scene, refs) {
      calls.push([scene, clone(refs)])
      if (extra.fail) throw Error('synthetic')
      return current ?? undefined
    },
  })
  verifications.push({
    name,
    sceneId,
    page,
    current: current ?? null,
    fail: extra.fail ?? false,
    valid,
    calls,
  })
}
const base = input(),
  page = projectExperienceView(base),
  current = { assertions: base.assertions, commitments: base.commitments }
verify('fresh exact', page, current)
verify('reordered page items', { ...clone(page), items: [...page.items].reverse() }, current)
verify('coverage not authority', { ...clone(page), coverage: { garbage: true } }, current)
verify('empty without current', { items: [] }, null)
verify('invalid scene', page, current, { sceneId: '' })
verify('unavailable', page, null)
verify('callback error', page, current, { fail: true })
verify('wrong current counts', page, { assertions: [], commitments: [] })
verify('duplicate page', { ...clone(page), items: [page.items[0], page.items[0]] }, current)
verify('overwide page', { items: Array(13).fill(page.items[0]) }, current)
verify('missing items', {}, current)
verify('changed qualification', page, {
  ...current,
  assertions: [{ ...snapshot(), qualificationKey: 'b'.repeat(64) }],
})
verify('changed content', page, { ...current, assertions: [snapshot('a', { content: 'Changed' })] })
verify('changed mutation', page, { ...current, commitments: [plan('p', { mutationVersion: 2 })] })
verify(
  'extra item property',
  { ...clone(page), items: page.items.map((v, i) => (i ? v : { ...v, extra: true })) },
  current,
)
verify('current item filtered', page, {
  ...current,
  assertions: [snapshot('a', { status: 'retracted' })],
})
verify('included references only', projectExperienceView(input({ limit: 1 })), {
  assertions: [snapshot()],
  commitments: [],
})
const files = [
  'src/memory/projections/experience-view.ts',
  'src/memory/domain/experience-view.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  projections,
  verifications,
}
const path = 'backend/internal/experienceview/testdata/source-oracle.json',
  data = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), data)
else {
  mkdirSync('backend/internal/experienceview/testdata', { recursive: true })
  writeFileSync(path, data)
}
console.log(
  `experience oracle: ${projections.length} projections, ${verifications.length} verifications`,
)
