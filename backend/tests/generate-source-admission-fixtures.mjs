import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { prepareKnowledgeAssertion } from '../../src/memory/knowledge/admission.ts'
import { trustedSourceEvidence } from '../../src/memory/learning/source-evidence.ts'
import { resolveSubjectAttribution } from '../../src/memory/write/subject-attribution.ts'
import { createKnowledgeService } from '../../src/memory/knowledge/facade.ts'
import { SourceAdmission } from '../../src/memory/write/source-admission.ts'
import { AssertionAuthority } from '../../src/memory/read/assertion-authority.ts'
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
const source = { sceneId: 's', messageId: 'm', senderId: 'p' }
const ref = { eventId: 'source-event', revision: 1, deletionEpoch: 0 }
const context = {
  sceneId: 's',
  simulation: false,
  provenance: 'source-only',
  sourceEvidence: [
    {
      ...source,
      available: true,
      isBot: false,
      mentions: [{ partIndex: 2, userId: 'q', isBot: false }],
      sentAt: '2026-02-28T10:00:00Z',
      receivedAt: '2026-02-28T10:00:01+00:00',
    },
  ],
}
const candidate = {
  content: 'Synthetic fact',
  kind: 'fact',
  sources: [source],
  subject: { kind: 'self', sourceMessageId: 'm' },
}
const proposal = {
  registryVersion: 1,
  predicate: 'uses_tool',
  subject: { kind: 'self', personId: 'p' },
  object: { kind: 'new', entityKind: 'tool', title: 'Synthetic tool' },
  polarity: 'positive',
  modality: 'asserted',
  temporalScope: 'current',
  conditions: [],
  sourceSpans: [{ messageId: 'm', partIndex: 0, start: 0, end: 1, stance: 'self' }],
}
const cases = []
const add = (op, name, args, fn) =>
  cases.push({
    op,
    name,
    args: JSON.stringify(args),
    expected: JSON.stringify(fn(...args)),
  })
for (const provenance of ['source-only', 'contextual', undefined])
  add(
    'prepare',
    `declaration-${provenance}`,
    [
      {
        ...context,
        provenance,
        dependencyMemoryIds: ['parent'],
        dependencyParentReferences: [{ id: 'parent', version: 1, deletionEpoch: 0 }],
        exposedKnowledgeEntities: [
          {
            entityId: 'e',
            backingRef: { id: 'parent', version: 1, deletionEpoch: 0 },
          },
        ],
      },
      { ...candidate, knowledge: [proposal] },
    ],
    prepareKnowledgeAssertion,
  )
for (const [name, patch] of Object.entries({
  empty: { knowledge: [] },
  many: { knowledge: [proposal, proposal, proposal, proposal] },
  mixed: { commitment: {} },
  relations: { relations: [{}] },
  merge: { mergeIntoId: 'x' },
  supersede: { supersedesId: 'x' },
  reported: {
    knowledge: [
      {
        ...proposal,
        sourceSpans: [{ ...proposal.sourceSpans[0], stance: 'quoted' }],
      },
    ],
  },
  qualified: {
    kind: 'claim',
    epistemicKind: 'reported',
    knowledge: [
      {
        ...proposal,
        sourceSpans: [{ ...proposal.sourceSpans[0], stance: 'reported' }],
      },
    ],
  },
}))
  add(
    'prepare',
    name,
    [context, { ...candidate, knowledge: [proposal], ...patch }],
    prepareKnowledgeAssertion,
  )
for (const [name, p] of Object.entries({
  unknown: { ...proposal, extra: true },
  astralLimit: {
    ...proposal,
    object: { kind: 'new', entityKind: 'tool', title: '😀'.repeat(251) },
  },
  reorder: Object.fromEntries(Object.entries(proposal).reverse()),
  badProperty: { ...proposal, property: 'count' },
  backward: {
    ...proposal,
    validFrom: '2026-02-02T00:00:00Z',
    validUntil: '2026-02-01T00:00:00Z',
  },
  literal: {
    ...proposal,
    predicate: 'has_property',
    property: 'count',
    object: { kind: 'literal', valueType: 'number', value: 4, unit: null },
  },
  existing: { ...proposal, object: { kind: 'existing', entityId: 'e' } },
  endBeforeStart: {
    ...proposal,
    sourceSpans: [{ ...proposal.sourceSpans[0], start: 9, end: 1 }],
  },
  wrongRange: {
    ...proposal,
    sourceSpans: [{ ...proposal.sourceSpans[0], partIndex: 4096 }],
  },
}))
  add(
    'prepare',
    name,
    [
      { ...context, provenance: 'contextual' },
      { ...candidate, knowledge: [p] },
    ],
    prepareKnowledgeAssertion,
  )
