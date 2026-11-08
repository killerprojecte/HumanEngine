/** Actual TS normal admission and temporal writer. Synthetic assertion ports
 * isolate this domain; separate native tests use real assertion/workspace owners. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'
assert.equal(process.version, 'v24.22.1')
const root = resolve(process.env.SOURCE_ROOT ?? '.')
const require = createRequire(resolve(root, 'package.json'))
const Database = require('better-sqlite3')
const { CommitmentService } = await import(
  pathToFileURL(resolve(root, 'src/memory/commitments/service.ts'))
)
const now = '2026-10-28T13:00:00.000Z'
const ref = (id, version = 1) => ({ id, version, deletionEpoch: 0 })
const views = {}
for (const [id, person] of [
  ['a1', 'a'],
  ['b1', 'b'],
  ['a2', 'a'],
  ['b2', 'b'],
]) {
  const source = { sceneId: 'scene', messageId: id, senderId: person }
  views[id] = {
    record: {
      id,
      scope: { kind: 'scene', sceneId: 'scene', personId: person },
      content: `Synthetic ${id}`,
      kind: 'fact',
      epistemicKind: 'explicit',
      sources: [source],
      importance: 'low',
      confidence: 0.5,
      learnedAt: now,
      status: 'active',
      locked: false,
      origin: 'learned',
      lineageStatus: 'verified',
    },
    proofs: [
      {
        proofId: id,
        sources: [source],
        parents: [],
        parentReferences: [],
        subjectWitness: { kind: 'self', personId: person, sourceMessageId: id },
      },
    ],
  }
}
const terms = {
  kind: 'plan',
  title: 'Synthetic plan',
  participantIds: ['a', 'b'],
  temporalScope: 'future',
  polarity: 'positive',
  modality: 'asserted',
  conditions: [],
}
const db = new Database(':memory:')
let id = 0,
  events = [],
  changes = []
const authority = {
  enabled: () => true,
  learningEnabled: () => true,
  current: (_, r) => (r.version === 1 && r.deletionEpoch === 0 ? views[r.id] : undefined),
  people: () => ['a', 'b'],
  beforeWrite: (c) => changes.push(structuredClone(c)),
  afterWrite: (h, n, p) =>
    events.push(['afterWrite', structuredClone(h), structuredClone(n), structuredClone(p)]),
}
const service = new CommitmentService(db, authority, {
  now: () => now,
  id: () => `command-${++id}`,
})
const steps = []
function step(name, backing, command, ctxOverride = {}) {
  const target = command.target
  const ctx = {
    sceneId: 'scene',
    simulation: false,
    provenance: target ? 'contextual' : 'source-only',
    sourceEvidence: [
      {
        sceneId: 'scene',
        messageId: backing,
        senderId: views[backing].record.scope.personId,
        available: true,
        isBot: false,
        mentions: [
          { userId: 'a', isBot: false },
          { userId: 'b', isBot: false },
        ],
      },
    ],
    exposedRefs: target
      ? [{ ...target, mutationVersion: service.storage.head(target.id)?.mutationVersion }]
      : [],
    ...ctxOverride,
  }
  events = []
  changes = []
  let result, error
  try {
    result = db.transaction(() => service.admit(ctx, ref(backing), command))()
  } catch (e) {
    error = e.message
  }
  steps.push(
    JSON.parse(
      JSON.stringify({
        name,
        ctx,
        backing: ref(backing),
        command,
        ...(error ? { error } : { result }),
        events,
        changes,
        commands: db
          .prepare('SELECT key,id,version FROM memory_commitment_commands ORDER BY key')
          .all(),
        snapshot: service.snapshot('command-1'),
      }),
    ),
  )
}
step('propose two participants', 'a1', { kind: 'propose', terms })
step('duplicate proposal', 'a1', { kind: 'propose', terms })
step('unexposed confirm', 'b1', { kind: 'confirm', target: ref('command-1') }, { exposedRefs: [] })
step(
  'source-only confirm',
  'b1',
  { kind: 'confirm', target: ref('command-1') },
  { provenance: 'source-only' },
)
step(
  'stale snapshot',
  'b1',
  { kind: 'confirm', target: ref('command-1') },
  { exposedRefs: [{ ...ref('command-1'), mutationVersion: 99 }] },
)
step('confirm other participant', 'b1', { kind: 'confirm', target: ref('command-1') })
step('duplicate confirmation with fresh exposure', 'b1', {
  kind: 'confirm',
  target: ref('command-1'),
})
step('complete first participant', 'a2', { kind: 'complete', target: ref('command-1') })
step('complete second participant', 'b2', { kind: 'complete', target: ref('command-1') })
step('amend retains proposal group', 'a2', {
  kind: 'amend',
  target: ref('command-1'),
  terms: { ...terms, title: 'Synthetic amended' },
})
step('old target confirm conflict', 'b1', { kind: 'confirm', target: ref('command-1') })
step('confirm amended version', 'b2', { kind: 'confirm', target: ref('command-1', 2) })
step('cancel amended version', 'a1', { kind: 'cancel', target: ref('command-1', 2) })
step('unwitnessed participant', 'a1', {
  kind: 'propose',
  terms: { ...terms, participantIds: ['outsider'] },
})
step('simulation short circuits invalid inputs', 'a1', { kind: 'bogus' }, { simulation: true })
step('strict command schema', 'a1', { kind: 'complete', target: ref('command-1', 2), extra: true })
const phases = []
for (const phase of ['afterWrite', 'remember']) {
  const db = new Database(':memory:')
  db.exec(
    `CREATE TABLE phase_probe(value TEXT); CREATE TRIGGER phase_abort BEFORE INSERT ON phase_probe WHEN NEW.value='abort' BEGIN SELECT RAISE(ABORT,'synthetic_phase_abort'); END`,
  )
  let afterCalls = 0
  const service = new CommitmentService(
    db,
    {
      ...authority,
      beforeWrite: () => {},
      afterWrite: () => {
        afterCalls++
        if (phase === 'afterWrite') db.prepare("INSERT INTO phase_probe VALUES('abort')").run()
      },
    },
    { now: () => now, id: () => 'phase-plan' },
  )
  if (phase === 'remember')
    db.exec(
      `CREATE TRIGGER remember_abort BEFORE INSERT ON memory_commitment_commands BEGIN SELECT RAISE(ABORT,'synthetic_phase_abort'); END`,
    )
  const input = { ...steps[0].ctx }
  let error
  db.transaction(() => {
    try {
      service.admit(input, ref('a1'), { kind: 'propose', terms })
    } catch (e) {
      error = e.message
    }
    db.prepare("INSERT INTO phase_probe VALUES('continued')").run()
  })()
  phases.push({
    phase,
    error,
    afterCalls,
    snapshot: service.snapshot('phase-plan'),
    commands: db
      .prepare('SELECT key,id,version FROM memory_commitment_commands ORDER BY key')
      .all(),
    continued: db.prepare('SELECT value FROM phase_probe').all(),
  })
  db.close()
}
const policies = []
for (const name of ['participants8', 'participants9', 'evidence64', 'revision64', 'groups64']) {
  const db = new Database(':memory:')
  const all = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']
  const service = new CommitmentService(
    db,
    { ...authority, people: () => all, beforeWrite: () => {}, afterWrite: () => {} },
    { now: () => now, id: () => 'policy-plan' },
  )
  const context = {
    ...steps[0].ctx,
    sourceEvidence: [
      {
        sceneId: 'scene',
        messageId: 'a1',
        senderId: 'a',
        available: true,
        isBot: false,
        mentions: all.map((userId) => ({ userId, isBot: false })),
      },
    ],
  }
  const operations = []
  if (name.startsWith('participants')) {
    operations.push({
      backing: ref('a1'),
      command: {
        kind: 'propose',
        terms: { ...terms, participantIds: name === 'participants8' ? all : [...all, 'ninth'] },
      },
    })
  } else {
    db.transaction(() =>
      service.admit(context, ref('a1'), {
        kind: 'propose',
        terms: { ...terms, participantIds: ['a'] },
      }),
    )()
    const head = service.storage.head('policy-plan'),
      revision = service.storage.revision('policy-plan', 1)
    if (name === 'evidence64') {
      revision.evidence = [
        revision.evidence[0],
        ...Array.from({ length: 62 }, () => structuredClone(revision.evidence[1])),
      ]
      db.transaction(() => service.storage.put(head, revision))()
      operations.push({
        backing: ref('a2'),
        command: { kind: 'confirm', target: ref('policy-plan') },
      })
      operations.push({
        backing: ref('a1'),
        command: { kind: 'confirm', target: ref('policy-plan') },
      })
    } else if (name === 'revision64') {
      Object.assign(head, { latestVersion: 64, activeVersion: 64, everActivatedVersion: 64 })
      revision.ref.version = 64
      db.transaction(() => service.storage.put(head, revision))()
      operations.push({
        backing: ref('a2'),
        command: {
          kind: 'amend',
          target: ref('policy-plan', 64),
          terms: { ...terms, participantIds: ['a'] },
        },
      })
    } else {
      revision.requiredProposalGroups = Array.from({ length: 64 }, () => [
        structuredClone(revision.evidence[0]),
      ])
      db.transaction(() => service.storage.put(head, revision))()
      operations.push({
        backing: ref('a2'),
        command: {
          kind: 'amend',
          target: ref('policy-plan'),
          terms: { ...terms, participantIds: ['a'] },
        },
      })
    }
  }
  const initial = service.snapshot('policy-plan')
  const cases = []
  for (const operation of operations) {
    const target = operation.command.target
    const ctx = {
      ...context,
      provenance: target ? 'contextual' : 'source-only',
      exposedRefs: target
        ? [{ ...target, mutationVersion: service.storage.head(target.id).mutationVersion }]
        : [],
    }
    let result, error
    try {
      result = db.transaction(() => service.admit(ctx, operation.backing, operation.command))()
    } catch (e) {
      error = e.message
    }
    cases.push(
      JSON.parse(
        JSON.stringify({
          ...operation,
          ctx,
          ...(error ? { error } : { result }),
          snapshot: service.snapshot('policy-plan'),
        }),
      ),
    )
  }
  policies.push({ name, initial, cases })
  db.close()
}
const { MemoryWorkspace } = await import(
  pathToFileURL(resolve(root, 'src/memory/read/memory-workspace.ts'))
)
const workspaceBatches = []
const workspace = {
  store: { db: { inTransaction: true }, assertRecoveryReady() {} },
  access: { invalidate() {}, terminalInvalidate() {} },
  mutations: {
    committed() {},
    sourceEdited(ctx, refs) {
      workspaceBatches.push(structuredClone(refs))
    },
  },
}
MemoryWorkspace.prototype.onPlanCommitted.call(
  workspace,
  {
    id: 'batch-plan',
    sceneId: 'scene',
    latestVersion: 1,
    activeVersion: 1,
    mutationVersion: 1,
    deletionEpoch: 0,
    deleted: false,
  },
  [
    {
      evidence: Array.from({ length: 41 }, (_, i) => ({ backing: ref(`batch-${i}`) })),
      requiredProposalGroups: [],
    },
  ],
)
assert.deepEqual(
  workspaceBatches.map((batch) => batch.length),
  [40, 1],
)
const keys = new Set()
function collect(v) {
  if (Array.isArray(v)) v.forEach(collect)
  else if (v && typeof v === 'object')
    for (const [k, x] of Object.entries(v)) {
      keys.add(k)
      collect(x)
    }
}
collect({ views, steps, phases, policies, workspaceBatches })
const comparisons = Object.fromEntries(
  [...keys]
    .sort()
    .map((a) => [a, Object.fromEntries([...keys].sort().map((b) => [b, a.localeCompare(b)]))]),
)
const files = [
  'src/memory/commitments/service.ts',
  'src/memory/commitments/transitions.ts',
  'src/memory/commitments/storage.ts',
  'src/memory/commitments/temporal-store.ts',
  'src/memory/commitments/temporal-contracts.ts',
  'src/memory/domain/commitments.ts',
  'src/memory/domain/proofs.ts',
  'src/memory.ts',
  'src/memory/read/memory-workspace.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((p) => [
      p,
      createHash('sha256')
        .update(readFileSync(resolve(root, p)))
        .digest('hex'),
    ]),
  ),
  now,
  views,
  steps,
  phases,
  policies,
  workspaceBatches,
  comparisons,
}
const path = resolve(
  process.env.OUTPUT_ROOT ?? root,
  'backend/internal/commitments/testdata/command-source-oracle.json',
)
const contents = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), contents)
else writeFileSync(path, contents)
console.log(`commitment command oracle: ${steps.length} actual-source operations`)
db.close()
