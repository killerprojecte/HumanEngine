/** Synthetic, fake-clock traces from the actual frozen TypeScript coordinator.
 * Run: node --import tsx backend/tests/generate-turnwait-fixtures.mjs [--check]
 * No timers, networking, provider calls or runtime implementation copies.
 */
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { TurnWaitCoordinator } from '../../src/runtime/turn-wait.ts'

const source = new URL('../../src/runtime/turn-wait.ts', import.meta.url)
const sourceSHA256 = createHash('sha256').update(readFileSync(source)).digest('hex')
if (process.version !== 'v24.22.1') throw Error('Use the pinned Node 24.22.1 oracle')
if (sourceSHA256 !== 'bf01e8b12103b76a0954546f7882137a531d9efff4961bf38b0cb627df3e1217')
  throw Error('Turn-wait oracle changed; review the baseline before replacing fixtures')
if (process.argv.slice(2).some((argument) => argument !== '--check'))
  throw Error('Only --check is supported')

const base = {
  quietMs: 1200,
  maxWaitMs: 6000,
  addressedQuietMs: 300,
  maxModelWaitMs: 10000,
  maxPending: 8,
}
const traces = []
const at = (time, op, input = {}) => ({ at: time, op, ...input })
const observe = (time, id, meaningful = true, addressed) =>
  at(time, 'observe', {
    event: { id, meaningful, ...(addressed === undefined ? {} : { addressed }) },
  })
const take = (time, save = 'wake') => at(time, 'takeReady', { save })
const finish = (time, handle = 'wake', waitUntil) =>
  at(time, 'finish', { handle, ...(waitUntil === undefined ? {} : { waitUntil }) })
const add = (name, steps, config = base) => traces.push({ name, config, steps })