for (const [name, c] of Object.entries({
  self: candidate,
  legacy: { ...candidate, subject: undefined, personId: 'p' },
  unresolved: { ...candidate, subject: { kind: 'unresolved' } },
  mentioned: {
    ...candidate,
    subject: {
      kind: 'mentioned',
      sourceMessageId: 'm',
      personId: 'q',
      mentionPartIndex: 2,
    },
  },
  mentionWrong: {
    ...candidate,
    subject: {
      kind: 'mentioned',
      sourceMessageId: 'm',
      personId: 'q',
      mentionPartIndex: 0,
    },
  },
  mismatch: { ...candidate, personId: 'q' },
  noSource: { ...candidate, sources: [] },
  badTime: { ...candidate, occurredAt: '2026-02-30T00:00:00Z' },
  offset: { ...candidate, occurredAt: '2026-02-28T00:00:00+23:59' },
  sourceNotCited: {
    ...candidate,
    subject: { kind: 'self', sourceMessageId: 'other' },
  },
  invalidSubject: { ...candidate, subject: { kind: 'other' } },
}))
  add('subject', name, [context, c], resolveSubjectAttribution)
add(
  'subject',
  'simulation',
  [{ ...context, simulation: true }, candidate],
  resolveSubjectAttribution,
)
const message = {
  origin: 'live',
  sceneId: 's',
  id: 'm',
  senderId: 'p',
  status: 'available',
  parts: [
    { type: 'text', text: 'A😀' },
    { type: 'image', url: 'not persisted' },
    { type: 'mention', userId: 'q' },
    { type: 'text', text: 'B' },
    { type: 'mention', userId: 'bot' },
  ],
  sourceEventRef: ref,
  sentAt: '2026-02-30T10:00:00Z',
  receivedAt: '2026-03-02T10:00:00+00:00',
}
for (const [name, patch] of Object.entries({
  live: {},
  nonlive: { origin: 'history' },
  bot: { senderId: 'bot' },
  limited: { sourceEventRegistrationLimited: true },
  unavailable: { status: 'recalled' },
  invalidClock: { sentAt: '2026-03-02', receivedAt: 'invalid' },
  noRef: { sourceEventRef: undefined },
}))
  add('evidence', name, [[{ ...message, ...patch }], []], (messages, recalled) =>
    trustedSourceEvidence(messages, (s, m) => recalled.includes(m)),
  )
