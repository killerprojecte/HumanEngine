import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

test('actual chat page shows local messages before remote, retains them on failure and rejects stale group responses', async () => {
  const requests: Array<{ options: any; resolve: (value: any) => void }> = []
  const watchers: Array<() => void> = [],
    unmounts: Array<() => void> = []
  const globals: Record<string, unknown> = {
    useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
    useApi: () => ({
      client: {
        GET: (_path: string, options: any) =>
          new Promise((resolve) => requests.push({ options, resolve })),
      },
    }),
    ref: (value?: unknown) => ({ value }),
    computed: (read: () => unknown) => ({
      get value() {
        return read()
      },
    }),
    watch: (_source: unknown, callback: () => void) => watchers.push(callback),
    onMounted() {},
    onBeforeRouteLeave() {},
    onBeforeUnmount: (callback: () => void) => unmounts.push(callback),
    nextTick: () => Promise.resolve(),
    window: { removeEventListener() {} },
  }
  const old = new Map(
    Object.keys(globals).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  )
  for (const [key, value] of Object.entries(globals))
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true })
  const tick = () => new Promise((resolve) => setImmediate(resolve))
  const message = (id: string) => ({
    id,
    sentAt: '2026-01-01T00:00:00.000Z',
    parts: [],
    status: 'available',
  })
  const page = (items: any[], coverage = 'partial', reason = 'platform_history_window') => ({
    data: { items, coverage, reason },
  })
  try {
    const source = readFileSync(new URL('../app/pages/chat.vue', import.meta.url), 'utf8')
      .split('<script setup lang="ts">')[1]!
      .split('</script>')[0]!
      .replace(/^import .*$/gm, '')
    const compiled = ts.transpileModule(
      source +
        '\nexport { preview, history, historyLoading, historyError, mode, sceneId, historyReason, transcript, following }',
      { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } },
    ).outputText
    const component = await import(
      `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
    )
    component.mode.value = 'real'
    component.sceneId.value = 'group-a'
    const first = component.preview(true)
    assert.equal(requests.length, 2, 'local and platform requests start independently')
    requests[0]!.resolve(page([message('cached')], 'partial', 'local_history_window'))
    await tick()
    assert.deepEqual(
      component.history.value.map((m: any) => m.id),
      ['cached'],
    )
    assert.equal(
      component.historyLoading.value,
      true,
      'cached transcript is visible while platform is pending',
    )
    requests[1]!.resolve(page([message('cached')], 'unavailable'))
    await first
    assert.equal(component.history.value[0].id, 'cached')
    assert.equal(component.historyError.value, 'cwHistoryRefreshFailed')
    assert.equal(component.historyLoading.value, false)

    const stale = component.preview(true)
    component.sceneId.value = 'group-b'
    watchers[0]!()
    assert.equal(requests[2]!.options.signal.aborted, true)
    assert.equal(requests[3]!.options.signal.aborted, true)
    assert.deepEqual(component.history.value, [])
    assert.equal(requests[4]!.options.params.path.id, 'group-b')
    requests[5]!.resolve(page([message('remote-b')]))
    await tick()
    requests[4]!.resolve(page([message('late-local-b')]))
    requests[2]!.resolve(page([message('stale-local-a')]))
    requests[3]!.resolve(page([message('stale-remote-a')]))
    await stale
    await tick()
    assert.deepEqual(
      component.history.value.map((m: any) => m.id),
      ['remote-b'],
      'late local and previous group data cannot replace current platform response',
    )
    let anchorTop = 20
    const anchor = { getBoundingClientRect: () => ({ top: anchorTop, bottom: anchorTop + 40 }) }
    const pane = {
      scrollTop: 100,
      getBoundingClientRect: () => ({ top: 0 }),
      querySelectorAll: () => [anchor],
      contains: (element: unknown) => element === anchor,
    }
    component.transcript.value = pane
    component.following.value = false
    const anchored = component.preview()
    requests[6]!.resolve(page([message('older'), message('remote-b')]))
    // The render adds content above the retained keyed message before nextTick completes.
    queueMicrotask(() => {
      anchorTop = 70
    })
    await anchored
    await tick()
    assert.equal(
      pane.scrollTop,
      150,
      'refresh preserves the visible message offset while reading older messages',
    )
    const last = component.preview()
    unmounts[0]!()
    assert.equal(requests[7]!.options.signal.aborted, true)
    requests[7]!.resolve(page([message('after-unmount')]))
    await last
    assert.deepEqual(
      component.history.value.map((m: any) => m.id),
      ['older', 'remote-b'],
    )
  } finally {
    for (const [key, descriptor] of old) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete (globalThis as any)[key]
    }
  }
})