add('quiet_merge_and_max_wait_skip', [
  ...Array.from({ length: 7 }, (_, i) => observe(i * 1000, `chat-${i}`, false)),
  take(6000, 'skip'),
  take(12000),
  observe(12000, 'question'),
  observe(12500, 'clarification'),
  take(13699),
  take(13700),
  take(13700),
  finish(13700),
])
add('meaningful_max_wait_wins_quiet_boundary', [
  ...Array.from({ length: 7 }, (_, i) => observe(i * 1000, `chat-${i}`)),
  take(6000),
  finish(6000),
])
add('addressed_alone_is_meaningful_and_uses_shorter_quiet', [
  observe(0, 'mention', false, true),
  take(299),
  take(300),
  finish(300),
])
add(
  'addressed_window_never_lengthens_quiet',
  [observe(0, 'mention', true, true), take(9), take(10)],
  {
    ...base,
    quietMs: 10,
    addressedQuietMs: 100,
  },
)
add('wait_once_clamp_and_past_wait_terminal', [
  observe(0, 'question'),
  take(1200),
  finish(1200, 'wake', 90000),
  take(11199),
  take(11200, 'recheck'),
  finish(11200, 'wake', 20000),
  finish(11200, 'recheck', 20000),
  take(90000),
  observe(90000, 'new'),
  take(91200),
  finish(91200, 'wake', 91200),
  take(95000),
])
add('fresh_batch_waits_for_its_own_quiet_deadline', [
  observe(0, 'first'),
  take(1200),
  finish(1200, 'wake', 2000),
  observe(1900, 'noise', false),
  take(2000, 'recheck'),
  take(3099),
  take(3100, 'skip'),
])
add('ready_fresh_batch_wins_wait_and_clears_it', [
  observe(0, 'first'),
  take(1200),
  finish(1200, 'wake', 4000),
  observe(2000, 'new'),
  take(3200),
  finish(3200),
  take(4000),
])
add('equal_pending_and_wait_deadline_consumes_only_fresh_batch', [
  observe(0, 'first'),
  take(1200),
  finish(1200, 'wake', 3200),
  observe(2000, 'new'),
  take(3200),
  finish(3200),
  take(3200),
])
add('new_input_invalidates_wait_without_releasing_active', [
  observe(0, 'first'),
  take(1200, 'old'),
  observe(1200, 'correction'),
  observe(1300, 'detail', false),
  take(2500),
  finish(2500, 'old', 10000),
  take(2500),
  finish(2500),
  observe(2500, 'first'),
])
add('recall_pending_first_last_and_addressed_recomputes_deadline', [
  observe(0, 'first'),
  observe(100, 'mention', true, true),
  observe(200, 'last'),
  at(200, 'recall', { id: 'mention' }),
  at(200, 'recall', { id: 'first' }),
  observe(300, 'tail'),
  at(300, 'recall', { id: 'tail' }),
  take(1400),
  at(1400, 'recall', { id: 'last' }),
  finish(1400, 'wake', 3000),
  take(3000),
])
add('recall_wait_source_only_and_dedup_survives', [
  observe(0, 'source'),
  take(1200),
  finish(1200, 'wake', 3000),
  at(1200, 'recall', { id: 'unrelated' }),
  at(1200, 'recall', { id: 'source' }),
  take(3000),
  observe(3000, 'source'),
])
add('edit_keeps_active_identity_until_finish_and_reobserves_same_id', [
  observe(0, 'source', true, true),
  take(300, 'old'),
  at(300, 'replace', { id: 'source' }),
  observe(300, 'source', true, true),
  take(600),
  finish(600, 'old', 10000),
  take(600),
  finish(600),
  observe(600, 'source'),
])
add('edit_pending_reorders_and_edit_wait_cancels_only_matching_wait', [
  observe(0, 'first'),
  observe(100, 'second'),
  at(100, 'replace', { id: 'first' }),
  observe(200, 'first'),
  take(1400),
  finish(1400, 'wake', 3000),
  at(1400, 'replace', { id: 'unrelated' }),
  at(1400, 'replace', { id: 'second' }),
  take(3000),
])
add('cancel_clears_work_not_seen_and_restart_starts_empty', [
  observe(0, 'active'),
  take(1200),
  observe(1200, 'pending'),
  at(1200, 'cancel'),
  finish(1200, 'wake', 3000),
  observe(1200, 'pending'),
  take(10000),
  at(10000, 'restart'),
  take(10000),
  finish(10000, 'wake', 11000),
  observe(10000, 'pending'),
  take(11200),
])
add(
  'full_does_not_mark_seen_duplicate_checked_before_full',
  [
    observe(0, 'a'),
    observe(0, 'b'),
    observe(0, 'c'),
    observe(0, 'a'),
    at(0, 'recall', { id: 'a' }),
    observe(0, 'c'),
    take(1200),
    finish(1200),
  ],
  { ...base, maxPending: 2 },
)
add(
  'seen_fifo_eviction_and_duplicate_does_not_refresh_order',
  [
    observe(0, 'a'),
    take(0),
    finish(0),
    observe(0, 'b'),
    take(0),
    finish(0),
    observe(0, 'c'),
    take(0),
    finish(0),
    observe(0, 'd'),
    take(0),
    finish(0),
    observe(0, 'a'),
    observe(0, 'e'),
    take(0),
    finish(0),
    observe(0, 'a'),
    observe(0, 'b'),
  ],
  { ...base, quietMs: 0, maxPending: 1 },
)
add(
  'config_shrink_does_not_trim_and_seen_evicts_exactly_one',
  [
    ...Array.from({ length: 10 }, (_, i) => observe(0, `id-${i}`)),
    at(0, 'configure', { config: { ...base, quietMs: 0, maxPending: 1 } }),
    observe(0, 'blocked'),
    take(0),
    finish(0),
    observe(0, 'new'),
    take(0),
    finish(0),
    observe(0, 'id-1'),
    observe(0, 'id-0'),
  ],
  { ...base, quietMs: 0, maxPending: 10 },
)
add('config_copy_recompute_and_invalid_update_is_atomic', [
  observe(0, 'first'),
  observe(200, 'second', false),
  at(200, 'configure', {
    config: { ...base, quietMs: 500, maxWaitMs: 2000, addressedQuietMs: 100 },
  }),
  at(200, 'mutateConfig', { field: 'quietMs', value: 900 }),
  take(700),
  at(700, 'configure', { config: { ...base, quietMs: 300, maxWaitMs: 600 } }),
  observe(700, 'first'),
  observe(700, 'fresh'),
  finish(700),
  at(700, 'configure', { config: { ...base, quietMs: 4000, maxWaitMs: 2000 } }),
  take(1000),
])
add('wake_reference_identity_and_returned_arrays_are_independent', [
  observe(0, 'source'),
  take(1200),
  at(1200, 'cloneWake', { handle: 'wake', save: 'clone' }),
  finish(1200, 'clone', 3000),
  at(1200, 'mutateRetained'),
  finish(1200, 'wake', 3000),
  at(1200, 'mutateWakeIds', { handle: 'wake', ids: ['different'] }),
  at(1200, 'recall', { id: 'source' }),
  take(3000),
])
add('wake_generation_mutation_is_not_a_new_authorized_generation', [
  observe(0, 'source'),
  take(1200),
  at(1200, 'mutateWakeGeneration', { handle: 'wake', value: 2 }),
  finish(1200, 'wake', 3000),
  take(3000),
])
add('empty_ids_rejected_and_unrelated_replace_still_invalidates', [
  observe(0, ''),
  at(0, 'replace', { id: '' }),
  observe(0, 'valid'),
  take(1200),
  at(1200, 'recall', { id: '' }),
  at(1200, 'replace', { id: 'absent' }),
  finish(1200, 'wake', 3000),
])
add('unicode_and_opaque_id_identity_is_lossless', [
  ...['😀', 'e\u0301', 'é', '\ud800', '\ud801', '\ufffd', '\u0000', '__proto__'].map((id) =>
    observe(0, id),
  ),
  observe(0, '\ud800'),
  take(1200),
  finish(1200),
  at(1200, 'replace', { id: '\ud800' }),
  observe(1200, '\ud800'),
  observe(1200, '\ud801'),
])
for (const [name, value] of [
  ['zero', 1200],
  ['past', 1199],
  ['nan', NaN],
  ['infinite', Infinity],
  ['negative_infinite', -Infinity],
  ['fractional', 1200.5],
])
  add(`wait_result_${name}`, [
    observe(0, 'source'),
    take(1200),
    finish(1200, 'wake', value),
    take(1200.5),
    take(5000),
  ])
