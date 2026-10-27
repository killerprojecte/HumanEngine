import assert from 'node:assert/strict'
import test from 'node:test'
import { personalPronounsSupported } from '../src/learning-policy.ts'
import {
  completed,
  harness,
  identifier,
  message,
  proposal,
  subjectId,
} from './helpers/learning-technical-fixture.ts'

test('technical source faithfulness distinguishes unsupported singular pronouns from lexical tokens and explicit declarations', () => {
  const source = 'The encoder uses fixed-width unsigned integers.'
  for (const candidate of [
    'She configured the integer encoder.',
    'He configured the integer encoder.',
    '她要求定宽整数编码。',
    '他要求定宽整数编码。',
    "She's selecting a packet codec.",
  ])
    assert.equal(personalPronounsSupported(candidate, [source]), false)
  for (const candidate of [
    'The encoder uses fixed-width integers.',
    '其他线程读取同一缓冲区。',
    'The diagnostic prints "she" as a literal token.',
  ])
    assert.equal(personalPronounsSupported(candidate, [source]), true)
  assert.equal(
    personalPronounsSupported('她负责整数编码器。', ['我是女性。编码器采用无符号整数。']),
    true,
  )
  assert.equal(
    personalPronounsSupported('他负责整数编码器。', ['本人为男性。编码器采用无符号整数。']),
    true,
  )
  for (const declaration of [
    '假如我是女性，则检查编码器。',
    '我不是女性。编码器采用无符号整数。',
    '我的代词不是她。',
  ])
    assert.equal(personalPronounsSupported('她负责整数编码器。', [declaration]), false)
  const claim = 'She configured the integer encoder.'
  assert.equal(
    personalPronounsSupported(claim, ['My pronouns are she/her. The encoder uses integers.']),
    true,
  )
  assert.equal(
    personalPronounsSupported('He configured the integer encoder.', [
      'I am male. The encoder uses integers.',
    ]),
    true,
  )
  for (const declaration of [
    'If I am female, I would test the encoder.',
    'I am not female.',
    'My pronouns are not she/her.',
    'Maybe my pronouns are she/her.',
    'I am female, joking.',
    'The terminal quotes "My pronouns are she/her."',
    '```\nMy pronouns are she/her.\n```',
    "The sample contains 'My pronouns are she/her.'",
  ])
    assert.equal(personalPronounsSupported(claim, [declaration]), false)
  const literal = 'She is the diagnostic operator.'
  assert.equal(personalPronounsSupported(literal, [], [literal]), true)
  assert.equal(
    personalPronounsSupported(literal, [], ['If she is the diagnostic operator.']),
    false,
  )
  assert.equal(
    personalPronounsSupported('She enables binary output.', [], ['She enables decimal output.']),
    false,
  )
})

test('source-only extraction rejects invented attributes without discarding independent technical candidates', async () => {
  const h = harness()
  try {
    const source = message(610, 'The encoder uses fixed-width unsigned integers.')
    const unrelated = message(611, 'My pronouns are she/her.', identifier(6))
    const rejected = [
      proposal('She configures the integer encoder.', source, true),
      proposal('She enables integer encoding.', source),
      proposal('She configures packet serialization.', source, true),
      proposal('My pronouns are she/her.', unrelated),
    ]
    rejected[1]!.arguments.sourceMessageIds = [source.id, unrelated.id]
    rejected[2]!.arguments.sourceMessageIds = [source.id, unrelated.id]
    rejected[3]!.arguments.personId = subjectId
    const valid = [
      proposal('The encoder uses fixed-width integers.', source, true),
      proposal('The encoder accepts unsigned values.', source, true),
    ]
    let neutralInstructionSeen = false,
      repairRequests = 0
    h.install(async (_role, prompts, tools) => {
      neutralInstructionSeen ||= JSON.stringify(prompts).includes(
        'stable personId or neutral wording',
      )
      if (tools?.some((item) => item.name === 'learning.repair')) {
        repairRequests++
        return completed()
      }
      return completed([...rejected, ...valid])
    })
    await h.run([source, unrelated])
    assert.equal(neutralInstructionSeen, true)
    assert.equal(repairRequests, 1)
    const records = h.engine.memory.adminList()
    assert.equal(records.length, 2)
    assert.ok(
      records.every((record) => valid.some((item) => item.arguments.content === record.content)),
    )
    assert.ok(
      records.every(
        (record) =>
          record.scope.kind === 'scene' &&
          record.scope.personId === subjectId &&
          record.kind === 'fact',
      ),
    )
    assert.ok(
      records.every(
        (record) =>
          record.sources.length === 1 &&
          record.sources[0]!.messageId === source.id &&
          record.sources[0]!.senderId === subjectId,
      ),
    )
    assert.equal(
      h.store.logs.filter((entry) => entry.code === 'learning_candidate_ungrounded').length,
      4,
    )
    for (const item of rejected)
      assert.equal(JSON.stringify(h.store.logs).includes(item.arguments.content as string), false)
  } finally {
    await h.close()
  }
})
