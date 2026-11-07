/** Real source and real temporary files, synthetic content only.
 * node --import tsx backend/tests/generate-tool-result-fixtures.mjs [--check]
 */
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { readdir, rm, truncate } from 'node:fs/promises'
import { ToolResultCache } from '../../src/tool-results.ts'

const sourceSHA256 = createHash('sha256')
  .update(readFileSync(new URL('../../src/tool-results.ts', import.meta.url)))
  .digest('hex')
if (process.version !== 'v24.22.1') throw Error('Use the pinned Node 24.22.1 oracle')
if (sourceSHA256 !== '6a74dee6988aca28f22adb1f392dd8d7cd75e1dea23922fc7a8b280e1375a08b')
  throw Error('Tool-result source changed; review the baseline before replacing fixtures')
if (process.argv.slice(2).some((value) => value !== '--check'))
  throw Error('Only --check is supported')
const sha = (value) => createHash('sha256').update(value).digest('hex')
const units = (text) => Array.from({ length: text.length }, (_, i) => text.charCodeAt(i))
const text = (value, repeat = 1) => ({ kind: 'text', units: units(value), repeat })
const binary = (length, chunkBytes) => ({
  kind: chunkBytes ? 'stream' : 'bytes',
  length,
  chunkBytes,
})
const store = (source, options = {}, more = {}) => ({ op: 'store', source, options, ...more })
const read = (input = {}, more = {}) => ({ op: 'read', input, ...more })
const traces = []
const add = (name, steps, ttl = 10000, now = 1000) => traces.push({ name, ttl, now, steps })

add('default_text_bom_surrogates_and_empty_metadata', [
  store(text('a😀中z')),
  read({ offset: 2, length: 3, encoding: 'utf8' }),
  read({ offset: 9 }),
  read({ length: 0 }),
  store(text('\ufeffexact')),
  store(text('\ud800\ud801\udc00\udfff')),
  store(text(''), { contentType: '' }),
  store(text('metadata'), { contentType: 'fixture/\ud800' }),
  store(binary(0)),
  read({ full: true }),
])
for (const [name, source] of [
  ['ascii', text('a', 40000)],
  ['quotes', text('"', 40000)],
  ['backslashes', text('\\', 40000)],
  ['controls', text('\u0000\n\t\r\b\f', 8000)],
  ['unicode', text('😀中é', 5000)],
  ['html_not_escaped', text('<>&\u2028\u2029', 7000)],
  ['binary', binary(60000)],
  ['stream', binary(120000, 7001)],
])
  add(`view_budget_${name}`, [
    store(source),
    read({ full: true }),
    read({ length: 32768 }),
    read({ offset: 3, full: true }),
    read({ offset: 3, length: 71, encoding: 'utf8' }),
    read({ length: 9007199254740991 }),
  ])
