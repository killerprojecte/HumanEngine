import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
// Run from repository root: node --import tsx backend/internal/knowledge/testdata/generate-admission-oracle.ts [--check]
// Overlay validation may set KNOWLEDGE_SOURCE_ROOT to the unchanged source repository.
// Only storage and external authority are mocked. All admission, projection, validators,
// endpoint, span, hashing, conflict, and policy code executes the actual TS source.
assert.equal(process.version, 'v24.22.1')
const sourceTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone
assert.equal(sourceTimezone, 'UTC', 'Admission oracle must run in UTC')
assert(
  process.argv.slice(2).every((x) => x === '--check'),
  'Unknown argument',
)
const root = resolve(process.env.KNOWLEDGE_SOURCE_ROOT ?? process.cwd())
const here = dirname(fileURLToPath(import.meta.url))
const source = async (p: string) => import(pathToFileURL(resolve(root, p)).href)
const { KnowledgeService } = await source('src/memory/knowledge/service.ts')
const { KnowledgeReader } = await source('src/memory/knowledge/read.ts')
const { KnowledgeEdgeSchema } = await source('src/memory/knowledge/storage.ts')
const now = Date.parse('2026-11-01T00:00:00.000Z')
const backing = { id: 'backing', version: 1, deletionEpoch: 0 }
const event = { sceneId: 's', messageId: 'm', senderId: 'p' }
const proof = {
  proofId: 'proof',
  sources: [event],
  parents: [],
  subjectWitness: { kind: 'self', personId: 'p', sourceMessageId: 'm' },
}
const view = {
  record: {
    id: 'backing',
    scope: { kind: 'scene', sceneId: 's', personId: 'p' },
    content: 'Synthetic',
    kind: 'fact',
    status: 'active',
    epistemicKind: 'explicit',
  },
  proofs: [proof],
}
const proposal = {
  registryVersion: 1,
  predicate: 'prefers_topic',
  subject: { kind: 'self', personId: 'p' },
  object: { kind: 'literal', valueType: 'text', value: 'Synthetic' },
  polarity: 'positive',
  modality: 'asserted',
  temporalScope: 'current',
  conditions: [],
  sourceSpans: [{ messageId: 'm', partIndex: 0, start: 0, end: 9, stance: 'self' }],
}
const general = {
  ...proposal,
  predicate: 'depends_on',
  subject: { kind: 'new', entityKind: 'task', title: 'First' },
  object: { kind: 'new', entityKind: 'task', title: 'Second' },
  sourceSpans: [{ ...proposal.sourceSpans[0], stance: 'asserted' }],
}
const cases: any[] = []
function run(name: string, options: any = {}) {
  // Seed operations have already consumed these deterministic IDs. Keep newly
  // generated IDs disjoint, just as real random UUIDs would be.
  const seededSequence = Math.max(
    0,
    ...Array.from(JSON.stringify(options.seed ?? []).matchAll(/"id-(\d+)"/g), (m) => Number(m[1])),
  )
  const input = structuredClone({
    name,
    ctx: { sceneId: 's', simulation: false, provenance: 'contextual', ...options.ctx },
    backing: options.backing ?? backing,
    inputs: options.inputs ?? [proposal],
    views: options.views ?? { backing: view },
    sources: options.sources ?? {
      m: { source: event, parts: [{ type: 'text', text: 'Synthetic 😀 text' }] },
    },
    seed: options.seed ?? [],
    enabled: options.enabled ?? true,
    learning: options.learning ?? true,
    auto: options.auto ?? true,
    transaction: options.transaction ?? true,
    now,
    nowStep: options.nowStep ?? 0,
    idStart: options.idStart ?? seededSequence,
  })
  const rows = structuredClone(input.seed)
  const writes: any[] = []
  const spanCalls: any[] = []
  let sequence = input.idStart
  const clockValues: number[] = []
  const clock = () => {
    const value = now + clockValues.length * input.nowStep
    clockValues.push(value)
    return value
  }
  const storage = {
    requireTransaction() {
      if (!input.transaction) throw Error('knowledge_transaction_required')
    },
    rows(scene?: string, limit?: number) {
      const values = rows
        .map((x: any) => x.edge)
        .filter((e: any) => scene === undefined || e.sceneId === scene)
        .sort((a: any, b: any) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
      return limit === undefined ? values : values.slice(0, limit)
    },
    get(id: string) {
      return rows.find((x: any) => x.edge.id === id)?.edge
    },
    byKey(key: string) {
      return rows.find((x: any) => x.key === key)?.edge
    },
    entity(id: string, scene: string) {
      const values = this.rows(scene, 201)
      if (values.length > 200) throw Error('knowledge_scan_limit')
      return values
        .flatMap((e: any) =>
          [e.subject, e.object]
            .filter((p) => p.kind === 'entity' && p.entity.originEdgeId === e.id)
            .map((p) => p.entity),
        )
        .find((e: any) => e.id === id)
    },
    put(edge: any, key: string) {
      KnowledgeEdgeSchema.parse(edge)
      assert(!rows.some((row: any) => row.edge.id === edge.id), 'oracle_fixture_id_collision')
      writes.push(structuredClone({ edge, key }))
      rows.push(structuredClone({ edge, key }))
    },
  }
  const authority = {
    assertReady() {},
    enabled() {
      return input.enabled
    },
    learningEnabled() {
      return input.learning
    },
    autoAdmission() {
      return input.auto
    },
    current(_ctx: any, ref: any) {
      return ref.version === 1 && ref.deletionEpoch === 0 ? input.views[ref.id] : undefined
    },
    source(_ctx: any, id: string) {
      return input.sources[id]
    },
  }
  const service = Object.create(KnowledgeService.prototype)
  Object.assign(service, {
    storage,
    authority,
    options: { now: clock, id: () => 'id-' + ++sequence },
    reader: new KnowledgeReader(storage, authority, clock),
  })
  service.spans = function (ctx: any, proposal: any, proofs: any[]) {
    try {
      const result = KnowledgeService.prototype.spans.call(this, ctx, proposal, proofs)
      spanCalls.push(structuredClone({ ctx, proposal, proofs, result }))
      return result
    } catch (e) {
      spanCalls.push(
        structuredClone({
          ctx,
          proposal,
          proofs,
          error: e instanceof Error ? e.message : String(e),
        }),
      )
      throw e
    }
  }
  let result: any, error: string | undefined
  try {
    result = service.admit(input.ctx, input.backing, input.inputs)
  } catch (e) {
    error = e instanceof Error ? e.message : String(e)
    if (error === 'oracle_fixture_id_collision') throw e
  }
  const output = structuredClone({
    ...input,
    result: result ?? null,
    ...(error ? { error } : {}),
    writes,
    rows,
    spanCalls,
    clockValues,
    idCalls: sequence - input.idStart,
  })
  cases.push(output)
  return output
}
const accepted = run('self-automatic')
assert.equal(accepted.result.created[0].status, 'accepted')
run('general-automatic', { inputs: [general] })
run('auto-disabled', { auto: false })
run('enabled-disabled', { enabled: false, inputs: [null] })
run('learning-disabled', { learning: false })
run('simulation-disabled', { ctx: { simulation: true } })
run('transaction-before-disabled', { transaction: false, enabled: false })
run('empty-batch', { inputs: [] })
run('input-limit', { inputs: [proposal, proposal, proposal, proposal] })
run('invalid-backing', { backing: { ...backing, extra: true } })
run('backing-missing', { views: {} })
run('proposal-invalid', { inputs: [{ ...proposal, extra: true }] })
run('batch-atomic-invalid-second', { inputs: [proposal, null] })
run('duplicate-within-batch', { inputs: [proposal, proposal] })
run('duplicate-existing', { seed: accepted.rows })
run('duplicate-unavailable', {
  seed: [
    { ...accepted.rows[0], edge: { ...accepted.rows[0].edge, backingFingerprint: 'f'.repeat(64) } },
  ],
})
run('visible-self-conflict', {
  seed: accepted.rows,
  inputs: [{ ...proposal, object: { ...proposal.object, value: 'Alternative' } }],
})
run('pending-self-conflict', {
  inputs: [proposal, { ...proposal, object: { ...proposal.object, value: 'Alternative' } }],
})
for (const [name, fields] of Object.entries({
  negative: { polarity: 'negative' },
  possible: { modality: 'possible' },
  past: { temporalScope: 'past' },
  conditional: { conditions: ['when needed'] },
  futureStart: { validFrom: '2026-11-02T00:00:00.000Z' },
  expired: { validUntil: '2026-11-01T00:00:00.000Z' },
  startBoundary: { validFrom: '2026-11-01T00:00:00.000Z' },
}))
  run('policy-' + name, { inputs: [{ ...proposal, ...fields }] })
for (const kind of ['observed', 'reported', 'inferred', 'hypothesis'])
  run('self-epistemic-' + kind, {
    views: { backing: { ...view, record: { ...view.record, epistemicKind: kind } } },
  })
run('general-observed', {
  inputs: [general],
  views: {
    backing: {
      ...view,
      record: { ...view.record, scope: { kind: 'scene', sceneId: 's' }, epistemicKind: 'observed' },
    },
  },
})
run('self-witness-missing', {
  views: { backing: { ...view, proofs: [{ ...proof, subjectWitness: undefined }] } },
})
run('self-person-mismatch', {
  inputs: [{ ...proposal, subject: { kind: 'self', personId: 'other' } }],
})
run('endpoint-type', { inputs: [{ ...general, predicate: 'prefers_topic' }] })
run('existing-source-only', {
  ctx: { provenance: 'source-only' },
  inputs: [{ ...general, subject: { kind: 'existing', entityId: 'absent' } }],
})
run('existing-not-exposed', {
  inputs: [{ ...general, subject: { kind: 'existing', entityId: 'absent' } }],
})
for (const stance of ['quoted', 'reported', 'asserted'])
  run('self-stance-' + stance, {
    inputs: [{ ...proposal, sourceSpans: [{ ...proposal.sourceSpans[0], stance }] }],
  })
run('reported-claim', {
  inputs: [{ ...general, sourceSpans: [{ ...proposal.sourceSpans[0], stance: 'reported' }] }],
  views: {
    backing: { ...view, record: { ...view.record, kind: 'claim', epistemicKind: 'reported' } },
  },
})
run('general-self-stance', { inputs: [{ ...general, sourceSpans: proposal.sourceSpans }] })
run('span-missing', { sources: {} })
run('span-surrogate-boundary', {
  inputs: [{ ...proposal, sourceSpans: [{ ...proposal.sourceSpans[0], end: 11 }] }],
})
run('span-surrogate-whole', {
  inputs: [{ ...proposal, sourceSpans: [{ ...proposal.sourceSpans[0], start: 10, end: 12 }] }],
})
run('read-assertion', { ctx: { exposedAssertions: [backing] } })
run('read-assertion-duplicate', { ctx: { exposedAssertions: [backing, backing] } })
run('read-assertion-missing', { ctx: { exposedAssertions: [{ ...backing, id: 'absent' }] } })
run('read-source-only', { ctx: { provenance: 'source-only', exposedAssertions: [backing] } })
run('read-assertion-limit', { ctx: { exposedAssertions: Array(129).fill(backing) } })
const snapshot = { id: accepted.rows[0].edge.id, version: 1, backing }
run('read-edge', { seed: accepted.rows, ctx: { exposedEdges: [snapshot] }, inputs: [general] })
run('read-edge-duplicate', { seed: accepted.rows, ctx: { exposedEdges: [snapshot, snapshot] } })
run('read-edge-stale', {
  seed: accepted.rows,
  ctx: { exposedEdges: [{ ...snapshot, version: 2 }] },
})
run('read-edge-missing', { ctx: { exposedEdges: [snapshot] } })
run('read-edge-limit', { ctx: { exposedEdges: Array(41).fill(snapshot) } })
const entitySeed = run('entity-origin', {
  inputs: [{ ...proposal, object: { kind: 'new', entityKind: 'topic', title: 'Topic' } }],
})
const entity = entitySeed.rows[0].edge.object.entity
run('existing-entity-exposed', {
  seed: entitySeed.rows,
  inputs: [{ ...proposal, object: { kind: 'existing', entityId: entity.id } }],
  ctx: { exposedEntities: [{ entityId: entity.id, backingRef: backing }] },
})
run('existing-entity-unavailable', {
  seed: [
    {
      ...entitySeed.rows[0],
      edge: { ...entitySeed.rows[0].edge, backingFingerprint: 'f'.repeat(64) },
    },
  ],
  inputs: [{ ...proposal, object: { kind: 'existing', entityId: entity.id } }],
  ctx: { exposedEntities: [{ entityId: entity.id, backingRef: backing }] },
})
for (const [property, valueType, value, unit] of [
  ['version', 'text', '1', undefined],
  ['enabled', 'boolean', true, undefined],
  ['count', 'number', 2, null],
  ['duration', 'number', 1.5, 'hours'],
  ['count', 'number', 1.5, null],
  ['duration', 'number', 1, 'days'],
])
  run('property-' + property + '-' + String(unit) + '-' + String(value), {
    inputs: [
      {
        ...general,
        predicate: 'has_property',
        property,
        object: { kind: 'literal', valueType, value, ...(unit !== undefined ? { unit } : {}) },
      },
    ],
  })
const timed = {
  ...proposal,
  validFrom: '2026-10-31T00:00:00.000Z',
  validUntil: '2026-11-02T00:00:00.000Z',
}
run('clock-advancing-eligible', {
  nowStep: 7,
  inputs: [timed, { ...timed, object: { ...timed.object, value: 'Another' } }],
})
run('clock-advancing-auto-off', {
  nowStep: 7,
  auto: false,
  inputs: [timed, { ...timed, object: { ...timed.object, value: 'Another' } }],
})
run('clock-advancing-conflict', {
  nowStep: 7,
  seed: accepted.rows,
  inputs: [{ ...timed, object: { ...timed.object, value: 'Alternative' } }],
})
run('clock-advancing-read-dependency', {
  nowStep: 7,
  ctx: { exposedAssertions: [backing] },
  inputs: [timed],
})
run('clock-advancing-conditional', {
  nowStep: 7,
  inputs: [{ ...timed, conditions: ['condition'] }],
})
const stancePrecedence = run('mixed-stance-backing-conflict-before-subject', {
  inputs: [
    {
      ...general,
      sourceSpans: [
        { ...proposal.sourceSpans[0], stance: 'self' },
        { ...proposal.sourceSpans[0], stance: 'quoted' },
      ],
    },
  ],
})
assert.equal(stancePrecedence.result.rejected[0].reason, 'knowledge_stance_backing_conflict')
assert.equal(cases.length, 68)
const generator = 'backend/internal/knowledge/testdata/generate-admission-oracle.ts'
const files = [
  'src/memory/knowledge/service.ts',
  'src/memory/knowledge/read.ts',
  'src/memory/knowledge/storage.ts',
  'src/memory/knowledge/contracts.ts',
  'src/memory/knowledge/qualification.ts',
  'src/memory/knowledge/registry.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'package-lock.json',
  generator,
]
const hashes = Object.fromEntries(
  files.map((p) => [
    p,
    createHash('sha256')
      .update(readFileSync(p === generator ? fileURLToPath(import.meta.url) : resolve(root, p)))
      .digest('hex'),
  ]),
)
const outputs: Record<string, string> = {
  'admission-oracle.json':
    JSON.stringify(
      { sourceNode: process.version, sourceTimezone, sources: hashes, cases },
      null,
      2,
    ) + '\n',
  'admission-oracle-sources.json': JSON.stringify(hashes, null, 2) + '\n',
}
for (const [name, value] of Object.entries(outputs)) {
  const path = resolve(here, name)
  if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), value)
  else writeFileSync(path, value)
}
console.log(`knowledge admission oracle generated/checked: ${cases.length} cases`)
