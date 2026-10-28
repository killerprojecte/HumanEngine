import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { chatMediaUrl, chatImageTypes, chatImageByteLimit } from '../app/utils/chatMedia.ts'

test('actual image component cancels stale loads, reloads after login, revokes blobs and safely closes preview', async () => {
  const pending: Array<(response: Response) => void> = []
  const signals: AbortSignal[] = []
  const watchers: Array<(value: unknown) => void> = []
  const mounts: Array<() => void> = [],
    unmounts: Array<() => void> = []
  const props = { mediaUrl: '/api/scenes/a/messages/b/media/0', mediaKind: 'image' }
  const authenticated = { value: true }
  const revoked: string[] = []
  let backs = 0
  const history = {
    state: {} as Record<string, unknown>,
    pushState(state: Record<string, unknown>) {
      this.state = state
    },
    replaceState(state: Record<string, unknown>) {
      this.state = state
    },
    back() {
      backs++
      // Browser navigation completes asynchronously in a later popstate event.
    },
  }
  const globals: Record<string, unknown> = {
    defineProps: () => props,
    useI18n: () => ({ t: (key: string) => key }),
    useApi: () => ({
      authenticated,
      authenticatedFetch: (_: string, init: RequestInit) => {
        signals.push(init.signal!)
        return new Promise<Response>((resolve, reject) => {
          const aborted = () => reject(new DOMException('Aborted', 'AbortError'))
          init.signal!.addEventListener('abort', aborted, { once: true })
          pending.push((response) => {
            init.signal!.removeEventListener('abort', aborted)
            resolve(response)
          })
        })
      },
    }),
    ref: (value?: unknown) => ({ value }),
    computed: (read: () => unknown) => ({
      get value() {
        return read()
      },
    }),
    watch: (_: unknown, callback: (value: unknown) => void) => watchers.push(callback),
    onMounted: (callback: () => void) => mounts.push(callback),
    onBeforeUnmount: (callback: () => void) => unmounts.push(callback),
    nextTick: (callback: () => unknown) => Promise.resolve().then(callback),
    window: { setTimeout, clearTimeout, addEventListener() {}, removeEventListener() {} },
    history,
    location: { href: 'http://fixture.local/chat' },
    imageFixtureHelpers: { chatMediaUrl, chatImageTypes, chatImageByteLimit },
  }
  const old = new Map(
    Object.keys(globals).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  )
  const revoke = URL.revokeObjectURL
  for (const [key, value] of Object.entries(globals))
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
  URL.revokeObjectURL = (url) => {
    revoked.push(url)
    revoke(url)
  }
  const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
  const image = () =>
    new Response(new Blob(['synthetic'], { type: 'image/png' }), {
      headers: { 'Content-Type': 'image/png' },
    })
  try {
    const source = readFileSync(new URL('../app/components/ChatImage.vue', import.meta.url), 'utf8')
      .split('<script setup lang="ts">')[1]!
      .split('</script>')[0]!
      .replace(
        /import .* from '~\/utils\/chatMedia'/,
        'const { chatMediaUrl, chatImageTypes, chatImageByteLimit } = (globalThis as any).imageFixtureHelpers',
      )
    const compiled = ts.transpileModule(
      source +
        '\nexport { load, source, decoded, failure, expanded, open, close, popped, imageError }',
      { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } },
    ).outputText
    const component = await import(
      `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
    )
    mounts[0]!()
    const newer = component.load()
    assert.equal(signals[0]!.aborted, true)
    pending[0]!(image())
    await tick()
    assert.equal(component.source.value, '', 'late response cannot resurrect an old resource')
    pending[1]!(image())
    await newer
    const first = component.source.value
    assert.match(first, /^blob:/)
    component.decoded.value = true
    component.open()
    assert.equal(component.expanded.value, true)
    component.close()
    component.close()
    assert.equal(backs, 1, 'double close must not navigate two history entries')
    history.state = {}
    component.popped()
    assert.equal(component.expanded.value, false)
    authenticated.value = false
    watchers[1]!(false)
    assert.ok(revoked.includes(first))
    assert.equal(component.source.value, '')
    authenticated.value = true
    watchers[1]!(true)
    assert.equal(pending.length, 3, 're-login starts a new authenticated fetch')
    pending[2]!(new Response('{}', { status: 410 }))
    await tick()
    assert.equal(component.failure.value, 'expired')
    const retry = component.load()
    pending[3]!(image())
    await retry
    const recovered = component.source.value
    component.imageError({ target: { src: recovered } })
    assert.ok(revoked.includes(recovered))
    assert.equal(component.failure.value, 'failed')
    const final = component.load()
    pending[4]!(image())
    await final
    component.decoded.value = true
    component.open()
    props.mediaUrl = '/api/scenes/a/messages/c/media/0'
    watchers[0]!(props.mediaUrl)
    assert.equal(backs, 1, 'scene/message navigation does not race an asynchronous history.back')
    assert.equal(history.state.chatImagePreview, undefined)
    pending[5]!(image())
    await tick()
    unmounts[0]!()
    await tick()
    assert.equal(component.source.value, '')
  } finally {
    URL.revokeObjectURL = revoke
    for (const [key, descriptor] of old) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    }
  }
})
