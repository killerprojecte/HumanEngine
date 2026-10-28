import { fixturePlanCompletion, fixtureReplyPlan } from './helpers/reply-plan.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { ReplyGrounding } from '../src/runtime.ts'
const segments = (text: string) => [{ parts: [{ type: 'text' as const, text }] }]
test('chronology requires the event clause, not a shared clock token', () => {
  const guard = new ReplyGrounding()
  guard.addEvidence('今天上午喝了茶。今天面试没过。')
  assert.equal(
    guard.check(segments('今天上午才刚有件好事，下午就挨这一下')),
    'unsupported_event_chronology',
  )
  assert.equal(guard.check(segments('今天上午喝了茶')), undefined)
  assert.equal(guard.check(segments('面试没过确实不好受')), undefined)
})
test('partial search does not establish absence and incomplete feedback does not activate a receipt', () => {
  const guard = new ReplyGrounding()
  guard.observeTool('memes.search', [])
  assert.equal(
    guard.check(segments('图库里只有刚才那张，别的开心图暂时没有存货')),
    'search_is_not_inventory_proof',
  )
  assert.equal(guard.check(segments('这次检索未找到合适候选')), undefined)
  guard.observeTool('memes.feedback', { preference: -0.2 })
  assert.equal(guard.check(segments('那张庆祝图以后不用了')), undefined)
  assert.equal(guard.check(segments('已记录对这张图的负反馈，会降低选择倾向')), undefined)
})

import { Store } from '../src/store.ts'
import { Runtime } from '../src/runtime.ts'
import { Router, type Completion } from '../src/models.ts'
const completion = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
async function runFixture(
  repair: boolean,
  cancel = false,
  input = '今天有件开心事，但今天面试没过。',
  invalidText = '今天上午才刚有件好事，下午就挨这一下。',
) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  const router = new Router(store.models)
  let thinkerCalls = 0
  let sawRejection = false
  router.complete = async (role, messages) => {
    const planned = fixturePlanCompletion(role, messages)
    if (planned) return planned
    if (role === 'planner')
      return completion([
        {
          id: 'plan',
          name: 'participation.decide',
          arguments: { action: 'reply', reasonCode: 'fixture' },
        },
      ])
    thinkerCalls++
    sawRejection ||= messages.some(
      (m) => m.role === 'tool' && String(m.content).includes('grounding_rejected'),
    )
    if (repair && thinkerCalls > 2) return completion([])
    return completion([
      {
        id: `send-${thinkerCalls}`,
        name: 'messages.send',
        arguments: {
          operationId: 'reply',
          segments: segments(repair && thinkerCalls > 1 ? '面试没过确实不好受。' : invalidText),
        },
      },
    ])
  }
  const runtime = new Runtime(store, () => router)
  if (cancel)
    runtime.beforeSend({
      id: 'fixture',
      priority: 0,
      handler: async () => ({ action: 'cancel', reason: 'fixture_cancel' }),
    })
  try {
    const messages = await runtime.simulate(runtime.createSimulation(), input)
    return { messages: messages.filter((m) => m.senderId === 'bot'), thinkerCalls, sawRejection }
  } finally {
    runtime.dispose()
    store.close()
  }
}
test('captured chronology overclaim is rejected before send and corrected in the same turn', async () => {
  const outcome = await runFixture(true)
  assert.equal(outcome.sawRejection, true)
  assert.equal(outcome.messages.length, 1)
  assert.deepEqual(outcome.messages[0]!.parts, segments('面试没过确实不好受。')[0]!.parts)
})
test('continually invalid model exhausts bounded repair budget with one honest fallback', async () => {
  const outcome = await runFixture(false)
  assert.equal(outcome.thinkerCalls, 3)
  assert.equal(outcome.messages.length, 1)
  assert.deepEqual(
    outcome.messages[0]!.parts,
    segments('这些细节我还无法确认，先不作推断。')[0]!.parts,
  )
})

