/** Actual pure topic reassignment policy; no model, database or user data. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { validTopicReassignment } from '../../src/runtime/topic-reassignment.ts'
assert.equal(process.version, 'v24.22.1')
const source = 'src/runtime/topic-reassignment.ts'
const sourceHash = createHash('sha256').update(readFileSync(source)).digest('hex')
const cases = []
const add = (name, input) =>
  cases.push({ name, raw: JSON.stringify(input), result: validTopicReassignment(input) })
const base = { groups: [], deliveredIds: ['a', 'b'], currentTopicId: 'current', retained: [] }
add('empty', base)
add('omitted-delivered-allowed', { ...base, groups: [{ topicId: 'new', messageIds: ['a'] }] })
add('duplicate-message', { ...base, groups: [{ topicId: 'new', messageIds: ['a', 'a'] }] })
add('foreign-message', { ...base, groups: [{ topicId: 'new', messageIds: ['foreign'] }] })
add('current-without-selected', { ...base, groups: [{ topicId: 'current', messageIds: [] }] })
add('selected-remaps-current', {
  ...base,
  selectedTopicId: 'proposal',
  groups: [{ topicId: 'proposal', messageIds: ['a'] }],
})
add('surrogate-identities', {
  ...base,
  deliveredIds: ['\ud800', '\ud801'],
  groups: [{ topicId: 'new', messageIds: ['\ud800', '\ud801'] }],
})
add('surrogate-is-not-replacement', {
  ...base,
  deliveredIds: ['\ud800'],
  groups: [{ topicId: 'new', messageIds: ['\ufffd'] }],
})
let seed = 0x54a1c39
const random = (n) => {
  seed ^= seed << 13
  seed ^= seed >>> 17
  seed ^= seed << 5
  return (seed >>> 0) % n
}
const topics = ['current', 'proposal', 'old', 'new', '\ud800', '\ud801', '😀']
const messages = ['a', 'b', 'c', '\ud800', '\ud801', '😀']
for (let i = 0; i < 900; i++) {
  const input = {
    ...base,
    deliveredIds: Array.from({ length: random(7) }, () => messages[random(messages.length)]),
    groups: Array.from({ length: random(7) }, () => ({
      topicId: topics[random(topics.length)],
      messageIds: Array.from({ length: random(5) }, () => messages[random(messages.length)]),
    })),
    retained: Array.from({ length: random(6) }, () => ({
      id: topics[random(topics.length)],
      messageIds: Array.from({ length: random(5) }, () => messages[random(messages.length)]),
      protected: !!random(2),
    })),
  }
  if (random(2)) input.selectedTopicId = topics[random(topics.length)]
  if (random(2)) input.maxSlots = [-1, 0, 1, 2.5, 4, 16, 100][random(7)]
  add('seeded-' + i, input)
}
const path = 'backend/internal/topicroute/testdata/reassignment-node.json'
const output = JSON.stringify({ source, sourceHash, cases }, null, 2) + '\n'
if (process.argv.includes('--check'))
  assert.equal(readFileSync(path, 'utf8'), output, 'Topic reassignment oracle changed')
else writeFileSync(path, output)
console.log(`${cases.length} actual topic reassignment cases`)
