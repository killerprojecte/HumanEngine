import test from 'node:test'
import assert from 'node:assert/strict'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { ReplyPlan, PlanEvidence } from '../src/runtime/reply-plan.ts'

const plan = (patch: Record<string, unknown> = {}) =>
  ({
    action: 'reply',
    deliveryTool: 'messages.send',
    targetMessageIds: ['message-1'],
    contextMessageIds: ['message-1'],
    strategy: 'Explain briefly.',
    constraints: ['Do not promise future delivery.'],
    facts: [],
    assets: [],
    emoji: [],
    expressionGoals: ['Use natural language.'],
    memorySelections: [],
    actions: [],
    ...patch,
  }) as ReplyPlan & {
    expressionGoals: string[]
    memorySelections: Array<{ evidenceId: string; memoryIds: string[] }>
  }
const record = (content: string) => ({
  id: 'memory-1',
  content,
  kind: 'claim',
  status: 'disputed',
  confidence: 0.2,
  lineageStatus: 'unverified',
  scope: { kind: 'scene', sceneId: 'scene' },
  sources: [{ sceneId: 'scene', messageId: 'message-1', senderId: 'user-1' }],
  occurredAt: '2026-10-01T00:00:00Z',
  learnedAt: '2026-10-02T00:00:00Z',
  revision: 'PRIVATE_REVISION',
  raw: 'RAW_MEMORY_DUMP',
  score: 0.87,
})

test('memory content is opt-in through exact record selection; uncertainty and source dates survive without internal scores', () => {
  const evidence: PlanEvidence[] = [
    {
      id: 'tool:1',
      toolName: 'memory.search',
      value: [record('Maybe enabled; a later source disputes this.')],
    },
    { id: 'tool:2', toolName: 'memory.person', value: [record('UNSELECTED_MEMORY')] },
  ]
  assert.deepEqual(buildReplyBrief(plan(), evidence, []).evidence, [])
  for (const selected of [
    plan({ memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['memory-1'] }] }),
  ]) {
    const brief = buildReplyBrief(selected, evidence, [])
    const wire = JSON.stringify(brief)
    assert.match(wire, /Maybe enabled; a later source disputes this/)
    assert.match(wire, /disputed/)
    assert.match(wire, /unverified/)
    assert.match(wire, /2026-10-01T00:00:00Z/)
    assert.match(wire, /message-1/)
    assert.doesNotMatch(wire, /UNSELECTED_MEMORY|confidence|revision|score|scope|RAW_MEMORY_DUMP/)
  }
})

test('exact selection includes a wanted seventh memory without arbitrary prefix selection; long excerpts explicitly mark omissions', () => {
  const evidence = [
    {
      id: 'tool:1',
      toolName: 'memory.search',
      value: Array.from({ length: 12 }, (_, i) => ({
        ...record(`Memory ${i}: ${'detail '.repeat(500)}`),
        id: `memory-${i}`,
      })),
    },
  ]
  const brief = buildReplyBrief(
    plan({ memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['memory-7'] }] }),
    evidence,
    [],
  )
  const receipt = brief.evidence[0] as unknown as {
    memories: { id: string; content: string; contentTruncated: boolean }[]
    caution: string
  }
  assert.equal(receipt.memories.length, 1)
  assert.equal(receipt.memories[0]!.id, 'memory-7')
  assert.equal(receipt.memories[0]!.content.length, 1200)
  assert.equal(receipt.memories[0]!.contentTruncated, true)
  assert.match(receipt.caution, /omit qualifications/)
  const citationOnly = buildReplyBrief(
    plan({ facts: [{ text: 'Necessary fact selected by Planner.', evidenceIds: ['tool:1'] }] }),
    evidence,
    [],
  )
  assert.doesNotMatch(JSON.stringify(citationOnly), /Memory 0|Memory 7|detail detail/)
  assert.match(JSON.stringify(citationOnly), /memory_not_selected/)
})

