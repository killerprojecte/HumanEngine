import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createHash as sourceHash } from 'node:crypto'
assert.equal(process.version, 'v24.22.1')
// Run from repository root after overlay integration:
// node --import tsx backend/tests/generate-semantic-purge-fixtures.ts
// The real source storage/lifecycle/schema functions generate every expected
// transition; this file does not contain a second implementation of purge.
import Database from 'better-sqlite3'
import { SemanticStorage } from '../../src/memory/semantic/storage.ts'
import { SemanticLifecycle } from '../../src/memory/semantic/lifecycle.ts'
import {
  SemanticCandidateSchema,
  SemanticAttemptSchema,
  semanticHash,
  type SemanticAuthority,
} from '../../src/memory/semantic/contracts.ts'
import { parseSemanticRecoveryEvent } from '../../src/memory/recovery/semantic-contract.ts'
const h = (x: string) => semanticHash(x)
const proof = (id: string, parents: string[] = []) => ({ proofId: id, sources: [], parents })
const parent = (id: string, proofs = [proof(`proof-${id}`)]) => ({
  ref: { id, version: 1, deletionEpoch: 0 },
  dependencyKey: h(id),
  fingerprint: h(`fingerprint-${id}`),
  proofs,
  roots: [h(`root-${id}`)],
})
const candidate = (id: string, parents = [parent('a'), parent('b')]) =>
  SemanticCandidateSchema.parse({
    schemaVersion: 1,
    id,
    sceneId: 'scene',
    personId: null,
    partitionKey: h('partition'),
    version: 1,
    inputKey: h(id),
    kind: 'summary_candidate',
    text: 'Synthetic declaration',
    selectedParentIds: parents.map((p) => p.ref.id),
    parents,
    anchorDependencies: [],
    anchorQualifications: [],
    hypothesis: false,
    status: 'candidate',
    admission: 'qualified_candidate',
    createdAt: '2026-10-29T00:00:00Z',
    applicableUntil: null,
  })
const attempt = (id: string, parentId = 'victim') =>
  SemanticAttemptSchema.parse({
    schemaVersion: 1,
    sceneId: 'scene',
    inputKey: h(id),
    status: 'started',
    version: 1,
    parents: [
      { ref: { id: parentId, version: 1, deletionEpoch: 0 }, dependencyKey: h('dependency') },
    ],
    roots: [h('root')],
  })
