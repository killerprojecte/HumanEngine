import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { KnowledgeReader } from '../../../../src/memory/knowledge/read.ts'
import { knowledgeBackingFingerprint } from '../../../../src/memory/knowledge/qualification.ts'
import {
  KnowledgeQuerySchema,
  KnowledgeToolQuerySchema,
} from '../../../../src/memory/knowledge/query.ts'
assert.equal(process.version, 'v24.22.1')
// Run from overlay/repository root. Storage and current-assertion access are mocks.
const check = process.argv.includes('--check')
assert(
  process.argv.slice(2).every((arg) => arg === '--check'),
  'Unknown argument',
)
const now = Date.parse('2026-11-01T00:00:00.000Z')
const views: Record<string, any> = {}
function edge(id: string): any {
  const source = { sceneId: 's', messageId: 'source-' + id, senderId: 'p' }
  const proof = { proofId: 'proof-' + id, sources: [source], parents: [] }
  const record = {
    id: 'backing-' + id,
    scope: { kind: 'scene', sceneId: 's' },
    content: 'Synthetic ' + id,
    kind: 'fact',
    status: 'active',
  }
  views[record.id] = { record, proofs: [proof] }
  return {
    id,
    version: 1,
    sceneId: 's',
    registryVersion: 1,
    predicate: 'prefers_topic',
    subject: { kind: 'person', personId: 'p' },
    object: {
      kind: 'entity',
      entity: {
        id: 'entity-' + id,
        sceneId: 's',
        kind: 'topic',
        title: id,
        backing: { id: record.id, version: 1, deletionEpoch: 0 },
        originEdgeId: id,
      },
    },
    polarity: 'positive',
    modality: 'asserted',
    temporalScope: 'current',
    conditions: [],
    backing: { id: record.id, version: 1, deletionEpoch: 0 },
    backingFingerprint: knowledgeBackingFingerprint(record),
    proofs: [proof],
    spans: [
      {
        source,
        rootId: 'a'.repeat(64),
        partIndex: 0,
        start: 0,
        end: 1,
        spanHash: 'b'.repeat(64),
        stance: 'asserted',
      },
    ],
    dependencies: [],
    readDependencies: [],
    status: 'candidate',
    admission: 'qualified_candidate',
    admissionPolicyVersion: 1,
    createdAt: '2026-11-01T00:00:00.000Z',
  }
}
const defaultQuery = {
  anchor: { kind: 'person', personId: 'p' },
  predicates: ['prefers_topic'],
  depth: 1,
}
const cases: any[] = []
function run(name: string, rows: any[], options: any = {}) {
  // Snapshot all inputs before execution, including rows later mutated by other cases.
  ;[rows, options] = structuredClone([rows, options])
  const ctx = {
    sceneId: 's',
    simulation: false,
    provenance: 'contextual',
    ...options.ctx,
  }
  const current = structuredClone(options.views ?? views)
  const input = structuredClone(options.input ?? defaultQuery)
  const enabled = options.enabled ?? true
  const storage = {
    rows(scene: string, limit: number) {
      return rows.filter((e) => e.sceneId === scene).slice(0, limit)
    },
    get(id: string) {
      return rows.find((e) => e.id === id)
    },
    entity(id: string, scene: string) {
      const subset = rows.filter((e) => e.sceneId === scene).slice(0, 201)
      if (subset.length > 200) throw Error('knowledge_scan_limit')
      return subset
        .flatMap((e) =>
          [e.subject, e.object]
            .filter((p) => p.kind === 'entity' && p.entity.originEdgeId === e.id)
            .map((p) => p.entity),
        )
        .find((e) => e.id === id)
    },
  }
  const reader = new KnowledgeReader(
    storage as any,
    {
      assertReady() {},
      enabled() {
        return enabled
      },
      current(_ctx: any, ref: any) {
        return ref.version === 1 && ref.deletionEpoch === 0 ? current[ref.id] : undefined
      },
    },
    () => now,
  )
  let result: any, error: string | undefined
  const op = options.op ?? 'query',
    exact = options.exact ?? rows[0]?.backing
  const snapshot = structuredClone({
    name,
    rows,
    views: current,
    ctx,
    input,
    enabled,
    now,
    op,
    exact,
  })
  try {
    result =
      op === 'discover'
        ? reader.discover(ctx, exact, input)
        : op === 'project'
          ? reader.project(ctx, rows[0])
          : reader.query(ctx, input)
  } catch (e) {
    error = e instanceof Error ? e.message : String(e)
  }
  cases.push(
    structuredClone({
      ...snapshot,
      result: result ?? null,
      ...(error ? { error } : {}),
    }),
  )
}
let a = edge('a'),
  b = edge('b')
