// Source oracle: actual unmodified TS owners, SQLite transactions and projections.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { resolve } from 'node:path'
import { writeFileSync, readFileSync, mkdirSync } from 'node:fs'
const root = resolve(process.env.HUMANENGINE_SOURCE ?? process.cwd())
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
const output = resolve(
  process.env.ORACLE_OUTPUT ??
    `${root}/backend/internal/memoryservice/testdata/privacy-source.json`,
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

const { knowledgeBackingFingerprint } = await import(
  pathToFileURL(`${root}/src/memory/knowledge/qualification.ts`)
)
const { semanticHash } = await import(pathToFileURL(`${root}/src/memory/semantic/contracts.ts`))
const cases = []
const schemas = {}
function scenario(name, run) {
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
  const memory = new MemoryService(store),
    steps = []
  const names = [
    'config',
    'memory_knowledge_edges',
    'memory_semantic_candidates',
    'memory_semantic_attempts',
    'memory_commitment_heads',
    'memory_commitment_revisions',
    'memory_commitment_commands',
    'memory_commitment_transitions',
    'memory_commitment_temporal_windows',
    'memory_identity_registry',
    'memory_working_set_pins',
    'memory_working_set_items',
    'memory_working_set_jobs',
    'memory_working_set_receipts',
    'memory_relation_entities',
    'memory_relation_candidates',
    'memory_relation_tombstones',
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
    'memory_command_receipts',
    'scenes',
    'recall_tombstones',
  ]
  const snapshot = () => {
    for (const row of store.db
      .prepare("SELECT name,sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL")
      .all())
      if (names.includes(row.name)) schemas[row.name] = row.sql
    return Object.fromEntries(
      names
        .filter((n) => store.db.prepare('SELECT 1 FROM sqlite_master WHERE name=?').get(n))
        .map((n) => [n, store.db.prepare(`SELECT * FROM ${n}`).all()]),
    )
  }
  const call = (operation, ...args) => {
    const before = snapshot()
    let result, error
    try {
      if (operation === 'prepareRecallTwice') {
        const execute = memory.prepareRecall(...args)
        const affected = execute()
        let repeatError
        try {
          execute()
        } catch (e) {
          repeatError = e.message
        }
        result = { affected, repeatError }
      } else result = memory[operation](...args)
    } catch (e) {
      error = e.message
    }
    steps.push({ operation, args, before, after: snapshot(), result, error })
    return result
  }
  run(call, memory, store)
  cases.push({ name, steps })
  store.close()
}

const proposal = (text, message) => [
  {
    sceneId: 's',
    simulation: false,
    provenance: 'source-only',
    sourceEvidence: [
      { sceneId: 's', messageId: message, senderId: 'p', available: true, isBot: false },
    ],
  },
  {
    content: text,
    kind: 'fact',
    personId: 'p',
    sources: [{ sceneId: 's', messageId: message, senderId: 'p' }],
  },
]
scenario('delete-current-and-repeat', (call) => {
  const r = call('propose', ...proposal('Synthetic delete', 'm'))
  assert.equal(r.status, 'saved')
  call('delete', r.memory.id)
  call('delete', r.memory.id)
  call('propose', ...proposal('Synthetic delete', 'new'))
})
scenario('recall-source-and-repeat', (call) => {
  const r = call('propose', ...proposal('Synthetic recall', 'm'))
  assert.equal(r.status, 'saved')
  call('recall', 's', 'm')
  call('recall', 's', 'm')
  call('propose', ...proposal('Synthetic recall again', 'm'))
})
scenario('recall-shared-source-independent-records', (call) => {
  call('propose', ...proposal('Synthetic first', 'm'))
  call('propose', ...proposal('Synthetic second', 'm'))
  call('recall', 'other', 'm')
  call('recall', 's', 'm')
})
scenario('recall-or-support-survivor', (call) => {
  call('propose', ...proposal('Synthetic shared statement', 'm1'))
  call('propose', ...proposal('Synthetic shared statement', 'm2'))
  call('recall', 's', 'm1')
  call('recall', 's', 'm2')
})
scenario('delete-manual-source', (call) => {
  const r = call('create', {
    scope: { kind: 'scene', sceneId: 's' },
    content: 'Synthetic manual delete',
    kind: 'fact',
    sources: [{ sceneId: 's', messageId: 'm', senderId: 'p' }],
  })
  call('delete', r.id)
})

const typedProposal = (content, message) => {
  const [ctx, candidate] = proposal(content, message)
  ctx.sourceEvidence[0].sourceText = 'I like tools'
  ctx.sourceEvidence[0].parts = [{ type: 'text', text: 'I like tools' }]
  candidate.epistemicKind = 'explicit'
  candidate.subject = { kind: 'self', sourceMessageId: message }
  candidate.knowledge = [
    {
      registryVersion: 1,
      predicate: 'prefers_topic',
      subject: { kind: 'self', personId: 'p' },
      object: { kind: 'new', entityKind: 'topic', title: 'tools' },
      polarity: 'positive',
      modality: 'asserted',
      temporalScope: 'current',
      conditions: [],
      sourceSpans: [{ messageId: message, partIndex: 0, start: 7, end: 12, stance: 'self' }],
    },
  ]
  return [ctx, candidate]
}
function enableGraphs(store) {
  store.set('settings', {
    ...store.settings,
    memoryKnowledgeEnabled: true,
    memoryKnowledgeAutoAdmissionEnabled: true,
    memoryCommitmentsEnabled: true,
    memorySemanticEnabled: true,
  })
}
function semanticSeed(memory, store, ids) {
  const ctx = { sceneId: 's', simulation: false }
  const refs = memory.captureParentReferences(ctx, memory.listByIds(ctx, ids))
  const selections = refs.map((ref) => ({
    ref,
    dependencyKey: memory.semanticCurrent(ctx, ref).dependencyKey,
  }))
  const result = store.db.transaction(() =>
    memory.semantic.admit(ctx, selections, [
      {
        kind: 'summary_candidate',
        selectedParentIds: ids,
        text: 'Synthetic combined declarations',
        hypothesis: false,
      },
    ]),
  )()
  assert.equal(result.created.length, 1, JSON.stringify(result))
  return result.created[0]
}
scenario('typed-knowledge-recall', (call, memory, store) => {
  enableGraphs(store)
  const r = call('propose', ...typedProposal('Synthetic graph', 'm'))
  assert.equal(memory.knowledge.storage.rows().length, 1)
  call('recall', 's', 'm')
  assert.equal(memory.knowledge.storage.rows().length, 0)
})
scenario('typed-knowledge-delete', (call, memory, store) => {
  enableGraphs(store)
  const r = call('propose', ...typedProposal('Synthetic graph delete', 'm'))
  call('delete', r.memory.id)
  assert.equal(memory.knowledge.storage.rows().length, 0)
})
scenario('semantic-partition-delete', (call, memory, store) => {
  enableGraphs(store)
  const a = call('propose', ...typedProposal('Synthetic first semantic', 'm1'))
  const b = call('propose', ...typedProposal('Synthetic second semantic', 'm2'))
  semanticSeed(memory, store, [a.memory.id, b.memory.id])
  call('delete', a.memory.id)
  assert.equal(memory.semantic.storage.rows().length, 0)
})
scenario('semantic-partition-or-recall-requalify', (call, memory, store) => {
  enableGraphs(store)
  const a = call('propose', ...typedProposal('Synthetic repeated semantic', 'm1'))
  call('propose', ...typedProposal('Synthetic repeated semantic', 'm3'))
  const b = call('propose', ...typedProposal('Synthetic other semantic', 'm2'))
  semanticSeed(memory, store, [a.memory.id, b.memory.id])
  call('recall', 's', 'm1')
  call('recall', 's', 'm3')
})
scenario('commitment-temporal-recall', (call, memory, store) => {
  enableGraphs(store)
  const args = typedProposal('Synthetic commitment source', 'm')
  delete args[1].knowledge
  args[1].commitment = {
    kind: 'propose',
    terms: {
      kind: 'plan',
      title: 'Synthetic plan',
      participantIds: ['p'],
      temporalScope: 'future',
      polarity: 'positive',
      modality: 'asserted',
      conditions: [],
    },
  }
  const r = call('propose', ...args)
  assert.equal(r.status, 'saved')
  assert.ok(memory.commitments.storage.allHeads().length)
  call('recall', 's', 'm')
})
scenario('historical-parent-recall', (call, memory, store) => {
  const a = call('propose', ...proposal('Synthetic historical parent', 'm1'))
  const refs = memory.captureParentReferences({ sceneId: 's', simulation: false }, [a.memory])
  const args = proposal('Synthetic historical child', 'm2')
  args[0].provenance = 'contextual'
  args[0].dependencyMemoryIds = [a.memory.id]
  args[0].dependencyParentReferences = refs
  call('propose', ...args)
  call('recall', 's', 'm1')
})

scenario('workspace-projection-delete', (call, memory, store) => {
  store.set('settings', {
    ...store.settings,
    memoryWorkingSetEnabled: true,
    memoryProjectionEnabled: true,
  })
  const r = call('propose', ...proposal('Synthetic workspace delete', 'm'))
  call('delete', r.memory.id)
})
scenario('workspace-commitment-recall', (call, memory, store) => {
  enableGraphs(store)
  store.set('settings', {
    ...store.settings,
    memoryWorkingSetEnabled: true,
    memoryProjectionEnabled: true,
  })
  const args = typedProposal('Synthetic workspace plan', 'm')
  delete args[1].knowledge
  args[1].commitment = {
    kind: 'propose',
    terms: {
      kind: 'plan',
      title: 'Synthetic workspace plan',
      participantIds: ['p'],
      temporalScope: 'future',
      polarity: 'positive',
      modality: 'asserted',
      conditions: [],
    },
  }
  call('propose', ...args)
  call('recall', 's', 'm')
})

scenario('delete-promoted-learned-fingerprints', (call, memory, store) => {
  const args = proposal('Synthetic portable declaration', 'm')
  delete args[1].personId
  const r = call('propose', ...args)
  const promoted = call('promotePublic', { sceneId: 's', simulation: false }, r.memory.id)
  assert.equal(promoted.status, 'saved')
  call('delete', promoted.memory.id)
})
scenario('recall-unverified-scene-quarantine', (call, memory, store) => {
  const r = call('create', {
    scope: { kind: 'scene', sceneId: 's' },
    content: 'Synthetic unverified legacy',
    kind: 'fact',
    sources: [],
  })
  const record = { ...r, lineageStatus: 'unverified' }
  store.db.prepare('UPDATE memories SET value=? WHERE id=?').run(JSON.stringify(record), r.id)
  call('recall', 's', 'unrelated')
})
scenario('prepared-recall-consumed-once', (call, memory, store) => {
  call('propose', ...proposal('Synthetic prepared recall', 'm'))
  call('prepareRecallTwice', 's', 'm')
})

function episodeSeed(call, memory, store) {
  enableGraphs(store)
  const ctx = { sceneId: 's', simulation: false }
  const a = call('propose', ...typedProposal('Synthetic episode first', 'm1'))
  const b = call('propose', ...typedProposal('Synthetic episode second', 'm2'))
  const refs = memory.captureParentReferences(ctx, [a.memory, b.memory])
  const views = refs.map((ref) => memory.semanticCurrent(ctx, ref))
  const entity = {
    id: 'episode-event',
    sceneId: 's',
    kind: 'event',
    title: 'Synthetic episode',
    backing: refs[0],
    originEdgeId: 'episode-edge-0',
  }
  store.db.transaction(() => {
    for (let i = 0; i < 2; i++) {
      const view = views[i],
        proofs = view.proofs,
        source = proofs[0].sources[0]
      const edge = {
        id: `episode-edge-${i}`,
        version: 1,
        sceneId: 's',
        registryVersion: 1,
        predicate: 'participates_in',
        subject: { kind: 'person', personId: 'p' },
        object: { kind: 'entity', entity },
        polarity: 'positive',
        modality: 'asserted',
        temporalScope: 'past',
        conditions: [],
        backing: refs[i],
        backingFingerprint: knowledgeBackingFingerprint(view.record),
        proofs,
        spans: [
          {
            source,
            rootId: semanticHash(['s', source.messageId]),
            partIndex: 0,
            start: 0,
            end: 1,
            spanHash: 'a'.repeat(64),
            stance: 'self',
          },
        ],
        dependencies: i
          ? [
              {
                entity,
                backingFingerprint: knowledgeBackingFingerprint(views[0].record),
                proofs: views[0].proofs,
              },
            ]
          : [],
        readDependencies: [],
        status: 'candidate',
        admission: 'qualified_candidate',
        admissionPolicyVersion: 1,
        createdAt: '2026-10-28T12:00:00.000Z',
      }
      memory.knowledge.storage.put(edge, `episode-key-${i}`)
    }
  })()
  const discovery = memory.semanticEpisodes.discover(ctx, refs[0])
  assert.equal(discovery.groups.length, 1, JSON.stringify(discovery))
  const group = discovery.groups[0]
  const result = store.db.transaction(() =>
    memory.semantic.admit(
      ctx,
      group.parents,
      [
        {
          kind: 'episode_candidate',
          selectedParentIds: group.parents.map((p) => p.ref.id),
          episodeAnchor: group.anchor,
          text: 'Synthetic episode candidate',
          hypothesis: false,
        },
      ],
      {
        anchor: group.anchor,
        membershipKey: group.anchorMembershipKey,
        anchorDependencies: group.anchorDependencies,
        anchorQualifications: group.anchorQualifications,
      },
    ),
  )()
  assert.equal(result.created.length, 1, JSON.stringify(result))
  return a.memory.id
}
scenario('episode-knowledge-dependency-recall', (call, memory, store) => {
  episodeSeed(call, memory, store)
  call('recall', 's', 'm1')
})
scenario('episode-knowledge-dependency-delete', (call, memory, store) => {
  const id = episodeSeed(call, memory, store)
  call('delete', id)
})

const sourceFiles = [
  'src/memory.ts',
  'src/store.ts',
  'src/memory/semantic/lifecycle.ts',
  'src/memory/semantic/candidates.ts',
  'src/memory/semantic/contracts.ts',
  'src/memory/knowledge/lifecycle.ts',
  'src/memory/identities/facade.ts',
  'src/memory/lifecycle/assertion-lifecycle.ts',
  'src/memory/lifecycle/commitment-privacy.ts',
  'src/memory/lifecycle/knowledge-privacy.ts',
  'src/memory/lifecycle/semantic-privacy.ts',
  'src/memory/lifecycle/proof-invalidation.ts',
  'src/memory/write/revision-coordinator.ts',
  'src/memory/storage/assertion-repository.ts',
  'package-lock.json',
]
const sources = Object.fromEntries(
  sourceFiles.map((p) => [
    p,
    crypto
      .createHash('sha256')
      .update(readFileSync(resolve(root, p)))
      .digest('hex'),
  ]),
)
const raw =
  JSON.stringify(
    {
      node: process.version,
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      sources,
      schemas,
      cases,
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
    scenarios: cases.length,
    operations: cases.reduce((n, c) => n + c.steps.length, 0),
  }),
)
