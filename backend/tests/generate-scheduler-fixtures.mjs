/** Synthetic fake-clock traces from the actual frozen TypeScript scheduler.
 * node --import tsx backend/tests/generate-scheduler-fixtures.mjs [--check]
 * No provider, transport, database or network calls.
 */
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { AsyncLocalStorage } from 'node:async_hooks'
import {
  WorkScheduler,
  effectiveModelPriority,
  withModelPriority,
  withOverdueLearning,
  getOverdueLearningAt,
} from '../../src/scheduler.ts'

const sourceSHA256 = createHash('sha256')
  .update(readFileSync(new URL('../../src/scheduler.ts', import.meta.url)))
  .digest('hex')
if (process.version !== 'v24.22.1') throw Error('Use the pinned Node 24.22.1 oracle')
if (sourceSHA256 !== '3340a98e5545c146a2332dc335931efeb8c577806d08cb2d05f1c92d30b52d36')
  throw Error('Scheduler oracle changed; review the baseline before replacing fixtures')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
const traces = []
const run = (id, priority = 0, extra = {}) => ({ op: 'run', id, priority, ...extra })
const done = (id, failure) => ({ op: 'complete', id, ...(failure ? { failure } : {}) })
const abort = (id) => ({ op: 'abort', id })
const advance = (at) => ({ op: 'advance', at })
const configure = (concurrency, callsPerMinute = 0) => ({
  op: 'configure',
  concurrency,
  callsPerMinute,
})
const base = { concurrency: 1, maxQueued: 256, reserveForeground: 0 }
const add = (name, steps, config = {}) =>
  traces.push({ name, config: { ...base, ...config }, steps })
const fg = (count, offset = 0, extra = {}) =>
  Array.from({ length: count }, (_, i) => run(`f${i + offset}`, 0, { auto: true, ...extra }))