test('a corrected send cancelled by a hook stays cancelled, without fallback', async () => {
  const outcome = await runFixture(true, true)
  assert.equal(outcome.sawRejection, true)
  assert.equal(outcome.messages.length, 0)
})
test('questions and prospective advice are not factual event chronology', () => {
  const guard = new ReplyGrounding()
  assert.equal(guard.check(segments('今天上午怎么样？')), undefined)
  assert.equal(guard.check(segments('今天下午记得休息。')), undefined)
  assert.equal(guard.check(segments('What happened this morning?')), undefined)
})

test('English input retains an English honest fallback', async () => {
  const outcome = await runFixture(false, false, 'My interview did not go well today.')
  assert.deepEqual(
    outcome.messages[0]!.parts,
    segments('I cannot verify those details, so I will not assume them.')[0]!.parts,
  )
})
test('a quoted event is not evidence for asserting that event happened to this user', () => {
  const guard = new ReplyGrounding()
  guard.addEvidence('例句：“今天上午喝了茶”。但今天面试没过。')
  assert.equal(guard.check(segments('今天上午喝了茶')), 'unsupported_event_chronology')
})

test('live media regression: inventory, future delivery and visual relations are separate claims', () => {
  const guard = new ReplyGrounding()
  guard.observeTool('memes.search', { items: [], coverage: 'partial' })
  for (const text of [
    '库里没有专门的“鞠躬道谢”图',
    '这边只有一张开心图',
    '手头没有别的表情',
    'The library has no thank-you image',
  ])
    assert.equal(guard.check(segments(text)), 'search_is_not_inventory_proof')
  for (const text of [
    '改天再单独给你补一张庆祝的',
    '明天给你发一张图',
    "I'll send you another image tomorrow",
  ])
    assert.equal(guard.check(segments(text)), 'unscheduled_future_delivery')
  assert.equal(
    guard.check(segments('这张捧着小爱心、笑到眯眼的蓝团子最适合表达感谢了')),
    'unsupported_visual_relation',
  )
  for (const text of [
    '这次没找到合适的鞠躬图',
    'This search has no matching image',
    '如果你下次想庆祝，可以再找我选图',
    '祝你明天一切顺利',
    '你计划明天给大家发一张图',
    '这张适合表达感谢的心情',
  ])
    assert.equal(guard.check(segments(text)), undefined, text)
})

test('unscheduled delivery capture is rejected at real send boundary and repaired', async () => {
  const outcome = await runFixture(
    true,
    false,
    '今天有件开心事，但今天面试没过。',
    '改天再单独给你补一张庆祝的',
  )
  assert.equal(outcome.sawRejection, true)
  assert.equal(outcome.messages.length, 1)
  assert.deepEqual(outcome.messages[0]!.parts, segments('面试没过确实不好受。')[0]!.parts)
})

test('query qualifiers and generic conditions cannot license independent unsupported claims', () => {
  const guard = new ReplyGrounding()
  guard.observeTool('memes.search', { items: [], coverage: 'partial' })
  for (const text of [
    '这次没找到但库里肯定没有',
    '这次没找到，库里肯定没有',
    '这次库里肯定没有',
    'This search found nothing but the library has no other image',
  ])
    assert.equal(guard.check(segments(text)), 'search_is_not_inventory_proof', text)
  for (const text of [
    '如果顺利我明天给你发一张',
    '要是顺利明天给你补一张',
    "If everything goes well I'll send you an image tomorrow",
  ])
    assert.equal(guard.check(segments(text)), 'unscheduled_future_delivery', text)
  for (const text of [
    '下次你再问我，我给你找一张',
    '如果你下次再问我，我给你找一张',
    '本次图库检索没有匹配结果',
    'This library search results list has no match',
  ])
    assert.equal(guard.check(segments(text)), undefined, text)
})