add(
  'zero_model_wait_disables_wait',
  [observe(0, 'source'), take(1200), finish(1200, 'wake', 2000), take(2000)],
  { ...base, maxModelWaitMs: 0 },
)
add(
  'zero_windows_and_safe_integer_config_have_no_extra_caps',
  [observe(0, 'source'), take(0), finish(0, 'wake', 1), take(1)],
  {
    quietMs: 0,
    maxWaitMs: 0,
    addressedQuietMs: 9007199254740991,
    maxModelWaitMs: 9007199254740991,
    maxPending: 9007199254740991,
  },
)
add('clock_rollback_keeps_insertion_order_not_timestamp_order', [
  observe(1000, 'first'),
  observe(0, 'second'),
  take(1199),
  take(1200),
  finish(1200),
])

// Fixed-seed stateful sequences supplement the named regression boundaries.
for (let seed = 1; seed <= 12; seed++) {
  let state = seed,
    now = 12
  const random = () => {
    state ^= state << 13
    state ^= state >>> 17
    state ^= state << 5
    return state >>> 0
  }
  const config = { ...base, quietMs: 12, maxWaitMs: 60, addressedQuietMs: 3, maxModelWaitMs: 100 }
  const steps = [observe(0, 'initial'), take(12)]
  for (let i = 0; i < 80; i++) {
    now += random() % 13
    const choice = random() % 12,
      id = `id-${random() % 14}`
    if (choice < 4) steps.push(observe(now, id, (random() & 1) === 1, random() % 3 === 0))
    else if (choice < 6) steps.push(take(now))
    else if (choice === 6) steps.push(finish(now, 'wake', now + (random() % 160) - 20))
    else if (choice === 7) steps.push(at(now, 'recall', { id }))
    else if (choice === 8) steps.push(at(now, 'replace', { id }))
    else if (choice === 9) steps.push(at(now, 'cancel'))
    else if (choice === 10)
      steps.push(
        at(now, 'configure', {
          config: { ...config, maxPending: 1 + (random() % 8), quietMs: random() % 13 },
        }),
      )
    else steps.push(at(now, 'snapshot'))
  }
  steps.push(at(now, 'cancel'), take(now + 10000))
  add(`seeded_${seed}`, steps, config)
}

// UTF-16 units preserve even lone surrogates without relying on Go JSON's lossy
// string decoding. IDs are opaque in this package; production JSON migration is
// a separate boundary and is not claimed by these fixtures.
const units = (value) => Array.from({ length: value.length }, (_, i) => value.charCodeAt(i))
const ids = (values) => values.map(units)
const wireWake = (wake) => ({
  kind: wake.kind,
  generation: wake.generation,
  eventIdsUTF16: ids(wake.eventIds),
  reason: wake.reason,
})
const optional = (value, encode = (value) => value) =>
  value === undefined ? { present: false } : { present: true, value: encode(value) }