add('zero_preview_is_not_a_storage_cap', [
  store(binary(50000, 1237), { previewBytes: 0 }),
  read({ full: true }),
  read({ offset: 49990, full: true }),
])
add('explicit_encoding_and_preview', [
  store(text('utf8/😀'), { encoding: 'base64', previewBytes: 2 }),
  read({ encoding: 'utf8' }),
  store(binary(123), { encoding: 'utf8', previewBytes: 9007199254740991 }),
])
add('invalid_ranges_and_error_order', [
  read({ offset: -1 }),
  read({ offset: 0.5 }),
  read({ length: -1 }),
  read({ offset: 9007199254740992 }),
  read({ full: true, length: 0 }),
  read({ offset: -1 }, { cancel: true }),
  read(),
  store(text('fixture')),
  read({ offset: 8 }),
  read({ offset: 8 }, { scope: 'wrong' }),
  read({ offset: 9007199254740991 }),
  read({ offset: -0, length: -0 }),
  read({ length: 9007199254740991 }),
  store(text('removed'), { previewBytes: -1 }),
  store(text('removed'), { previewBytes: 0.5 }),
  store(text('removed'), { previewBytes: 9007199254740992 }),
])
add('expiry_is_not_sliding_and_removal_is_immediate', [
  store(text('fixture')),
  { op: 'time', now: 10999 },
  read(),
  { op: 'time', now: 11000 },
  read({}, { scope: 'wrong' }),
  read({ offset: 100 }),
  read(),
  store(text('next')),
  { op: 'time', now: 21000 },
  { op: 'expire' },
  read(),
])
add('scope_scene_plugin_dependency_isolation', [
  store(text('a'), {}, { save: 'a' }),
  store(text('b'), {}, { save: 'b', owner: { scope: 'real:scene-b', sceneId: 'scene-b' } }),
  read({}, { handle: 'a', scope: 'simulation:scene-a:run-a' }),
  read({}, { handle: 'a', adopt: true }),
  { op: 'invalidate', sceneId: 'scene-b', messageId: 'not-dependent' },
  read({}, { handle: 'b', scope: 'real:scene-b' }),
  { op: 'invalidate', sceneId: 'scene-a', messageId: 'message-a' },
  read({}, { handle: 'a' }),
  read({}, { handle: 'b', scope: 'real:scene-b' }),
  { op: 'invalidatePlugin', pluginId: 'fixture' },
  read({}, { handle: 'b', scope: 'real:scene-b' }),
  store(text('new'), {}, { owner: { pluginId: 'other' } }),
  { op: 'invalidatePlugin', pluginId: 'fixture' },
  read(),
])
add('validation_false_throw_and_post_io_guard', [
  store(text('valid'), {}, { guard: true }),
  { op: 'guard', value: false },
  read({ offset: 999 }),
  read(),
  { op: 'guard', value: true },
  store(text('valid'), {}, { guard: true }),
  { op: 'guard', error: 'fixture_validation_failed' },
  read(),
  { op: 'guard', value: true },
  read(),
  { op: 'guard', falseAt: 2 },
  read({}, { adopt: true }),
  read(),
  { op: 'guard', falseAt: 1 },
  store(text('rejected'), {}, { guard: true }),
  { op: 'guard', falseAt: 2 },
  store(text('rejected'), {}, { guard: true }),
  { op: 'guard', falseAt: 3 },
  store(text('rejected'), {}, { guard: true }),
  { op: 'guard', error: 'fixture_validation_failed' },
  store(text('rejected'), {}, { guard: true }),
  { op: 'guard', error: 'tool_result_guard_error' },
  store(text('rejected'), {}, { guard: true }),
])
add('adoption_is_after_guards_and_errors_do_not_remove', [
  store(text('valid')),
  read({}, { adopt: true }),
  read({}, { adoptError: 'fixture_adopt_failed' }),
  read(),
  read({}, { adoptInvalidate: true }),
  read(),
])
add('missing_or_short_file_and_recovery', [
  store(text('fixture')),
  { op: 'truncate' },
  read(),
  store(text('next')),
  { op: 'unlink' },
  read(),
  store(text('recovery')),
  read(),
])
add('source_errors_clean_staged_files', [
  store({ kind: 'failed' }),
  store({ kind: 'toolError' }),
  store({ kind: 'invalidChunk' }),
  store(text('recovery')),
  store(text('cancelled'), {}, { cancel: true }),
  { op: 'dispose' },
  read(),
  store(text('closed'), {}, { cancel: true }),
])
add('no_restart_persistence', [
  store(text('ephemeral')),
  { op: 'restart' },
  read(),
  store(text('fresh')),
])
add('fractional_epoch_uses_date_timeclip', [store(text('time'))], 10000.75, -0.25)
add('extended_iso_year', [store(text('time'))], 10000, 253402300800000)

