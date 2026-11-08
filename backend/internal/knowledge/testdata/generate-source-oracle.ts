import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createHash as sourceHash } from 'node:crypto'
assert.equal(process.version, 'v24.22.1')
// Run from repository root, after integration/unfreeze:
// node --import tsx backend/internal/knowledge/testdata/generate-source-oracle.ts
// This invokes the actual source validators and lifecycle, not a copied algorithm.
import { createHash } from 'node:crypto'
import { KnowledgeLifecycle } from '../../../../src/memory/knowledge/lifecycle.ts'
import { KnowledgeEdgeSchema } from '../../../../src/memory/knowledge/storage.ts'
const hash = (v: unknown) => createHash('sha256').update(JSON.stringify(v)).digest('hex')
const proof = (id: string, parents: string[] = []) => ({
  proofId: id,
  sources: [{ sceneId: 's', messageId: id, senderId: 'p' }],
  parents,
})
const p = proof('p', ['deleted']),
  q = proof('q'),
  z = proof('z')
function edge(id: string, proofs: any[] = [q]): any {
  proofs = structuredClone(proofs)
  return {
    id,
    version: 1,
    sceneId: 's',
    registryVersion: 1,
    predicate: 'prefers_topic',
    subject: { kind: 'person', personId: 'p' },
    object: { kind: 'literal', valueType: 'text', value: 'Synthetic' },
    polarity: 'positive',
    modality: 'asserted',
    temporalScope: 'current',
    conditions: [],
    backing: { id: 'backing-' + id, version: 1, deletionEpoch: 0 },
    backingFingerprint: 'b'.repeat(64),
    proofs,
    spans: proofs.map((p) => ({
      source: p.sources[0],
      rootId: hash(['s', p.sources[0].messageId]),
      partIndex: 0,
      start: 0,
      end: 1,
      spanHash: 'a'.repeat(64),
      stance: 'self',
    })),
    dependencies: [],
    readDependencies: [],
    status: 'accepted',
    admission: 'self_explicit_no_visible_conflict',
    admissionPolicyVersion: 1,
    createdAt: '2026-11-01T00:00:00.000Z',
  }
}
const dep = (e: any, proofs: any[] = [q]) => ({
  edgeId: e.id,
  edgeVersion: e.version,
  backing: structuredClone(e.backing),
  backingFingerprint: e.backingFingerprint,
  proofs,
})
const cases: any[] = []
function run(name: string, seed: any[], op: any = { kind: 'assertion', id: 'deleted' }) {
  const input = structuredClone(seed),
    rows = structuredClone(seed),
    effects: any[] = []
  let error: string | undefined
  const storage = {
    requireTransaction() {},
    rows(scene?: string) {
      return rows.filter((e) => scene === undefined || e.sceneId === scene)
    },
    remove(id: string) {
      effects.push({ kind: 'remove', id })
      rows.splice(
        rows.findIndex((e) => e.id === id),
        1,
      )
    },
    replace(edge: any) {
      effects.push({ kind: 'replace', edge })
      rows[rows.findIndex((e) => e.id === edge.id)] = edge
    },
  }
  const lifecycle = new KnowledgeLifecycle(storage, () => undefined)
  try {
    for (const e of rows) KnowledgeEdgeSchema.parse(e)
    if (op.kind === 'assertion') lifecycle.purgeAssertion(op.id)
    else lifecycle.purgeSource(op.scene, op.message)
  } catch (e) {
    error = e instanceof Error ? e.message : String(e)
  }
  cases.push({ name, input, op, effects, rows, error })
}
run('direct', [edge('a')], { kind: 'assertion', id: 'backing-a' })
run('strip-proof', [edge('a', [p, q])])
let a = edge('a', [p, q]),
  b = edge('b'),
  c = edge('c')
