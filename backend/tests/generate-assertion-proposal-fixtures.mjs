// Source oracle: actual unmodified TS owners, SQLite transactions and projections.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'
import { writeFileSync, readFileSync, mkdirSync } from 'node:fs'
const root = resolve(process.env.HUMANENGINE_SOURCE ?? process.cwd())
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
const output = resolve(
  process.env.ORACLE_OUTPUT ??
    `${root}/backend/internal/assertionproposal/testdata/proposal-source.json`,
)
let sequence = 0
crypto.randomUUID = () => `00000000-0000-4000-8000-${String(++sequence).padStart(12, '0')}`
syncBuiltinESMExports()
const NativeDate = Date
class FixedDate extends NativeDate {
  constructor(...args) {
    super(...(args.length ? args : ['2026-10-28T12:00:00.000Z']))
  }
  static now() {
    return 1793188800000
  }
}
globalThis.Date = FixedDate
const { Store } = await import(pathToFileURL(`${root}/src/store.ts`))
const { MemoryService } = await import(pathToFileURL(`${root}/src/memory.ts`))
const { ProposalCommand } = await import(
  pathToFileURL(`${root}/src/memory/write/proposal-command.ts`)
)
const results = []
function fixture(name, run) {
  const store = new Store(':memory:')
  store.saveScene({
    id: 's',
    kind: 'group',
    adapterId: 'synthetic',
    accountId: 'bot',
    platformSceneId: 's',
    name: 'Synthetic',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  store.set('settings', { ...store.settings, memoryProjectionEnabled: true })
  const memory = new MemoryService(store)
  assert(memory.proposals instanceof ProposalCommand)
  const traces = []
  const tableNames = [
    'memories',
    'memory_sources',
    'memory_supports',
    'memory_vectors',
    'memory_deleted',
    'memory_assertion_state',
    'memory_assertion_revisions',
    'memory_assertion_revision_sources',
    'memory_assertion_revision_parents',
    'memory_projection_outbox',
    'memory_fts',
    'memory_person_projection_state',
    'memory_recovery_intents',
    'scenes',
    'recall_tombstones',
    'source_event_heads',
    'source_event_revisions',
    'source_event_edit_keys',
    'source_event_tombstones',
  ]
  function snapshot() {
    const tables = {}
    for (const name of tableNames) {
      if (!store.db.prepare('SELECT 1 FROM sqlite_master WHERE name=?').get(name)) continue
      const rows = store.db.prepare(`SELECT * FROM ${name}`).all()
      tables[name] = rows.sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b), 'en'))
    }
    return tables
  }
  function input(content, messageId, patch = {}, ctxPatch = {}, senderId = 'p') {
    const source = { sceneId: 's', messageId, senderId }
    return [
      {
        sceneId: 's',
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...source, available: true, isBot: false }],
        ...ctxPatch,
      },
      { content, kind: 'fact', personId: 'p', sources: [source], ...patch },
    ]
  }
  function invoke(label, args) {
    const before = snapshot()
    let result, error
    try {
      result = memory.proposals.proposeAssertion(...args)
    } catch (e) {
      error = e.message
    }
    const after = snapshot()
    traces.push({
      operation: 'proposal',
      deferredCommit: name === 'nested-child-rollback-restores-bookkeeping',
      label,
      context: args[0],
      candidate: args[1],
      ...(error ? { error } : { result }),
      before,
      after,
    })
    return result
  }
  const saved = (r) => {
    assert.equal(r?.status, 'saved', JSON.stringify(r))
    return r.memory
  }
  try {
    run({ store, memory, traces, snapshot, input, invoke, saved })
    results.push({
      name,
      sourceSchemas: store.db
        .prepare(
          "SELECT name,sql FROM sqlite_master WHERE type='table' AND name LIKE 'source_event_%' ORDER BY name",
        )
        .all(),
      traces,
    })
  } finally {
    store.close()
  }
}
fixture('exact-and-semantic-corroboration', ({ memory, input, invoke, saved }) => {
  const a = saved(
    invoke('new', input('Synthetic tea', 'm1', { importance: 'high', confidence: 0.6 })),
  )
  saved(invoke('same-source-replay', input('Synthetic tea', 'm1')))
  saved(invoke('normalized-exact-second-source', input('  SYNTHETIC   TEA  ', 'm2')))
  const parent = saved(invoke('parent', input('Synthetic authority', 'p1')))
  const refs = memory.captureParentReferences({ sceneId: 's', simulation: false }, [parent])
  saved(
    invoke(
      'semantic-merge-preserves-text',
      input(
        'Synthetic equivalent',
        'm3',
        { mergeIntoId: a.id },
        {
          provenance: 'contextual',
          dependencyMemoryIds: [parent.id],
          dependencyParentReferences: refs,
        },
      ),
    ),
  )
  invoke(
    'source-only-semantic-merge-reject',
    input('Synthetic alternate', 'm4', { mergeIntoId: a.id }),
  )
})
fixture('correction-reversion-and-source-reuse', ({ input, invoke, saved }) => {
  const a = saved(invoke('tea', input('Synthetic tea', 'm1')))
  const b = saved(
    invoke('coffee-corrects-tea', input('Synthetic coffee', 'm2', { supersedesId: a.id })),
  )
  invoke('revert-reuses-old-source', input('Synthetic tea', 'm1', { supersedesId: b.id }))
  const c = saved(invoke('fresh-tea-revert', input('Synthetic tea', 'm3', { supersedesId: b.id })))
  assert.notEqual(c.id, a.id)
  invoke('append-historical-source-reject', input('Synthetic tea', 'm2'))
  const d = saved(
    invoke('fresh-coffee-revert', input('Synthetic coffee', 'm4', { supersedesId: c.id })),
  )
  assert.notEqual(d.id, b.id)
  saved(invoke('fresh-tea-second-revert', input('Synthetic tea', 'm5', { supersedesId: d.id })))
})
fixture('simulation-bot-tombstone-and-lock', ({ store, memory, input, invoke, saved }) => {
  invoke('simulation', input('Synthetic no learn', 'm0', {}, { simulation: true }))
  invoke(
    'bot',
    input(
      'Synthetic bot',
      'm0',
      {},
      {
        sourceEvidence: [
          { sceneId: 's', messageId: 'm0', senderId: 'p', available: true, isBot: true },
        ],
      },
    ),
  )
  const a = saved(invoke('new', input('Synthetic locked', 'm1')))
  memory.lock(a.id, true)
  invoke('locked', input('Synthetic locked', 'm2'))
  memory.lock(a.id, false)
  memory.delete(a.id)
  invoke('deleted-content', input('Synthetic locked', 'm3'))
  store.saveScene({ ...store.scene('s'), learningEnabled: false })
  invoke('learning-off', input('Synthetic off', 'm4'))
})
fixture('sql-failure-after-correction-write', ({ store, input, invoke, saved, snapshot }) => {
  const a = saved(invoke('new', input('Synthetic initial', 'm1')))
  store.db.exec(
    "CREATE TEMP TRIGGER fail_new_assertion BEFORE INSERT ON memories WHEN NEW.content='Synthetic fail' BEGIN SELECT RAISE(ABORT,'synthetic_second_write_failure'); END",
  )
  const before = snapshot()
  invoke('second-write-fails', input('Synthetic fail', 'm2', { supersedesId: a.id }))
  assert.deepEqual(snapshot(), before)
  store.db.exec('DROP TRIGGER fail_new_assertion')
  saved(
    invoke('recovery-after-rollback', input('Synthetic repaired', 'm3', { supersedesId: a.id })),
  )
})
fixture(
  'nested-child-rollback-restores-bookkeeping',
  ({ store, memory, input, invoke, saved, traces, snapshot }) => {
    memory.revisions.transaction(() => {
      const outer = saved(invoke('outer-write', input('Synthetic outer', 'm1')))
      const before = snapshot()
      try {
        memory.revisions.transaction(() => {
          saved(
            invoke(
              'inner-rolled-back',
              input('Synthetic transient', 'm2', { supersedesId: outer.id }),
            ),
          )
          throw Error('synthetic_child_abort')
        })()
      } catch (e) {
        assert.equal(e.message, 'synthetic_child_abort')
      }
      assert.deepEqual(snapshot(), before)
      saved(invoke('outer-survives', input('Synthetic survivor', 'm3')))
    })()
    traces.push({
      operation: 'outerCommit',
      label: 'committed-after-child-rollback',
      after: snapshot(),
    })
    assert.equal(
      store.db.prepare("SELECT count(*) n FROM memories WHERE content='Synthetic transient'").get()
        .n,
      0,
    )
  },
)
fixture(
  'correction-history-missing-and-cycle',
  ({ store, memory, input, invoke, saved, traces, snapshot }) => {
    const a = saved(invoke('root', input('Synthetic root', 'm1')))
    const b = saved(invoke('next', input('Synthetic next', 'm2', { supersedesId: a.id })))
    const call = (label, current) =>
      traces.push({
        operation: 'correctionHistory',
        label,
        id: a.id,
        current,
        before: snapshot(),
        history: memory.proposals.correctionHistory(a.id, current) ?? null,
      })
    call('complete-history', b)
    call('missing-history', { ...b, supersedesId: 'missing' })
    const original = store.db.prepare('SELECT value FROM memories WHERE id=?').get(a.id).value
    store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...a, supersedesId: b.id }), a.id)
    call('cycle-history', b)
    store.db.prepare('UPDATE memories SET value=? WHERE id=?').run(original, a.id)
  },
)
fixture('dependency-cycle-and-stale-reference', ({ memory, input, invoke, saved }) => {
  const a = saved(invoke('root', input('Synthetic dependency root', 'm1')))
  const refs = memory.captureParentReferences({ sceneId: 's', simulation: false }, [a])
  invoke(
    'direct-self-cycle',
    input(
      a.content,
      'm2',
      {},
      { provenance: 'contextual', dependencyMemoryIds: [a.id], dependencyParentReferences: refs },
    ),
  )
  const child = saved(
    invoke(
      'child',
      input(
        'Synthetic derived',
        'm3',
        {},
        { provenance: 'contextual', dependencyMemoryIds: [a.id], dependencyParentReferences: refs },
      ),
    ),
  )
  const childRefs = memory.captureParentReferences({ sceneId: 's', simulation: false }, [child])
  invoke(
    'indirect-cycle',
    input(
      a.content,
      'm4',
      {},
      {
        provenance: 'contextual',
        dependencyMemoryIds: [child.id],
        dependencyParentReferences: childRefs,
      },
    ),
  )
  saved(invoke('root-changed', input(a.content, 'm5')))
  invoke(
    'stale-parent-reference',
    input(
      'Synthetic stale',
      'm6',
      {},
      { provenance: 'contextual', dependencyMemoryIds: [a.id], dependencyParentReferences: refs },
    ),
  )
})
fixture(
  'source-event-edits-and-or-support',
  ({ store, memory, input, invoke, saved, traces, snapshot }) => {
    const identity = {
      sceneId: 's',
      messageId: 'm1',
      senderId: 'p',
      adapterId: 'synthetic',
      accountId: 'bot',
    }
    const first = store.sourceEvents.observe({
      kind: 'ingress',
      identity,
      bodyHash: 'a'.repeat(64),
      occurredAt: null,
    }).ref
    const withRef = (ref, messageId = 'm1') => ({
      sourceEvidence: [
        { sceneId: 's', messageId, senderId: 'p', available: true, isBot: false, eventRef: ref },
      ],
    })
    const a = saved(invoke('event-backed', input('Synthetic event', 'm1', {}, withRef(first))))
    saved(invoke('independent-alternative', input('Synthetic event', 'm2')))
    const next = store.sourceEvents.observe({
      kind: 'edit',
      identity,
      bodyHash: 'b'.repeat(64),
      occurredAt: null,
      editId: 'edit',
      order: { kind: 'observed_cas', expectedBase: first },
    }).ref
    invoke('stale-source-event', input('Synthetic stale event', 'm1', {}, withRef(first)))
    invoke('edited-legacy-no-ref', input('Synthetic missing ref', 'm1'))
    saved(
      invoke('current-source-event', input('Synthetic revised source', 'm1', {}, withRef(next))),
    )
    memory.recall('s', 'm1')
    traces.push({
      operation: 'recallBoundary',
      sceneId: 's',
      messageId: 'm1',
      id: a.id,
      label: 'or-support-survivor-after-recall',
      sanitized: memory.authority.sanitize(memory.repository.get(a.id)),
      after: snapshot(),
    })
    assert.notEqual(memory.repository.get(a.id).status, 'invalidated')
    memory.recall('s', 'm2')
    traces.push({
      operation: 'recallBoundary',
      sceneId: 's',
      messageId: 'm2',
      id: a.id,
      label: 'all-supports-recalled',
      after: snapshot(),
    })
    assert.equal(memory.repository.get(a.id).status, 'invalidated')
  },
)
fixture('subject-witness-mismatch', ({ input, invoke, saved }) => {
  saved(invoke('legacy-witness', input('Synthetic subject', 'm1')))
  invoke(
    'self-incompatible-witness',
    input('Synthetic subject', 'm2', { subject: { kind: 'self', sourceMessageId: 'm2' } }),
  )
  invoke(
    'self-person-mismatch',
    input('Synthetic mismatch', 'm3', {
      subject: { kind: 'self', sourceMessageId: 'm3' },
      personId: 'other',
    }),
  )
})
const files = [
  'src/memory/write/proposal-command.ts',
  'src/memory/write/source-admission.ts',
  'src/memory/write/reconciliation-policy.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/write/revision-coordinator.ts',
  'src/memory.ts',
  'src/store.ts',
  'src/memory/read/assertion-authority.ts',
  'src/memory/lifecycle/proof-invalidation.ts',
  'src/memory/storage/event-revisions.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/storage/assertion-history.ts',
  'src/memory/projections/coordinator.ts',
  'src/memory/projections/processor.ts',
  'src/memory/projections/people.ts',
  'src/memory/storage/projection-outbox.ts',
  'src/memory/lifecycle/assertion-lifecycle.ts',
  'src/memory/lifecycle/knowledge-privacy.ts',
  'src/memory/lifecycle/semantic-privacy.ts',
  'src/memory/lifecycle/commitment-privacy.ts',
  'package-lock.json',
  'backend/tests/generate-assertion-proposal-fixtures.mjs',
]
const sources = Object.fromEntries(
  files.map((p) => [
    p,
    crypto
      .createHash('sha256')
      .update(
        readFileSync(p.startsWith('backend/tests/') ? new URL(import.meta.url) : resolve(root, p)),
      )
      .digest('hex'),
  ]),
)
const runtime = {
  node: process.version,
  timezone: 'UTC',
  v8: process.versions.v8,
  icu: process.versions.icu,
  unicode: process.versions.unicode,
}
const runtimeHash = crypto.createHash('sha256').update(JSON.stringify(runtime)).digest('hex')
const raw =
  JSON.stringify(
    {
      source: 'src/memory/write/proposal-command.ts',
      clock: '2026-10-28T12:00:00.000Z',
      uuid: 'crypto.randomUUID patched before native ESM import; monotonic synthetic UUIDs',
      runtime,
      runtimeHash,
      sources,
      cases: results,
    },
    null,
    2,
  ) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(output, 'utf8'), raw)
else {
  mkdirSync(resolve(output, '..'), { recursive: true })
  writeFileSync(output, raw)
}
console.log(
  JSON.stringify({
    cases: results.length,
    traces: results.reduce((n, c) => n + c.traces.length, 0),
    check: process.argv.includes('--check'),
    output,
  }),
)