run('person-conflict-alternatives', [a, b])
b.polarity = 'negative'
b.object = { kind: 'literal', valueType: 'text', value: 'Alternative' }
run('negative-alternative', [a, b])
run('discover', [a, b], {
  op: 'discover',
  input: { predicates: ['prefers_topic'], depth: 2 },
})
run('entity-not-exposed', [a], {
  input: {
    ...defaultQuery,
    anchor: { kind: 'entity', entityId: 'entity-a', backingRef: a.backing },
  },
})
run('entity-exposed', [a], {
  ctx: { exposedEntities: [{ entityId: 'entity-a', backingRef: a.backing }] },
  input: {
    ...defaultQuery,
    anchor: { kind: 'entity', entityId: 'entity-a', backingRef: a.backing },
  },
})
run('entity-unavailable', [], {
  ctx: { exposedEntities: [{ entityId: 'entity-a', backingRef: a.backing }] },
  input: {
    ...defaultQuery,
    anchor: { kind: 'entity', entityId: 'entity-a', backingRef: a.backing },
  },
})
for (const disabled of [false, true]) {
  run('null-anchor-' + disabled, [a], {
    enabled: !disabled,
    input: { ...defaultQuery, anchor: null },
  })
}
run('simulation-validation-order', [a], {
  ctx: { simulation: true },
  input: { depth: 9, predicates: [] },
})
run('disabled-validation-order', [a], {
  enabled: false,
  input: { depth: 9, predicates: [] },
})
for (const input of [
  { ...defaultQuery, depth: 0 },
  { ...defaultQuery, depth: 3 },
  { ...defaultQuery, predicates: [] },
  { ...defaultQuery, predicates: ['bad'] },
  { ...defaultQuery, predicates: Array(8).fill('prefers_topic') },
])
  run('invalid-query-' + cases.length, [a], { input })
run('duplicate-predicates', [a], {
  input: { ...defaultQuery, predicates: ['prefers_topic', 'prefers_topic'] },
})
run('invalid-discovery-ref', [a], {
  op: 'discover',
  exact: { ...a.backing, extra: 1 },
})
run('stale-discovery-ref', [a], {
  op: 'discover',
  exact: { ...a.backing, version: 2 },
})
for (const field of [
  'id',
  'scope',
  'content',
  'kind',
  'epistemicKind',
  'occurredAt',
  'validFrom',
  'validUntil',
  'internalUse',
]) {
  const current = structuredClone(views)
  current[a.backing.id].record[field] =
    field === 'scope' ? { kind: 'scene', sceneId: 'other' } : 'changed'
  run('fingerprint-' + field, [a], { views: current })
}
const statusViews = structuredClone(views)
statusViews[a.backing.id].record.status = 'candidate'
run('status-not-fingerprinted', [a], { views: statusViews })
for (const field of ['proofId', 'parents', 'sources']) {
  const current = structuredClone(views)
  current[a.backing.id].proofs[0][field] =
    field === 'proofId' ? 'other' : field === 'parents' ? ['wrong-parent'] : []
  run('proof-binding-' + field, [a], { views: current })
}
for (const [name, patch] of Object.entries({
  expired: { validUntil: '2026-11-01T00:00:00.000Z' },
  future: { validFrom: '2026-11-01T00:00:00.001Z' },
  current: { validFrom: '2026-11-01T00:00:00.000Z' },
  past: { temporalScope: 'past' },
  unspecified: { temporalScope: 'unspecified' },
}))
  run('applicability-' + name, [{ ...a, ...patch }], { op: 'project' })
