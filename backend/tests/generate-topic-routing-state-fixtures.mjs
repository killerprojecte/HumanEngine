/** Actual source routing metadata transitions, synthetic IDs only. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { TopicRoutingState } from '../../src/runtime/topic-routing-state.ts'
assert.equal(process.version, 'v24.22.1')
const source = 'src/runtime/topic-routing-state.ts'
const sourceHash = createHash('sha256').update(readFileSync(source)).digest('hex')
const state = new TopicRoutingState(),
  steps = []
const snapshot = () => ({
  overflowed: state.overflowed,
  metadata: [...state.metadata],
  deferred: [...state.deferred],
  acknowledged: [...state.acknowledged],
  protectedSends: [...state.protectedSends],
  turns: [...state.turns],
})
const step = (op) => {
  let result = null
  switch (op.kind) {
    case 'defer':
      result = state.defer(op.id, op.schedule)
      break
    case 'ack':
      state.acknowledge(op.id, op.topicId)
      break
    case 'forget':
      state.forgetMessage(op.id)
      break
    case 'evict':
      state.evict(op.id)
      break
    case 'cancel':
      state.cancel()
      break
    case 'metadata':
      state.metadata.set(op.id, op.value)
      break
    case 'protect':
      state.protectedSends.add(op.id)
      break
    case 'turn':
      state.turns.set(op.id, op.topicId)
      break
    case 'deleteDeferred':
      state.deferred.delete(op.id)
      break
    case 'overflow':
      state.overflowed = op.value
      break
    default:
      throw Error('unknown fixture operation')
  }
  const digest = createHash('sha256').update(JSON.stringify(snapshot())).digest('hex')
  steps.push({ raw: JSON.stringify(op), result, digest })
}
step({
  kind: 'metadata',
  id: 'topic',
  value: { provisional: false, cueSourceId: 'a', cue: { initiatorId: 'sender', label: 'Cue' } },
})
step({ kind: 'protect', id: 'topic' })
step({ kind: 'turn', id: 'topic', topicId: 'turn' })
for (let i = 0; i < 61; i++) step({ kind: 'defer', id: 'd' + i, schedule: !!(i % 2) })
step({ kind: 'defer', id: 'd0', schedule: true })
step({ kind: 'deleteDeferred', id: 'd0' })
step({ kind: 'defer', id: 'd0', schedule: true })
step({ kind: 'overflow', value: Number.MAX_SAFE_INTEGER })
step({ kind: 'defer', id: 'overflow', schedule: false })
for (let i = 0; i < 503; i++) step({ kind: 'ack', id: 'm' + i, topicId: i % 2 ? 'topic' : 'other' })
step({ kind: 'ack', id: 'm4', topicId: 'replacement' })
step({ kind: 'ack', id: 'after-update', topicId: 'new' })
step({ kind: 'forget', id: 'a' })
step({ kind: 'evict', id: 'topic' })
step({ kind: 'cancel' })
const ids = ['a', 'b', '\ud800', '\ud801', '\ufffd', '😀', 'topic', 'other']
let seed = 0x653ab17
const random = (n) => {
  seed ^= seed << 13
  seed ^= seed >>> 17
  seed ^= seed << 5
  return (seed >>> 0) % n
}
for (let i = 0; i < 500; i++) {
  const id = ids[random(ids.length)],
    topicId = ids[random(ids.length)]
  const kind = [
    'defer',
    'ack',
    'forget',
    'evict',
    'cancel',
    'metadata',
    'protect',
    'turn',
    'deleteDeferred',
  ][random(9)]
  const op = { kind, id, topicId, schedule: !!random(2) }
  if (kind === 'metadata')
    op.value = {
      provisional: !!random(2),
      cueSourceId: ids[random(ids.length)],
      cue: { initiatorId: ids[random(ids.length)], label: 'Cue' + i },
    }
  step(op)
}
const output = JSON.stringify({ source, sourceHash, steps }, null, 2) + '\n'
const path = 'backend/internal/topicroute/testdata/routing-state-node.json'
if (process.argv.includes('--check'))
  assert.equal(readFileSync(path, 'utf8'), output, 'Routing state oracle changed')
else writeFileSync(path, output)
console.log(`${steps.length} actual routing metadata transitions`)