add(
  'reservation_and_foreground_capacity',
  [
    run('a', 2),
    run('b', 2),
    run('c', 2),
    run('d', 2),
    run('foreground'),
    done('a'),
    done('b'),
    done('c'),
    done('d'),
    done('foreground'),
  ],
  { concurrency: 4, reserveForeground: 1 },
)
add(
  'default_capacity_and_fifo',
  [
    run('a', 2),
    run('b', 2),
    run('c', 1, { auto: true }),
    run('d', 1, { auto: true }),
    done('a'),
    done('b'),
  ],
  { concurrency: 2 },
)
add(
  'aged_background_ties_realtime_behind_foreground',
  [
    run('held', 1),
    run('aged', 2, { auto: true }),
    advance(99),
    run('recent', 1, { auto: true }),
    advance(100),
    run('foreground'),
    done('held'),
    done('foreground'),
  ],
  { backgroundAgingMs: 100, reserveForeground: 1 },
)
add(
  'aging_only_changes_exact_priority_two',
  [
    run('held'),
    run('three', 3, { auto: true }),
    run('two', 2, { auto: true }),
    advance(300000),
    run('realtime', 1, { auto: true }),
    run('negative', -1, { auto: true }),
    done('held'),
  ],
  { backgroundAgingMs: 300000 },
)
add(
  'queued_and_running_cancellation_ownership',
  [
    run('active', 1),
    run('waiting', 1),
    abort('waiting'),
    abort('active'),
    run('next', 1),
    done('active'),
    done('next'),
  ],
  { concurrency: 2, maxQueued: 2, reserveForeground: 1 },
)
add(
  'preaborted_before_full_and_no_slot_preemption',
  [
    run('background', 2),
    run('queued'),
    run('preaborted', 0, { aborted: true }),
    run('full'),
    abort('queued'),
    run('foreground'),
    done('background'),
    done('foreground'),
  ],
  { maxQueued: 1, reserveForeground: 1 },
)
add(
  'configure_shrink_preserves_running_ownership',
  [
    run('a', 2),
    run('b', 1),
    configure(3),
    configure(1),
    run('foreground'),
    done('a'),
    done('b'),
    done('foreground'),
  ],
  { concurrency: 2, reserveForeground: 1 },
)
add(
  'reserve_exceeding_concurrency_keeps_one_background',
  [run('a', 2), run('b', 1), run('f0'), run('f1'), done('a'), done('b'), done('f0'), done('f1')],
  { concurrency: 3, reserveForeground: 9007199254740991 },
)
add(
  'explicit_oldest_learning_after_four_starts',
  [
    run('held'),
    run('background', 2, { auto: true }),
    run('older', 2, { auto: true, overdueLearningAt: 0 }),
    run('newer', 1, { auto: true, overdueLearningAt: 0 }),
    ...fg(12, 0, { overdueLearningAt: 0 }),
    done('held'),
  ],
  { backgroundAgingMs: 0, reserveForeground: 1 },
)
add('deadline_only_counts_subsequent_foreground_starts', [
  run('held'),
  run('learning', 1, { auto: true, overdueLearningAt: 100 }),
  ...fg(4),
  run('halfway'),
  ...fg(5, 5),
  done('held'),
  advance(100),
  done('halfway'),
])
add(
  'unmarked_aged_work_has_no_learning_turn',
  [run('held'), run('background', 2, { auto: true }), ...fg(12), done('held')],
  { backgroundAgingMs: 0 },
)
add(
  'fairness_respects_reserved_nonforeground_capacity',
  [
    run('background', 2),
    run('f0'),
    run('l1', 1, { overdueLearningAt: 0 }),
    run('l2', 1, { auto: true, overdueLearningAt: 0 }),
    ...Array.from({ length: 5 }, (_, i) => run(`f${i + 1}`)),
    done('f0'),
    done('f1'),
    done('f2'),
    done('f3'),
    done('background'),
    done('f4'),
    done('l1'),
    done('f5'),
  ],
  { concurrency: 2, reserveForeground: 1 },
)
add('cancel_oldest_keeps_surviving_learning_allowance', [
  run('held'),
  run('removed', 1, { overdueLearningAt: 0 }),
  run('learning', 1, { overdueLearningAt: 0 }),
  ...fg(3),
  run('fourth'),
  ...fg(4, 4),
  done('held'),
  abort('removed'),
  done('fourth'),
  abort('learning'),
  done('learning'),
])
add('cancel_all_learning_resets_allowance', [
  run('held'),
  run('removed', 1, { overdueLearningAt: 0 }),
  ...fg(3),
  run('fourth'),
  ...fg(5, 4),
  done('held'),
  abort('removed'),
  run('new-learning', 1, { auto: true, overdueLearningAt: 0 }),
  done('fourth'),
])
add(
  'rolling_minute_start_boundary_and_existing_queue_limit',
  [
    configure(2, 2),
    run('a', 0, { auto: true }),
    advance(1000),
    run('b', 0, { auto: true }),
    run('c', 0, { auto: true }),
    run('d', 0, { auto: true }),
    run('full'),
    advance(59999),
    advance(60000),
    advance(60999),
    advance(61000),
  ],
  { concurrency: 2, maxQueued: 2 },
)
add('fairness_consumes_rate_allowance', [
  configure(1, 5),
  run('held'),
  run('learning', 1, { auto: true, overdueLearningAt: 0 }),
  ...fg(5),
  done('held'),
  advance(59999),
  advance(60000),
])
add('cancel_rate_wait_spends_no_call_disable_drains_once', [
  configure(1, 1),
  run('a', 0, { auto: true }),
  run('cancelled'),
  run('survivor', 0, { auto: true }),
  abort('cancelled'),
  configure(1),
  advance(120000),
])
add('enabling_rate_does_not_count_unlimited_starts', [
  run('a', 0, { auto: true }),
  run('b', 0, { auto: true }),
  configure(1, 1),
  run('c', 0, { auto: true }),
  run('d', 0, { auto: true }),
  configure(1),
  configure(1, 1),
  run('e', 0, { auto: true }),
  advance(60000),
])
add('rate_reconfiguration_keeps_accounted_starts', [
  configure(1, 3),
  run('a', 0, { auto: true }),
  advance(1000),
  run('b', 0, { auto: true }),
  configure(1, 1),
  run('c', 0, { auto: true }),
  advance(60000),
  advance(61000),
])
add(
  'timer_cancelled_when_only_capacity_blocked',
  [
    configure(2, 1),
    run('background', 2),
    run('blocked', 2),
    run('foreground'),
    abort('foreground'),
    advance(60000),
    done('background'),
    done('blocked'),
  ],
  { concurrency: 2, reserveForeground: 1 },
)
add('failures_preserve_error_and_release_slots', [
  run('held'),
  run('error'),
  run('nonerror'),
  run('last', 0, { auto: true }),
  done('held', 'error'),
  done('error', 'error'),
  done('nonerror', 'nonerror'),
])
add('early_completion_and_duplicate_completion', [
  run('held'),
  run('queued'),
  done('queued'),
  done('queued', 'error'),
  done('held'),
  done('held', 'error'),
  abort('queued'),
])
add('captured_submission_context_does_not_leak', [
  run('held'),
  run('background', 2, {
    auto: true,
    scopePriorities: [2, 1],
    scopeDeadlines: [1, 100, 2],
    trace: 'background',
    model: true,
  }),
  run('realtime', 1, {
    auto: true,
    scopePriorities: [1],
    scopeDeadlines: [0],
    trace: 'realtime',
    model: true,
  }),
  run('plain', 0, { auto: true }),
  done('held'),
])
add('fractional_learning_deadline_and_foreground_optout', [
  run('held'),
  run('learning', 2, { auto: true, overdueLearningAt: 0.5 }),
  advance(0.25),
  ...fg(4),
  run('halfway'),
  ...fg(5, 4, { overdueLearningAt: 0 }),
  done('held'),
  advance(0.5),
  done('halfway'),
])
add('constructor_concurrency_not_validated', [run('queued'), configure(1), done('queued')], {
  concurrency: 0,
})
add(
  'constructor_fractional_concurrency_not_rounded',
  [run('a'), run('b'), run('c'), done('a'), done('b'), done('c')],
  { concurrency: 1.5 },
)
add(
  'constructor_zero_queue_rejects_even_idle',
  [run('rejected'), run('cancelled', 0, { aborted: true })],
  { maxQueued: 0 },
)
add('invalid_configure_is_atomic', [
  run('held'),
  run('waiting'),
  ...[-1, 0, 0.5, 33, NaN, Infinity].map((n) => configure(n)),
  ...[-1, 0.5, 6001, NaN, Infinity, 9007199254740992].map((n) => configure(2, n)),
  done('held'),
  done('waiting'),
])
add('raw_nonfinite_deadlines_never_gain_fairness', [
  run('held'),
  run('nan', 1, { auto: true, overdueLearningAt: NaN }),
  run('infinite', 2, { auto: true, overdueLearningAt: Infinity }),
  run('negative', 2, { auto: true, overdueLearningAt: -Infinity }),
  ...fg(8),
  done('held'),
])