test('handoff excludes proposed arguments and raw action dumps; only actual receipt state authorizes outcome wording', () => {
  const p = plan({
    actions: [{ toolName: 'memes.feedback', arguments: { secret: 'PROPOSED_ARGUMENTS' } }],
  })
  assert.deepEqual(buildReplyBrief(p, [], []).actionReceipts, [])
  const receipts = [
    {
      id: 'action:1',
      toolName: 'memes.feedback',
      value: {
        ok: true,
        actionTaken: 'ranking_adjusted',
        positive: false,
        disabled: false,
        assetId: 'asset-1',
        sourceMessageId: 'message-1',
        arguments: 'RAW_ARGUMENTS',
        raw: 'RAW_RECEIPT',
      },
    },
    {
      id: 'action:2',
      toolName: 'learning.propose',
      value: { status: 'rejected', reason: 'source_unavailable' },
    },
    { id: 'action:3', toolName: 'extension.propose', value: { result: 'No explicit outcome' } },
  ]
  const brief = buildReplyBrief(p, [], receipts)
  assert.deepEqual(
    brief.actionReceipts.map((r) => r.status),
    ['confirmed', 'rejected', 'unconfirmed'],
  )
  const wire = JSON.stringify(brief)
  assert.match(wire, /ranking_adjusted/)
  assert.match(wire, /"disabled":false/)
  assert.doesNotMatch(wire, /PROPOSED_ARGUMENTS|RAW_ARGUMENTS|RAW_RECEIPT|"actions"/)
  assert.equal(
    buildReplyBrief(p, [], [{ id: 'action:4', value: { ok: true, error: 'conflicting_failure' } }])
      .actionReceipts[0]!.status,
    'failed',
  )
})

test('selected citations and vision caveats survive; unselected results and raw pages do not', () => {
  const p = plan({
    facts: [{ text: 'A limited first-frame observation.', evidenceIds: ['context:1', 'tool:1'] }],
    assets: [{ assetId: 'asset-1', evidenceId: 'tool:2' }],
  })
  const brief = buildReplyBrief(
    p,
    [
      {
        id: 'context:1',
        value: {
          trust: 'untrusted_image_descriptions',
          framePolicy: 'first_frame_only',
          images: [
            { messageId: 'message-1', status: 'unavailable', reason: 'route_not_configured' },
          ],
        },
      },
      {
        id: 'tool:1',
        toolName: 'web-read.read',
        value: {
          url: 'https://example.test/source',
          title: 'Source',
          coverage: 'partial',
          rawBody: 'RAW_PAGE',
          body: 'RAW_PAGE',
          headers: { authorization: 'SECRET' },
        },
      },
      {
        id: 'tool:2',
        toolName: 'memes.search',
        value: {
          items: [
            {
              id: 'asset-1',
              tags: { emotion: ['happy'], appearance: ['UNNECESSARY_VISUAL_DETAIL'] },
            },
            { id: 'asset-2', tags: { emotion: ['UNSELECTED_ASSET'] } },
          ],
        },
      },
    ],
    [],
  )
  const wire = JSON.stringify(brief)
  assert.match(wire, /first_frame_only|untrusted_image_descriptions/)
  assert.match(wire, /route_not_configured/)
  assert.match(wire, /https:\/\/example.test\/source/)
  assert.match(wire, /partial/)
  assert.doesNotMatch(wire, /RAW_PAGE|SECRET|UNSELECTED_ASSET|UNNECESSARY_VISUAL_DETAIL/)
  const projected = new ContextProjection('scene').project(brief)
  assert.equal(
    projected.plan.assets[0]!.assetId,
    (projected.evidence[2] as { assets: { assetId: string }[] }).assets[0]!.assetId,
  )
})

test('combined outbound payload preserves literal utterance, sentAt, clock/timezone and short structural IDs with measured character reduction', () => {
  const canonical = 'canonical-message-identifier-that-is-deliberately-long'
  const utterance = '[image/unknown] is literal user text. Do not rewrite it.'
  const messages = [
    {
      id: canonical,
      senderId: 'user',
      sentAt: '2026-10-28T10:00:00Z',
      parts: [{ type: 'text', text: utterance }],
    },
  ]
  const p = plan({
    targetMessageIds: [canonical],
    contextMessageIds: [canonical],
    facts: [{ text: 'The user supplied this text.', evidenceIds: ['message:' + canonical] }],
    actions: [{ toolName: 'learning.propose', arguments: { raw: 'private'.repeat(100) } }],
  })
  const evidence = [{ id: 'message:' + canonical, value: messages[0] }]
  const envelope = { now: '2026-10-28T10:01:00Z', timezone: 'Asia/Shanghai', messages }
  const before = JSON.stringify({ ...envelope, plan: p, evidence, actionReceipts: [] })
  const after = new ContextProjection('scene').stringify({
    ...envelope,
    ...buildReplyBrief(p, evidence, []),
  })
  assert.ok(after.length < before.length)
  assert.match(after, /2026-10-28T10:00:00Z/)
  assert.match(after, /2026-10-28T10:01:00Z/)
  assert.match(after, /Asia\/Shanghai/)
  assert.equal(JSON.parse(after).messages[0].parts[0].text, utterance)
  assert.doesNotMatch(after, /canonical-message|privateprivate/)
  console.log(
    `Reply brief synthetic JSON characters: ${before.length} -> ${after.length}; tokens not measured`,
  )
  assert.equal(buildReplyBrief(p, evidence, []).plan.replySuggestion, undefined)
  assert.equal(
    buildReplyBrief(plan({ replySuggestion: 'Optional example.' }), [], []).plan.replySuggestion,
    'Optional example.',
  )
})

