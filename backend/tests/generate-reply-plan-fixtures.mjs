/** Synthetic differential oracle executing the actual TypeScript policy. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { z } from 'zod'
import {
  ReplyPlanSchema,
  validateReplyPlan,
  buildReplyPlan,
  withinSignal,
  plannerReadTools,
  plannerActionTools,
} from '../../src/runtime/reply-plan.ts'
import { validWorkspaceState } from '../../src/runtime/workspace-state-evidence.ts'
import { validateRelationSelection } from '../../src/runtime/relation-evidence.ts'
import { SemanticStateEvidenceSchema } from '../../src/memory/read/semantic-exposure.ts'
import { PlannerEvidenceBudget } from '../../src/runtime/planner-evidence-budget.ts'
import { expandContextMessages } from '../../src/models/prompt-context.ts'
import { ContextProjection } from '../../src/runtime/context-projection.ts'
assert.equal(process.version, 'v24.22.1')
process.env.TZ = 'UTC' // Explicit synthetic source deployment, not a host default.
const check = process.argv.includes('--check')
const write = (path, value) => {
  const text = JSON.stringify(value, null, 2) + '\n'
  if (check) assert.equal(readFileSync(path, 'utf8'), text, path)
  else writeFileSync(path, text)
}
for (const suggestions of [false, true])
  write(
    `backend/internal/replyplan/schema/plan${suggestions ? '-suggestions' : ''}.json`,
    z.toJSONSchema(
      suggestions ? ReplyPlanSchema : ReplyPlanSchema.omit({ replySuggestion: true }),
      { io: 'input' },
    ),
  )
write(
  'backend/internal/replyplan/schema/semantic-state.json',
  z.toJSONSchema(SemanticStateEvidenceSchema, { io: 'input' }),
)
const tool = (name, effect) => ({
  name,
  description: 'Fixture',
  inputSchema: {},
  ...(effect ? { effect } : {}),
})
const plan = (patch = {}) => ({
  action: 'reply',
  targetMessageIds: ['m1'],
  contextMessageIds: ['m1'],
  strategy: 'Answer briefly.',
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  actions: [],
  ...patch,
})
const scope = (patch = {}) => ({
  targetMessageIds: ['m1'],
  contextMessageIds: ['m1', 'm2'],
  evidence: [{ id: 'source', value: 'Synthetic fact' }],
  actionTools: [tool('learning.propose'), tool('experience.propose'), tool('memes.feedback')],
  ...patch,
})
const call = (name, args = {}, id = name) => ({ id, name, arguments: args })
const completion = (calls) => ({
  text: '',
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  toolCalls: calls,
  assistant: { role: 'assistant', content: '', toolCalls: calls },
})
for (const enabled of [false, true])
  await buildReplyPlan({
    ...scope(),
    messages: [],
    signal: new AbortController().signal,
    eligible: () => true,
    readTools: [],
    replySuggestionsEnabled: enabled,
    execute: async () => null,
    complete: async (_m, tools) => {
      write(
        `backend/internal/replyplan/schema/plan-tool${enabled ? '-suggestions' : ''}.json`,
        tools[0],
      )
      return completion([call('reply.plan', plan())])
    },
  })
const cases = [],
  builds = []
const capture = (fn) => {
  try {
    return { value: fn() }
  } catch (e) {
    return { error: e.message }
  }
}
const add = (name, kind, input, fn) =>
  cases.push({ name, kind, input: JSON.stringify(input), expected: JSON.stringify(capture(fn)) })
const validation = (name, value, input = scope()) =>
  add(name, 'validate', { value, input }, () => validateReplyPlan(value, input))
for (const value of [
  null,
  0,
  -0,
  true,
  {},
  [],
  ['m', null],
  [['m'], null],
  { toString: 'shadow' },
  [{ toString: null }],
])
  add('string-coercion-' + JSON.stringify(value), 'string', { value }, () => String(value))
add(
  'string-coercion-undefined-array',
  'string',
  { value: ['m', null], undefinedElements: [1] },
  () => String(['m', undefined]),
)
validation('defaults', plan())
validation(
  'custom-delivery',
  plan({ deliveryTool: 'custom.send' }),
  scope({ deliveryTools: [tool('custom.send', 'send')] }),
)
validation('invalid-delivery', plan({ deliveryTool: 'custom.send' }))
validation('suggestion-disabled', plan({ replySuggestion: 'hello' }))
validation(
  'suggestion-enabled',
  plan({ replySuggestion: 'hello' }),
  scope({ replySuggestionsEnabled: true }),
)
for (const [name, value] of [
  ['null', null],
  ['array', []],
  ['empty', {}],
  ['hidden', { ...plan(), hiddenReasoning: 'secret' }],
  ['wrong-action', plan({ action: 'send' })],
  ['missing-target', plan({ targetMessageIds: ['unknown'] })],
  ['outside-context', plan({ contextMessageIds: ['m2'] })],
  ['unknown-fact', plan({ facts: [{ text: 'fact', evidenceIds: ['missing'] }] })],
])
  validation(name, value)
for (const key of Object.keys(plan())) {
  const v = plan()
  delete v[key]
  validation(`required-${key}`, v)
  validation(`null-${key}`, plan({ [key]: null }))
}
for (const [key, max, item] of [
  ['targetMessageIds', 20, 'm1'],
  ['contextMessageIds', 24, 'm1'],
  ['constraints', 12, ''],
  ['facts', 24, { text: 'fact', evidenceIds: ['source'] }],
  ['assets', 8, { assetId: 'a', evidenceId: 'asset' }],
  ['emoji', 8, '🙂'],
  ['expressionGoals', 12, 'goal'],
  ['stateEvidenceIds', 4, 'state'],
  ['actions', 4, { toolName: 'learning.propose', arguments: {} }],
])
  for (const n of [max, max + 1])
    validation(
      `${key}-length-${n}`,
      plan({ [key]: Array.from({ length: n }, () => structuredClone(item)) }),
    )
for (const [key, max] of [
  ['strategy', 1000],
  ['replySuggestion', 2000],
])
  for (const n of [max, max + 1])
    validation(
      `${key}-utf16-${n}`,
      plan({ [key]: '🙂'.repeat(Math.floor(n / 2)) + (n % 2 ? 'x' : '') }),
      scope({ replySuggestionsEnabled: true }),
    )
for (const [key, max] of [
  ['strategy', 1000],
  ['replySuggestion', 2000],
])
  for (const n of [max, max + 1])
    validation(
      `${key}-codepoints-${n}`,
      plan({ [key]: '🙂'.repeat(n) }),
      scope({ replySuggestionsEnabled: true }),
    )
validation(
  'lone-surrogate-id',
  plan({ targetMessageIds: ['\ud800'], contextMessageIds: ['\ud800'] }),
  scope({ targetMessageIds: ['\ud800'], contextMessageIds: ['\ud800'] }),
)
validation(
  'fallback-context-target',
  plan({ targetMessageIds: ['m2'], contextMessageIds: ['m2'] }),
  scope({ targetMessageIds: [] }),
)
for (const status of ['available', 'deleted', undefined])
  validation(
    'retrieved-target-' + status,
    plan({ targetMessageIds: ['read-m'], contextMessageIds: ['read-m'] }),
    scope({
      evidence: [
        { id: 'read', toolName: 'messages.get', value: { items: [{ id: 'read-m', status }] } },
      ],
    }),
  )
validation(
  'retrieved-null-item',
  plan(),
  scope({ evidence: [{ id: 'read', toolName: 'messages.get', value: { items: [null] } }] }),
)
validation(
  'duplicate-receipts',
  plan(),
  scope({
    evidence: [
      { id: 'same', value: 1 },
      { id: 'same', value: 2 },
    ],
  }),
)
const selected = (ids = ['inner'], id = 'receipt') =>
  plan({ memorySelections: [{ evidenceId: id, memoryIds: ids }] })
for (const name of ['memory.search', 'memory.person', 'memory.relations', 'custom.read'])
  validation(
    'memory-' + name,
    selected(),
    scope({ evidence: [{ id: 'receipt', toolName: name, value: [{ id: 'inner' }] }] }),
  )
validation(
  'outer-not-inner',
  selected(['receipt']),
  scope({ evidence: [{ id: 'receipt', toolName: 'memory.search', value: [{ id: 'inner' }] }] }),
)
validation(
  'duplicate-inner',
  selected(['inner', 'inner']),
  scope({ evidence: [{ id: 'receipt', toolName: 'memory.search', value: [{ id: 'inner' }] }] }),
)
validation(
  'duplicate-selection',
  plan({
    memorySelections: [
      { evidenceId: 'r', memoryIds: ['a'] },
      { evidenceId: 'r', memoryIds: ['a'] },
    ],
  }),
)
validation(
  'total-memory-limit',
  plan({
    memorySelections: [
      { evidenceId: 'r', memoryIds: Array(7).fill('a') },
      { evidenceId: 's', memoryIds: Array(6).fill('a') },
    ],
  }),
)
const edge = (target, ids) => ({ target: { entityId: target }, requiredMemoryIds: ids })
const relations = [
  { id: 'one', relationEvidence: [edge('entity', ['one', 'two'])] },
  { id: 'two', relationEvidence: [edge('entity', ['two'])] },
]
add(
  'relation-shadowed-toString',
  'relation',
  { records: [{ id: { toString: 'shadow' } }], ids: [] },
  () => {
    validateRelationSelection([{ id: { toString: 'shadow' } }], [])
    return true
  },
)
for (const ids of [['one'], ['two'], ['one', 'two']])
  validation(
    'relation-closure-' + ids.join('-'),
    selected(ids),
    scope({ evidence: [{ id: 'receipt', toolName: 'memory.relations', value: relations }] }),
  )
for (const records of [
  [null],
  [{ id: 'one' }],
  [{ id: 'one', relationEvidence: [edge(null, [])] }],
  relations,
])
  add('relation-native-' + JSON.stringify(records), 'relation', { records, ids: ['one'] }, () => {
    validateRelationSelection(records, ['one'])
    return true
  })
validation(
  'asset-bound',
  plan({ assets: [{ assetId: 'a', evidenceId: 'asset' }] }),
  scope({ evidence: [{ id: 'asset', toolName: 'memes.search', value: { items: [{ id: 'a' }] } }] }),
)
validation(
  'wrong-asset',
  plan({ assets: [{ assetId: 'a', evidenceId: 'asset' }] }),
  scope({
    evidence: [{ id: 'asset', toolName: 'memory.search', value: { items: [{ id: 'a' }] } }],
  }),
)
const action = (args = {}, name = 'learning.propose') => ({ toolName: name, arguments: args })
for (const [name, actions] of [
  ['same', [action({ a: 1, b: 2 }), action({ b: 2, a: 1 })]],
  ['nested', [action({ x: { b: 1, a: 2 } }), action({ x: { a: 2, b: 1 } })]],
  ['different', [action({ a: 1 }), action({ a: 2 })]],
  ['unicode-tie', [action({ é: 1, é: 2 }), action({ é: 2, é: 1 })]],
  ['numeric-keys', [action({ 10: 1, 2: 2 }), action({ 2: 2, 10: 1 })]],
  ['forbidden', [action({}, 'messages.send')]],
])
  validation('actions-' + name, plan({ actions }))
validation(
  'record-proto-key',
  plan({ actions: [action(JSON.parse('{"__proto__":{"x":1},"constructor":2,"x":3}'))] }),
)
for (const name of ['learning.propose', 'experience.propose', 'memes.feedback'])
  validation('noReply-' + name, plan({ action: 'noReply', actions: [action({}, name)] }))
const ref = (id = 'a') => ({ id, version: 1, deletionEpoch: 0 })
const view = {
  ref: ref(),
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  lifecycle: 'pending',
  confirmation: 'confirmed',
}
const record = (id = 'a') => ({
  id,
  scope: { kind: 'scene', sceneId: 'scene' },
  content: 'Synthetic declaration',
  kind: 'assertion',
  status: 'active',
  origin: 'source',
  learnedAt: 'not-a-date',
  sources: [],
})
const summary = {
  id: 'summary',
  version: 1,
  personId: null,
  kind: 'summary_candidate',
  text: 'Synthetic summary',
  selectedParentIds: ['a', 'b'],
  status: 'admitted',
  admission: 'qualified_candidate',
  hypothesis: false,
  anchorQualifications: [],
  createdAt: 'arbitrary',
  applicableUntil: null,
  parents: ['a', 'b'].map((id) => ({ ref: ref(id), proofs: [{ sources: [], parents: [] }] })),
  declarations: ['a', 'b'].map((id) => ({ ref: ref(id), record: record(id) })),
  rootCount: 0,
  applicability: 'current',
  interpretation: 'attributed_summary_not_verified_truth',
}
const states = {
  'memory.workingSet': {
    stateKind: 'working_set',
    page: {
      enabled: true,
      coverage: 'complete',
      omittedCold: 0,
      omittedEntries: 0,
      interpretation: 'accessibility_not_truth_or_absence',
      items: [
        {
          kind: 'assertion',
          snapshot: { ref: ref(), record: record() },
          accessibility: { state: 'hot', reviewDue: false, pinIntent: false },
        },
      ],
    },
  },
  'memory.episodes': {
    stateKind: 'experience_view',
    page: {
      interpretation: 'independent_qualified_records_not_synthesized_episodes',
      sourceBodyCoverage: 'unavailable',
      coverage: { status: 'complete', reasons: [], returned: 1, omittedEntries: 0 },
      items: [
        {
          kind: 'shared_plan_declaration',
          commitmentRef: ref(),
          view,
          completionBasis: 'participant_self_report_not_observed_event',
        },
      ],
    },
  },
  'memory.identities': {
    stateKind: 'identity_view',
    page: {
      sceneId: 'scene',
      personId: 'person',
      revision: 0,
      coverage: 'complete',
      interpretation: 'administrator_verified_links_not_read_authority_or_personal_facts',
      items: [
        {
          personId: 'person',
          aliases: [{ label: 'Alias', sourceKind: 'manual', validFrom: null, validUntil: null }],
        },
      ],
    },
  },
  'memory.knowledge': {
    stateKind: 'knowledge_graph',
    page: {
      coverage: 'complete',
      interpretation: 'attributed_claims_not_transitive_truth',
      edges: [
        {
          id: 'edge',
          backing: ref(),
          polarity: 'positive',
          modality: 'asserted',
          temporalScope: 'current',
          conditions: [],
          proofs: [],
          dependencies: [],
          readDependencies: [],
          spans: [],
        },
      ],
      paths: [['edge']],
      supportClosure: { edges: [], records: [{ ref: ref(), record: { id: 'a' } }] },
    },
  },
  'memory.summaries': {
    stateKind: 'semantic_view',
    page: { coverage: 'complete', sourceBodyCoverage: 'unavailable', items: [summary] },
  },
  'memory.commitmentTimeline': {
    stateKind: 'commitment_timeline',
    anchor: { ...ref(), mutationVersion: 2 },
    coverage: 'limited',
    interpretation: 'previously_recorded_declaration_state_not_current_truth',
    sourceBodyCoverage: 'unavailable',
    stateTime: 'server_recorded_not_occurred',
    reasons: [],
    window: { maxTransitions: 64, throughMutationVersion: 2 },
    recordedStates: [
      { recordedAt: '2024-02-30', mutationVersion: 1, status: 'available', states: [view] },
    ],
  },
}
const stateCase = (name, tool, value) => {
  add(name, 'workspace', { tool, value }, () => validWorkspaceState(tool, value))
  validation(
    name + '-selected',
    plan({ stateEvidenceIds: ['state'] }),
    scope({ evidence: [{ id: 'state', toolName: tool, value: [value] }] }),
  )
}
for (const [tool, value] of Object.entries(states)) {
  stateCase(tool + '-valid', tool, value)
  for (const key of Object.keys(value)) {
    const v = structuredClone(value)
    delete v[key]
    stateCase(tool + '-missing-' + key, tool, v)
  }
  stateCase(tool + '-extra-top', tool, { ...value, extra: 1 })
  if (value.page) {
    for (const key of Object.keys(value.page)) {
      const v = structuredClone(value)
      delete v.page[key]
      stateCase(tool + '-missing-page-' + key, tool, v)
    }
  }
}
const semanticMutation = (name, fn) => {
  const value = structuredClone(states['memory.summaries'])
  fn(value.page.items[0])
  stateCase(name, 'memory.summaries', value)
}
semanticMutation('summary-not-admitted', (s) => (s.status = 'candidate'))
semanticMutation('summary-missing-declaration', (s) => s.declarations.pop())
semanticMutation('summary-mismatched-ref', (s) => (s.declarations[0].ref.version = 2))
semanticMutation('summary-duplicate-parent', (s) => (s.parents[1] = structuredClone(s.parents[0])))
semanticMutation('summary-duplicate-selection-allowed', (s) => (s.selectedParentIds = ['a', 'a']))
semanticMutation(
  'summary-proof-duplicate-binding',
  (s) => (s.parents[0].proofs[0].parents = ['a', 'a']),
)
semanticMutation('summary-proof-missing-binding', (s) => {
  s.parents[0].proofs[0].parents = ['a']
  s.parents[0].proofs[0].parentReferences = []
})
semanticMutation('summary-proof-bound', (s) => {
  s.parents[0].proofs[0].parents = ['a']
  s.parents[0].proofs[0].parentReferences = [ref('a')]
})
semanticMutation('summary-record-strip-extra', (s) => (s.declarations[0].record.unknown = true))
semanticMutation('summary-ref-strict-extra', (s) => (s.declarations[0].ref.unknown = true))
semanticMutation('summary-utf16-overflow', (s) => (s.text = '🙂'.repeat(3001)))
semanticMutation(
  'summary-source-date-valid',
  (s) =>
    (s.parents[0].proofs[0].sourceObservations = [
      { sceneId: 's', messageId: 'm', senderId: 'u', sentAt: '2024-02-29T23:59:59+23:59' },
    ]),
)
semanticMutation(
  'summary-source-date-invalid',
  (s) =>
    (s.parents[0].proofs[0].sourceObservations = [
      { sceneId: 's', messageId: 'm', senderId: 'u', sentAt: '2023-02-29T23:59:59Z' },
    ]),
)
for (const tool of ['memory.commitments', 'memory.history', 'memory.commitmentHistory'])
  for (const count of [0, 1, 2, 12, 13, 20, 21]) {
    const item =
      tool === 'memory.commitments'
        ? { stateKind: 'commitment', view: [] }
        : tool === 'memory.history'
          ? { stateKind: 'assertion_history', revisions: [null] }
          : { stateKind: 'commitment_history', anchor: [], revisions: [{ ref: null }] }
    validation(
      `${tool}-${count}`,
      plan({ stateEvidenceIds: ['state'] }),
      scope({
        evidence: [
          { id: 'state', toolName: tool, value: Array.from({ length: count }, () => item) },
        ],
      }),
    )
  }
// Traverse realistic nested DTOs: source execution decides whether each mutation
// is valid, including intentionally permissive fields and strip-unknown records.
const paths = (v, p = []) =>
  v && typeof v === 'object'
    ? Object.entries(v).flatMap(([k, x]) => [[...p, k], ...paths(x, [...p, k])])
    : []
const replace = (v, path, x) => {
  let parent = v
  for (const key of path.slice(0, -1)) parent = parent[key]
  parent[path.at(-1)] = x
}
for (const [tool, original] of Object.entries(states))
  for (const path of paths(original))
    for (const replacement of [null, -1, 'wrong', {}, []]) {
      const value = structuredClone(original)
      replace(value, path, replacement)
      add(
        'state-mutation/' + tool + '/' + path.join('.') + '/' + JSON.stringify(replacement),
        'workspace',
        { tool, value },
        () => validWorkspaceState(tool, value),
      )
    }
const qualification = {
  edgeId: 'edge',
  edgeVersion: 1,
  registryVersion: 1,
  predicate: 'participates_in',
  subject: { kind: 'person', personId: 'person' },
  object: {
    kind: 'entity',
    entityId: 'entity',
    entityKind: 'plan',
    backing: ref(),
    originEdgeId: 'edge',
  },
  polarity: 'positive',
  modality: 'asserted',
  temporalScope: 'current',
  conditions: [],
  occurredAt: null,
  validFrom: null,
  validUntil: null,
  status: 'candidate',
  admission: 'qualified_candidate',
  interpretation: 'attributed_proposed_association_not_observed_event',
}
semanticMutation('episode-qualified', (s) => {
  s.kind = 'episode_candidate'
  s.status = 'candidate'
  s.anchorQualifications = [qualification]
  s.interpretation = 'grouped_declarations_not_observed_event'
})
for (const endpoint of [
  { kind: 'literal', valueType: 'text', value: 'text' },
  { kind: 'literal', valueType: 'number', value: 3, unit: null },
  { kind: 'literal', valueType: 'boolean', value: true },
])
  semanticMutation('episode-literal-' + endpoint.valueType, (s) => {
    s.kind = 'episode_candidate'
    s.anchorQualifications = [{ ...qualification, object: endpoint }]
    s.interpretation = 'grouped_declarations_not_observed_event'
  })
for (const ending of ['\n', '\r', '\r\n', '\u2028', '\u2029'])
  semanticMutation(
    'datetime-ending-' + JSON.stringify(ending),
    (s) =>
      (s.parents[0].proofs[0].sourceObservations = [
        { sceneId: 's', messageId: 'm', senderId: 'u', sentAt: '2024-01-01T00:00:00Z' + ending },
      ]),
  )
for (const fields of [
  ['deliveryTool', 'expressionGoals', 'memorySelections', 'stateEvidenceIds', 'replySuggestion'],
  ['action'],
]) {
  const value = plan()
  add(
    'undefined-plan-' + fields.join(','),
    'validateUndefined',
    { value, input: scope({ replySuggestionsEnabled: true }), fields },
    () =>
      validateReplyPlan(
        { ...value, ...Object.fromEntries(fields.map((k) => [k, undefined])) },
        scope({ replySuggestionsEnabled: true }),
      ),
  )
}
const rawValue = JSON.stringify(
  plan({ actions: [action({ amount: 0 }), action({ amount: null })] }),
).replace('"amount":0', '"amount":1e400')
cases.push({
  name: 'nonfinite-action-stringify',
  kind: 'validate',
  input: '{"value":' + rawValue + ',"input":' + JSON.stringify(scope()) + '}',
  expected: JSON.stringify(capture(() => validateReplyPlan(JSON.parse(rawValue), scope()))),
})
const tools = [
  tool('messages.get'),
  tool('messages.send', 'read'),
  tool('memory.search', 'learningCandidate'),
  tool('memes.feedback', 'read'),
  tool('custom.read', 'read'),
  tool('custom.effect', 'learningCandidate'),
  tool('custom.send', 'send'),
  tool('unknown'),
]
add('capabilities', 'capabilities', { tools }, () => ({
  read: plannerReadTools(tools),
  action: plannerActionTools(tools),
}))
const messages = [
  { role: 'system', content: 'System', opaque: 'kept' },
  {
    role: 'user',
    content: JSON.stringify({
      now: 't',
      timezone: 'UTC',
      messages: [{ id: 'a' }, { id: 'b' }],
      sceneKind: 'group',
      botUserId: 'bot',
      request: 'go',
    }),
    contextLayout: 'messages',
    contextCacheBoundaries: [0, 1],
  },
]
for (const [name, msgs] of [
  ['expanded', messages],
  ['without-system', messages.slice(1)],
  [
    'empty-history',
    [{ role: 'user', content: '{"messages":[],"x":1}', contextLayout: 'messages' }],
  ],
  ['invalid-envelope', [{ role: 'user', content: '[]', contextLayout: 'messages' }]],
  ['invalid-history', [{ role: 'user', content: '{"messages":1}', contextLayout: 'messages' }]],
  ['invalid-boundary', [{ ...messages[1], contextCacheBoundaries: [2] }]],
  ['images-no-expand', [{ ...messages[1], images: ['image'] }]],
])
  add('context-' + name, 'context', { messages: msgs }, () => expandContextMessages(msgs))
const budgetReceipt = {
  id: 'r',
  toolName: 'memory.search',
  value: [
    { id: 'a', content: '🙂'.repeat(600) },
    { id: 'b', content: 'qualifier' },
  ],
  coverage: { status: 'limited', returnedRecords: 2, omittedRecords: 3 },
}
for (const max of [1, 399, 1000, 5000, 131072, 0, 1.5, -1, 9007199254740992])
  add(
    'budget-' + max,
    'budget',
    { messages: [], receipt: budgetReceipt, toolCallId: 'c', max },
    () => new PlannerEvidenceBudget(max).receipt([], budgetReceipt, 'c'),
  )
for (const n of [65534, 65535])
  add(
    'budget-nonarray-' + n,
    'budget',
    {
      messages: [],
      receipt: { id: 'r', toolName: 'custom.read', value: 'x'.repeat(n) },
      toolCallId: 'c',
      max: 131072,
    },
    () =>
      new PlannerEvidenceBudget().receipt(
        [],
        { id: 'r', toolName: 'custom.read', value: 'x'.repeat(n) },
        'c',
      ),
  )
const projectionCases = [
  {
    name: 'identities',
    sceneId: 'scene',
    steps: [
      {
        op: 'project',
        value: {
          sceneId: 'scene',
          messages: [
            { id: 'long message/id', senderId: 'user:1', parts: [], sceneId: 'scene', raw: 'omit' },
          ],
          evidence: [
            {
              id: 'receipt',
              toolName: 'memes.search',
              value: { items: [{ id: 'asset:1', format: 'image', tags: [] }] },
            },
          ],
        },
      },
      { op: 'restore', value: { targetMessageIds: ['m1'], assetIds: ['a1'] } },
      { op: 'restore', value: { messageId: 'unknown' } },
    ],
  },
  {
    name: 'collision',
    sceneId: 'scene',
    steps: [
      {
        op: 'project',
        value: {
          memoryIds: ['e1', 'long/id', 'e2', 'long/id'],
          targetMessageIds: ['m1', 'another/id'],
        },
      },
      {
        op: 'stringify',
        value: {
          topicId: 'topic',
          contextMessageIds: ['m1'],
          query: 'unchanged',
          receivedAt: 'omit',
        },
        trailingFields: ['topicId'],
      },
    ],
  },
  {
    name: 'canonical',
    sceneId: 'ob:s:scene',
    steps: [
      {
        op: 'project',
        value: {
          id: 'ob:s:scene:m:' + Buffer.from(JSON.stringify('message')).toString('base64url'),
          senderId: 'ob:u:' + Buffer.from(JSON.stringify(['a', 'b', 'user'])).toString('base64url'),
          parts: [],
        },
      },
      { op: 'restore', value: { targetMessageIds: ['message'], personId: 'user' } },
      { op: 'project', value: { sceneId: 'foreign' } },
    ],
  },
]
for (const c of projectionCases)
  add('projection-' + c.name, 'projection', c, () => {
    const p = new ContextProjection(c.sceneId)
    return c.steps.map((s) =>
      capture(() =>
        s.op === 'project'
          ? p.project(s.value)
          : s.op === 'restore'
            ? p.restoreArguments(s.value)
            : p.stringify(s.value, s.trailingFields),
      ),
    )
  })
for (const toolName of [['messages.get'], { toString: 'shadow' }]) {
  const c = {
    sceneId: 'scene',
    steps: [{ op: 'project', value: { toolName, value: { items: [{ id: 'long/id' }] } } }],
  }
  add('projection-coerced-tool-' + JSON.stringify(toolName), 'projection', c, () => {
    const p = new ContextProjection(c.sceneId)
    return c.steps.map((s) => capture(() => p.project(s.value)))
  })
}
{
  const encoded = Buffer.from(JSON.stringify('ok')).toString('base64url'),
    lowBytes =
      [...encoded].map((c) => String.fromCharCode(c.charCodeAt(0) + 256)).join('') + '\u013Dignored'
  const c = {
    sceneId: 'ob:s:scene',
    steps: [
      { op: 'project', value: { messageId: 'ob:s:scene:m:' + lowBytes } },
      { op: 'restore', value: { messageId: 'ok' } },
    ],
  }
  add('projection-canonical-low-byte-base64', 'projection', c, () => {
    const p = new ContextProjection(c.sceneId)
    return c.steps.map((s) =>
      capture(() => (s.op === 'project' ? p.project(s.value) : p.restoreArguments(s.value))),
    )
  })
}
const buildCase = async (name, recipe) => {
  const events = []
  const controller = new AbortController()
  let eligible = true,
    round = 0,
    executions = 0
  const projection = recipe.projection
    ? new ContextProjection(recipe.projection.sceneId)
    : undefined
  if (projection) projection.project(recipe.projection.seed)
  const input = {
    ...scope(recipe.scope),
    messages: recipe.messages ?? [{ role: 'user', content: 'Synthetic context' }],
    readTools: recipe.readTools ?? [tool('memory.search')],
    evidenceMaxBytes: recipe.max,
    replySuggestionsEnabled: recipe.suggestions,
    projection,
    signal: controller.signal,
    eligible: () => eligible,
    complete: async (messages, tools) => {
      events.push({
        kind: 'complete',
        messages: structuredClone(messages),
        tools: tools.map((t) => t.name),
      })
      round++
      if (recipe.abortComplete === round) controller.abort(new Error('cancelled'))
      if (recipe.ineligibleComplete === round) eligible = false
      return completion(recipe.rounds[round - 1] ?? recipe.rounds.at(-1))
    },
    execute: async (name, args) => {
      events.push({ kind: 'execute', name, args: structuredClone(args) })
      executions++
      if (recipe.abortExecute === executions) controller.abort(new Error('cancelled'))
      if (recipe.ineligibleExecute === executions) eligible = false
      if (recipe.executeError) throw new Error(recipe.executeError)
      if (recipe.executeUndefined) return undefined
      return structuredClone(recipe.values?.[executions - 1] ?? [])
    },
  }
  if (recipe.deliver)
    input.deliver = (name, value, args) => {
      events.push({
        kind: 'deliver',
        name,
        value: structuredClone(value),
        args: structuredClone(args),
      })
      if (recipe.deliver === 'mutate' && Array.isArray(value)) value.push({ id: 'new' })
      if (recipe.deliver === 'replace') return []
      return value
    }
  if (recipe.abortBefore) controller.abort(new Error('cancelled'))
  if (recipe.ineligibleBefore) eligible = false
  let result
  try {
    result = { value: await buildReplyPlan(input) }
  } catch (e) {
    result = { error: e.message }
  }
  builds.push({ name, input: JSON.stringify(recipe), expected: JSON.stringify({ events, result }) })
}
await buildCase('direct', { rounds: [[call('reply.plan', plan())]] })
await buildCase('read-and-cite', {
  rounds: [
    [call('memory.search', { query: 'fact' }, 'read')],
    [call('reply.plan', selected(['a'], 'tool:1'))],
  ],
  values: [[{ id: 'a', content: 'fact' }]],
  deliver: 'same',
})
await buildCase('repair-citation', {
  rounds: [
    [call('memory.search', {}, 'read')],
    [call('reply.plan', selected(['tool:1'], 'tool:1'))],
    [call('reply.plan', selected(['a'], 'tool:1'))],
  ],
  values: [[{ id: 'a' }]],
  deliver: 'same',
})
await buildCase('repair-no-initial-receipt-leak', {
  scope: { evidence: [{ id: 'initial', toolName: 'memory.search', value: [{ id: 'a' }] }] },
  rounds: [
    [call('reply.plan', selected(['bad'], 'initial'))],
    [call('reply.plan', selected(['a'], 'initial'))],
  ],
})
for (const changed of [false, true])
  await buildCase('repair-action-' + changed, {
    rounds: [
      [
        call(
          'reply.plan',
          plan({ facts: [{ text: 'f', evidenceIds: ['missing'] }], actions: [action({ x: 1 })] }),
        ),
      ],
      [call('reply.plan', plan({ actions: [action({ x: changed ? 2 : 1 })] }))],
    ],
  })
await buildCase('malformed-cannot-authorize', {
  rounds: [
    [call('reply.plan', plan({ unknown: true, actions: [action()] }))],
    [call('reply.plan', plan({ actions: [action()] }))],
  ],
})
await buildCase('malformed-repaired-empty', {
  rounds: [[call('reply.plan', {})], [call('reply.plan', plan())]],
})
await buildCase('repair-one-only', { rounds: [[call('reply.plan', {})], [call('reply.plan', {})]] })
await buildCase('repair-no-reads', { rounds: [[call('reply.plan', {})], [call('memory.search')]] })
await buildCase('invalid-delivery-not-repaired', {
  rounds: [[call('reply.plan', plan({ deliveryTool: 'unknown' }))]],
})
await buildCase('ambiguous', { rounds: [[call('reply.plan', plan()), call('memory.search')]] })
await buildCase('multiple-final', {
  rounds: [[call('reply.plan', plan()), call('reply.plan', plan())]],
})
await buildCase('missing-plan', { rounds: [[]] })
await buildCase('forbidden-tool', { rounds: [[call('messages.send')]] })
await buildCase('tool-budget', {
  rounds: [Array.from({ length: 13 }, (_, i) => call('memory.search', {}, 'c' + i))],
})
await buildCase('round-budget', { rounds: [[call('memory.search')]], values: [] })
await buildCase('last-round-invalid-no-repair', {
  rounds: [...Array.from({ length: 5 }, () => [call('memory.search')]), [call('reply.plan', {})]],
})
for (const flag of [
  'abortBefore',
  'ineligibleBefore',
  'abortComplete',
  'ineligibleComplete',
  'abortExecute',
  'ineligibleExecute',
])
  await buildCase(flag, {
    [flag]: flag.endsWith('Before') ? true : 1,
    rounds: [[call('memory.search')], [call('reply.plan', plan())]],
    values: [[{ id: 'a' }]],
  })
for (const deliver of ['mutate', 'replace'])
  await buildCase('deliver-' + deliver, {
    rounds: [[call('memory.search')], [call('reply.plan', plan())]],
    values: [[{ id: 'a' }]],
    deliver,
  })
await buildCase('execute-error', {
  rounds: [[call('memory.search')]],
  executeError: 'fixture_read_failed',
})
await buildCase('limited-evidence-not-citable', {
  max: 2200,
  rounds: [
    [call('memory.search')],
    [call('reply.plan', selected(['a'], 'tool:1'))],
    [call('reply.plan', plan())],
  ],
  values: [[{ id: 'a', content: 'x'.repeat(4000) }]],
  deliver: 'same',
})
await buildCase('large-unbudgeted-value', {
  readTools: [tool('custom.read', 'read')],
  rounds: [[call('custom.read')]],
  values: ['x'.repeat(65535)],
})
await buildCase('projection-restored', {
  scope: { targetMessageIds: ['long/message'], contextMessageIds: ['long/message'] },
  projection: { sceneId: 'scene', seed: { messageIds: ['long/message'] } },
  rounds: [
    [call('memory.search', { messageId: 'm1' })],
    [
      call(
        'reply.plan',
        plan({
          targetMessageIds: ['m1'],
          contextMessageIds: ['m1'],
          memorySelections: [{ evidenceId: 'e1', memoryIds: ['a'] }],
        }),
      ),
    ],
  ],
  values: [[{ id: 'a' }]],
  deliver: 'same',
})
await buildCase('projection-unknown-repair', {
  projection: { sceneId: 'scene', seed: { messageIds: ['m1'] } },
  rounds: [
    [call('reply.plan', plan({ targetMessageIds: ['missing'] }))],
    [call('reply.plan', plan())],
  ],
})
await buildCase('undefined-read-result', {
  executeUndefined: true,
  deliver: 'same',
  rounds: [[call('memory.search')], [call('reply.plan', plan())]],
})
await buildCase('repair-hint-bounds', {
  rounds: [
    Array.from({ length: 7 }, (_, i) => call('memory.search', {}, 'r' + i)),
    [call('reply.plan', {})],
    [call('reply.plan', plan())],
  ],
  values: Array.from({ length: 7 }, () => [
    { id: '🙂'.repeat(150) },
    ...Array.from({ length: 14 }, (_, i) => ({ id: 'inner' + i })),
  ]),
})
const signals = []
for (const beforeAttach of [false, true])
  for (const order of [
    ['reject', 'abort'],
    ['abort', 'reject'],
    ['resolve', 'abort'],
    ['abort', 'resolve'],
    ['reject'],
    ['resolve'],
  ]) {
    const controller = new AbortController()
    let resolve, reject
    const work = new Promise((yes, no) => {
      resolve = yes
      reject = no
    })
    const apply = () => {
      for (const op of order) {
        if (op === 'reject') reject(new Error('provider_failed'))
        else if (op === 'resolve') resolve('success')
        else controller.abort(new Error('cancelled'))
      }
    }
    if (beforeAttach) apply()
    const pending = withinSignal(work, controller.signal)
    if (!beforeAttach) apply()
    let expected
    try {
      expected = { value: await pending }
    } catch (e) {
      expected = { error: e.message }
    }
    await work.catch(() => {})
    signals.push({
      name: beforeAttach + '-' + order.join('-'),
      input: JSON.stringify({ beforeAttach, order }),
      expected: JSON.stringify(expected),
    })
  }
const actionKeys = new Set(['toolName', 'arguments'])
const collectKeys = (v) => {
  if (Array.isArray(v)) v.forEach(collectKeys)
  else if (v && typeof v === 'object')
    for (const [k, x] of Object.entries(v)) {
      actionKeys.add(k)
      collectKeys(x)
    }
}
const findActions = (v) => {
  if (Array.isArray(v)) v.forEach(findActions)
  else if (v && typeof v === 'object')
    for (const [k, x] of Object.entries(v)) {
      if (k === 'actions') collectKeys(x)
      else findActions(x)
    }
}
for (const c of [...cases, ...builds]) findActions(JSON.parse(c.input))
const comparisons = [...actionKeys].flatMap((a) =>
  [...actionKeys].map((b) => [a, b, a.localeCompare(b)]),
)
const sources = [
  'src/runtime/reply-plan.ts',
  'src/runtime/workspace-state-evidence.ts',
  'src/runtime/relation-evidence.ts',
  'src/runtime/evidence-scope.ts',
  'src/runtime/planner-evidence-budget.ts',
  'src/models/prompt-context.ts',
  'src/runtime/context-projection.ts',
  'src/memory/read/semantic-exposure.ts',
  'src/memory/semantic/contracts.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'src/memory/knowledge/contracts.ts',
  'node_modules/zod/src/v4/core/regexes.ts',
  'node_modules/zod/src/v4/core/checks.ts',
  'node_modules/zod/src/v4/core/schemas.ts',
  'node_modules/zod/src/v4/core/util.ts',
]
write('backend/internal/replyplan/testdata/node.json', {
  node: process.version,
  locale: new Intl.Collator().resolvedOptions().locale,
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  sources: sources.map((path) => ({
    path,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  })),
  comparisons,
  cases,
  builds,
  signals,
})
console.log(
  `${cases.length} policy/evidence/projection cases; ${builds.length} complete planner traces`,
)
