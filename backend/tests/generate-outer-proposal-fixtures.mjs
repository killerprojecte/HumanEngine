// Actual TS outer orchestration. Inner domain writers are explicit observation
// ports here; this contract oracle does not claim native transaction coverage.
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createHash } from 'node:crypto'
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
const root = resolve(process.env.HUMANENGINE_SOURCE ?? process.cwd())
const output =
  process.env.ORACLE_OUTPUT ??
  resolve(root, 'backend/internal/memoryservice/testdata/outer-proposal-source.json')
const { ProposalCommand } = await import(
  pathToFileURL(resolve(root, 'src/memory/write/proposal-command.ts')).href
)
const source = { sceneId: 's', messageId: 'm', senderId: 'p' }
const context = {
  sceneId: 's',
  provenance: 'contextual',
  dependencyMemoryIds: ['old'],
  dependencyParentReferences: [{ id: 'old', version: 2, deletionEpoch: 0 }],
  sourceEvidence: [
    {
      ...source,
      available: true,
      isBot: false,
      sourceText: 'Synthetic plan.',
      eventRef: { revision: 1 },
    },
  ],
  exposedCommitments: [{ id: 'plan', version: 2 }],
  exposedKnowledgeEntities: [],
  exposedKnowledgeEdges: [],
}
const candidate = {
  content: 'Synthetic statement',
  kind: 'claim',
  epistemicKind: 'explicit',
  sources: [source],
  subject: { kind: 'self', sourceMessageId: 'm' },
  commitment: { kind: 'propose', plan: { title: 'Synthetic plan' } },
}
const inputs = []
function add(name, edit = () => {}, options = {}) {
  const c = structuredClone(context),
    p = structuredClone(candidate)
  edit(c, p)
  inputs.push({ name, context: c, candidate: p, ...options })
}
add('typed-marker')
add('empty-source-text', (c) => {
  c.sourceEvidence[0].sourceText = ' \ufeff\n'
})
add('non-js-whitespace', (c) => {
  c.sourceEvidence[0].sourceText = '\u0085'
})
add('utf16-exact-limit', (c) => {
  c.sourceEvidence[0].sourceText = '😀'.repeat(1000)
})
add('utf16-over-limit', (c) => {
  c.sourceEvidence[0].sourceText = '😀'.repeat(1000) + 'x'
})
add('no-source', (_, p) => {
  p.sources = []
})
add('two-sources', (_, p) => {
  p.sources.push({ ...source, messageId: 'other' })
})
add('non-explicit', (_, p) => {
  p.epistemicKind = 'inferred'
})
add('bot-then-valid', (c) => {
  c.sourceEvidence.unshift({ ...c.sourceEvidence[0], isBot: true, sourceText: 'WRONG' })
})
add('unavailable-then-valid', (c) => {
  c.sourceEvidence.unshift({ ...c.sourceEvidence[0], available: false, sourceText: 'WRONG' })
})
add('first-valid-empty', (c) => {
  c.sourceEvidence.unshift({ ...c.sourceEvidence[0], sourceText: '' })
})
add('wrong-subject', (_, p) => {
  p.subject.kind = 'other'
})
add('wrong-subject-source', (_, p) => {
  p.subject.sourceMessageId = 'other'
})
add('mixed-relation', (_, p) => {
  p.relations = [{}]
})
add('mixed-correction', (_, p) => {
  p.supersedesId = 'old'
})
add('mixed-merge', (_, p) => {
  p.mergeIntoId = 'old'
})
add('unset-plan-kind', (_, p) => {
  p.commitment = {}
})
add('null-plan-kind', (_, p) => {
  p.commitment = { kind: null }
})
add('clear-times', (_, p) => {
  Object.assign(p, { occurredAt: 'old', validFrom: 'old', validUntil: 'old' })
})
add('no-event-ref', (c) => {
  delete c.sourceEvidence[0].eventRef
})
add('inner-reject', () => {}, { innerStatus: 'rejected' })
add('command-reject', () => {}, { commandReject: true })
add('command-throw', () => {}, { commandThrow: true })
add('ordinary-only', (_, p) => {
  delete p.commitment
})
add('relation-only', (_, p) => {
  delete p.commitment
  p.relations = [{ type: 'synthetic' }]
})
add('empty-knowledge', (_, p) => {
  p.knowledge = []
})
add('invalid-knowledge-before-commitment', (_, p) => {
  p.knowledge = [{}]
})
const knowledgeProposal = {
  registryVersion: 1,
  predicate: 'prefers_topic',
  subject: { kind: 'self', personId: 'p' },
  object: { kind: 'new', entityKind: 'topic', title: 'tools' },
  polarity: 'positive',
  modality: 'asserted',
  temporalScope: 'current',
  conditions: [],
  sourceSpans: [{ messageId: 'm', partIndex: 0, start: 0, end: 1, stance: 'self' }],
}
add('knowledge-contextual', (c, p) => {
  delete p.commitment
  p.knowledge = [knowledgeProposal]
})
add('knowledge-source-only', (c, p) => {
  delete p.commitment
  c.provenance = 'source-only'
  p.knowledge = [knowledgeProposal]
})
add('knowledge-mixed-commitment', (c, p) => {
  p.knowledge = [knowledgeProposal]
})
add('knowledge-mixed-relation', (c, p) => {
  delete p.commitment
  p.knowledge = [knowledgeProposal]
  p.relations = [{}]
})
add('default-provenance', (c, p) => {
  delete c.provenance
})
add('null-provenance', (c, p) => {
  c.provenance = null
})
add('empty-provenance', (c, p) => {
  c.provenance = ''
})
add('absent-exposure', (c, p) => {
  delete c.exposedCommitments
})
add('empty-relations', (c, p) => {
  delete p.commitment
  p.relations = []
})
const cases = []
for (const input of inputs) {
  const calls = []
  const revisions = {
    transaction: (fn) => () => {
      calls.push(['transaction'])
      return fn()
    },
    checkpoint: () => calls.push(['checkpoint']),
    history: {
      state: (id) => {
        calls.push(['state', id])
        return { version: 7, deletionEpoch: 3 }
      },
    },
  }
  const stub = {
    admit: (...args) => {
      calls.push(['relations', ...args])
      return { created: [], rejected: [] }
    },
  }
  const commitments = {
    admit: (...args) => {
      calls.push(['commitment', ...args])
      if (input.commandThrow) throw Error('synthetic_command_write_failure')
      return input.commandReject
        ? { status: 'rejected', reason: 'synthetic_rejected' }
        : { status: 'saved', item: { ref: { id: 'plan', version: 1 } } }
    },
  }
  const knowledge = {
    admit: (...args) => {
      calls.push(['knowledge', ...args])
      return { created: [], rejected: [] }
    },
  }
  const command = new ProposalCommand({}, {}, {}, revisions, stub, commitments, knowledge, {})
  command.proposeAssertion = (...args) => {
    calls.push(['assertion', ...args])
    return input.innerStatus === 'rejected'
      ? { status: 'rejected', reason: 'synthetic_inner_rejected' }
      : { status: 'saved', memory: { id: 'backing' } }
  }
  let result, error
  try {
    result = command.propose(structuredClone(input.context), structuredClone(input.candidate))
  } catch (e) {
    error = e.message
  }
  cases.push({ ...input, result, error, calls })
}
const files = [
  'src/memory/write/proposal-command.ts',
  'src/memory/knowledge/admission.ts',
  'src/memory/domain/record-values.ts',
  'package-lock.json',
]
const sources = Object.fromEntries(
  files.map((p) => [
    p,
    createHash('sha256')
      .update(readFileSync(resolve(root, p)))
      .digest('hex'),
  ]),
)
sources['backend/tests/generate-outer-proposal-fixtures.mjs'] = createHash('sha256')
  .update(readFileSync(new URL(import.meta.url)))
  .digest('hex')
const result =
  JSON.stringify(
    {
      scope:
        'Actual outer source with observed inner writer/domain ports; native transactions require separate integration tests.',
      sources,
      cases,
    },
    null,
    2,
  ) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(output, 'utf8'), result)
else writeFileSync(output, result)
console.log(`outer proposal source oracle: ${cases.length} cases`)
