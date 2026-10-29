import assert from 'node:assert/strict'
import test from 'node:test'
import { z } from '@hono/zod-openapi'
import { CandidateSchema, buildLearningCandidateSchema } from '../src/memory/learning/contracts.ts'
import {
  knowledgeExtractionInstructions,
  subjectLearningMessages,
  repairLearningCandidate,
} from '../src/memory/learning/subject-input.ts'
import { trustedSourceEvidence } from '../src/memory/learning/source-evidence.ts'
import { extractSources } from '../src/memory/learning/extraction.ts'
import type { SourceStageContext } from '../src/memory/learning/batch-contracts.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { Completion } from '../src/models.ts'
import { Store } from '../src/store.ts'

const knowledge = {
  registryVersion: 1 as const,
  predicate: 'uses_tool' as const,
  subject: { kind: 'self' as const, personId: 'speaker' },
  object: { kind: 'new' as const, entityKind: 'tool' as const, title: 'Tool A' },
  polarity: 'negative' as const,
  modality: 'possible' as const,
  temporalScope: 'future' as const,
  conditions: ['Only if the project changes'],
  sourceSpans: [{ messageId: 'source', partIndex: 2, start: 2, end: 9, stance: 'self' as const }],
}
const base = {
  content: 'A qualified tool-use claim.',
  kind: 'claim' as const,
  sourceMessageIds: ['source'],
}
const message: RuntimeMessage = {
  id: 'source',
  sceneId: 'scene',
  senderId: 'speaker',
  sentAt: '2026-10-29T09:00:00Z',
  receivedAt: '2026-10-29T09:00:01Z',
  origin: 'live',
  status: 'available',
  parts: [
    { type: 'text', text: 'Header' },
    { type: 'mention', userId: 'other' },
    { type: 'text', text: '😀Tool A条件声明' },
  ],
}

test('knowledge remains absent from disabled schemas and enabled proposals retain all qualified fields', () => {
  const off = buildLearningCandidateSchema(true, true)
  assert.equal(off.safeParse({ ...base, knowledge: [knowledge] }).success, false)
  assert.equal(Object.hasOwn(z.toJSONSchema(off).properties ?? {}, 'knowledge'), false)
  const on = buildLearningCandidateSchema(false, false, true)
  assert.deepEqual(on.parse({ ...base, knowledge: [knowledge] }).knowledge, [knowledge])
  assert.equal(on.safeParse({ ...base, knowledge: Array(4).fill(knowledge) }).success, false)
  assert.equal(on.safeParse({ ...base, knowledge: [knowledge], relations: [] }).success, false)
  for (const patch of [
    { registryVersion: 2 },
    { predicate: 'invented' },
    { status: 'accepted' },
    { sourceSpans: [{ ...knowledge.sourceSpans[0], messageId: 'unrelated' }] },
  ])
    assert.equal(on.safeParse({ ...base, knowledge: [{ ...knowledge, ...patch }] }).success, false)
})

test('structured scene knowledge cannot enter public review and source-only cannot guess existing endpoints', () => {
  const sourceOnly = buildLearningCandidateSchema(false, false, true, true)
  assert.equal(sourceOnly.safeParse({ ...base, knowledge: [knowledge] }).success, true)
  const existing = { kind: 'existing' as const, entityId: 'unexposed-entity' }
  for (const patch of [{ subject: existing }, { object: existing }]) {
    const input = { ...base, knowledge: [{ ...knowledge, ...patch }] }
    assert.equal(sourceOnly.safeParse(input).success, false)
    assert.equal(buildLearningCandidateSchema(false, false, true).safeParse(input).success, true)
  }
  assert.equal(
    CandidateSchema.safeParse({ ...base, publicTechnicalDomain: 'javascript' }).success,
    true,
  )
  for (const schema of [CandidateSchema, sourceOnly])
    assert.equal(
      schema.safeParse({ ...base, knowledge: [knowledge], publicTechnicalDomain: 'javascript' })
        .success,
      false,
    )
  assert.match(
    knowledgeExtractionInstructions(true),
    /Source-only extraction.*never existing endpoints/,
  )
  assert.match(
    knowledgeExtractionInstructions(true),
    /Reported or quoted spans must retain their stance/,
  )
  assert.match(knowledgeExtractionInstructions(true), /titles are labels, not identity/)
  assert.match(knowledgeExtractionInstructions(false), /disabled/)
})