b.readDependencies = [dep(a, [p, q])]
c.readDependencies = [dep(b)]
run('captured-chain', [a, b, c])
for (const mismatch of ['edgeVersion', 'backing', 'backingFingerprint']) {
  a = edge('a', [p, q])
  b = edge('b')
  const d = dep(a)
  if (mismatch === 'edgeVersion') d.edgeVersion = 9
  if (mismatch === 'backing') d.backing = { id: 'older', version: 1, deletionEpoch: 0 }
  if (mismatch === 'backingFingerprint') d.backingFingerprint = 'f'.repeat(64)
  b.readDependencies = [d]
  run('mismatch-' + mismatch, [a, b])
}
a = edge('a', [p, q])
b = edge('b')
b.readDependencies = [dep(a, [z])]
run('empty-intersection', [a, b])
a = edge('a', [p, q])
a.spans = a.spans.slice(0, 1)
run('no-surviving-span', [a])
a = edge('a')
b = edge('b')
c = edge('c')
b.readDependencies = [dep(a)]
c.dependencies = [
  {
    entity: {
      id: 'entity',
      sceneId: 's',
      kind: 'task',
      title: 'Synthetic',
      backing: b.backing,
      originEdgeId: 'b',
    },
    backingFingerprint: 'c'.repeat(64),
    proofs: [q],
  },
]
run('transitive-reverse', [c, b, a], { kind: 'assertion', id: 'backing-a' })
a = edge('a', [p, q])
b = edge('b', [q])
a.readDependencies = [dep(b)]
b.readDependencies = [dep(a, [p, q])]
run('cycle-converges', [a, b])
run('source-purge', [edge('a', [p, q])], { kind: 'source', scene: 's', message: 'p' })
a = edge('a', [p, q])
a.version = Number.MAX_SAFE_INTEGER
run('validate-before-write', [a, edge('b', [p])])
const schema: any[] = []
function validate(name: string, e: any) {
  const parsed = KnowledgeEdgeSchema.safeParse(e)
  schema.push({
    name,
    edge: e,
    valid: parsed.success,
    ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
  })
}
validate('base', edge('schema'))
for (const [name, mutate] of Object.entries({
  unknown: (e: any) => (e.extra = true),
  badRoot: (e: any) => (e.spans[0].rootId = '0'.repeat(64)),
  parentBinding: (e: any) => {
    e.proofs[0].parents = ['x']
    e.proofs[0].parentReferences = []
  },
  badDate: (e: any) => (e.createdAt = '2026-02-30T00:00:00Z'),
  badProperty: (e: any) => (e.property = 'count'),
  unicodeTitle: (e: any) => (e.object.value = '😀'.repeat(2000)),
  unsafeVersion: (e: any) => (e.version = Number.MAX_SAFE_INTEGER + 1),
})) {
  const e = edge('schema')
  mutate(e)
  validate(name, e)
}
function reverseFields(v: any): any {
  if (Array.isArray(v)) return v.map(reverseFields)
  if (v && typeof v === 'object')
    return Object.fromEntries(
      Object.entries(v)
        .reverse()
        .map(([k, x]) => [k, reverseFields(x)]),
    )
  return v
}
validate('reordered-fields', reverseFields(edge('schema')))
for (const count of [1999, 2000, 2001]) {
  const e = edge('schema')
  e.object.value = '😀'.repeat(count)
  validate('unicode-boundary-' + count, e)
}
for (const date of [
  '2024-02-29T23:59:59.999Z',
  '2025-02-29T00:00:00Z',
  '2026-11-01T24:00:00Z',
  '2026-11-01T00:00:00+00:00',
]) {
  const e = edge('schema')
  e.createdAt = date
  validate('date-' + date, e)
}
const result = { cases, schema }

const files = [
  'src/memory/knowledge/storage.ts',
  'src/memory/knowledge/lifecycle.ts',
  'src/memory/knowledge/contracts.ts',
  'src/memory/knowledge/qualification.ts',
  'src/memory/knowledge/registry.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [p, sourceHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  ...result,
}
const outputPath = 'backend/internal/knowledge/testdata/source-oracle.json',
  text = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(outputPath, 'utf8'), text)
else {
  mkdirSync('backend/internal/knowledge/testdata', { recursive: true })
  writeFileSync(outputPath, text)
}
console.log('knowledge source oracle generated/checked')
