/** Actual workspace resolver with explicit recording current-snapshot ports.
 * Native integration separately proves the concrete shared read owner. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { WorkspaceAuthority } from '../../src/memory/read/workspace-authority.ts'
import { MemoryWorkspace } from '../../src/memory/read/memory-workspace.ts'
assert.equal(process.version, 'v24.22.1')
const ref = (id = 'a', extra = {}) => ({ id, version: 1, deletionEpoch: 0, ...extra })
const source = (id = 'm') => ({ sceneId: 's', messageId: id, senderId: 'p' })
const snapshot = (id = 'a', extra = {}) => ({
  record: {
    id,
    scope: { kind: 'scene', sceneId: 's', personId: 'p' },
    content: `Synthetic ${id}`,
    kind: 'experience',
    status: 'active',
    sources: [source()],
    learnedAt: '2000-01-01T00:00:00.000Z',
    ...extra,
  },
  proofs: [{ proofId: 'one', sources: [source()], parents: [] }],
})
const plan = (id = 'p', extra = {}) => ({
  ref: ref(id),
  sceneId: 's',
  mutationVersion: 1,
  lifecycle: 'pending',
  recordedAt: '2001-01-01T00:00:00.000Z',
  evidence: [{ backing: ref(), proofs: [{ proofId: 'one', sources: [source()], parents: [] }] }],
  requiredProposalGroups: [],
  ...extra,
})
const head = (id = 'p', extra = {}) => ({
  id,
  latestVersion: 1,
  activeVersion: null,
  deletionEpoch: 0,
  deleted: false,
  ...extra,
})
const scenarios = []
function add(name, extra = {}) {
  const input = {
    context: { sceneId: 's', simulation: false },
    refs: [{ kind: 'assertion', ...ref() }],
    assertions: { a: snapshot() },
    plans: { 'p:1': plan() },
    heads: [head()],
    sourceHeads: {},
    enabled: true,
    ...extra,
  }
  const calls = []
  const ctx = (c) => ({ sceneId: c.sceneId, simulation: c.simulation })
  const ready = () => calls.push(['ready'])
  const sourceHead = (scene, message) => {
    calls.push(['sourceHead', scene, message])
    return input.sourceHeads[`${scene}:${message}`]
  }
  const current = (c, r) => {
    calls.push([
      'assertion',
      ctx(c),
      { id: r.id, version: r.version, deletionEpoch: r.deletionEpoch },
    ])
    return input.assertions[r.id]
  }
  const currentPlan = (c, r) => {
    calls.push(['plan', ctx(c), { id: r.id, version: r.version, deletionEpoch: r.deletionEpoch }])
    return input.plans[`${r.id}:${r.version}`]
  }
  const authority = new WorkspaceAuthority({
    ready,
    current,
    sourceHead,
    currentPlan,
    list() {
      throw Error('unexpected list')
    },
    capture() {
      throw Error('unexpected capture')
    },
  })
  const settings = { memoryColdAfterDays: 30, memoryReviewAfterDays: 7 }
  Object.defineProperty(settings, 'memoryCommitmentsEnabled', {
    get() {
      calls.push(['enabled', ctx(input.context)])
      return input.enabled
    },
  })
  const db = new Database(':memory:')
  const workspace = new MemoryWorkspace(
    { db, settings, assertRecoveryReady: ready, sourceEvents: { registered: sourceHead } },
    authority,
    {
      current: currentPlan,
      list() {
        throw Error('unexpected list')
      },
      storage: {
        heads(scene) {
          calls.push(['heads', scene])
          return input.heads
        },
      },
    },
    () => false,
  )
  calls.length = 0
  let result
  try {
    result = { value: workspace.resolve(input.context, input.refs) }
  } catch (e) {
    result = { error: e.message }
  }
  scenarios.push({ name, ...input, ...result, calls })
  db.close()
}
add('open plan protects assertion')
add('no plans', { heads: [], plans: {} })
add('simulation before width', {
  context: { sceneId: 's', simulation: true },
  refs: Array.from({ length: 41 }, () => ({ kind: 'assertion', ...ref() })),
})
add('source width', { refs: Array.from({ length: 41 }, () => ({ kind: 'assertion', ...ref() })) })
add('unknown kinds filtered before width', {
  refs: Array.from({ length: 41 }, () => ({ kind: 'unknown', ...ref() })),
})
add('disabled retained plans unknown', { enabled: false })
add('disabled empty complete', { enabled: false, heads: [], plans: {} })
add('201 heads unknown', {
  heads: Array.from({ length: 201 }, (_, i) => head(`p${i}`, { deleted: true })),
  plans: {},
})
add('exact 200 heads complete', {
  heads: Array.from({ length: 200 }, (_, i) => head(`p${i}`, { deleted: true })),
  plans: {},
})
add('needs review precedence', {
  heads: [head('p'), head('q')],
  plans: { 'p:1': plan(), 'q:1': plan('q', { lifecycle: 'needs_review' }) },
})
add('active latest unique order', {
  heads: [head('p', { activeVersion: 1, latestVersion: 2 })],
  plans: {
    'p:1': plan(),
    'p:2': plan('p', {
      ref: ref('p', { version: 2 }),
      lifecycle: 'needs_review',
      mutationVersion: 2,
    }),
  },
})
add('qualification reference overrides backing', {
  plans: {
    'p:1': plan('p', { evidence: [{ backing: ref('old'), qualificationRef: ref(), proofs: [] }] }),
  },
})
add('terminal plan no assertion protection', {
  plans: { 'p:1': plan('p', { lifecycle: 'completed' }) },
})
add('both reference kinds reorder', {
  refs: [
    { kind: 'commitment', ...ref('p'), mutationVersion: 1 },
    { kind: 'assertion', ...ref() },
  ],
})
add('changed mutation omitted', { refs: [{ kind: 'commitment', ...ref('p'), mutationVersion: 2 }] })
add('duplicate references retained', {
  refs: [
    { kind: 'assertion', ...ref() },
    { kind: 'assertion', ...ref() },
  ],
})
add('missing assertion', { assertions: {} })
add('internal assertion omitted', { assertions: { a: snapshot('a', { internalUse: 'working' }) } })
add('shared assertion omitted', {
  assertions: { a: snapshot('a', { scope: { kind: 'sharedPublicKnowledge' } }) },
})
add('wrong scene omitted', {
  assertions: { a: snapshot('a', { scope: { kind: 'scene', sceneId: 'other' } }) },
})
add('empty person and invalid time', {
  assertions: {
    a: snapshot('a', {
      scope: { kind: 'scene', sceneId: 's', personId: '' },
      learnedAt: 'invalid',
      epistemicKind: 'explicit',
    }),
  },
})
add('ordered duplicate source-head occurrences', {
  assertions: {
    a: {
      ...snapshot(),
      proofs: [{ proofId: 'two', sources: [source('m'), source('n'), source('m')], parents: [] }],
    },
  },
  sourceHeads: {
    's:m': {
      ref: { eventId: 'synthetic', revision: 2, deletionEpoch: 0 },
      bodyCoverage: 'metadata_only',
    },
    's:n': { second: true },
  },
  plans: {
    'p:1': plan('p', {
      requiredProposalGroups: [
        [{ backing: ref(), proofs: [{ proofId: 'g', sources: [source('n')], parents: [] }] }],
      ],
    }),
  },
})
const files = [
  'src/memory/read/workspace-authority.ts',
  'src/memory/read/memory-workspace.ts',
  'src/memory/working-set/contracts.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  scenarios,
}
const path = 'backend/internal/memoryworkspace/testdata/resolve-source-oracle.json',
  data = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), data)
else {
  mkdirSync('backend/internal/memoryworkspace/testdata', { recursive: true })
  writeFileSync(path, data)
}
console.log(`workspace resolve oracle: ${scenarios.length} actual-source scenarios`)