add('evidence', 'recalled', [[message], ['m']], (messages, recalled) =>
  trustedSourceEvidence(messages, (s, m) => recalled.includes(m)),
)
for (const [name, patch, ctxPatch, cPatch] of [
  ['legacy', {}, {}, {}],
  ['registered', { registered: true }, {}, {}],
  ['edited', { registered: true, edited: true }, {}, {}],
  [
    'registered-ref',
    { registered: true },
    { sourceEvidence: [{ ...context.sourceEvidence[0], eventRef: ref }] },
    {},
  ],
  [
    'stale-ref',
    { registered: true, edited: true },
    { sourceEvidence: [{ ...context.sourceEvidence[0], eventRef: ref }] },
    {},
  ],
  ['disabled', { enabled: false }, {}, {}],
  ['recall', { recalled: true }, {}, {}],
  ['missing', {}, {}, { sources: [] }],
  ['foreign', {}, {}, { sources: [{ ...source, sceneId: 'other' }] }],
  ['simulation', {}, { simulation: true }, {}],
  ['no-provenance', {}, { provenance: undefined }, {}],
  ['contextual-missing', {}, { provenance: 'contextual' }, {}],
  ['contextual-empty', {}, { provenance: 'contextual', dependencyMemoryIds: [] }, {}],
  ['duplicate', {}, {}, { sources: [source, source] }],
  ['unavailable', {}, { sourceEvidence: [{ ...context.sourceEvidence[0], available: false }] }, {}],
  ['bot', {}, { sourceEvidence: [{ ...context.sourceEvidence[0], isBot: true }] }, {}],
  ['custom-bot', {}, { botUserId: 'p' }, {}],
  ['subject-mismatch', {}, {}, { personId: 'q' }],
  ['forged-ref', {}, {}, { sources: [{ ...source, eventRef: ref }] }],
  ['wrong-identity', { registered: true, foreignIdentity: true }, {}, {}],
]) {
  const state = { enabled: true, ...patch },
    ctx = { ...context, ...ctxPatch },
    c = { ...candidate, ...cPatch }
  const store = {
    scene: () => ({
      id: 's',
      adapterId: 'adapter',
      accountId: 'account',
      learningEnabled: state.enabled,
    }),
    recalled: () => !!state.recalled,
    sourceEvents: {
      registered: () =>
        state.registered
          ? {
              identity: {
                adapterId: 'adapter',
                accountId: state.foreignIdentity ? 'other' : 'account',
                senderId: 'p',
              },
            }
          : undefined,
      hasEdits: () => !!state.edited,
      current: () => ({ eligible: !state.edited }),
    },
  }
  const repo = { get: () => undefined }
  const authority = new AssertionAuthority(store, repo)
  const admission = new SourceAdmission(store, repo, authority)
  add('admit', name, [state, ctx, c], (_, ctx, c) => admission.prepare(ctx, c))
}
const spanContext = {
  ...context,
  sourceEvidence: [
    {
      ...context.sourceEvidence[0],
      parts: [{ type: 'other' }, { type: 'text', text: 'A😀B' }],
    },
  ],
}
const spanProposal = {
  ...proposal,
  sourceSpans: [{ messageId: 'm', partIndex: 1, start: 1, end: 3, stance: 'self' }],
}
for (const [name, state, ctx, p, proofs] of [
  ['astral', {}, spanContext, spanProposal, [{ sources: [source] }]],
  [
    'split-start',
    {},
    spanContext,
    {
      ...spanProposal,
      sourceSpans: [{ ...spanProposal.sourceSpans[0], start: 2 }],
    },
    [{ sources: [source] }],
  ],
  [
    'split-end',
    {},
    spanContext,
    {
      ...spanProposal,
      sourceSpans: [{ ...spanProposal.sourceSpans[0], end: 2 }],
    },
    [{ sources: [source] }],
  ],
  [
    'nontext',
    {},
    spanContext,
    {
      ...spanProposal,
      sourceSpans: [{ ...spanProposal.sourceSpans[0], partIndex: 0 }],
    },
    [{ sources: [source] }],
  ],
  ['no-proof', {}, spanContext, spanProposal, []],
  [
    'wrong-sender',
    {},
    spanContext,
    { ...spanProposal, subject: { kind: 'self', personId: 'q' } },
    [{ sources: [source] }],
  ],
  ['registered-no-ref', { registered: true }, spanContext, spanProposal, [{ sources: [source] }]],
  [
    'registered-ref',
    { registered: true },
    {
      ...spanContext,
      sourceEvidence: [{ ...spanContext.sourceEvidence[0], eventRef: ref }],
    },
    spanProposal,
    [{ sources: [{ ...source, eventRef: ref }] }],
  ],
  [
    'registered-wrong-proof',
    { registered: true },
    {
      ...spanContext,
      sourceEvidence: [{ ...spanContext.sourceEvidence[0], eventRef: ref }],
    },
    spanProposal,
    [{ sources: [source] }],
  ],
  [
    'edited-ref',
    { registered: true, edited: true },
    {
      ...spanContext,
      sourceEvidence: [{ ...spanContext.sourceEvidence[0], eventRef: ref }],
    },
    spanProposal,
    [{ sources: [{ ...source, eventRef: ref }] }],
  ],
  ['recalled', { recalled: true }, spanContext, spanProposal, [{ sources: [source] }]],
  [
    'empty-range',
    {},
    spanContext,
    {
      ...spanProposal,
      sourceSpans: [{ ...spanProposal.sourceSpans[0], end: 1 }],
    },
    [{ sources: [source] }],
  ],
]) {
  const store = {
    db: {},
    settings: {},
    scene: () => ({ adapterId: 'adapter', accountId: 'account' }),
    recalled: () => !!state.recalled,
    sourceEvents: {
      registered: () =>
        state.registered
          ? {
              identity: {
                adapterId: 'adapter',
                accountId: 'account',
                senderId: 'p',
              },
            }
          : undefined,
      hasEdits: () => !!state.edited,
      current: () => ({ eligible: !state.edited }),
    },
  }
  const service = createKnowledgeService(store, () => undefined)
  add('spans', name, [state, ctx, p, proofs], (_, ctx, p, proofs) => {
    try {
      return { spans: service.spans(ctx, p, proofs) }
    } catch (error) {
      return { error: error.message }
    }
  })
}
// Exact historical source privacy is stricter than current OR applicability.
const parentSourceA = { ...source, messageId: 'parent-a' }
const parentSourceB = { ...source, messageId: 'parent-b' }
const parentRecord = {
  id: 'parent',
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  kind: 'fact',
  content: 'Synthetic parent',
  sources: [parentSourceA, parentSourceB],
  importance: 'medium',
  confidence: 0.7,
  learnedAt: '2026-01-01T00:00:00Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
}
const parentProofs = [parentSourceA, parentSourceB].map((source) => ({
  sources: [source],
  parents: [],
  parentReferences: [],
}))
const parentRef = { id: 'parent', version: 1, deletionEpoch: 0 }
const parentContext = {
  ...context,
  provenance: 'contextual',
  dependencyMemoryIds: ['parent'],
  dependencyParentReferences: [parentRef],
}
for (const recalled of [[], ['parent-a'], ['parent-a', 'parent-b']]) {
  add(
    'parent-recall',
    `recalled-${recalled.length}`,
    [recalled, parentRecord, parentProofs, parentContext, candidate],
    (recalled, parent, proofs, ctx, c) => {
      const store = {
        scene: () => ({ adapterId: 'adapter', accountId: 'account', learningEnabled: true }),
        recalled: (_, id) => recalled.includes(id),
        sourceEvents: { registered: () => undefined, hasEdits: () => false },
      }
      const repo = {
        get: (id) => (id === 'parent' ? parent : undefined),
        supports: (id) => (id === 'parent' ? proofs : []),
      }
      const revisions = {
        history: {
          state: () => ({ version: 1, deletionEpoch: 0, deleted: false }),
          revision: (id, version) =>
            id === 'parent' && version === 1 ? { record: parent, supports: proofs } : undefined,
        },
      }
      const authority = new AssertionAuthority(store, repo, revisions)
      const admission = new SourceAdmission(store, repo, authority).prepare(ctx, c)
      return {
        validParent: authority.validParent(parent.scope, 'parent'),
        currentApplicable: authority.currentApplicable(parent),
        bound: authority.proofParentsAvailable(
          parent.scope,
          { sources: c.sources, parents: ['parent'], parentReferences: [parentRef] },
          'current',
        ),
        admission: admission.status === 'accepted' ? 'accepted' : admission.reason,
      }
    },
  )
}
const files = [
  'src/memory/write/source-admission.ts',
  'src/memory/write/subject-attribution.ts',
  'src/memory/knowledge/admission.ts',
  'src/memory/knowledge/facade.ts',
  'src/memory/knowledge/service.ts',
  'src/memory/knowledge/qualification.ts',
  'src/memory/knowledge/contracts.ts',
  'src/memory/learning/contracts.ts',
  'src/memory/learning/source-evidence.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/read/assertion-authority.ts',
  'src/memory/read/domain-source-events.ts',
  'src/memory/read/bound-parents.ts',
  'src/memory/read/proof-parents.ts',
  'src/memory/read/provenance.ts',
  'src/memory/read/applicability.ts',
  'package-lock.json',
  'backend/tests/generate-source-admission-fixtures.mjs',
]
const sources = Object.fromEntries(
  files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
const raw =
  JSON.stringify({ node: process.version, timezone: 'UTC', sources, cases }, null, 2) + '\n'
const path = 'backend/internal/sourceadmission/testdata/source-admission.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else {
  mkdirSync('backend/internal/sourceadmission/testdata', { recursive: true })
  writeFileSync(path, raw)
}
console.log(`${cases.length} actual-source cases`)
