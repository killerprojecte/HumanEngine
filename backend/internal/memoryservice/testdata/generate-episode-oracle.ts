import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { SemanticEpisodeAnchors } from '../../../../src/memory/semantic/episode-anchors.ts'
assert.equal(process.version, 'v24.22.1')
assert(process.argv.slice(2).every((x) => x === '--check'))
const check = process.argv.includes('--check')
const hash = (x: any) => createHash('sha256').update(JSON.stringify(x)).digest('hex')
const ref = (id: string) => ({ id, version: 1, deletionEpoch: 0 })
const proof = (id: string) => ({ proofId: 'proof-' + id, sources: [], parents: [] })
function base() {
  const entity = {
    id: 'event',
    sceneId: 's',
    kind: 'event',
    title: 'Synthetic event',
    backing: ref('a'),
    originEdgeId: 'edge-a',
  }
  const current: any = {},
    anchors: any = {}
  const edges = ['a', 'b'].map((id) => {
    const record = {
      id,
      scope: { kind: 'scene', sceneId: 's', personId: 'person-' + id },
      content: 'Synthetic ' + id,
      kind: 'experience',
      status: 'active',
    }
    current[id] = {
      record,
      proofs: [proof(id)],
      dependencyKey: hash(id),
      roots: [hash('root-' + id)],
      rootCoverage: 'complete',
      derived: false,
    }
    anchors[id] = { record, proofs: [proof(id)] }
    return {
      id: 'edge-' + id,
      version: 1,
      sceneId: 's',
      registryVersion: 1,
      predicate: 'participates_in',
      subject: { kind: 'person', personId: 'person-' + id },
      object: { kind: 'entity', entity },
      polarity: 'positive',
      modality: 'asserted',
      temporalScope: 'past',
      conditions: [],
      backing: ref(id),
      backingFingerprint: hash(record),
      proofs: [proof(id)],
      dependencies:
        id === 'a'
          ? []
          : [{ entity, backingFingerprint: hash(current.a.record), proofs: [proof('a')] }],
      readDependencies: [],
      status: 'candidate',
      admission: 'qualified_candidate',
    }
  })
  return {
    context: { sceneId: 's', simulation: false },
    trigger: ref('a'),
    current,
    anchors,
    first: { edges: [edges[0]], coverage: 'complete' },
    page: { edges, supportClosure: { edges: [] }, coverage: 'complete' },
  }
}
const cases: any[] = []
function run(name: string, mutate: (x: any) => void = () => {}) {
  const x: any = base()
  mutate(x)
  const input = structuredClone(x),
    trace: any[] = []
  const ports: any = {
    current: (c: any, r: any) => {
      trace.push(['current', c, r])
      return x.current[r.id]
    },
    anchorCurrent: (c: any, r: any) => {
      trace.push(['anchor', c, r])
      return x.anchors[r.id]
    },
    triggerRecord: (c: any, r: any) => {
      trace.push(['anchor', c, r])
      return x.anchors[r.id]?.record
    },
    knowledge: {
      query: (c: any, q: any) => {
        trace.push(['query', c, q])
        return q.anchor.kind === 'person' ? x.first : x.page
      },
    },
  }
  const p = new SemanticEpisodeAnchors(ports)
  let output: any, error: string | undefined
  try {
    output = p.discover(x.context, x.trigger)
  } catch (e: any) {
    error = e.message
  }
  const discoveryTrace = structuredClone(trace)
  const validations: any[] = []
  if (output?.groups.length) {
    const g = output.groups[0],
      parents = g.parents.map((p: any) => p.ref)
    for (const [name, refs, key] of [
      ['exact', parents, g.anchorMembershipKey],
      ['wrong-key', parents, '0'.repeat(64)],
      ['duplicates', [parents[0], parents[0]], g.anchorMembershipKey],
      ['reversed', [...parents].reverse(), g.anchorMembershipKey],
    ] as any) {
      validations.push({
        name,
        parents: refs,
        key,
        expected: p.valid(x.context, g.anchor, refs, key),
      })
    }
  }
  // Discovery trace only; validation calls are exercised separately in Go.
  cases.push({
    name,
    input,
    output,
    trace: discoveryTrace,
    ...(error ? { error } : {}),
    validations,
  })
}
run('two-original-participants')
run('simulation', (x) => (x.context.simulation = true))
run('missing-trigger', (x) => {
  delete x.current.a
  delete x.anchors.a
})
run('wrong-scene-trigger', (x) => {
  x.current.a.record.scope.sceneId = 'other'
})
run('no-person-trigger', (x) => {
  delete x.current.a.record.scope.personId
})
run('knowledge-limited', (x) => (x.first.coverage = 'limited'))
run('page-limited', (x) => (x.page.coverage = 'limited'))
run('one-declaration', (x) => x.page.edges.pop())
run('missing-origin', (x) => {
  x.page.edges = x.page.edges.filter((e: any) => e.id !== 'edge-a')
  x.page.edges.push({ ...structuredClone(x.page.edges[0]), id: 'edge-c' })
})
run('missing-current-parent', (x) => delete x.current.b)
run('derived-parent', (x) => (x.current.b.derived = true))
run('incomplete-roots', (x) => (x.current.b.rootCoverage = 'limited'))
run('empty-proof', (x) => (x.current.b.proofs = []))
run('mismatched-parent-person', (x) => (x.current.b.record.scope.personId = 'wrong'))
run('revoked-anchor-proof', (x) => (x.anchors.b.proofs = []))
run('missing-anchor', (x) => delete x.anchors.b)
run('conflicting-event-identity', (x) =>
  x.first.edges.push({
    ...structuredClone(x.first.edges[0]),
    object: { kind: 'entity', entity: { ...x.first.edges[0].object.entity, title: 'changed' } },
  }),
)
run('group-limit', (x) => {
  for (let i = 0; i < 4; i++)
    x.first.edges.push({
      ...structuredClone(x.first.edges[0]),
      object: { kind: 'entity', entity: { ...x.first.edges[0].object.entity, id: 'event-' + i } },
    })
})
run('internal-trigger-keeps-originals', (x) => {
  const marker = {
    id: 'marker',
    scope: { kind: 'scene', sceneId: 's', personId: 'person-a' },
    internalUse: 'knowledge_edge',
  }
  x.anchors.marker = { record: marker, proofs: [proof('marker')] }
  x.trigger = ref('marker')
  const e = x.page.edges[0]
  e.readDependencies = [
    { backing: ref('a'), backingFingerprint: hash(x.current.a.record), proofs: [proof('a')] },
  ]
  e.backing = ref('marker')
  e.backingFingerprint = hash(marker)
  e.proofs = [proof('marker')]
  e.object.entity.backing = ref('marker')
  x.page.edges[1].dependencies[0].backingFingerprint = hash(marker)
  x.page.edges[1].dependencies[0].proofs = [proof('marker')]
})
run('internal-only-no-original', (x) => {
  x.current.a.record.internalUse = 'knowledge_edge'
  delete x.current.a
  x.anchors.a.record.internalUse = 'knowledge_edge'
})
run(
  'read-dependency-origin-missing',
  (x) =>
    (x.page.edges[1].readDependencies = [
      {
        edgeId: 'missing',
        backing: ref('a'),
        backingFingerprint: hash(x.current.a.record),
        proofs: [proof('a')],
      },
    ]),
)
run('property-and-time', (x) => {
  x.page.edges[1].property = 'duration'
  x.page.edges[1].validFrom = '2026-01-01T00:00:00Z'
  x.anchors.b.record.occurredAt = '2025-12-01T00:00:00Z'
})
run(
  'proof-field-order-irrelevant',
  (x) => (x.anchors.b.proofs = [{ parents: [], sources: [], proofId: 'proof-b' }]),
)
run(
  'non-event-first-edge',
  (x) =>
    (x.first.edges = [
      {
        ...x.first.edges[0],
        object: { kind: 'entity', entity: { ...x.first.edges[0].object.entity, kind: 'topic' } },
      },
    ]),
)
run('unrelated-trigger', (x) => {
  x.current.c = {
    ...structuredClone(x.current.a),
    record: { ...structuredClone(x.current.a.record), id: 'c' },
  }
  x.anchors.c = x.current.c
  x.trigger = ref('c')
})
run('reversed-page-order', (x) => x.page.edges.reverse())
run(
  'duplicate-edge-overwrite',
  (x) => (x.page.supportClosure.edges = [structuredClone(x.page.edges[0])]),
)
run('read-dependency-cycle', (x) => {
  x.page.edges[0].readDependencies = [
    {
      edgeId: 'edge-b',
      backing: ref('b'),
      backingFingerprint: hash(x.current.b.record),
      proofs: [proof('b')],
    },
  ]
})
run('closure-limit-41', (x) => {
  for (let i = 0; i < 39; i++) {
    const edge = { ...structuredClone(x.page.edges[0]), id: 'support-' + i }
    x.page.supportClosure.edges.push(edge)
    x.page.edges[1].readDependencies.push({
      edgeId: edge.id,
      backing: ref('a'),
      backingFingerprint: hash(x.current.a.record),
      proofs: [proof('a')],
    })
  }
})
run('parents-limit-13', (x) => {
  for (let i = 0; i < 11; i++) {
    const id = 'extra-' + i
    x.current[id] = structuredClone(x.current.a)
    x.current[id].record.id = id
    x.anchors[id] = structuredClone(x.current[id])
    x.page.edges[1].readDependencies.push({
      backing: ref(id),
      backingFingerprint: hash(x.current[id].record),
      proofs: [proof('a')],
    })
  }
})
run('anchor-dependencies-limit-129', (x) => {
  for (let i = 0; i < 128; i++)
    x.page.edges[1].readDependencies.push({
      backing: ref('a'),
      backingFingerprint: hash('variant-' + i),
      proofs: [proof('a')],
    })
})
run('conflicting-parent-revisions', (x) =>
  x.page.edges[1].readDependencies.push({
    backing: { ...ref('a'), version: 2 },
    backingFingerprint: hash(x.current.a.record),
    proofs: [proof('a')],
  }),
)
run('utf16-parent-order', (x) => {
  const renamed = JSON.parse(
    JSON.stringify(x).replaceAll('"a"', '"\\ud83d\\ude00"').replaceAll('"b"', '"\\ue000"'),
  )
  Object.assign(x, renamed)
})
assert(cases.find((c) => c.name === 'two-original-participants').output.groups.length === 1)
assert(cases.find((c) => c.name === 'internal-trigger-keeps-originals').output.groups.length === 1)
const sources = [
  'src/memory/semantic/episode-anchors.ts',
  'src/memory/semantic/contracts.ts',
  'src/memory/knowledge/contracts.ts',
  'src/memory/knowledge/registry.ts',
  'src/memory/domain/contracts.ts',
  'package-lock.json',
  'backend/internal/memoryservice/testdata/generate-episode-oracle.ts',
]
const manifest = sources.map((path) => ({
  path,
  sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
}))
const output = JSON.stringify({ sources: manifest, cases }, null, 2) + '\n'
const file = 'backend/internal/memoryservice/testdata/episode-oracle.json'
if (check) assert.equal(readFileSync(file, 'utf8'), output)
else writeFileSync(file, output)
console.log(`${cases.length} source episode cases ${check ? 'checked' : 'generated'}`)