const negativeReceipt = {
  ok: true,
  actionTaken: 'ranking_adjusted',
  scope: 'asset',
  assetId: 'image-a',
  sourceMessageId: 'feedback-a',
  positive: false,
  disabled: false,
}
test('authoritative feedback acknowledgement replaces captured overpromise only for exact source', () => {
  const guard = new ReplyGrounding()
  guard.addFeedbackSource('feedback-a', segments('这张图以后别用了')[0]!.parts)
  guard.observeTool('memes.feedback', negativeReceipt)
  const bad = {
    replyToId: 'feedback-a',
    parts: segments('那张我不再用啦，后面也会少发表情、多跟你说话')[0]!.parts,
  }
  const unrelated = { replyToId: 'other-topic', parts: segments('这项功能我也很喜欢')[0]!.parts }
  const normalized = guard.normalizeFeedback([bad, unrelated])
  assert.match(JSON.stringify(normalized[0]), /负反馈/)
  assert.doesNotMatch(JSON.stringify(normalized[0]), /仍可使用/)
  assert.match(JSON.stringify(normalized[0]), /不改动全局/)
  assert.deepEqual(normalized[1], unrelated)
  assert.deepEqual(
    guard.normalizeFeedback(segments('没有显式回复目标')),
    segments('没有显式回复目标'),
  )
  assert.equal(guard.check(segments('没有显式回复目标')), 'feedback_target_required')
  guard.observeTool('memes.feedback', negativeReceipt)
  assert.deepEqual(guard.normalizeFeedback([bad, unrelated]), normalized)
})
test('failed, malformed and unrelated feedback receipts never create an acknowledgement', () => {
  const guard = new ReplyGrounding()
  guard.addFeedbackSource('feedback-a', segments('Do not use this image')[0]!.parts)
  const send = [{ replyToId: 'feedback-a', parts: segments('ordinary reply')[0]!.parts }]
  for (const receipt of [
    { ...negativeReceipt, ok: false },
    { error: 'rejected' },
    { ...negativeReceipt, sourceMessageId: 'outside' },
    { ...negativeReceipt, positive: undefined },
  ]) {
    guard.observeTool('memes.feedback', receipt)
    assert.deepEqual(guard.normalizeFeedback(send), send)
  }
  guard.observeTool('memes.feedback', { ...negativeReceipt, positive: true, disabled: true })
  const output = JSON.stringify(guard.normalizeFeedback(send))
  assert.match(output, /positive feedback/)
  assert.match(output, /was disabled when/)
  assert.doesNotMatch(output, /remains enabled/)
})