function sourceValue(spec) {
  if (spec.kind === 'text') return String.fromCharCode(...spec.units).repeat(spec.repeat)
  const bytes = Buffer.alloc(spec.length ?? 4)
  for (let i = 0; i < bytes.length; i++) bytes[i] = i % 256
  if (spec.kind === 'bytes') return bytes
  return (async function* () {
    if (spec.kind === 'invalidChunk') {
      yield 'bad'
      return
    }
    for (let offset = 0; offset < bytes.length; offset += spec.chunkBytes ?? 4)
      yield bytes.subarray(offset, offset + (spec.chunkBytes ?? 4))
    if (spec.kind === 'failed') throw Error('fixture_source_failed')
    if (spec.kind === 'toolError') throw Error('tool_result_source_error')
  })()
}
function observe(result) {
  const nestedBytes = Buffer.byteLength(JSON.stringify(JSON.stringify(result.content)))
  const decodedSHA256 = sha(
    Buffer.from(result.content, result.encoding === 'utf8' ? 'utf8' : 'base64'),
  )
  const contentBytes = Buffer.byteLength(result.content)
  return {
    result: JSON.stringify({ ...result, resultId: '<result>', content: sha(result.content) }),
    nestedBytes,
    decodedSHA256,
    contentBytes,
  }
}
async function execute(trace) {
  let now = trace.now,
    guard = { value: true },
    calls = 0
  let cache = new ToolResultCache(trace.ttl, () => now)
  const handles = new Map(),
    observations = []
  try {
    for (const step of trace.steps) {
      let outcome = {},
        adopted = [],
        adoptionCleanup
      const controller = new AbortController()
      if (step.cancel) controller.abort(Error('fixture_cancelled'))
      const id = handles.get(step.handle ?? 'latest') ?? 'missing'
      const validate = () => {
        calls++
        if (guard.error) throw Error(guard.error)
        return guard.falseAt ? calls < guard.falseAt : guard.value
      }
      try {
        switch (step.op) {
          case 'store': {
            const owner = {
              scope: 'real:scene-a',
              sceneId: 'scene-a',
              pluginId: 'fixture',
              dependencies: ['message-a'],
              validations: step.guard ? [validate] : [],
              ...step.owner,
            }
            const result = await cache.store(
              sourceValue(step.source),
              owner,
              controller.signal,
              step.options,
            )
            handles.set(step.save ?? 'latest', result.resultId)
            outcome = observe(result)
            break
          }
          case 'read':
            outcome = observe(
              await cache.read(
                { resultId: id, ...step.input },
                step.scope ?? 'real:scene-a',
                controller.signal,
                step.adopt || step.adoptError || step.adoptInvalidate
                  ? (entry) => {
                      adopted.push(...entry.dependencies)
                      if (step.adoptError) throw Error(step.adoptError)
                      if (step.adoptInvalidate)
                        adoptionCleanup = cache.invalidatePlugin(entry.pluginId)
                    }
                  : undefined,
              ),
            )
            break
          case 'guard':
            guard = step
            calls = 0
            break
          case 'time':
            now = step.now
            break
          case 'expire':
            await cache.expire()
            break
          case 'invalidate':
            await cache.invalidate(step.sceneId, step.messageId)
            break
          case 'invalidatePlugin':
            await cache.invalidatePlugin(step.pluginId)
            break
          case 'truncate':
            await truncate(cache.entries.get(id).path, 0)
            break
          case 'unlink':
            await rm(cache.entries.get(id).path)
            break
          case 'dispose':
            await cache.dispose()
            break
          case 'restart':
            await cache.dispose()
            cache = new ToolResultCache(trace.ttl, () => now)
            break
          default:
            throw Error('fixture_unknown_step')
        }
      } catch (error) {
        if (error.message === 'fixture_unknown_step') throw error
        outcome = { error: error.message }
      }
      await adoptionCleanup
      const directory = await cache.directory
      const files = directory
        ? await readdir(directory).catch((error) =>
            error.code === 'ENOENT' ? [] : Promise.reject(error),
          )
        : []
      observations.push({
        input: step,
        expected: {
          ...outcome,
          adopted,
          entries: cache.entries.size,
          files: files.length,
          pending: cache.pending.size,
          writes: cache.writes.size,
        },
      })
    }
    return { ...trace, steps: observations }
  } finally {
    await cache.dispose()
  }
}

// A stalled next and a separately stalled return exercise the actual source's
// abort race. No synthetic timeout is substituted for source cancellation.
async function stalled(mode) {
  const cache = new ToolResultCache(10000, () => 1000),
    controller = new AbortController()
  let start,
    returns = 0,
    destroys = 0
  const started = new Promise((resolve) => {
    start = resolve
  })
  const source = {
    destroy() {
      destroys++
    },
    [Symbol.asyncIterator]() {
      return {
        next() {
          start()
          return new Promise(() => {})
        },
        return() {
          returns++
          return new Promise(() => {})
        },
      }
    },
  }
  const pending = cache
    .store(
      source,
      {
        scope: 'real:scene-a',
        sceneId: 'scene-a',
        pluginId: 'fixture',
        dependencies: ['message-a'],
        validations: [],
      },
      controller.signal,
    )
    .then(
      () => {
        throw Error('fixture_unexpected_success')
      },
      (error) => error.message,
    )
  await started
  if (mode === 'cancel') controller.abort()
  if (mode === 'dispose') await cache.dispose()
  if (mode === 'invalidate') await cache.invalidate('scene-a', 'message-a')
  if (mode === 'plugin') await cache.invalidatePlugin('fixture')
  const error = await pending
  await cache.dispose()
  return {
    mode,
    error,
    returns,
    destroys,
    pending: cache.pending.size,
    writes: cache.writes.size,
    entries: cache.entries.size,
  }
}

const fixture = {
  version: 1,
  node: process.version,
  source: 'src/tool-results.ts',
  sourceSHA256,
  traces: [],
  stalled: [],
}
for (const trace of traces) fixture.traces.push(await execute(trace))
for (const mode of ['cancel', 'dispose', 'invalidate', 'plugin'])
  fixture.stalled.push(await stalled(mode))
const content = JSON.stringify(fixture, null, 2) + '\n'
const destination = new URL('../internal/toolresults/testdata/source-node.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(destination, 'utf8') !== content) throw Error('Tool-result fixtures are stale')
} else {
  mkdirSync(new URL('../internal/toolresults/testdata/', import.meta.url), { recursive: true })
  writeFileSync(destination, content)
}
console.log(
  `${fixture.traces.length} tool-result traces, ${fixture.traces.reduce((n, t) => n + t.steps.length, 0)} transitions, ${fixture.stalled.length} stalled iterator cases`,
)