let seed = 0x51c4ed
const random = () => {
  seed ^= seed << 13
  seed ^= seed >>> 17
  seed ^= seed << 5
  return (seed >>> 0) / 4294967296
}
for (let n = 0; n < 12; n++) {
  const steps = [],
    ids = []
  let time = 0
  for (let i = 0; i < 180; i++) {
    const pick = random()
    if (pick < 0.45 || ids.length === 0) {
      const id = `j${ids.length}`
      ids.push(id)
      steps.push(
        run(id, Math.floor(random() * 4) - 1, {
          auto: random() < 0.3,
          ...(random() < 0.4 ? { overdueLearningAt: time + (random() < 0.5 ? 0 : 100) } : {}),
        }),
      )
    } else if (pick < 0.63)
      steps.push(
        done(ids[Math.floor(random() * ids.length)], random() < 0.15 ? 'error' : undefined),
      )
    else if (pick < 0.76) steps.push(abort(ids[Math.floor(random() * ids.length)]))
    else if (pick < 0.89)
      steps.push(
        configure(1 + Math.floor(random() * 5), random() < 0.5 ? 0 : 1 + Math.floor(random() * 5)),
      )
    else {
      time += [1, 99, 100, 59999, 60000, 300000][Math.floor(random() * 6)]
      steps.push(advance(time))
    }
  }
  steps.push(configure(4))
  for (const id of ids) steps.push(done(id))
  steps.push(advance(time + 60000))
  add(`seeded_interleaving_${n}`, steps, {
    concurrency: 1 + (n % 4),
    maxQueued: 5 + n,
    reserveForeground: n % 3,
    backgroundAgingMs: n % 2 ? 100 : 0,
  })
}
const traceContext = new AsyncLocalStorage()
const flush = async () => {
  for (let i = 0; i < 128; i++) await Promise.resolve()
}
const optional = (value) => (value === undefined ? { present: false } : { present: true, value })
async function execute(trace) {
  let now = 0,
    timerSequence = 0
  const timers = new Map(),
    jobs = new Map(),
    previousStates = new Map(),
    events = []
  const oldSet = globalThis.setTimeout,
    oldClear = globalThis.clearTimeout
  globalThis.setTimeout = (callback, delay) => {
    const timer = {
      id: timerSequence++,
      at: now + Math.trunc(delay),
      callback,
      unref() {
        return this
      },
    }
    timers.set(timer.id, timer)
    return timer
  }
  globalThis.clearTimeout = (timer) => {
    if (timer) timers.delete(timer.id)
  }
  const scheduler = new WorkScheduler(trace.config.concurrency, trace.config.maxQueued, {
    reserveForeground: trace.config.reserveForeground,
    backgroundAgingMs: trace.config.backgroundAgingMs,
    now: () => now,
  })
  try {
    const output = []
    for (const step of trace.steps) {
      let error
      try {
        if (step.op === 'run') {
          const controller = new AbortController()
          if (step.aborted) controller.abort()
          let resolve, reject
          const promise = new Promise((yes, no) => {
            resolve = yes
            reject = no
          })
          promise.catch(() => {})
          const job = { controller, resolve, reject, state: 'pending' }
          jobs.set(step.id, job)
          const task = async () => {
            events.push({
              id: step.id,
              at: now,
              priority: effectiveModelPriority(0),
              deadline: optional(getOverdueLearningAt()),
              trace: optional(traceContext.getStore()),
              aborted: controller.signal.aborted,
            })
            if (!step.auto) await promise
            return step.id
          }
          const invoke = () =>
            scheduler.run(
              step.model ? effectiveModelPriority(step.priority) : step.priority,
              controller.signal,
              task,
              { overdueLearningAt: step.model ? getOverdueLearningAt() : step.overdueLearningAt },
            )
          const scopes = [
            ...(step.scopePriorities ?? []).map((value) => (fn) => withModelPriority(value, fn)),
            ...(step.scopeDeadlines ?? []).map((value) => (fn) => withOverdueLearning(fn, value)),
          ]
          const scoped = scopes.reduceRight((fn, scope) => () => scope(fn), invoke)
          const result = step.trace === undefined ? scoped() : traceContext.run(step.trace, scoped)
          result.then(
            (value) => {
              job.state = 'resolved'
              job.value = value
            },
            (failure) => {
              job.state = 'rejected'
              job.error = failure.message
            },
          )
        } else if (step.op === 'complete') {
          const job = jobs.get(step.id)
          if (step.failure)
            job.reject(step.failure === 'nonerror' ? 'raw_failure' : new Error('fixture_failure'))
          else job.resolve()
        } else if (step.op === 'abort') jobs.get(step.id).controller.abort()
        else if (step.op === 'configure') scheduler.configure(step.concurrency, step.callsPerMinute)
        else if (step.op === 'advance') {
          now = step.at
          while (true) {
            const timer = [...timers.values()]
              .filter((timer) => timer.at <= now)
              .sort((a, b) => a.at - b.at || a.id - b.id)[0]
            if (!timer) break
            timers.delete(timer.id)
            timer.callback()
            await flush()
          }
        } else throw Error('unknown_fixture_operation')
      } catch (caught) {
        error = caught.message
      }
      await flush()
      output.push({
        input: step,
        expected: {
          ...(error ? { error } : {}),
          status: scheduler.status,
          events: events.splice(0),
          jobs: Object.fromEntries(
            [...jobs]
              .filter(([id, job]) => {
                if (previousStates.get(id) === job.state) return false
                previousStates.set(id, job.state)
                return true
              })
              .map(([id, job]) => [
                id,
                {
                  state: job.state,
                  ...(job.value === undefined ? {} : { value: job.value }),
                  ...(job.error === undefined ? {} : { error: job.error }),
                },
              ]),
          ),
          timers: [...timers.values()].map((timer) => timer.at).sort((a, b) => a - b),
        },
      })
    }
    return { name: trace.name, config: trace.config, steps: output }
  } finally {
    globalThis.setTimeout = oldSet
    globalThis.clearTimeout = oldClear
  }
}
const invalidOptions = []
for (const field of ['reserveForeground', 'backgroundAgingMs'])
  for (const value of [-1, 0.5, NaN, Infinity, -Infinity, 9007199254740992]) {
    const options = { [field]: value }
    try {
      new WorkScheduler(1, 256, options)
      throw Error('fixture_expected_rejection')
    } catch (error) {
      invalidOptions.push({ options, error: error.message })
    }
  }