async function runFeedbackAck(
  cancel: boolean,
  unknownDelivery = false,
  omittedTarget: boolean | 'always' = false,
  mixed = false,
  retargetHook: false | 'other' | 'missing' | 'reorder' | 'swap' = false,
  withoutFeedback = false,
  singlePlannerTarget = false,
  readOldSource = false,
) {
  let warming = false
  let sawTargetRejection = false
  let adapterCalls = 0
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  const router = new Router(store.models)
  let calls = 0
  let sourceMessageId = ''
  router.complete = async (role, messages) => {
    const plan = role === 'planner' ? fixtureReplyPlan(messages) : undefined
    if (plan) {
      sourceMessageId = readOldSource ? 'old-live-feedback' : plan.contextMessageIds.at(-1)!
      if (readOldSource && !messages.some((m) => m.role === 'tool'))
        return completion([
          { id: 'history', name: 'messages.get', arguments: { ids: [sourceMessageId] } },
        ])
      plan.targetMessageIds = [...new Set([...plan.contextMessageIds, sourceMessageId])]
      plan.contextMessageIds = [...plan.targetMessageIds]
      plan.actions = withoutFeedback
        ? []
        : [{ toolName: 'memes.feedback', arguments: { sourceMessageId } }]
      return completion([{ id: 'reply-plan', name: 'reply.plan', arguments: plan }])
    }
    if (role === 'planner')
      return completion([
        {
          id: 'plan',
          name: 'participation.decide',
          arguments: {
            action: warming ? 'silent' : 'reply',
            reasonCode: 'feedback',
            targetMessageIds: singlePlannerTarget
              ? [JSON.parse(messages.at(-1)!.content as string).messages.at(-1).id]
              : [],
          },
        },
      ])
    const inputs = JSON.parse(messages.find((m) => m.role === 'user')!.content as string).messages
    sourceMessageId = readOldSource ? 'old-live-feedback' : inputs.at(-1).id
    sawTargetRejection ||= messages.some(
      (m) => m.role === 'tool' && String(m.content).includes('feedback_target_required'),
    )
    if (++calls > 2 && omittedTarget !== 'always') return completion([])
    return completion([
      {
        id: `send-${calls}`,
        name: 'messages.send',
        arguments: {
          operationId: 'same-ack',
          segments: [
            {
              replyToId:
                omittedTarget && (calls === 1 || omittedTarget === 'always')
                  ? undefined
                  : sourceMessageId,
              parts: segments(
                withoutFeedback ? '普通回复' : '那张我不再用啦，后面也会少发表情、多跟你说话',
              )[0]!.parts,
            },
            ...(mixed
              ? [{ replyToId: inputs[0].id, parts: segments('以后不再用旧接口')[0]!.parts }]
              : []),
          ],
        },
      },
    ])
  }
  const runtime = new Runtime(store, () => router)
  runtime.addExtension({
    tools: [{ name: 'memes.feedback', description: 'Fixture', inputSchema: {} }],
    execute: async (_name, arguments_) => {
      assert.equal(typeof arguments_.sourceMessageId, 'string')
      assert.ok(
        !/^m\d+$/.test(arguments_.sourceMessageId as string),
        'feedback runtime receives canonical source, never model alias',
      )
      return { ...negativeReceipt, sourceMessageId: arguments_.sourceMessageId }
    },
  })
  if (retargetHook)
    runtime.beforeSend({
      id: 'retarget',
      priority: 0,
      handler: async (parts) => ({
        action: 'continue',
        segments:
          retargetHook === 'swap'
            ? parts.map((part, index) => ({
                ...part,
                replyToId: parts[parts.length - 1 - index]!.replyToId,
                parts: segments('那张我不再用啦，后面也会少发表情、多跟你说话')[0]!.parts,
              }))
            : retargetHook === 'reorder'
              ? [...parts].reverse().map((part) => ({ ...part }))
              : parts.map((part) => ({
                  ...part,
                  replyToId: retargetHook === 'missing' ? undefined : 'other-target',
                  parts: segments('那张我不再用啦，后面也会少发表情、多跟你说话')[0]!.parts,
                })),
      }),
    })
  if (cancel)
    runtime.beforeSend({
      id: 'cancel',
      priority: 0,
      handler: async () => ({ action: 'cancel', reason: 'test' }),
    })
  try {
    if (unknownDelivery) {
      const scene = {
        id: 'receipt-scene',
        kind: 'group' as const,
        adapterId: 'receipt-fixture',
        accountId: 'bot',
        platformSceneId: 'group',
      }
      store.saveScene({
        ...scene,
        name: 'Receipt',
        participationEnabled: true,
        learningEnabled: false,
        proactiveEnabled: false,
      })
      runtime.addAdapter(scene.adapterId, {
        send: async (_scene, segment) => {
          adapterCalls++
          if (readOldSource) assert.match(JSON.stringify(segment.parts), /负反馈/)
          return { status: 'unknown' as const }
        },
        getMessage: async () => ({
          items: readOldSource
            ? [
                {
                  id: 'old-live-feedback',
                  sceneId: scene.id,
                  senderId: 'person',
                  sentAt: '2026-01-01T00:00:00Z',
                  receivedAt: '2026-01-01T00:00:00Z',
                  origin: 'history' as const,
                  status: 'available' as const,
                  parts: segments('这张旧图不用了')[0]!.parts,
                },
              ]
            : [],
          coverage: 'complete' as const,
        }),
        history: async () => ({ items: [], coverage: 'complete' as const }),
        dispose() {},
      })
      const now = new Date().toISOString()
      runtime.ingest(
        {
          id: 'feedback-live',
          ...(readOldSource ? { replyToId: 'old-live-feedback' } : {}),
          sceneId: scene.id,
          senderId: 'person',
          sentAt: now,
          receivedAt: now,
          origin: 'live',
          status: 'available',
          parts: segments('这张图不要再发了')[0]!.parts,
        },
        scene,
      )
      const deadline = Date.now() + 3000
      while (!adapterCalls && Date.now() < deadline)
        await new Promise((resolve) => setTimeout(resolve, 10))
      assert.equal(calls, 1)
      await new Promise((resolve) => setTimeout(resolve, 10))
      assert.equal(adapterCalls, 1)
      return runtime.preview(scene.id).filter((m) => m.senderId === 'bot')
    }
    const simulation = runtime.createSimulation()
    if (mixed) {
      warming = true
      await runtime.simulate(simulation, '我们以后不用旧接口')
      warming = false
    }
    const result = (await runtime.simulate(simulation, '这张图不要再发了')).filter(
      (m) => m.senderId === 'bot',
    )
    if (omittedTarget) assert.equal(sawTargetRejection, true)
    if (omittedTarget === 'always') assert.equal(calls, 3)
    return result
  } finally {
    runtime.dispose()
    store.close()
  }
}
test('send boundary renders actual feedback state once despite repeated feedback/send operation', async () => {
  const sent = await runFeedbackAck(false)
  assert.equal(sent.length, 1)
  assert.match(JSON.stringify(sent[0]!.parts), /负反馈/)
  assert.doesNotMatch(JSON.stringify(sent[0]!.parts), /仍可使用/)
  assert.doesNotMatch(JSON.stringify(sent[0]!.parts), /少发表情/)
})
test('authoritative acknowledgement still obeys terminal hook cancellation without fallback', async () => {
  assert.deepEqual(await runFeedbackAck(true), [])
})

