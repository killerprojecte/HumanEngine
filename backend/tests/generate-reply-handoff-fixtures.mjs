/** Synthetic source differential oracle for the planner -> replyer handoff. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { buildReplyBrief } from '../../src/runtime/reply-brief.ts'
import { packReplyEvidence } from '../../src/runtime/reply-evidence-budget.ts'
import { selectEvidenceGroups } from '../../src/runtime/evidence-selector.ts'
import { knowledgeEvidence } from '../../src/runtime/knowledge-evidence.ts'
import { buildReplyPlan } from '../../src/runtime/reply-plan.ts'
import { ContextProjection } from '../../src/runtime/context-projection.ts'
assert.equal(process.version, 'v24.22.1')
process.env.TZ = 'UTC'
const check = process.argv.includes('--check')
const corpus = JSON.parse(readFileSync('backend/internal/replyplan/testdata/node.json', 'utf8'))
const cases = [],
  loops = []
const capture = (fn) => {
  try {
    return { value: fn() }
  } catch (e) {
    return { error: e.message }
  }
}
const add = (name, kind, input, run) =>
  cases.push({
    name,
    kind,
    input: JSON.stringify(input),
    expected: JSON.stringify(capture(() => run(structuredClone(input)))),
  })
const plan = (patch = {}) => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: ['m1'],
  contextMessageIds: ['m1'],
  strategy: 'Qualified reply.',
  constraints: ['Do not invent confirmation.'],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: ['Be concise.'],
  memorySelections: [],
  stateEvidenceIds: [],
  actions: [],
  ...patch,
})
const ref = (id = 'record') => ({ id, version: 1, deletionEpoch: 0 })
const record = (id = 'record', content = 'Reported, not verified. 中文😀\n"\\') => ({
  id,
  content,
  kind: 'assertion',
  status: 'disputed',
  lineageStatus: 'unverified',
  parentBinding: { mode: 'all', parents: ['parent'] },
  scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
  occurredAt: null,
  learnedAt: '2026-11-01',
  observedAt: '2026-11-02',
  validFrom: null,
  validUntil: '2027-01-01',
  epistemicKind: 'reported',
  sources: [{ sceneId: 'scene', messageId: 'm1', senderId: 'speaker', raw: 'REMOVE' }],
  raw: 'REMOVE',
  confidence: 0.5,
})
const declaration = {
  kind: 'confirm',
  personId: 'person',
  backing: ref(),
  qualificationRef: ref('qualification'),
  proofs: [
    {
      proofId: 'REMOVE',
      sourceObservations: [
        {
          sceneId: 'scene',
          messageId: 'm1',
          senderId: 'speaker',
          sentAt: '2026-11-01',
          receivedAt: '2026-11-02',
          raw: 'REMOVE',
        },
      ],
      sources: [
        {
          sceneId: 'scene',
          messageId: 'm1',
          senderId: 'speaker',
          eventRef: { eventId: 'event', revision: 2, deletionEpoch: 0, raw: 'REMOVE' },
        },
      ],
    },
  ],
}
const view = {
  ref: ref('commitment'),
  mutationVersion: 4,
  sceneId: 'scene',
  recordedAt: '2026-11-01',
  confirmation: 'incomplete',
  lifecycle: 'needs_review',
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  changed: true,
  confirmedParticipantIds: ['person'],
  missingParticipantIds: ['other'],
  completedParticipantIds: [],
  cancelledParticipantIds: [],
  terms: {
    kind: 'plan',
    title: 'Synthetic plan',
    participantIds: ['person', 'other'],
    polarity: 'negative',
    modality: 'possible',
    temporalScope: 'future',
    conditions: ['ONLY IF BOTH AGREE; NOT PERMANENT'],
    timeQualification: '19:00 date unknown',
    occurredAt: null,
    validFrom: null,
    validUntil: null,
    raw: 'REMOVE',
  },
  evidence: [declaration],
  requiredProposalGroups: [[declaration], [declaration, declaration], null],
  raw: 'REMOVE',
}
const briefCase = (name, input) =>
  add(name, 'brief', { plan: plan(), evidence: [], actionReceipts: [], ...input }, (x) =>
    buildReplyBrief(x.plan, x.evidence, x.actionReceipts, x.imageContext, x.relatedMessageIds),
  )
briefCase('empty', {})
const records = Array.from({ length: 12 }, (_, i) =>
  record('r' + i, `Record ${i}. ` + '中文'.repeat(70) + '但未确认，不是永久偏好'),
)
for (const shape of ['array', 'items', 'single'])
  briefCase('memory-' + shape, {
    plan: plan({
      memorySelections: [
        { evidenceId: 'mem', memoryIds: shape === 'single' ? ['r0'] : ['r7', 'r2'] },
      ],
      facts: [{ text: 'Qualified', evidenceIds: ['mem'] }],
    }),
    evidence: [
      {
        id: 'mem',
        toolName: 'memory.person',
        value:
          shape === 'array'
            ? records
            : shape === 'items'
              ? {
                  trust: 'recalled',
                  coverage: 'limited',
                  items: records,
                  sources: records[0].sources,
                }
              : records[0],
      },
    ],
  })
briefCase('unselected-and-missing-memory', {
  plan: plan({ facts: [{ text: 'Citation only', evidenceIds: ['m'] }] }),
  evidence: [
    {
      id: 'm',
      toolName: 'memory.search',
      value: { trust: 'recalled', status: 'limited', coverage: 'limited', items: records },
    },
    { id: 'u', toolName: 'memory.person', value: records },
  ],
})
briefCase('duplicate-selections-last-wins', {
  plan: plan({
    memorySelections: [
      { evidenceId: 'm', memoryIds: ['r2'] },
      { evidenceId: 'm', memoryIds: ['r7'] },
    ],
  }),
  evidence: [{ id: 'm', toolName: 'memory.search', value: records }],
})
briefCase('memory-null-and-id-coercion', {
  plan: plan({ memorySelections: [{ evidenceId: 'm', memoryIds: ['undefined', '1', 'null'] }] }),
  evidence: [
    {
      id: 'm',
      toolName: 'memory.search',
      value: [
        null,
        { content: 'No ID' },
        { id: 1, content: 'Numeric ID' },
        { id: null, content: null },
      ],
    },
  ],
})
briefCase('memory-relations-complete-qualification', {
  plan: plan({ memorySelections: [{ evidenceId: 'm', memoryIds: ['record'] }] }),
  evidence: [
    {
      id: 'm',
      toolName: 'memory.relations',
      value: [
        {
          ...record(),
          relationCoverage: { status: 'limited' },
          relationEvidence: [
            {
              id: 'edge',
              version: 3,
              predicate: 'reports',
              personId: 'person',
              polarity: 'negative',
              modality: 'possible',
              temporalScope: 'past',
              conditions: ['not now'],
              requiredMemoryIds: ['record', 'dependency'],
              target: { entityId: 'entity', kind: 'plan', title: 'Reported plan', raw: 'REMOVE' },
              sources: record().sources,
              proofId: 'REMOVE',
            },
          ],
        },
      ],
    },
  ],
})
const vision = {
  trust: 'untrusted',
  framePolicy: 'per_image',
  images: [
    { messageId: 'm1', status: 'unavailable', reason: 'not_configured' },
    {
      messageId: 'related',
      description: 'Uncertain',
      framePolicy: 'first_frame_only',
      raw: 'REMOVE',
    },
    { messageId: 'unrelated', description: 'REMOVE' },
  ],
  omitted: [
    { messageId: 'related', reason: 'budget' },
    { messageId: 'unrelated', reason: 'REMOVE' },
  ],
}
briefCase('vision-recursive-related-and-automatic-receipts', {
  evidence: [
    { id: 'auto', value: vision },
    { id: 'omit', value: { omitted: [{ messageId: 'm1', reason: 'budget' }] } },
    { id: 'none', value: { images: [] } },
  ],
  imageContext: [null, [vision, { imageContext: [[vision], false] }], { images: [] }],
  relatedMessageIds: ['related'],
})
briefCase('web-assets-messages', {
  plan: plan({
    facts: ['web', 'web-list', 'msg', 'msg-inline', 'msg-list'].map((id) => ({
      text: id,
      evidenceIds: [id],
    })),
    assets: [{ evidenceId: 'assets', assetId: 'a' }],
  }),
  evidence: [
    {
      id: 'web',
      toolName: 'web-read.read',
      value: {
        title: 'Title',
        url: 'https://example.invalid',
        sourceUrl: 'https://example.invalid/source',
        snippet: 'Brief',
        publishedAt: 'today',
        text: 'REMOVE',
        headers: { authorization: 'REMOVE' },
        coverage: 'partial',
        trust: 'untrusted',
      },
    },
    {
      id: 'web-list',
      value: {
        results: [{ title: 'Result', body: 'REMOVE' }, null],
        items: [{ title: 'SHOULD_NOT_USE' }],
      },
    },
    {
      id: 'assets',
      toolName: 'memes.search',
      value: {
        status: 'available',
        items: [
          {
            id: 'a',
            available: true,
            disabled: false,
            tags: {
              emotion: ['happy'],
              intent: ['reply'],
              memeMeaning: 'yes',
              appearance: 'REMOVE',
            },
          },
          { id: 'b', tags: {} },
        ],
      },
    },
    {
      id: 'msg',
      value: {
        id: 'm1',
        senderId: 'speaker',
        parts: [{ type: 'text', text: 'Literal [image/unknown]' }],
        raw: 'REMOVE',
      },
    },
    {
      id: 'msg-inline',
      value: {
        id: 'other',
        senderId: 'speaker',
        senderDisplayName: 'Name',
        sentAt: 'now',
        parts: [],
        replyToId: 'm1',
        status: 'available',
        raw: 'REMOVE',
      },
    },
    {
      id: 'msg-list',
      toolName: 'messages.byUser',
      value: { coverage: 'limited', items: [{ id: 'm1' }, { id: 2 }, null, { id: 'other' }] },
    },
  ],
})
briefCase('actions-status-precedence', {
  plan: plan({ actions: [{ toolName: 'test', arguments: { raw: 'REMOVE' } }] }),
  actionReceipts: [false, true, undefined]
    .flatMap((ok) =>
      [undefined, null, 'error'].map((error) => ({
        id: 'a',
        toolName: 'test',
        value: {
          ok,
          error,
          status: 'queued',
          actionTaken: 'proposed',
          positive: false,
          disabled: false,
          memory: { id: 'memory' },
          habit: { id: 'habit' },
          raw: 'REMOVE',
        },
      })),
    )
    .concat([
      { id: 'unconfirmed', value: {} },
      { id: 'confirmed', value: { ok: true } },
    ]),
})
for (const [tool, state] of [
  ['memory.commitments', { stateKind: 'commitment', view, coverage: 'limited' }],
  [
    'memory.commitmentHistory',
    {
      stateKind: 'commitment_history',
      anchor: { ...ref(), mutationVersion: 4 },
      historyWindow: { maxRevisions: 20, omittedRevisions: 1, throughVersion: 3, raw: 'REMOVE' },
      revisions: [view, null, { ref: null }],
      coverage: 'limited',
      interpretation: 'previously_recorded_not_current_truth',
      sourceBodyCoverage: 'unavailable',
    },
  ],
  [
    'memory.history',
    {
      stateKind: 'assertion_history',
      kind: 'assertion_history',
      interpretation: 'previously_recorded_not_current_truth',
      coverage: { status: 'limited' },
      sourceBodyCoverage: 'unavailable',
      asOfRecorded: 'then',
      validAt: null,
      revisions: [
        {
          assertionId: 'record',
          version: 1,
          recordedAt: null,
          validFrom: null,
          validTo: null,
          validityCoverage: 'unknown',
          ref: ref(),
          record: record(),
          raw: 'REMOVE',
        },
        null,
      ],
    },
  ],
  [
    'memory.commitmentTimeline',
    {
      stateKind: 'commitment_timeline',
      anchor: { ...ref(), mutationVersion: 4 },
      window: {
        maxTransitions: 64,
        firstRecordedAt: 'then',
        firstMutationVersion: 1,
        prunedThroughMutationVersion: 0,
        throughMutationVersion: 4,
        raw: 'REMOVE',
      },
      recordedStates: [
        {
          recordedAt: 'then',
          mutationVersion: 2,
          reason: 'recorded',
          activeVersion: 1,
          latestVersion: 1,
          status: 'available',
          states: [view, null],
        },
      ],
      coverage: 'limited',
      reasons: ['pruned'],
      interpretation: 'previously_recorded_declaration_state_not_current_truth',
      sourceBodyCoverage: 'unavailable',
      stateTime: 'server_recorded_not_occurred',
      asOfRecorded: null,
    },
  ],
]) {
  for (const chosen of [true, false])
    briefCase(tool + '-' + chosen, {
      plan: plan({
        stateEvidenceIds: chosen ? ['s'] : [],
        facts: [{ text: 'qualified', evidenceIds: ['s'] }],
      }),
      evidence: [{ id: 's', toolName: tool, value: [state, { stateKind: 'unknown' }] }],
    })
}
// Reuse only synthetic inputs, never old expected outputs. The current actual
// source determines projections for every workspace/refinement mutation.
for (const c of corpus.cases.filter((c) => c.kind === 'workspace')) {
  const x = JSON.parse(c.input)
  briefCase('workspace/' + c.name, {
    plan: plan({ stateEvidenceIds: ['s'] }),
    evidence: [{ id: 's', toolName: x.tool, value: [x.value] }],
  })
}
// Exercise the alternate valid workspace entry variants, not only invalid mutations.
for (const tool of ['memory.workingSet', 'memory.episodes']) {
  const base = JSON.parse(corpus.cases.find((c) => c.name === tool + '-valid').input).value
  if (tool === 'memory.workingSet')
    base.page.items = [
      {
        kind: 'commitment',
        view,
        accessibility: {
          ...base.page.items[0].accessibility,
          pinned: true,
          pinIntent: true,
          pinStatus: 'active',
          pinRevision: 3,
          policyCoverage: 'complete',
          metadataRevision: 2,
          contextSelectedCount: 7,
          lastSelectedAt: 'then',
          protection: { reason: 'policy' },
        },
      },
    ]
  else
    base.page.items = [
      {
        kind: 'reported_experience',
        assertionRef: ref(),
        record: { ...record(), kind: 'experience' },
        time: { occurredAt: null, recordedAt: 'then', raw: 'REMOVE' },
      },
    ]
  briefCase('workspace-alternate/' + tool, {
    plan: plan({ stateEvidenceIds: ['s'] }),
    evidence: [{ id: 's', toolName: tool, value: [base] }],
  })
}
briefCase('selected-memory-short-circuits-vision-probe', {
  plan: plan({ facts: [{ text: 'Qualified', evidenceIds: ['m'] }] }),
  evidence: [
    {
      id: 'm',
      toolName: 'memory.search',
      value: { images: [{ messageId: { toString: 'shadow' } }] },
    },
  ],
})
add(
  'knowledge-own-proto-field',
  'knowledge',
  { value: JSON.parse('{"__proto__":{"spanHash":"remove","meaning":"keep"},"proofId":"remove"}') },
  (x) => knowledgeEvidence(x.value),
)
add(
  'knowledge-recursive',
  'knowledge',
  {
    value: {
      proofId: 'remove',
      nested: [
        {
          backingFingerprint: 'remove',
          conditions: ['unless x'],
          proofs: [{ spanHash: 'remove', sourceObservations: [{ receivedAt: 'keep' }] }],
        },
      ],
      span: { spanHash: 'remove', text: 'Keep' },
      __proto__: null,
    },
  },
  (x) => knowledgeEvidence(x.value),
)
const bytes = (s) => new TextEncoder().encode(s).length
function capabilities(input, trace, caller) {
  let serialCalls = 0,
    tokenCalls = 0
  const projection = input.serializer === 'projection' ? new ContextProjection('scene') : undefined
  const serialize = (value) => {
    serialCalls++
    trace.push({ kind: 'serialize', value: JSON.stringify(value) })
    if (input.mutateOriginal && serialCalls === 1) {
      if (caller.candidates) caller.candidates[0].value.text = 'CHANGED CALLER'
      else {
        caller.brief.plan.strategy = 'CHANGED CALLER'
        caller.envelope.now = 'CHANGED CALLER'
      }
    }
    if (input.serializer === 'throw') throw new Error('PRIVATE_ERROR')
    if (input.serializer === 'invalid') return '{'
    if (input.serializer === 'rawSurrogate') return '"\ud800"'
    if (input.serializer === 'whitespaceJSON') return '  [null]  '
    if (input.serializer === 'unstable') return JSON.stringify({ value, n: serialCalls })
    if (input.serializer === 'finalGrowth')
      return JSON.stringify({ pad: serialCalls >= 3 ? 'x'.repeat(50) : '' })
    if (input.serializer === 'envelope')
      return JSON.stringify({ now: 'fixture', evidence: value, constraints: ['不可猜测'] })
    return projection ? projection.stringify(value, ['now']) : JSON.stringify(value)
  }
  const budget = { ...(input.budget ?? { mode: 'bytes', limit: 100000 }) }
  if (budget.mode === 'tokens') {
    const token = input.tokenizer ?? {}
    budget.tokenizer = {
      verified: token.verified ?? true,
      version: token.version ?? 'fixture-v1',
      modelIdentity: token.modelIdentity ?? 'fixture-model',
      count: (serialized) => {
        tokenCalls++
        trace.push({ kind: 'tokenize', serialized })
        if (token.mode === 'throw') throw new Error('PRIVATE_ERROR')
        if (token.mode === 'sequence') return tokenCalls
        if (token.mode === 'constant') return token.value
        if (token.mode === 'nan') return NaN
        if (token.mode === 'infinity') return Infinity
        if (token.mode === 'utf16') return serialized.length
        return bytes(serialized)
      },
    }
  }
  return { serialize, budget }
}
function selector(input) {
  const trace = []
  const { serialize, budget } = capabilities(input, trace, input)
  const result = selectEvidenceGroups({
    candidates: input.candidates,
    selectedIds: input.selectedIds,
    serialize,
    budget,
  })
  return { result, trace }
}
const group = (id = 'a', patch = {}) => ({
  id,
  rank: 1,
  complete: true,
  value: { text: id },
  ...patch,
})
const selectionCase = (name, input) => add(name, 'select', { candidates: [], ...input }, selector)
selectionCase('select-empty', {})
selectionCase('select-rank-mandatory-utf16', {
  candidates: [
    group('b'),
    group('a'),
    group('\ue000'),
    group('😀'),
    group('\ud800'),
    group('m', { rank: -100, mandatory: true }),
  ],
})
for (const [name, candidates] of [
  ['missing', null],
  ['too-many', Array.from({ length: 41 }, (_, i) => group('g' + i))],
  ['duplicate', [group(), group()]],
  ['incomplete', [group('a', { complete: false })]],
  ['mandatory-incomplete', [group('a', { complete: false, mandatory: true })]],
  ['group-limit', Array.from({ length: 14 }, (_, i) => group('g' + i))],
  [
    'mandatory-group-limit',
    Array.from({ length: 13 }, (_, i) => group('g' + i, { mandatory: true })),
  ],
  ['units-first', [group('a', { units: 12 }), group('b')]],
  ['units-later', [group('a'), group('b', { units: 12 })]],
  ['mandatory-units', [group('a', { units: 13, mandatory: true })]],
  ['units-40', [group('a', { units: 40 })]],
  ['null-value', [group('a', { value: null })]],
])
  selectionCase('select-' + name, { candidates })
for (const [field, values] of Object.entries({
  id: ['', null],
  rank: [null, '1'],
  complete: [null, 'true'],
  units: [null, 0, -1, 1.5, 41, 9007199254740992],
}))
  for (const value of values)
    selectionCase('invalid-' + field + '-' + value, {
      candidates: [group('a', { [field]: value })],
    })
for (const selectedIds of [[], ['b'], ['unknown'], ['a', 'a']])
  selectionCase('selected-ids-' + selectedIds.join(','), {
    candidates: [group('a'), group('b')],
    selectedIds,
  })
selectionCase('mandatory-not-selected', {
  candidates: [group('a', { mandatory: true })],
  selectedIds: [],
})
for (const limit of [0, 1, 2, 16, 17, 18, 19, 20, 21, 22, 23, 100, -1, 1.5, 9007199254740992])
  selectionCase('byte-limit-' + limit, {
    candidates: [group('a')],
    budget: { mode: 'bytes', limit },
  })
const qualified = group('a', {
  value: {
    content: '中文😀\n"\\'.repeat(30) + '但未确认，不是永久偏好',
    conditions: ['if both agree'],
    conflict: { status: 'unresolved' },
    sources: ['one', 'two'],
  },
})
const exact = bytes(
  JSON.stringify({ now: 'fixture', evidence: [qualified.value], constraints: ['不可猜测'] }),
)
for (const limit of [exact - 1, exact, exact + 1])
  selectionCase('utf8-qualification-' + limit, {
    candidates: [qualified],
    serializer: 'envelope',
    budget: { mode: 'bytes', limit },
  })
selectionCase('mandatory-budget', {
  candidates: [group('a', { mandatory: true })],
  budget: { mode: 'bytes', limit: 2 },
})
for (const serializer of ['throw', 'invalid', 'unstable', 'finalGrowth'])
  selectionCase('serializer-' + serializer, {
    candidates: [group('a')],
    serializer,
    budget: { mode: 'bytes', limit: serializer === 'finalGrowth' ? 20 : 100000 },
  })
for (const serializer of ['rawSurrogate', 'whitespaceJSON'])
  selectionCase('serializer-' + serializer, { candidates: [group('a')], serializer })
selectionCase('mutate-original', { candidates: [group('a')], mutateOriginal: true })
for (const tokenizer of [
  {},
  { mode: 'utf16' },
  { mode: 'constant', value: 0 },
  { mode: 'constant', value: -1 },
  { mode: 'constant', value: 1.5 },
  { mode: 'constant', value: 9007199254740992 },
  { mode: 'sequence' },
  { mode: 'throw' },
  { mode: 'nan' },
  { mode: 'infinity' },
  { verified: false },
  { version: '' },
  { modelIdentity: '' },
])
  selectionCase('tokenizer-' + JSON.stringify(tokenizer), {
    candidates: [qualified],
    budget: { mode: 'tokens', limit: 10000 },
    tokenizer,
  })
selectionCase('unverified-mode', { budget: { mode: 'other', limit: 10 } })
for (let limit = 2; limit < 500; limit += 29)
  selectionCase('selection-sweep-' + limit, {
    candidates: Array.from({ length: 30 }, (_, i) =>
      group(String(i), {
        rank: 30 - i,
        value: { text: '中文😀'.repeat(i + 1), qualification: 'not permanent' },
      }),
    ),
    budget: { mode: 'bytes', limit },
  })
const envelope = {
  now: '2026-11-01T00:00:00Z',
  timezone: 'UTC',
  messages: [
    { id: 'm1', senderId: 'speaker', parts: [{ type: 'text', text: 'Literal [image/unknown].' }] },
  ],
}
const rawBrief = (patch = {}) => ({
  coverageByEvidence: [],
  plan: plan({
    facts: [
      { text: 'Qualified memory', evidenceIds: ['m1'] },
      { text: 'Web-only fact', evidenceIds: ['web'] },
    ],
    replySuggestion: 'DRAFT_CANARY',
    strategy: 'STRATEGY_CANARY',
    expressionGoals: ['GOAL_CANARY'],
  }),
  evidence: [
    {
      id: 'm1',
      toolName: 'memory.search',
      memories: [record('r1', '中文'.repeat(200) + '未确认，不是永久偏好')],
    },
    { id: 'web', toolName: 'web-read.read', snippet: 'Public context' },
  ],
  imageContext: [vision],
  actionReceipts: [{ id: 'action', status: 'failed' }],
  ...patch,
})
function pack(input) {
  const trace = []
  const { serialize, budget } = capabilities(input, trace, input)
  const result = packReplyEvidence({
    brief: input.brief,
    envelope: input.envelope,
    serialize,
    budget,
    contextLayout: input.contextLayout,
    mandatoryEvidenceIds: input.mandatoryEvidenceIds,
    upstreamOmissions: input.upstreamOmissions,
  })
  return { result, trace }
}
const packCase = (name, input = {}) =>
  add(name, 'pack', { brief: rawBrief(), envelope, ...input }, pack)
for (const limit of [10, 1300, 2100, 2600, 100000])
  for (const contextLayout of [undefined, 'messages'])
    packCase('pack-budget-' + limit + '-' + contextLayout, {
      budget: { mode: 'bytes', limit },
      contextLayout,
    })
for (const serializer of ['projection', 'unstable', 'throw', 'invalid'])
  packCase('pack-serializer-' + serializer, { serializer, contextLayout: 'messages' })
packCase('pack-mutate-original', { mutateOriginal: true })
for (const mandatoryEvidenceIds of [['absent'], ['m1'], ['m1', 'm1']])
  packCase('pack-mandatory-' + mandatoryEvidenceIds, {
    mandatoryEvidenceIds,
    budget: { mode: 'bytes', limit: 2100 },
  })
for (const change of [
  { memories: [] },
  { contentOmitted: 'not_selected' },
  { memories: [{ id: 'r', content: 1 }] },
  { memories: [{ id: 'r', content: 'partial', contentTruncated: true }] },
])
  for (const mandatory of [false, true]) {
    const b = rawBrief()
    Object.assign(b.evidence[0], change)
    packCase('pack-incomplete-' + JSON.stringify(change) + '-' + mandatory, {
      brief: b,
      mandatoryEvidenceIds: mandatory ? ['m1'] : [],
    })
  }
for (const n of [12, 13, 40, 41])
  for (const linked of [false, true]) {
    const b = rawBrief()
    b.evidence = linked
      ? Array.from({ length: n }, (_, i) => ({
          id: 'm' + i,
          toolName: 'memory.search',
          memories: [record('r' + i, 'complete')],
        }))
      : [
          {
            id: 'm1',
            toolName: 'memory.search',
            memories: Array.from({ length: n }, (_, i) => record('r' + i, 'complete')),
          },
        ]
    b.plan.facts = linked
      ? [{ text: 'ALL REQUIRED', evidenceIds: b.evidence.map((e) => e.id) }]
      : []
    packCase('pack-records-' + n + '-' + linked, { brief: b })
  }
for (const n of [12, 13, 40, 41])
  packCase('pack-independent-' + n, {
    brief: rawBrief({
      evidence: Array.from({ length: n }, (_, i) => ({
        id: 'm' + i,
        toolName: 'memory.search',
        memories: [record('r' + i, 'complete')],
      })),
    }),
  })
{
  const b = rawBrief()
  b.evidence.push({ id: 'm1', toolName: 'memory.person', memories: [] })
  packCase('pack-duplicate-receipt', { brief: b })
}
for (const missing of [false, true]) {
  const b = rawBrief()
  b.evidence.push(
    ...['m2', 'm3'].map((id) => ({
      id,
      toolName: 'memory.person',
      memories: [record(id, 'Counterclaim')],
    })),
  )
  if (missing) b.evidence[1] = { id: 'web', toolName: 'memory.search', memories: [] }
  b.plan.facts = [
    { text: 'first', evidenceIds: ['m1', 'm2', 'web'] },
    { text: 'second', evidenceIds: ['m2', 'm3'] },
  ]
  for (const limit of [2100, 100000])
    packCase('pack-transitive-' + missing + '-' + limit, {
      brief: b,
      budget: { mode: 'bytes', limit },
    })
}
for (const upstreamOmissions of [
  [{ evidenceId: 'u', reason: 'planner_evidence_budget', omittedRecords: 3 }],
  [{ evidenceId: 'm1', reason: 'planner_evidence_budget', omittedRecords: 2 }],
  [{ evidenceId: '', reason: 'planner_evidence_budget', omittedRecords: 1 }],
  [{ evidenceId: 'm1', reason: 'other', omittedRecords: 1 }],
  ...[0, -1, 1.5, 9007199254740992].map((omittedRecords) => [
    { evidenceId: 'm1', reason: 'planner_evidence_budget', omittedRecords },
  ]),
  Array.from({ length: 2 }, () => ({
    evidenceId: 'u',
    reason: 'planner_evidence_budget',
    omittedRecords: 1,
  })),
  Array.from({ length: 13 }, (_, i) => ({
    evidenceId: 'u' + i,
    reason: 'planner_evidence_budget',
    omittedRecords: 1,
  })),
])
  packCase('pack-upstream-' + JSON.stringify(upstreamOmissions), { upstreamOmissions })
packCase('pack-upstream-only', {
  brief: rawBrief({ evidence: [] }),
  upstreamOmissions: [{ evidenceId: 'u', reason: 'planner_evidence_budget', omittedRecords: 4 }],
})
for (const c of corpus.cases.filter(
  (c) => c.kind === 'workspace' && (c.name.endsWith('-valid') || c.name.startsWith('summary-')),
)) {
  const x = JSON.parse(c.input)
  packCase('pack-workspace/' + c.name, {
    brief: rawBrief({ evidence: [{ id: 'm1', toolName: x.tool, states: [x.value] }] }),
  })
}
for (const toolName of ['memory.commitments', 'memory.history', 'memory.commitmentHistory'])
  for (const n of [0, 1, 12, 13, 20, 21]) {
    const states =
      toolName === 'memory.commitments'
        ? Array.from({ length: n }, () => ({ stateKind: 'commitment', view }))
        : [{ stateKind: 'assertion_history', revisions: Array.from({ length: n }, () => ({})) }]
    packCase('pack-state-count-' + toolName + '-' + n, {
      brief: rawBrief({ evidence: [{ id: 'm1', toolName, states }] }),
    })
  }
packCase('pack-state-summed-history-limit', {
  brief: rawBrief({
    evidence: [
      {
        id: 'm1',
        toolName: 'memory.history',
        states: [{ revisions: Array(11).fill({}) }, { revisions: Array(10).fill({}) }],
      },
    ],
  }),
})
packCase('pack-state-bad-history', {
  brief: rawBrief({ evidence: [{ id: 'm1', toolName: 'memory.history', states: [{}] }] }),
})
packCase('pack-state-missing-mandatory', {
  brief: rawBrief({ evidence: [{ id: 'm1', toolName: 'memory.commitments', states: [] }] }),
  mandatoryEvidenceIds: ['m1'],
})
for (const tokenizer of [
  {},
  { mode: 'utf16' },
  { mode: 'sequence' },
  { mode: 'throw' },
  { verified: false },
])
  packCase('pack-tokens-' + JSON.stringify(tokenizer), {
    budget: { mode: 'tokens', limit: 100000 },
    tokenizer,
    contextLayout: 'messages',
  })
// Exact byte boundary is measured by the actual source, including JSON wrappers.
for (const contextLayout of [undefined, 'messages']) {
  const input = { brief: rawBrief(), envelope, contextLayout }
  const full = pack(structuredClone(input)).result
  for (const limit of [full.usage.count - 1, full.usage.count, full.usage.count + 1])
    packCase('pack-exact-' + contextLayout + '-' + limit, {
      contextLayout,
      budget: { mode: 'bytes', limit },
    })
}
const tool = (name) => ({ name, description: 'Fixture', inputSchema: {} })
const call = (name, args = {}, id = name) => ({ id, name, arguments: args })
const completion = (calls) => ({
  text: '',
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  toolCalls: calls,
  assistant: { role: 'assistant', content: '', toolCalls: calls },
})
async function loop(recipe) {
  const events = []
  let round = 0,
    executions = 0
  const scope = {
    targetMessageIds: ['m1'],
    contextMessageIds: ['m1', 'm2'],
    evidence: [{ id: 'source', value: 'Synthetic fact' }],
    actionTools: ['learning.propose', 'experience.propose', 'memes.feedback'].map(tool),
    ...recipe.scope,
  }
  const built = await buildReplyPlan({
    ...scope,
    messages: [{ role: 'user', content: 'Synthetic context' }],
    readTools: recipe.readTools ?? [tool('memory.search')],
    evidenceMaxBytes: recipe.max,
    replySuggestionsEnabled: true,
    signal: new AbortController().signal,
    eligible: () => true,
    complete: async (messages, tools) => {
      events.push({
        kind: 'complete',
        messages: structuredClone(messages),
        tools: tools.map((t) => t.name),
      })
      return completion(recipe.rounds[round++] ?? recipe.rounds.at(-1))
    },
    execute: async (name, args) => {
      events.push({ kind: 'execute', name, args: structuredClone(args) })
      return structuredClone(recipe.values?.[executions++] ?? [])
    },
    deliver: (name, value, args) => {
      events.push({
        kind: 'deliver',
        name,
        value: structuredClone(value),
        args: structuredClone(args),
      })
      return value
    },
  })
  const brief = buildReplyBrief(
    built.plan,
    built.evidence,
    recipe.actionReceipts ?? [],
    recipe.imageContext ?? [],
    recipe.relatedMessageIds ?? [],
  )
  const upstreamOmissions = built.evidence
    .filter((e) => e.coverage?.reason === 'planner_evidence_budget')
    .map((e) => ({
      evidenceId: e.id,
      reason: 'planner_evidence_budget',
      omittedRecords: e.coverage.omittedRecords,
    }))
  const packed = pack({
    brief,
    envelope,
    budget: { mode: 'bytes', limit: recipe.replyLimit ?? 100000 },
    contextLayout: recipe.contextLayout,
    serializer: recipe.serializer,
    upstreamOmissions,
  })
  return { events, result: { value: built }, brief, packed }
}
const memoryPlan = plan({
  memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['record'] }],
  facts: [{ text: 'Qualified memory statement', evidenceIds: ['tool:1'] }],
  replySuggestion: 'Qualified draft.',
})
const statePlan = plan({
  stateEvidenceIds: ['tool:1'],
  facts: [{ text: 'Qualified plan declaration', evidenceIds: ['tool:1'] }],
})
for (const [name, recipe] of [
  [
    'read-memory-to-reply',
    {
      rounds: [
        [call('memory.search', { query: 'synthetic' }, 'read')],
        [call('reply.plan', memoryPlan)],
      ],
      values: [[record()]],
    },
  ],
  [
    'repair-then-reply',
    {
      rounds: [
        [call('memory.search', {}, 'read')],
        [
          call('reply.plan', {
            ...memoryPlan,
            memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['wrong'] }],
          }),
        ],
        [call('reply.plan', memoryPlan)],
      ],
      values: [[record()]],
      contextLayout: 'messages',
      serializer: 'projection',
    },
  ],
  [
    'reply-budget-omits-whole',
    {
      rounds: [[call('memory.search', {}, 'read')], [call('reply.plan', memoryPlan)]],
      values: [[record('record', 'x'.repeat(5000) + ' NOT CONFIRMED')]],
      replyLimit: 1800,
    },
  ],
  [
    'planner-omission-disclosed',
    {
      rounds: [
        [call('memory.search', {}, 'read')],
        [call('reply.plan', plan({ replySuggestion: 'DRAFT TO REMOVE' }))],
      ],
      values: [[record('record', 'x'.repeat(5000))]],
      max: 2200,
    },
  ],
  [
    'state-declaration-to-reply',
    {
      rounds: [[call('memory.commitments', {}, 'read')], [call('reply.plan', statePlan)]],
      readTools: [tool('memory.commitments')],
      values: [[{ stateKind: 'commitment', view }]],
      contextLayout: 'messages',
    },
  ],
  [
    'actions-and-images-to-reply',
    {
      rounds: [
        [
          call(
            'reply.plan',
            plan({ actions: [{ toolName: 'learning.propose', arguments: { note: 'proposal' } }] }),
          ),
        ],
      ],
      actionReceipts: [
        {
          id: 'a',
          toolName: 'learning.propose',
          value: { ok: false, reason: 'not_admitted', raw: 'REMOVE' },
        },
      ],
      imageContext: [vision],
      relatedMessageIds: ['related'],
      contextLayout: 'messages',
    },
  ],
]) {
  recipe.deliver = 'same'
  recipe.suggestions = true
  loops.push({
    name,
    input: JSON.stringify(recipe),
    expected: JSON.stringify(await loop(structuredClone(recipe))),
  })
}
const paths = [
  ...new Set([
    ...corpus.sources.map((x) => x.path),
    'src/runtime/reply-brief.ts',
    'src/runtime/reply-evidence-budget.ts',
    'src/runtime/evidence-selector.ts',
    'src/runtime/knowledge-evidence.ts',
  ]),
]
const output = {
  node: process.version,
  locale: corpus.locale,
  timezone: 'UTC',
  sources: paths.map((path) => ({
    path,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  })),
  cases,
  loops,
}
const path = 'backend/internal/replyplan/testdata/handoff.json',
  text = JSON.stringify(output, null, 2) + '\n'
if (check) assert.equal(readFileSync(path, 'utf8'), text, path)
else writeFileSync(path, text)
console.log(
  `${cases.length} source handoff/selection/packing cases; ${loops.length} planner-to-reply traces`,
)