const scopes = []
for (const priorities of [[], [1], [2], [1, 2], [2, 1], [0], [3], [NaN]])
  for (const deadlines of [[], [0], [100, 1], [1, 100], [0.5], [-1], [Infinity], [NaN]]) {
    let result, error
    try {
      const wrappers = [
        ...priorities.map((v) => (f) => withModelPriority(v, f)),
        ...deadlines.map((v) => (f) => withOverdueLearning(f, v)),
      ]
      wrappers.reduceRight(
        (f, wrap) => () => wrap(f),
        () => {
          result = {
            priority: effectiveModelPriority(0),
            higherBase: effectiveModelPriority(3),
            deadline: optional(getOverdueLearningAt()),
          }
        },
      )()
    } catch (caught) {
      error = caught.message
    }
    scopes.push({ priorities, deadlines, expected: error ? { error } : result })
  }
const fixture = {
  version: 1,
  baseline: '24fe8a773d87f2f09996e259fca46524c2c02928',
  source: 'src/scheduler.ts',
  sourceSHA256,
  node: process.version,
  invalidOptions,
  scopes,
  traces: [],
}
for (const trace of traces) fixture.traces.push(await execute(trace))
const content =
  JSON.stringify(
    fixture,
    (_key, value) =>
      typeof value === 'number' && !Number.isFinite(value) ? { number: String(value) } : value,
    2,
  ) + '\n'
const directory = new URL('../internal/scheduler/testdata/', import.meta.url),
  destination = new URL('traces-node.json', directory)
if (process.argv.includes('--check')) {
  if (readFileSync(destination, 'utf8') !== content) throw Error('Scheduler fixtures are stale')
} else {
  mkdirSync(directory, { recursive: true })
  writeFileSync(destination, content)
}
console.log(
  `${fixture.traces.length} traces, ${fixture.traces.reduce((n, t) => n + t.steps.length, 0)} transitions, ${invalidOptions.length} invalid options, ${scopes.length} context cases`,
)