test('unknown acknowledgement transport is not retried or followed by a fallback', async () => {
  assert.deepEqual(await runFeedbackAck(false, true), [])
})

test('successful feedback requires explicit send targets and aggregates multiple receipts truthfully', () => {
  const guard = new ReplyGrounding()
  guard.addFeedbackSource('feedback-a', segments('给这些图反馈')[0]!.parts)
  guard.observeTool('memes.feedback', negativeReceipt)
  assert.equal(guard.check(segments('那张我不再用啦')), 'feedback_target_required')
  assert.equal(
    guard.check([{ replyToId: 'invented-target', parts: segments('坏ack')[0]!.parts }]),
    'feedback_target_unknown',
  )
  guard.observeTool('memes.feedback', {
    ...negativeReceipt,
    assetId: 'image-b',
    positive: true,
    disabled: true,
  })
  const ack = guard.normalizeFeedback([
    { replyToId: 'feedback-a', parts: segments('bad promise')[0]!.parts },
  ])
  assert.match(JSON.stringify(ack), /相关图片/)
  assert.doesNotMatch(JSON.stringify(ack), /已处于停用状态/)
  assert.equal(guard.check(ack), undefined)
  guard.addFeedbackSource('technical-topic', segments('讨论旧接口')[0]!.parts)
  assert.equal(
    guard.check([{ replyToId: 'technical-topic', parts: segments('以后不再用旧接口')[0]!.parts }]),
    undefined,
  )
})