test('source snapshot preserves original part indices and UTF16 text without changing joined commitment text', () => {
  const original = structuredClone(message)
  const evidence = trustedSourceEvidence([original], () => false)[0]!
  assert.deepEqual(evidence.parts, [
    { type: 'text', text: 'Header' },
    { type: 'other' },
    { type: 'text', text: '😀Tool A条件声明' },
  ])
  assert.equal(evidence.sourceText, 'Header\n😀Tool A条件声明')
  const spanPart = evidence.parts![2]!
  assert.equal(spanPart.type === 'text' && spanPart.text.slice(2, 8), 'Tool A')
  original.parts[2] = { type: 'text', text: 'later-mutated-body' }
  assert.equal(spanPart.type === 'text' && spanPart.text, '😀Tool A条件声明')
  assert.deepEqual(subjectLearningMessages([message], true)[0]!.textParts, [
    { partIndex: 0, text: 'Header' },
    { partIndex: 2, text: '😀Tool A条件声明' },
  ])
  assert.equal(trustedSourceEvidence([{ ...message, origin: 'history' }], () => false).length, 0)
  assert.equal(trustedSourceEvidence([message], () => true)[0]!.available, false)
})

test('changed repair drops previous structured semantics rather than pretending unchanged knowledge was saved', () => {
  const candidate = CandidateSchema.parse({ ...base, knowledge: [knowledge] })
  assert.deepEqual(repairLearningCandidate(candidate, candidate.content).candidate.knowledge, [
    knowledge,
  ])
  assert.equal(
    repairLearningCandidate(candidate, 'Changed neutral statement.').candidate.knowledge,
    undefined,
  )
  assert.deepEqual(candidate.knowledge, [knowledge])
})

test('actual source-only extractor sends original textParts and span instructions, with no prior memory input', async () => {
  const store = new Store(':memory:')
  let captured = false
  try {
    store.set('settings', { ...store.settings, memoryKnowledgeEnabled: true })
    const schema = buildLearningCandidateSchema(false, false, true, true)
    const context: SourceStageContext = {
      sceneId: 'scene',
      messages: [message],
      signal: new AbortController().signal,
      store,
      projection: new ContextProjection('scene'),
      candidateSchema: schema,
      relationInstructions: knowledgeExtractionInstructions(true),
      router: {
        complete: async (_role, messages, tools) => {
          captured = true
          assert.match(messages[0]!.content!, /UTF-16 code-unit offsets, end exclusive/)
          const wire = JSON.parse(messages[1]!.content!)
          assert.deepEqual(wire.messages[0].textParts, [
            { partIndex: 0, text: 'Header' },
            { partIndex: 2, text: '😀Tool A条件声明' },
          ])
          assert.equal(Object.hasOwn(wire, 'relatedMemories'), false)
          assert.ok(tools?.find((tool) => tool.name === 'learning.propose')!.inputSchema.properties)
          const completion: Completion = {
            text: '',
            toolCalls: [
              {
                id: 'synthetic-call',
                name: 'learning.propose',
                arguments: { ...base, knowledge: [knowledge] },
              },
            ],
            usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
            modelId: 'synthetic-model',
            assistant: { role: 'assistant', content: '' },
          }
          return completion
        },
      },
    }
    const result = await extractSources(context)
    assert.equal(captured, true)
    assert.equal(result.calls.length, 1)
    assert.deepEqual((result.calls[0]!.arguments as { knowledge: unknown[] }).knowledge, [
      knowledge,
    ])
  } finally {
    store.close()
  }
})

test('reported and quoted knowledge cannot launder an ordinary explicit fact, while asserted property statements stay typed', () => {
  const on = buildLearningCandidateSchema(false, false, true, true)
  for (const stance of ['quoted', 'reported'] as const) {
    const proposal = { ...knowledge, sourceSpans: [{ ...knowledge.sourceSpans[0], stance }] }
    assert.equal(
      on.safeParse({ ...base, kind: 'fact', epistemicKind: 'explicit', knowledge: [proposal] })
        .success,
      false,
    )
    assert.equal(
      on.safeParse({ ...base, epistemicKind: 'reported', knowledge: [proposal] }).success,
      true,
    )
  }
  const property = {
    ...knowledge,
    predicate: 'has_property',
    property: 'version',
    subject: { kind: 'new', entityKind: 'tool', title: 'Tool A' },
    object: { kind: 'literal', valueType: 'text', value: 'v2' },
    sourceSpans: [{ ...knowledge.sourceSpans[0], stance: 'asserted' }],
  }
  assert.equal(on.safeParse({ ...base, knowledge: [property] }).success, true)
  assert.equal(
    on.safeParse({ ...base, knowledge: [{ ...property, property: 'invented' }] }).success,
    false,
  )
})
