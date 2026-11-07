/** Synthetic actual-source oracle. Comparator pairs are TEST DATA ONLY. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { assignTopic, parseTopicCue } from '../../src/runtime/topic-admission.ts'
assert.equal(process.version, 'v24.22.1')
assert.equal(process.versions.icu, '78.3')
assert.equal(Intl.DateTimeFormat().resolvedOptions().locale, 'en-US')
const source = 'src/runtime/topic-admission.ts'
const sourceHash = createHash('sha256').update(readFileSync(source)).digest('hex')
const cases = [],
  parses = [],
  pairs = [],
  normalizations = []
const ids = [
  'a',
  'A',
  'ä',
  'a\u0308',
  'z',
  'Z',
  '10',
  '2',
  '_',
  '-',
  '😀',
  '\ue000',
  '\ud800',
  '\ud801',
  '\udfff',
  '\ufffd',
  '中文',
  '中',
  '\u{1e6c0}',
  'ß',
  'ss',
  'a\u0000',
  'a\u200d',
]
for (const a of ids)
  for (const b of ids)
    pairs.push({ a: JSON.stringify(a), b: JSON.stringify(b), result: a.localeCompare(b) })
const input = { sceneId: 'scene', senderId: 'sender', now: 200000 }
const topic = (id, extra = {}) => ({
  id,
  sceneId: 'scene',
  touchedAt: 200000,
  participants: [],
  provisional: false,
  active: false,
  pending: false,
  waiting: false,
  sendState: 'none',
  ...extra,
})
const encode = (x) =>
  JSON.stringify(x, (_, v) =>
    typeof v === 'number' && !Number.isFinite(v) ? { $number: String(v) } : v,
  )
function add(name, topics = [], i = {}, config = {}) {
  const v = { input: { ...input, ...i }, topics, config }
  let result, error
  try {
    result = JSON.stringify(assignTopic(v.input, topics, config))
  } catch (e) {
    error = e.message
  }
  cases.push({ name, raw: encode(v), result, error })
}
add('empty')
add('single', [topic('a')])
add('participant', [topic('a'), topic('z', { participants: ['sender'] })])
add('ambiguous', [topic('a'), topic('z')])
add('ambiguous-participants', [
  topic('a', { participants: ['sender'] }),
  topic('z', { participants: ['sender'] }),
])
for (const kind of ['reply', 'thread', 'task']) {
  add(kind, [topic('a')], { anchors: [{ kind, topicId: 'a' }], cue: '[' })
  add(kind + '-missing', [topic('a')], { anchors: [{ kind, topicId: 'z' }] })
  add(kind + '-duplicate', [topic('a')], {
    anchors: [
      { kind, topicId: 'a' },
      { kind, topicId: 'a' },
    ],
  })
  add(kind + '-conflict', [topic('z'), topic('a')], {
    anchors: [
      { kind, topicId: 'z' },
      { kind, topicId: 'a' },
      { kind, topicId: 'outside' },
    ],
  })
}
add('reply-wins', [topic('a'), topic('z')], {
  anchors: [
    { kind: 'reply', topicId: 'a' },
    { kind: 'thread', topicId: 'z' },
  ],
  unavailableReply: true,
})
add('unavailable-before-thread', [topic('a')], {
  unavailableReply: true,
  anchors: [{ kind: 'thread', topicId: 'a' }],
})
add('unavailable', [], { unavailableReply: true })
for (const touchedAt of [80000, 79999, 200001, -1, NaN, Infinity, -Infinity])
  add('recency-' + touchedAt, [topic('a', { touchedAt })])
add('foreign-scene-duplicates', [
  topic('a', { sceneId: 'other' }),
  topic('a', { sceneId: 'other' }),
])
add('duplicate-snapshot', [topic('a'), topic('a')])
add('too-many', [topic('a'), topic('z')], {}, { maxSlots: 1 })
for (const [key, values] of Object.entries({
  maxSlots: [0, 1, 16, 17, 1.5, NaN, Infinity, 9007199254740992],
  recentMs: [0, 1, -1, 1.5, NaN, Infinity, 9007199254740992],
}))
  for (const value of values) add(key + '-' + value, [], {}, { [key]: value })
for (const now of [-1, 0, 1.5, NaN, Infinity, 9007199254740992]) add('now-' + now, [], { now })
for (const cue of [
  '',
  ' ',
  'é',
  'e\u0301',
  '\ufeffe\u0301\u3000',
  'x'.repeat(32),
  'x'.repeat(33),
  '😀'.repeat(32),
  '😀'.repeat(33),
  'a\nb',
  'a[b',
  '\ud800',
  '\ud801',
  '\u0085',
  '\u180e',
])
  add('cue-' + JSON.stringify(cue), [], { cue })
add(
  'cue-existing-stale',
  [topic('a', { touchedAt: 0, cue: { initiatorId: 'sender', label: 'é' } })],
  { cue: 'e\u0301' },
)
add('cue-other-initiator', [topic('a', { cue: { initiatorId: 'other', label: 'é' } })], {
  cue: 'é',
})
add(
  'cue-ambiguous',
  [
    topic('a', { cue: { initiatorId: 'sender', label: 'é' } }),
    topic('z', { cue: { initiatorId: 'sender', label: 'é' } }),
  ],
  { cue: 'é' },
)
for (const flag of ['active', 'pending', 'waiting'])
  add('protected-' + flag, [topic('a', { [flag]: true })], { cue: 'new' }, { maxSlots: 1 })
for (const sendState of ['none', 'settled', 'partial', 'unknown', 'unexpected'])
  add('send-' + sendState, [topic('a', { sendState })], { cue: 'new' }, { maxSlots: 1 })
add(
  'reuse-first-provisional',
  [topic('z', { provisional: true, active: true }), topic('a', { provisional: true })],
  { unavailableReply: true },
  { maxSlots: 2 },
)
add(
  'cue-does-not-reuse-provisional',
  [topic('a', { provisional: true })],
  { cue: 'new' },
  { maxSlots: 1 },
)
for (const a of ids)
  for (const b of ids)
    if (a !== b) {
      add(
        'collation-' + JSON.stringify([a, b]),
        [topic(a), topic(b)],
        { cue: 'new' },
        { maxSlots: 2 },
      )
      add(
        'utf16-' + JSON.stringify([a, b]),
        [topic(a, { active: true }), topic(b, { active: true })],
        {},
        { maxSlots: 2 },
      )
    }
let seed = 0x35ca19
const rand = (n) => {
  seed ^= seed << 13
  seed ^= seed >>> 17
  seed ^= seed << 5
  return (seed >>> 0) % n
}
for (let n = 0; n < 600; n++) {
  const count = rand(17),
    chosen = ids.slice(0, count)
  const topics = chosen.map((id) =>
    topic(id, {
      touchedAt: [0, 79999, 80000, 200000, 200001, NaN, Infinity, -Infinity][rand(8)],
      participants: rand(2) ? ['sender'] : [],
      provisional: rand(5) === 0,
      active: rand(3) === 0,
      pending: rand(5) === 0,
      waiting: rand(7) === 0,
      sendState: ['none', 'settled', 'partial', 'unknown'][rand(4)],
    }),
  )
  add('seeded-' + n, topics, rand(2) ? { cue: 'new' } : {}, { maxSlots: Math.max(1, count) })
}
for (const cue of [
  'x' + '\u0301'.repeat(31),
  'a' + '\u0315'.repeat(30) + '\u0300',
  'x' + '\u0315'.repeat(28) + '\u0344',
  '\u1100\u1161\u11a8',
  'a\u034f\u0301',
  '\ud800e\u0301\ud801',
])
  add('normalization-' + JSON.stringify(cue), [], { cue })
const normalizationAlphabet = [
  'a',
  'e',
  'x',
  'é',
  '\u0301',
  '\u0300',
  '\u0315',
  '\u0327',
  '\u0344',
  '\u034f',
  '\u1100',
  '\u1161',
  '\u11a8',
  '\ud800',
  '\ud801',
  '\udfff',
  '😀',
  '\u{1e6e3}',
]
for (let n = 0; n < 400; n++) {
  const cue = Array.from(
    { length: rand(45) },
    () => normalizationAlphabet[rand(normalizationAlphabet.length)],
  ).join('')
  add('normalization-seeded-' + n, [], { cue })
}
const whitespace = [
  9,
  10,
  11,
  12,
  13,
  32,
  0xa0,
  0x1680,
  ...Array.from({ length: 11 }, (_, i) => 0x2000 + i),
  0x2028,
  0x2029,
  0x202f,
  0x205f,
  0x3000,
  0xfeff,
  0x85,
  0x180e,
  0x200b,
]
const texts = [
  '[topic:hello] trailing',
  '[话题:话题]',
  ' [topic:x]',
  '[Topic:x]',
  '[topic:]',
  '[topic: ]',
  '[topic:[' + 'x]',
  '[topic:a\nb]',
  '[topic:\r\nx]',
  '[topic:e\u0301]',
  '[topic:\ud800]',
  '[topic:\ud801]',
  '[topic:' + '😀'.repeat(32) + ']',
  '[topic:' + '😀'.repeat(33) + ']',
  '[topic:' + 'a'.repeat(33) + ']',
  '[topic:' + ' '.repeat(100) + 'x]',
  '[topic:' + ' '.repeat(100) + ']',
  ...whitespace.map((c) => '[topic:' + String.fromCharCode(c) + 'x' + String.fromCharCode(c) + ']'),
]
for (const text of texts)
  parses.push({ raw: JSON.stringify(text), result: JSON.stringify(parseTopicCue(text)) ?? null })
// Built-in normalization is the exact operation called by the pinned policy.
// Cover every scalar with a canonical decomposition, including all Hangul,
// plus composition/reordering probes and unbounded-length combining runs.
const normalizeCase = (text) =>
  normalizations.push({ raw: JSON.stringify(text), result: JSON.stringify(text.normalize('NFC')) })
for (let cp = 0; cp <= 0x10ffff; cp++) {
  if (cp >= 0xd800 && cp <= 0xdfff) continue
  const ch = String.fromCodePoint(cp),
    decomposed = ch.normalize('NFD')
  if (decomposed !== ch) {
    normalizeCase(ch)
    normalizeCase(decomposed)
    normalizeCase('x' + decomposed + '\u0327\u0301')
  }
}
const marks = [
  '\u0300',
  '\u0301',
  '\u0315',
  '\u0316',
  '\u0327',
  '\u0344',
  '\u034f',
  '\u035c',
  '\u05ae',
  '\u05b0',
  '\u1dce',
  '\u{1e6e3}',
]
for (let n = 0; n < 256; n++) {
  const run = Array.from({ length: 32 + rand(1024) }, () => marks[rand(marks.length)]).join('')
  normalizeCase((n % 2 ? 'a' : '\u1100\u1161\u11a8') + run + '\ud800' + run + '\ud801\u0301')
}
normalizeCase('a' + '\u0315'.repeat(8192) + '\u0300'.repeat(8192))
const output =
  JSON.stringify(
    {
      source,
      sourceHash,
      node: process.version,
      icu: process.versions.icu,
      locale: Intl.DateTimeFormat().resolvedOptions().locale,
      cases,
      parses,
      pairs,
      normalizations,
    },
    null,
    2,
  ) + '\n'
const path = 'backend/internal/topicroute/testdata/admission-node.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), output)
else writeFileSync(path, output)
console.log(
  `${cases.length} admission cases; ${parses.length} cue parses; ${pairs.length} exact test-only comparator pairs; ${normalizations.length} NFC probes`,
)
