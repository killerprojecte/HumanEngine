import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'

// Exercise the actual setup logic, not a duplicate state-machine implementation.
// This does not replace rendered browser QA or layout/accessibility checks.
const source = readFileSync(new URL('../app/pages/memories.vue', import.meta.url), 'utf8')
  .split('<script setup lang="ts">')[1]!
  .split('</script>')[0]!
  .replace(/^import .*\n/gm, '')
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText
const record = (id: string, content = id) => ({
  id,
  content,
  scope: { kind: 'scene', sceneId: 's' },
  kind: 'fact',
  importance: 'medium',
  confidence: 1,
  locked: false,
  sources: [],
  learnedAt: '2026-01-01T00:00:00.000Z',
  status: 'active',
  origin: 'manual',
  revision: { version: 1, deletionEpoch: 0 },
})
function fixture(options: { holdScenes?: boolean } = {}) {
  const sceneReads: { resolve(value: any): void }[] = []
  const pending: { options: any; resolve(value: any): void }[] = []
  const mutations: string[] = []
  const dispose: (() => void)[] = []
  const client = {
    GET: (path: string, requestOptions: any) =>
      path === '/api/scenes'
        ? options.holdScenes
          ? new Promise((resolve) => sceneReads.push({ resolve }))
          : Promise.resolve({ data: [] })
        : new Promise((resolve) => pending.push({ options: requestOptions, resolve })),
    POST: async () => {
      mutations.push('post')
      return { data: record('new') }
    },
    PATCH: async () => {
      mutations.push('patch')
      return { data: record('selected', 'updated') }
    },
    DELETE: async () => {
      mutations.push('delete')
      return { data: { ok: true } }
    },
  }
  const scope = effectScope()
  const setup = new Function(
    'ref',
    'computed',
    'reactive',
    'watch',
    'nextTick',
    'useI18n',
    'useApi',
    'onBeforeRouteLeave',
    'onMounted',
    'onBeforeUnmount',
    'window',
    'document',
    `${compiled}\nreturn { readPage, load, confirmAction, save, begin, selected, selectedId,
      mode, modalOpen, confirmation, page, loaded, memories, total, matched, pageBusy,
      mutationBusy, recoveryRequired, editorUncertain, sceneFilter, kindFilter, query, administrationPersonIds,
      error, draft, original: () => original };`,
  )
  const state = scope.run(() =>
    setup(
      ref,
      computed,
      reactive,
      watch,
      nextTick,
      () => ({ t: (key: string) => key, locale: ref('en') }),
      () => ({
        client,
        authenticatedFetch: async () => ({
          ok: true,
          json: async () => ({ sceneId: 's', people: [] }),
        }),
      }),
      () => {},
      () => {},
      (callback: () => void) => dispose.push(callback),
      { addEventListener() {}, removeEventListener() {} },
      { activeElement: undefined },
    ),
  ) as any
  return {
    state,
    pending,
    sceneReads,
    mutations,
    close: () => {
      for (const callback of dispose) callback()
      scope.stop()
    },
  }
}
function response(items: ReturnType<typeof record>[], page = 1, matched = items.length) {
  return {
    data: { items, page, limit: 20, matched, total: matched, personIds: ['future', 'inactive'] },
  }
}

test('aborted/stale pages cannot replace newer rows or clear uncertainty', async () => {
  const f = fixture()
  try {
    f.state.recoveryRequired.value = true
    f.state.editorUncertain.value = true
    const first = f.state.readPage()
    const second = f.state.readPage()
    assert.equal(f.pending[0]!.options.signal.aborted, true)
    assert.equal(
      f.pending[1]!.options.params.query.locale,
      Intl.DateTimeFormat().resolvedOptions().locale,
    )
    f.pending[1]!.resolve(response([record('fresh')]))
    await second
    f.pending[0]!.resolve(response([record('stale')]))
    await first
    assert.equal(f.state.memories.value[0].id, 'fresh')
    assert.equal(f.state.recoveryRequired.value, true)
    assert.equal(f.state.editorUncertain.value, true)
    assert.deepEqual([...f.state.administrationPersonIds.value], ['future', 'inactive'])
  } finally {
    f.close()
  }
})

