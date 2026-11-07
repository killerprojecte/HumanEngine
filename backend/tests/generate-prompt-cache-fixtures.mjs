/** Actual runtime membership/prefix source, synthetic bodies only. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { PromptContextWindows } from '../../src/runtime/prompt-context-window.ts'
import { PromptCacheBoundaries } from '../../src/runtime/prompt-cache-boundaries.ts'
assert.equal(process.version, 'v24.22.1')
const hash = (value) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const windows = new PromptContextWindows(),
  boundaries = new PromptCacheBoundaries(),
  tickets = new Map(),
  steps = []
const step = (op) => {
  let result = null,
    error = null
  try {
    if (op.kind === 'select')
      result = windows
        .select(op.key, op.current, op.options, op.authority, op.required)
        .map((v) => v.id)
    if (op.kind === 'prepare') {
      const t = boundaries.prepare(
        op.key,
        op.current.map((v) => ({ id: v.id, value: v })),
        op.authority,
      )
      tickets.set(op.ticket, t)
      result = t.contextCacheBoundaries
    }
    if (op.kind === 'commit') tickets.get(op.ticket)?.commit()
    if (op.kind === 'delete') {
      windows.delete(op.key)
      boundaries.delete(op.key)
    }
    if (op.kind === 'clear') {
      windows.clear()
      boundaries.clear()
    }
  } catch (e) {
    error = e.message
  }
  const snapshot = {
    windows: [...windows.windows],
    boundaries: [...boundaries.states].map(([key, state]) => [key, state.successful ?? null]),
  }
  steps.push({ raw: JSON.stringify(op), result, error, digest: hash(snapshot) })
}
const options = (extra = {}) => ({
  enabled: true,
  maxMessages: 5,
  retainMessages: 2,
  maxBytes: 1000,
  ...extra,
})
const messages = (n) =>
  Array.from({ length: n }, (_, i) => ({
    id: 'm' + i,
    text: i === 1 ? '你好🙂\ud800' : 'text ' + i,
  }))
for (let n = 0; n < 12; n++)
  step({
    kind: 'select',
    key: 'turn',
    current: messages(n),
    options: options(),
    authority: ['a'],
    required: [],
  })
for (const bytes of [1, 2, 20, 40, 80, 140, 1000])
  for (const required of [[], ['m4'], ['m0'], ['m4', 'm4']])
    step({
      kind: 'select',
      key: 'turn',
      current: messages(5),
      options: options({ maxBytes: bytes }),
      authority: ['a'],
      required,
    })
step({
  kind: 'select',
  key: 'disabled',
  current: messages(30),
  options: options({ enabled: false }),
  authority: null,
  required: ['absent'],
})
step({
  kind: 'select',
  key: 'empty',
  current: [],
  options: options(),
  authority: null,
  required: ['absent'],
})
step({
  kind: 'select',
  key: 'turn',
  current: [{ id: 'a' }, { id: 'a' }],
  options: options(),
  authority: null,
  required: [],
})
for (let n = 0; n < 9; n++) {
  step({
    kind: 'prepare',
    key: 'p',
    current: messages(n),
    authority: { revision: 1 },
    ticket: 't' + n,
  })
  if (n % 2 === 0) step({ kind: 'commit', ticket: 't' + n })
  if (n > 1) step({ kind: 'commit', ticket: 't' + (n - 1) })
}
step({ kind: 'prepare', key: 'p', current: messages(9), authority: { revision: 2 }, ticket: 'new' })
step({ kind: 'delete', key: 'p' })
step({ kind: 'commit', ticket: 'new' })
step({
  kind: 'prepare',
  key: 'dup',
  current: [{ id: 'x' }, { id: 'x' }],
  authority: null,
  ticket: 'dup',
})
for (let i = 0; i < 270; i++) {
  step({
    kind: 'select',
    key: 'k' + i,
    current: messages(3),
    options: options(),
    authority: i,
    required: [],
  })
  step({ kind: 'prepare', key: 'k' + i, current: messages(3), authority: i, ticket: 'k' + i })
  if (i % 3 === 0) step({ kind: 'commit', ticket: 'k' + i })
}
step({ kind: 'commit', ticket: 'k0' })
let seed = 128174
const rand = (n) => {
  seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
  // Using the low bit for branching locks this LCG's call pattern into only
  // prepares. Scale the full word so both window and prefix paths are exercised.
  return Math.floor((seed / 0x100000000) * n)
}
const randomizedKinds = { select: 0, prepare: 0, commit: 0 }
for (let i = 0; i < 700; i++) {
  const current = messages(rand(12)),
    key = 'r' + rand(20)
  if (current.length && rand(3) === 0) current[rand(current.length)].text = 'changed' + rand(5)
  if (rand(2)) {
    randomizedKinds.select++
    step({
      kind: 'select',
      key,
      current,
      options: options({
        maxMessages: 1 + rand(8),
        maxBytes: [20, 60, 100, 300, 1000][rand(5)],
        retainMessages: 1 + rand(5),
      }),
      authority: rand(3),
      required: rand(4) === 0 ? ['m' + rand(12)] : [],
    })
  } else {
    randomizedKinds.prepare++
    step({ kind: 'prepare', key, current, authority: rand(3), ticket: 'random' + i })
    if (rand(2)) {
      randomizedKinds.commit++
      step({ kind: 'commit', ticket: 'random' + i })
    }
  }
}
assert.ok(randomizedKinds.select >= 200, JSON.stringify(randomizedKinds))
assert.ok(randomizedKinds.prepare >= 200, JSON.stringify(randomizedKinds))
assert.ok(randomizedKinds.commit >= 100, JSON.stringify(randomizedKinds))

// Compare byte thresholds around every suffix, not just round byte budgets.
// Distinct lone-surrogate IDs must survive both the oracle and Go fixture reader.
const unicode = ['\ud800', '\ud801', '\ufffd', '🙂', 'last'].map((id, i) => ({
  id,
  text: ['\u0000\n"\\', '<>&\u2028\u2029', '汉字', '\udfff', 'plain'][i],
  data: { 10: 10, 2: 2, z: 1e-7, '01': 1e21 },
}))
for (let n = 1; n <= unicode.length; n++) {
  const maxBytes = Buffer.byteLength(JSON.stringify(unicode.slice(-n)))
  for (const delta of [-1, 0, 1])
    for (const required of [[], [unicode.at(-n).id]])
      step({
        kind: 'select',
        key: 'exact-bytes',
        current: unicode,
        options: options({ retainMessages: 5, maxBytes: maxBytes + delta }),
        authority: { 2: 2, 1: 1, z: '\ud800', a: '🙂' },
        required,
      })
}

// Non-index property insertion order belongs in both body and authority hashes.
for (const [index, current] of [
  [
    { id: 'a', text: 'one' },
    { id: 'b', text: 'two' },
  ],
  [
    { id: 'a', text: 'one' },
    { id: 'b', text: 'two' },
    { id: 'c', text: 'three' },
  ],
  [
    { text: 'one', id: 'a' },
    { id: 'b', text: 'two' },
    { id: 'c', text: 'three' },
  ],
].entries()) {
  step({
    kind: 'select',
    key: 'order',
    current,
    options: options(),
    authority: { z: 1, a: 2 },
    required: [],
  })
  step({
    kind: 'prepare',
    key: 'order',
    current,
    authority: { z: 1, a: 2 },
    ticket: 'order' + index,
  })
  step({ kind: 'commit', ticket: 'order' + index })
}
step({
  kind: 'prepare',
  key: 'order',
  current: messages(3),
  authority: { a: 2, z: 1 },
  ticket: 'authority-order',
})
step({ kind: 'clear' })
step({ kind: 'commit', ticket: 'k269' })

// Cycles cannot be carried in JSON fixtures. Verify source mutation ordering
// directly; ownership_test.go checks the same boundary in the Go data model.
const cycle = { id: 'a' }
cycle.self = cycle
for (const [current, authority, required, expected, cleared] of [
  [[{ id: 'a' }, { id: 'a' }], cycle, [], TypeError, false],
  [[cycle, { id: 'a' }], null, [], TypeError, false],
  [[{ id: 'a' }, { id: 'a' }], null, [], /prompt_context_duplicate_message/, false],
  [[{ id: 'a' }], cycle, ['absent'], /prompt_context_target_budget_exhausted/, true],
  [[], cycle, ['absent'], /prompt_context_target_budget_exhausted/, true],
]) {
  const cache = new PromptContextWindows()
  cache.select('key', messages(2), options(), null)
  const before = [...cache.windows]
  assert.throws(() => cache.select('key', current, options(), authority, required), expected)
  assert.deepEqual([...cache.windows], cleared ? [] : before)
}
const failures = new PromptCacheBoundaries()
const pending = failures.prepare('key', [{ id: 'a', value: { id: 'a' } }], null)
const beforeFailure = [...failures.states]
assert.throws(() => failures.prepare('key', [{ id: 'a', value: cycle }], null), TypeError)
assert.deepEqual([...failures.states], beforeFailure)
pending.commit()
assert.deepEqual(
  failures.prepare(
    'key',
    [
      { id: 'a', value: { id: 'a' } },
      { id: 'b', value: null },
    ],
    null,
  ).contextCacheBoundaries,
  [0, 1],
)
const sourceHashes = Object.fromEntries(
  ['src/runtime/prompt-context-window.ts', 'src/runtime/prompt-cache-boundaries.ts'].map((p) => [
    p,
    createHash('sha256').update(readFileSync(p)).digest('hex'),
  ]),
)
const output = JSON.stringify({ node: process.version, sourceHashes, steps }, null, 2) + '\n',
  path = 'backend/internal/promptcache/testdata/source-node.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), output)
else writeFileSync(path, output)
console.log(`${steps.length} actual prompt window/prefix transitions`)