const episode = (id: string, dependency: ReturnType<typeof parent>) => {
  const base = candidate(id)
  const { roots: _, dependencyKey: __, ...anchorDependency } = dependency
  return SemanticCandidateSchema.parse({
    ...base,
    kind: 'episode_candidate',
    partitionKey: null,
    episodeAnchor: { id: 'anchor', version: 1 },
    episodeMembershipKey: h('membership'),
    anchorDependencies: [anchorDependency],
    anchorQualifications: [
      {
        edgeId: 'edge',
        edgeVersion: 1,
        registryVersion: 1,
        predicate: 'participates_in',
        subject: { kind: 'person', personId: 'person' },
        object: {
          kind: 'entity',
          entityId: 'anchor',
          entityKind: 'event',
          backing: { id: 'a', version: 1, deletionEpoch: 0 },
          originEdgeId: 'edge',
        },
        polarity: 'positive',
        modality: 'asserted',
        temporalScope: 'past',
        conditions: [],
        occurredAt: null,
        validFrom: null,
        validUntil: null,
        status: 'candidate',
        admission: 'qualified_candidate',
        interpretation: 'attributed_proposed_association_not_observed_event',
      },
    ],
  })
}
const cases = [
  { name: 'absent', candidates: [], attempts: [] },
  {
    name: 'direct-and-ordered-attempts',
    candidates: [candidate('z-delete', [parent('victim'), parent('b')]), candidate('a-keep')],
    attempts: [attempt('z-attempt'), attempt('a-attempt'), attempt('unaffected', 'other')],
  },
  {
    name: 'proof-survival-and-exhaustion',
    candidates: [
      candidate('z-delete', [parent('a', [proof('only', ['victim'])]), parent('b')]),
      candidate('a-survive', [
        parent('a', [proof('drop', ['victim']), proof('keep')]),
        parent('b'),
      ]),
    ],
    attempts: [],
  },
  {
    name: 'anchor-direct-and-proof',
    candidates: [
      episode('anchor-direct', parent('victim')),
      episode('anchor-exhausted', parent('anchor-parent', [proof('drop', ['victim'])])),
      episode(
        'anchor-survive',
        parent('anchor-parent', [proof('drop', ['victim']), proof('keep')]),
      ),
    ],
    attempts: [],
  },
]
const results = cases.map((input) => {
  const db = new Database(':memory:')
  const events: unknown[] = []
  const storage = new SemanticStorage(
    db,
    () => {},
    (m) => {
      events.push(parseSemanticRecoveryEvent({ kind: 'semantic_mutation', schemaVersion: 1, ...m }))
    },
  )
  // Purge never consults semantic qualification resolvers; throwing getters
  // make accidental expansion of that contract visible in the source oracle.
  const authority = new Proxy(
    {},
    {
      get() {
        throw new Error('purge_resolver_unexpected')
      },
    },
  ) as SemanticAuthority
  const lifecycle = new SemanticLifecycle(storage, authority)
  db.transaction(() => {
    for (const c of input.candidates) storage.write(c, undefined, 'admit')
    for (const a of input.attempts) storage.writeAttempt(a, undefined)
  })()
  events.length = 0
  db.transaction(() => lifecycle.purgeAssertion('victim'))()
  const rows = (table: string, order: string) =>
    db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(table)
      ? db.prepare(`SELECT * FROM ${table} ORDER BY ${order}`).all()
      : []
  const result = {
    ...input,
    target: 'victim',
    expectedCandidates: rows('memory_semantic_candidates', 'id'),
    expectedAttempts: rows('memory_semantic_attempts', 'input_key'),
    events,
  }
  db.close()
  return result
})
const contracts: any[] = []
const contract = (name: string, value: unknown, kind = 'candidate') => {
  const parsed = (kind === 'candidate' ? SemanticCandidateSchema : SemanticAttemptSchema).safeParse(
    value,
  )
  contracts.push({
    name,
    kind,
    input: structuredClone(value),
    valid: parsed.success,
    ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
  })
}
const reverseFields = (v: any): any =>
  Array.isArray(v)
    ? v.map(reverseFields)
    : v && typeof v === 'object'
      ? Object.fromEntries(
          Object.entries(v)
            .reverse()
            .map(([k, x]) => [k, reverseFields(x)]),
        )
      : v
contract('reordered-candidate', reverseFields(candidate('canonical')))
contract('reordered-attempt', reverseFields(attempt('canonical')), 'attempt')
for (const length of [5999, 6000, 6001])
  contract(`unicode-text-${length}`, { ...candidate('unicode'), text: '😀'.repeat(length) })
for (const createdAt of [
  '2024-02-29T23:59:59.999Z',
  '2025-02-29T00:00:00Z',
  '2026-11-01T00:00:00+00:00',
])
  contract(`date-${createdAt}`, { ...candidate('date'), createdAt })
contract('fractional-version', { ...candidate('fractional'), version: 1.5 })
contract(
  'unsafe-attempt-version',
  { ...attempt('overflow'), version: Number.MAX_SAFE_INTEGER + 1 },
  'attempt',
)
contract('nested-optional-episode', reverseFields(episode('episode', parent('anchor-parent'))))
const result = { cases: results, contracts }

const files = [
  'src/memory/semantic/storage.ts',
  'src/memory/semantic/lifecycle.ts',
  'src/memory/semantic/contracts.ts',
  'src/memory/knowledge/contracts.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'src/memory/recovery/semantic-contract.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [p, sourceHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  ...result,
}
const outputPath = 'backend/internal/semantic/testdata/source-oracle.json',
  text = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(outputPath, 'utf8'), text)
else {
  mkdirSync('backend/internal/semantic/testdata', { recursive: true })
  writeFileSync(outputPath, text)
}
console.log('semantic source oracle generated/checked')