test('page response cannot replace the captured editing revision', async () => {
  const f = fixture()
  try {
    const selected = record('selected', 'original')
    f.state.selected.value = selected
    f.state.mode.value = 'edit'
    const request = f.state.readPage()
    f.pending[0]!.resolve(
      response([{ ...record('selected', 'newer'), revision: { version: 2, deletionEpoch: 1 } }]),
    )
    await request
    assert.equal(f.state.selected.value.content, 'original')
    assert.equal(f.state.selected.value.revision.version, 1)
  } finally {
    f.close()
  }
})

test('successful delete followed by refresh failure is not an uncertain or repeated mutation', async () => {
  const f = fixture()
  try {
    f.state.selected.value = record('selected')
    f.state.selectedId.value = 'selected'
    f.state.confirmation.value = 'delete'
    f.state.modalOpen.value = true
    const deleting = f.state.confirmAction()
    await nextTick()
    f.pending[0]!.resolve({ error: { error: 'synthetic page failure' } })
    await deleting
    assert.deepEqual(f.mutations, ['delete'])
    assert.equal(f.state.recoveryRequired.value, false)
    assert.equal(f.state.editorUncertain.value, false)
    assert.equal(f.state.selected.value, undefined)
    assert.equal(f.state.error.value, 'synthetic page failure')
  } finally {
    f.close()
  }
})

test('post-delete server page clamp does not launch a redundant page read', async () => {
  const f = fixture()
  try {
    f.state.loaded.value = true
    f.state.page.value = 2
    f.state.selected.value = record('selected')
    f.state.selectedId.value = 'selected'
    f.state.confirmation.value = 'delete'
    f.state.modalOpen.value = true
    const deleting = f.state.confirmAction()
    await nextTick()
    f.pending[0]!.resolve(
      response(
        Array.from({ length: 20 }, (_, i) => record(`left-${i}`)),
        1,
        20,
      ),
    )
    await deleting
    await nextTick()
    assert.equal(f.state.page.value, 1)
    assert.equal(f.state.memories.value.length, 20)
    assert.equal(f.pending.length, 1)
    assert.equal(f.state.mutationBusy.value, false)
  } finally {
    f.close()
  }
})

test('unmount aborts pending reads and blocks late responses', async () => {
  const f = fixture()
  const request = f.state.readPage()
  f.close()
  assert.equal(f.pending[0]!.options.signal.aborted, true)
  f.pending[0]!.resolve(response([record('too-late')]))
  await request
  assert.equal(f.state.memories.value.length, 0)
})

test('filter changes during explicit initial load reject old rows and run the deferred query', async () => {
  const f = fixture()
  try {
    const initial = f.state.load()
    f.state.query.value = 'new filter'
    await nextTick()
    f.pending[0]!.resolve(response([record('old-filter')]))
    await initial
    assert.equal(f.state.memories.value.length, 0)
    assert.equal(f.pending.length, 2)
    assert.equal(f.pending[1]!.options.params.query.q, 'new filter')
    f.pending[1]!.resolve(response([record('matching-new-filter')]))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.state.memories.value[0].id, 'matching-new-filter')
    assert.equal(f.state.mutationBusy.value, false)
  } finally {
    f.close()
  }
})