test('vision handoff keeps only selected messages and explicit reply edges, including unavailable caveats when facts are empty', () => {
  const context = [
    {
      trust: 'untrusted_image_descriptions',
      framePolicy: 'first_frame_only',
      images: [
        { messageId: 'message-1', status: 'unavailable', reason: 'route_not_configured' },
        {
          messageId: 'quoted-image',
          status: 'described',
          description: 'An uncertain first-frame observation.',
        },
        { messageId: 'unrelated', status: 'described', description: 'UNRELATED_IMAGE_CANARY' },
      ],
    },
  ]
  const brief = buildReplyBrief(plan(), [], [], context, ['quoted-image'])
  assert.equal(brief.imageContext[0]!.images.length, 2)
  const wire = JSON.stringify(brief)
  assert.match(wire, /route_not_configured/)
  assert.match(wire, /first_frame_only/)
  assert.match(wire, /uncertain first-frame observation/)
  assert.doesNotMatch(wire, /UNRELATED_IMAGE_CANARY/)
  assert.deepEqual(
    buildReplyBrief(
      plan(),
      [],
      [],
      [{ images: [{ messageId: 'unrelated', description: 'Never selected' }] }],
    ).imageContext,
    [],
  )
})

test('per-image frame policies and selected budget omissions survive even without any described images', () => {
  const context = {
    trust: 'untrusted_image_descriptions',
    framePolicy: 'per_image',
    images: [
      {
        messageId: 'message-1',
        status: 'described',
        framePolicy: 'stored_library_observation',
        description: 'Stored observation, not a new visual inspection.',
      },
      {
        messageId: 'quoted-image',
        status: 'described',
        framePolicy: 'first_frame_only',
        description: 'First frame only.',
      },
    ],
    omitted: [
      { messageId: 'budget-image', reason: 'image_budget_exhausted' },
      { messageId: 'unrelated', reason: 'UNRELATED_OMISSION' },
    ],
  }
  const brief = buildReplyBrief(plan(), [], [], [context], ['quoted-image', 'budget-image'])
  assert.deepEqual(
    brief.imageContext[0]!.images.map((image) => image.framePolicy),
    ['stored_library_observation', 'first_frame_only'],
  )
  assert.deepEqual(brief.imageContext[0]!.omitted, [
    { messageId: 'budget-image', reason: 'image_budget_exhausted' },
  ])
  const omittedOnly = buildReplyBrief(
    plan(),
    [
      {
        id: 'context:1',
        value: {
          images: [],
          omitted: [{ messageId: 'message-1', reason: 'image_budget_exhausted' }],
        },
      },
    ],
    [],
    [{ images: [], omitted: [{ messageId: 'message-1', reason: 'image_budget_exhausted' }] }],
  )
  assert.equal(omittedOnly.imageContext.length, 1)
  assert.equal(omittedOnly.evidence.length, 1)
  assert.match(JSON.stringify(omittedOnly.imageContext), /image_budget_exhausted/)
  assert.doesNotMatch(JSON.stringify(brief), /UNRELATED_OMISSION/)
})

test('web-read citations preserve sourceUrl and trust without returning the page body', () => {
  const brief = buildReplyBrief(
    plan({ facts: [{ text: 'A sourced fact.', evidenceIds: ['tool:web'] }] }),
    [
      {
        id: 'tool:web',
        toolName: 'web-read.read',
        value: {
          sourceUrl: 'https://example.test/primary-source',
          trust: 'untrusted_public_web',
          title: 'Primary source',
          text: 'RAW_PUBLIC_PAGE_CANARY',
          body: 'RAW_PUBLIC_PAGE_CANARY',
          html: 'RAW_HTML_CANARY',
        },
      },
    ],
    [],
  )
  const wire = JSON.stringify(brief)
  assert.match(wire, /https:\/\/example.test\/primary-source/)
  assert.match(wire, /untrusted_public_web/)
  assert.doesNotMatch(wire, /RAW_PUBLIC_PAGE_CANARY|RAW_HTML_CANARY/)
})