b = edge('b')
b.subject = { kind: 'person', personId: 'other' }
b.readDependencies = [
  {
    edgeId: a.id,
    edgeVersion: a.version,
    backing: a.backing,
    backingFingerprint: a.backingFingerprint,
    proofs: a.proofs,
  },
]
run('read-dependency-closure', [a, b], {
  input: { ...defaultQuery, anchor: { kind: 'person', personId: 'other' } },
})
for (const field of ['edgeVersion', 'backingFingerprint', 'proofs']) {
  const altered = structuredClone(b)
  altered.readDependencies[0][field] =
    field === 'proofs' ? [] : field === 'edgeVersion' ? 2 : 'f'.repeat(64)
  run('captured-' + field, [a, altered])
}
b.readDependencies = []
b.dependencies = [
  {
    entity: a.object.entity,
    backingFingerprint: a.backingFingerprint,
    proofs: a.proofs,
  },
]
run('entity-dependency-closure', [a, b], {
  input: { ...defaultQuery, anchor: { kind: 'person', personId: 'other' } },
})
const cycle = structuredClone(a)
cycle.readDependencies = [
  {
    edgeId: a.id,
    edgeVersion: 1,
    backing: a.backing,
    backingFingerprint: a.backingFingerprint,
    proofs: a.proofs,
  },
]
run('cycle', [cycle])
run('no-spans', [{ ...a, spans: [] }])
run(
  'scan-limit',
  Array.from({ length: 201 }, (_, i) => edge('scan-' + i)),
)
run(
  'conflict-group-limit',
  Array.from({ length: 13 }, (_, i) => edge('group-' + i)),
)
const budget = {
  ...a,
  readDependencies: Array.from({ length: 512 }, () => ({
    backing: a.backing,
    backingFingerprint: a.backingFingerprint,
    proofs: a.proofs,
  })),
}
run('dependency-budget', [budget])
const schema: any[] = []
for (const input of [
  defaultQuery,
  { ...defaultQuery, extra: true },
  { ...defaultQuery, anchor: { kind: 'memory', memoryId: 'm' } },
  { ...defaultQuery, anchor: { kind: 'person', personId: '' } },
  { ...defaultQuery, depth: 1.5 },
  { ...defaultQuery, predicates: [] },
])
  for (const tool of [false, true]) {
    const r = (tool ? KnowledgeToolQuerySchema : KnowledgeQuerySchema).safeParse(input)
    schema.push({
      input,
      tool,
      valid: r.success,
      ...(r.success ? { canonical: JSON.stringify(r.data) } : {}),
    })
  }
// Zod's installed implementation counts Unicode code points, not UTF-16 units.
for (const length of [257, 512, 513]) {
  for (const kind of ['person', 'entity', 'memory']) {
    const id = '😀'.repeat(length)
    const anchor =
      kind === 'person'
        ? { kind, personId: id }
        : kind === 'entity'
          ? { kind, entityId: id, backingRef: a.backing }
          : { kind, memoryId: id }
    const input = { ...defaultQuery, anchor }
    for (const tool of [false, true]) {
      const r = (tool ? KnowledgeToolQuerySchema : KnowledgeQuerySchema).safeParse(input)
      assert.equal(r.success, length <= 512 && (kind !== 'memory' || tool))
      schema.push({
        name: `emoji-${kind}-${length}`,
        input,
        tool,
        valid: r.success,
        ...(r.success ? { canonical: JSON.stringify(r.data) } : {}),
      })
    }
  }
}
const manifestPath = 'backend/internal/knowledge/testdata/read-oracle-sources.json'
const expected: Record<string, string> = JSON.parse(readFileSync(manifestPath, 'utf8'))
const sources = Object.fromEntries(
  Object.keys(expected).map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
assert.deepEqual(
  sources,
  expected,
  'Source/provenance changed: review and refresh the expected manifest explicitly',
)
// Catch the original aliasing regression before writing any fixture.
assert.equal(cases[0].rows[1].polarity, 'positive')
assert.equal(cases[0].rows[1].object.kind, 'entity')
assert.equal(
  cases.find((c) => c.name === 'read-dependency-closure').rows[1].readDependencies.length,
  1,
)
assert.equal(cases.find((c) => c.name === 'read-dependency-closure').rows[1].dependencies.length, 0)
assert.notDeepEqual(
  cases.find((c) => c.name === 'proof-binding-parents').views[a.backing.id].proofs[0].parents,
  a.proofs[0].parents,
)
const output = 'backend/internal/knowledge/testdata/read-oracle.json'
const bytes = Buffer.from(JSON.stringify({ sources, schema, cases }, null, 2) + '\n')
if (check) {
  assert(
    readFileSync(output).equals(bytes),
    'read-oracle.json differs; --check does not write files',
  )
  console.log(
    `Oracle matches: ${cases.length} mock-backed source cases, ${schema.length} schema cases`,
  )
} else {
  writeFileSync(output, bytes)
  console.log(`Generated ${cases.length} mock-backed source cases, ${schema.length} schema cases`)
}