test('missing feedback target is rejected and explicitly corrected without guessing', async () => {
  const sent = await runFeedbackAck(false, false, true)
  assert.equal(sent.length, 1)
  assert.match(JSON.stringify(sent), /负反馈/)
})
test('actual mixed-target send preserves technical text and renders only feedback acknowledgement', async () => {
  const sent = await runFeedbackAck(false, false, false, true)
  assert.equal(sent.length, 2)
  assert.match(JSON.stringify(sent[0]), /负反馈/)
  assert.deepEqual(sent[1]!.parts, segments('以后不再用旧接口')[0]!.parts)
})
test('hook cannot retarget a receipt segment to bypass authoritative acknowledgement', async () => {
  assert.deepEqual(await runFeedbackAck(false, false, false, false, 'other'), [])
})

test('feedback bound segments cannot be removed or reordered; ordinary operation hooks retain reordering', async () => {
  assert.deepEqual(await runFeedbackAck(false, false, false, false, 'missing'), [])
  assert.deepEqual(await runFeedbackAck(false, false, false, true, 'reorder'), [])
  const reordered = await runFeedbackAck(false, false, false, true, 'reorder', true)
  assert.equal(reordered.length, 2)
  assert.deepEqual(reordered[0]!.parts, segments('以后不再用旧接口')[0]!.parts)
  assert.deepEqual(reordered[1]!.parts, segments('普通回复')[0]!.parts)
})

test('cross-swapped targets cannot move a feedback overpromise into an unrelated reply', async () => {
  assert.deepEqual(await runFeedbackAck(false, false, false, true, 'swap'), [])
})
test('successful feedback followed by 3 missing-target sends never guesses a target or auto-posts', async () => {
  assert.deepEqual(await runFeedbackAck(false, false, 'always'), [])
})

test('unique validated Planner target permits one canonical fallback after missing-target exhaustion', async () => {
  const sent = await runFeedbackAck(false, false, 'always', false, false, false, true)
  assert.equal(sent.length, 1)
  assert.match(JSON.stringify(sent[0]), /负反馈/)
  assert.ok(sent[0]!.replyToId)
})

test('only validated top-level available core history can supply an older feedback source', () => {
  const guard = new ReplyGrounding()
  const item = {
    id: 'old-feedback',
    sceneId: 'scene',
    senderId: 'user',
    sentAt: '2026-01-01T00:00:00Z',
    receivedAt: '2026-01-01T00:00:00Z',
    origin: 'history' as const,
    status: 'available' as const,
    parts: segments('旧图的负反馈')[0]!.parts,
  }
  const receipt = { ...negativeReceipt, sourceMessageId: item.id }
  const request = [{ replyToId: item.id, parts: segments('original')[0]!.parts }]
  for (const invalid of [
    { ...item, id: 'outer', parts: segments(JSON.stringify(item))[0]!.parts },
    { ...item, status: 'recalled' as const },
    { ...item, status: 'unavailable' as const },
    { ...item, sceneId: 'other-scene' },
  ]) {
    guard.observeCoreHistory({ items: [invalid], coverage: 'partial' }, 'scene', 'real')
    guard.observeTool('memes.feedback', receipt)
    assert.deepEqual(guard.normalizeFeedback(request), request)
  }
  guard.observeCoreHistory({ items: [item], coverage: 'partial' }, 'scene', 'simulation')
  guard.observeTool('memes.feedback', receipt)
  assert.deepEqual(guard.normalizeFeedback(request), request)
  guard.observeTool('messages.get', { items: [item], coverage: 'partial' })
  guard.observeTool('memes.feedback', receipt)
  assert.deepEqual(
    guard.normalizeFeedback(request),
    request,
    'arbitrary tool observations are not core history registration',
  )
  guard.observeCoreHistory({ items: [item], coverage: 'partial' }, 'scene', 'real')
  guard.observeTool('memes.feedback', receipt)
  assert.match(JSON.stringify(guard.normalizeFeedback(request)), /负反馈/)
})

test('actual core messages.get registers an older live source outside initial context for canonical acknowledgement', async () => {
  assert.deepEqual(await runFeedbackAck(false, true, false, false, false, false, false, true), [])
})