function wireStep(step) {
  const result = { ...step }
  if ('id' in result) {
    result.idUTF16 = units(result.id)
    delete result.id
  }
  if (result.ids) {
    result.idsUTF16 = ids(result.ids)
    delete result.ids
  }
  if (result.event) {
    result.event = { ...result.event, idUTF16: units(result.event.id) }
    delete result.event.id
  }
  return result
}
function execute(trace) {
  let now = 0,
    suppliedConfig = { ...trace.config }
  let queue = new TurnWaitCoordinator(suppliedConfig, () => now)
  const handles = new Map()
  const steps = trace.steps.map((step) => {
    now = step.at
    let result, error
    try {
      switch (step.op) {
        case 'observe':
          result = queue.observe(step.event)
          break
        case 'recall':
          queue.recall(step.id)
          break
        case 'replace':
          queue.replace(step.id)
          break
        case 'takeReady':
          result = queue.takeReady()
          if (result && step.save) handles.set(step.save, result)
          break
        case 'finish':
          if (!handles.has(step.handle)) throw Error('fixture_missing_wake_handle')
          result = queue.finish(
            handles.get(step.handle),
            'waitUntil' in step ? { waitUntil: step.waitUntil } : {},
          )
          break
        case 'cancel':
          queue.cancel()
          break
        case 'configure':
          suppliedConfig = { ...step.config }
          queue.configure(suppliedConfig)
          break
        case 'mutateConfig':
          suppliedConfig[step.field] = step.value
          break
        case 'restart':
          suppliedConfig = { ...trace.config }
          queue = new TurnWaitCoordinator(suppliedConfig, () => now)
          break
        case 'cloneWake':
          handles.set(step.save, structuredClone(handles.get(step.handle)))
          break
        case 'mutateWakeIds':
          handles.get(step.handle).eventIds = [...step.ids]
          break
        case 'mutateWakeGeneration':
          handles.get(step.handle).generation = step.value
          break
        case 'mutateRetained':
          queue.retainedEventIds().push('outside-mutation')
          break
        case 'snapshot':
          break
        default:
          throw Error('fixture_unknown_operation')
      }
    } catch (caught) {
      if (caught.message.startsWith('fixture_')) throw caught
      error = caught.message
    }
    return {
      input: wireStep(step),
      expected: {
        ...(error ? { error } : {}),
        result: optional(result, step.op === 'takeReady' ? wireWake : undefined),
        nextWakeAt: optional(queue.nextWakeAt()),
        pendingAddressed: optional(queue.pendingAddressed()),
        retention: queue.retention(),
        retainedEventIdsUTF16: ids(queue.retainedEventIds()),
        current: Object.fromEntries(
          [...handles].map(([key, wake]) => [key, queue.isCurrent(wake)]),
        ),
      },
    }
  })
  return { name: trace.name, config: trace.config, steps }
}

const invalidConfigs = []
for (const field of Object.keys(base))
  for (const value of [-1, 0.5, NaN, Infinity, -Infinity, 9007199254740992])
    invalidConfigs.push({ ...base, [field]: value })
invalidConfigs.push({ ...base, maxPending: 0 }, { ...base, maxWaitMs: base.quietMs - 1 })
const invalidConfigurationCases = invalidConfigs.map((config) => {
  try {
    new TurnWaitCoordinator(config, () => 0)
    throw Error('fixture_expected_invalid_config')
  } catch (error) {
    if (error.message.startsWith('fixture_')) throw error
    return { config, error: error.message }
  }
})
const fixture = {
  version: 1,
  baseline: '24fe8a773d87f2f09996e259fca46524c2c02928',
  source: 'src/runtime/turn-wait.ts',
  sourceSHA256,
  node: process.version,
  invalidConfigurations: invalidConfigurationCases,
  traces: traces.map(execute),
}
const content =
  JSON.stringify(
    fixture,
    (_key, value) =>
      typeof value === 'number' && !Number.isFinite(value) ? { number: String(value) } : value,
    2,
  ) + '\n'
const destination = new URL('../internal/turnwait/testdata/traces-node.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(destination, 'utf8') !== content) throw Error('Turn-wait fixtures are stale')
} else {
  mkdirSync(new URL('../internal/turnwait/testdata/', import.meta.url), { recursive: true })
  writeFileSync(destination, content)
}
console.log(
  `${fixture.traces.length} traces, ${fixture.traces.reduce((count, trace) => count + trace.steps.length, 0)} transitions, ${invalidConfigurationCases.length} invalid configurations`,
)
