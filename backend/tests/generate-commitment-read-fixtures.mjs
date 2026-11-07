/** Actual TS Current/List/ProjectCommitment with recording assertion ports.
 * These ports isolate read-domain semantics; native composition separately tests
 * real RelationAuthority and shared database scope, not fixture authorization. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { CommitmentService } from '../../src/memory/commitments/service.ts'
import { projectCommitment } from '../../src/memory/commitments/transitions.ts'
import { commitmentBackingFingerprint } from '../../src/memory/domain/commitments.ts'
assert.equal(process.version, 'v24.22.1')
const clone = structuredClone
const context = { sceneId: 'scene', simulation: false }
const ref = (id = 'plan', version = 1) => ({ id, version, deletionEpoch: 0 })
const head = (id = 'plan', extra = {}) => ({
  id,
  sceneId: 'scene',
  latestVersion: 1,
  activeVersion: null,
  everActivatedVersion: null,
  everTerminal: null,
  deletionEpoch: 0,
  mutationVersion: 1,
  deleted: false,
  ...extra,
})
const terms = (extra = {}) => ({
  kind: 'plan',
  title: 'Synthetic plan',
  participantIds: ['a', 'b'],
  temporalScope: 'future',
  polarity: 'positive',
  modality: 'asserted',
  conditions: [],
  ...extra,
})
const views = {}
function evidence(id, kind = 'proposal', personId) {
  const source = { sceneId: 'scene', messageId: id, senderId: personId ?? 'a' }
  const record = {
    id,
    scope: { kind: 'scene', sceneId: 'scene', personId: personId ?? 'a' },
    content: `Synthetic ${id}`,
    kind: 'fact',
    epistemicKind: 'explicit',
    sources: [source],
    importance: 'low',
    confidence: 0.5,
    learnedAt: '2026-10-28T00:00:00.000Z',
    status: 'active',
    locked: false,
    origin: 'learned',
    lineageStatus: 'verified',
  }
  const proof = {
    proofId: id,
    sources: [source],
    parents: [],
    parentReferences: [],
    subjectWitness: { kind: 'self', personId: personId ?? 'a', sourceMessageId: id },
  }
  views[id] = { record, proofs: [proof] }
  return {
    backing: ref(id),
    backingFingerprint: commitmentBackingFingerprint(record),
    proofs: [proof],
    ...(personId ? { personId } : {}),
    kind,
  }
}
const proposal = evidence('proposal'),
  confirmA = evidence('confirm-a', 'confirm', 'a'),
  confirmB = evidence('confirm-b', 'confirm', 'b'),
  completeA = evidence('complete-a', 'complete', 'a'),
  completeB = evidence('complete-b', 'complete', 'b'),
  cancelA = evidence('cancel-a', 'cancel', 'a'),
  inherited = evidence('inherited'),
  outsider = evidence('outsider', 'confirm', 'outsider')
const revision = (id = 'plan', version = 1, extra = {}) => ({
  ref: ref(id, version),
  sceneId: 'scene',
  terms: terms(),
  recordedAt: '2026-10-28T00:00:00.000Z',
  evidence: [proposal],
  requiredProposalGroups: [],
  ...extra,
})
const scenarios = []
const current = (id = 'plan', version = 1, extra = {}) => ({
  kind: 'current',
  ref: ref(id, version),
  ...extra,
})
const list = (options) => ({ kind: 'list', options: options ?? {} })
function scenario(name, config = {}) {
  const db = new Database(':memory:')
  const records = clone(config.views ?? views),
    calls = []
  const authority = {
    enabled(ctx) {
      calls.push(['enabled', ctx])
      return config.enabled ?? true
    },
    learningEnabled() {
      throw Error('unexpected_learning')
    },
    current(ctx, r) {
      calls.push(['current', ctx, r])
      if (config.errorID === r.id) throw Error('synthetic_current_failure')
      const v = records[r.id]
      return v &&
        (!config.references?.[r.id] ||
          JSON.stringify(config.references[r.id]) === JSON.stringify(r))
        ? v
        : undefined
    },
    people() {
      throw Error('unexpected_people')
    },
    beforeWrite() {
      throw Error('unexpected_write')
    },
  }
  try {
    const service = new CommitmentService(db, authority)
    const heads = clone(config.heads ?? [head()]),
      revisions = clone(config.revisions ?? [revision()])
    db.transaction(() => {
      for (const h of heads) service.storage.put(h)
      for (const r of revisions) {
        const h = heads.find((h) => h.id === r.ref.id)
        service.storage.put(h, r)
      }
    })()
    const tables = () =>
      Object.fromEntries(
        [
          'memory_commitment_heads',
          'memory_commitment_revisions',
          'memory_commitment_commands',
        ].map((n) => [n, db.prepare(`SELECT * FROM ${n} ORDER BY rowid`).all()]),
      )
    const initial = tables(),
      ops = config.ops ?? [current(), list()],
      trace = []
    for (const op of ops) {
      calls.length = 0
      let result, error
      try {
        const ctx = op.context ?? context
        if (op.kind === 'current') result = service.current(ctx, op.ref)
        else if (op.kind === 'list') result = service.list(ctx, op.options)
        else if (op.kind === 'project')
          result = projectCommitment(ctx, op.head, op.revision, authority)
        else throw Error('unknown op')
      } catch (e) {
        error = e.name === 'ZodError' ? 'commitment_schema_invalid' : e.message
      }
      assert.deepEqual(tables(), initial)
      trace.push({
        ...(error
          ? { error }
          : result === undefined
            ? { undefined: true }
            : { result: clone(result) }),
        calls: clone(calls),
      })
    }
    scenarios.push({
      name,
      heads,
      revisions,
      views: records,
      enabled: config.enabled ?? true,
      references: config.references ?? {},
      errorID: config.errorID ?? null,
      ops,
      trace,
    })
  } finally {
    db.close()
  }
}
scenario('proposed default current and list')
scenario('disabled validates lifecycle then limit', {
  enabled: false,
  ops: [
    current(),
    list(),
    list({ lifecycle: 'invalid', limit: 0 }),
    list({ limit: 0 }),
    list({ limit: 1.5 }),
    list({ limit: 13 }),
  ],
})
scenario('nullish defaults and explicit empty lifecycle', {
  ops: [
    list({ limit: null, lifecycle: null }),
    list({ lifecycle: '' }),
    list({ limit: 0, lifecycle: '' }),
    { ...current(), ref: { ...ref(), mutationVersion: 999 } },
  ],
})
scenario('simulation avoids enabled port', {
  ops: [
    current('plan', 1, { context: { ...context, simulation: true } }),
    { ...list(), context: { ...context, simulation: true } },
  ],
})
scenario('current rejects missing scene epoch and inactive version', {
  ops: [
    current('absent'),
    current('plan', 2),
    { ...current(), ref: { ...ref(), deletionEpoch: 1 } },
    { ...current(), context: { ...context, sceneId: 'other' } },
  ],
})
scenario('deleted head hides current and list', { heads: [head('plan', { deleted: true })] })
scenario('missing retained revision fallback', { revisions: [] })
scenario('changed missing revision fallback', {
  heads: [head('plan', { activeVersion: 1, everActivatedVersion: 1 })],
  revisions: [],
})
scenario('both active and latest versions retain source order', {
  heads: [
    head('plan', {
      latestVersion: 2,
      activeVersion: 1,
      everActivatedVersion: 1,
      mutationVersion: 4,
    }),
  ],
  revisions: [
    revision(),
    revision('plan', 2, { terms: terms({ title: 'Synthetic revised plan' }) }),
  ],
  ops: [current(), current('plan', 2), list()],
})
const confirmed = [proposal, confirmA, confirmB]
scenario('confirmed without completions pending', {
  revisions: [revision('plan', 1, { evidence: confirmed })],
})
scenario('partial confirmations and foreign participants', {
  revisions: [revision('plan', 1, { evidence: [proposal, confirmA, confirmA, outsider] })],
})
scenario('all completed becomes done', {
  revisions: [revision('plan', 1, { evidence: [...confirmed, completeA, completeB] })],
  ops: [current(), list({ lifecycle: 'open' }), list({ lifecycle: 'terminal' })],
})
scenario('cancel wins over full completion', {
  revisions: [revision('plan', 1, { evidence: [...confirmed, completeA, completeB, cancelA] })],
})
scenario('remembered terminal loss needs review', {
  heads: [
    head('plan', {
      activeVersion: 1,
      everActivatedVersion: 1,
      everTerminal: { version: 1, state: 'done' },
    }),
  ],
  revisions: [revision('plan', 1, { evidence: confirmed })],
})
scenario('remembered other-version terminal does not taint current', {
  heads: [
    head('plan', {
      latestVersion: 2,
      activeVersion: 2,
      everActivatedVersion: 1,
      everTerminal: { version: 1, state: 'done' },
    }),
  ],
  revisions: [revision('plan', 2)],
  ops: [current('plan', 2), list()],
})
for (const [name, change] of Object.entries({
  negative: { polarity: 'negative' },
  possible: { modality: 'possible' },
  conditional: { conditions: ['if it rains'] },
  past: { temporalScope: 'past' },
  unspecified: { temporalScope: 'unspecified' },
}))
  scenario(`cannot confirm ${name}`, {
    revisions: [revision('plan', 1, { terms: terms(change), evidence: confirmed })],
  })
scenario('lost proposal omits terms', {
  views: { ...views, proposal: undefined },
  revisions: [revision('plan', 1, { evidence: confirmed })],
})
scenario('required proposal groups use AND of OR', {
  revisions: [
    revision('plan', 1, {
      evidence: confirmed,
      requiredProposalGroups: [[proposal, inherited], [inherited]],
    }),
  ],
  views: { ...views, inherited: undefined },
})
for (const [name, modify] of Object.entries({
  wrong_scene: (v) => {
    v.record.scope.sceneId = 'other'
  },
  quarantined: (v) => {
    v.record.lineageStatus = 'quarantined'
  },
  deleted: (v) => {
    v.record.status = 'deleted'
  },
  changed_content: (v) => {
    v.record.content = 'changed'
  },
  no_proofs: (v) => {
    v.proofs = []
  },
  proof_changed: (v) => {
    v.proofs[0].subjectWitness.personId = 'other'
  },
})) {
  const v = clone(views)
  modify(v.proposal)
  scenario(`current evidence excludes ${name}`, { views: v })
}
scenario('disputed evidence remains qualified', {
  views: {
    ...views,
    proposal: { ...views.proposal, record: { ...views.proposal.record, status: 'disputed' } },
  },
})
scenario('qualification ref is used without upgrading backing', {
  revisions: [
    revision('plan', 1, { evidence: [{ ...proposal, qualificationRef: ref('proposal', 2) }] }),
  ],
  references: { proposal: ref('proposal', 2) },
})
scenario('duplicate exact proof occurrences preserved', {
  revisions: [
    revision('plan', 1, {
      evidence: [{ ...proposal, proofs: [...proposal.proofs, ...proposal.proofs] }],
    }),
  ],
})
scenario('current authority error aborts result', { errorID: 'proposal' })
scenario('person filter occurs before limit', {
  heads: [head('p1'), head('p2'), head('p3')],
  revisions: [
    revision('p1'),
    revision('p2', 1, { terms: terms({ participantIds: ['outsider'] }) }),
    revision('p3'),
  ],
  ops: [
    list({ personId: 'a', limit: 1 }),
    list({ personId: 'absent' }),
    list({ personId: '' }),
    list({ limit: 2 }),
  ],
})
scenario('201-head coverage counts deleted prefix', {
  heads: Array.from({ length: 201 }, (_, i) => head(`p${i}`, { deleted: i > 0 })),
  revisions: [revision('p0')],
  ops: [list()],
})
scenario('latest rowid order and limit coverage', {
  heads: Array.from({ length: 14 }, (_, i) => head(`p${i}`)),
  revisions: Array.from({ length: 14 }, (_, i) => revision(`p${i}`)),
  ops: [list(), list({ limit: 1 }), list({ lifecycle: 'terminal' })],
})
const files = [
  'src/memory/commitments/service.ts',
  'src/memory/commitments/transitions.ts',
  'src/memory/commitments/storage.ts',
  'src/memory/domain/commitments.ts',
  'src/memory/domain/proofs.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  scenarios,
}
const path = 'backend/internal/commitments/testdata/read-source-oracle.json',
  contents = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), contents)
else writeFileSync(path, contents)
console.log(`commitment read oracle: ${scenarios.length} actual-source scenarios`)