test('successful creation resets filters and refreshes once without cancelling its own read', async () => {
  const f = fixture()
  try {
    f.state.loaded.value = true
    f.state.query.value = 'old filter'
    f.state.page.value = 3
    f.state.begin()
    f.state.draft.value.content = 'new'
    f.state.draft.value.sceneId = 's'
    const saving = f.state.save()
    await nextTick()
    assert.deepEqual(f.mutations, ['post'])
    assert.equal(f.pending.length, 1)
    assert.equal(f.pending[0]!.options.signal.aborted, false)
    assert.equal(f.pending[0]!.options.params.query.scene, 'scene:s')
    assert.equal(f.pending[0]!.options.params.query.page, 1)
    assert.equal(f.pending[0]!.options.params.query.q, '')
    f.pending[0]!.resolve(response([record('new')]))
    await saving
    await nextTick()
    assert.equal(f.pending.length, 1)
    assert.equal(f.state.memories.value[0].id, 'new')
    assert.equal(f.state.recoveryRequired.value, false)
  } finally {
    f.close()
  }
})

test('late scenes response cannot unlock rows for old controls or clear recovery state', async () => {
  const f = fixture({ holdScenes: true })
  let stop = () => {}
  try {
    f.state.recoveryRequired.value = true
    const initial = f.state.load()
    f.pending[0]!.resolve(response([record('old-controls')]))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.state.memories.value[0].id, 'old-controls')
    assert.equal(f.state.mutationBusy.value, true)
    f.state.kindFilter.value = 'claim'
    await nextTick()
    stop = watch(
      f.state.mutationBusy,
      (busy) => {
        if (!busy)
          assert.equal(
            f.state.memories.value.some((row: any) => row.id === 'old-controls'),
            false,
          )
      },
      { flush: 'sync' },
    )
    f.sceneReads[0]!.resolve({ data: [] })
    await initial
    assert.equal(f.state.memories.value.length, 0)
    assert.equal(f.state.recoveryRequired.value, true)
    await new Promise((resolve) => setTimeout(resolve, 0))
    assert.equal(f.pending.length, 2)
    assert.equal(f.pending[1]!.options.params.query.kind, 'claim')
    f.pending[1]!.resolve(response([record('new-controls')]))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.state.memories.value[0].id, 'new-controls')
  } finally {
    stop()
    f.close()
  }
})

test('failed request does not suppress a deferred retry when controls return to its query', async () => {
  const f = fixture({ holdScenes: true })
  try {
    const initial = f.state.load()
    f.state.kindFilter.value = 'claim'
    await nextTick()
    f.state.kindFilter.value = 'all'
    await nextTick()
    f.pending[0]!.resolve({ error: { error: 'synthetic failure' } })
    f.sceneReads[0]!.resolve({ data: [] })
    await initial
    assert.equal(f.pending.length, 2)
    assert.equal(f.pending[1]!.options.params.query.kind, 'all')
    f.pending[1]!.resolve(response([record('retry')]))
    f.sceneReads[1]!.resolve({ data: [] })
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.state.memories.value[0].id, 'retry')
    assert.deepEqual(f.mutations, [])
  } finally {
    f.close()
  }
})

test('rapid text edits debounce to one latest request, abort the old read and never prefetch', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 })
  const f = fixture()
  try {
    f.state.loaded.value = true
    const old = f.state.readPage()
    f.state.query.value = 'a'
    await nextTick()
    assert.equal(f.pending[0]!.options.signal.aborted, true)
    t.mock.timers.tick(100)
    f.state.query.value = 'ab'
    await nextTick()
    t.mock.timers.tick(100)
    f.state.query.value = 'abc'
    await nextTick()
    t.mock.timers.tick(249)
    assert.equal(f.pending.length, 1, 'no new request before the final debounce deadline')
    t.mock.timers.tick(1)
    assert.equal(f.pending.length, 2)
    assert.equal(f.pending[1]!.options.params.query.q, 'abc')
    f.pending[1]!.resolve(response([record('latest')]))
    await nextTick()
    f.pending[0]!.resolve(response([record('aborted')]))
    await old
    await nextTick()
    t.mock.timers.tick(10000)
    assert.equal(f.pending.length, 2, 'one latest-query request and no background prefetch')
    assert.equal(f.state.memories.value[0].id, 'latest')
  } finally {
    f.close()
    t.mock.timers.reset()
  }
})
