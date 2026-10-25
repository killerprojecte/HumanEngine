import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime } from '../src/runtime.ts'
import { PluginManager, validateConfig } from '../src/plugins.ts'

test('trusted plugin schema config, lifecycle cleanup, custom page registration and disabled actions', async () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store),
    plugins = new PluginManager(store, runtime)
  let disposed = 0
  try {
    await plugins.register({
      manifest: {
        id: 'fixture',
        name: 'Fixture',
        description: 'Integration fixture',
        version: '1.0.0',
        capabilities: ['tools', 'admin-page'],
        configSchema: {
          type: 'object',
          properties: {
            label: { type: 'string', minLength: 1 },
            secret: { type: 'string', secret: true },
          },
          required: ['label'],
          additionalProperties: false,
        },
      },
      setup(ctx) {
        ctx.onDispose(() => {
          disposed++
        })
        ctx.registerAdminPage({
          id: 'status',
          title: 'Status',
          entry: '/plugins/fixture/status.html',
        })
        ctx.registerAdminAction('status', async () => ({ label: ctx.config.label }))
        ctx.registerTool({
          name: 'fixture.read',
          description: 'Read current scope',
          effect: 'read',
          inputSchema: { type: 'object', properties: {}, additionalProperties: false },
          execute: async (_input, context) => ({ scene: context.scene.id }),
        })
      },
    })
    await assert.rejects(plugins.configure('fixture', { label: '', secret: 'secret' }))
    await plugins.configure('fixture', { label: 'Ready', secret: 'hidden' })
    assert.deepEqual(plugins.config('fixture'), { label: 'Ready' })
    await plugins.enable('fixture')
    assert.equal(plugins.list()[0]?.pages.length, 1)
    assert.deepEqual(await plugins.action('fixture', 'status', {}), { label: 'Ready' })
    await plugins.configure('fixture', { label: 'Updated' })
    assert.equal(disposed, 1)
    await plugins.disable('fixture')
    assert.equal(disposed, 2)
    assert.equal(plugins.list()[0]?.pages.length, 0)
    await assert.rejects(plugins.action('fixture', 'status', {}), /plugin_disabled/)
    assert.throws(() =>
      validateConfig(
        { extra: true },
        { type: 'object', properties: {}, additionalProperties: false },
      ),
    )
  } finally {
    await plugins.dispose()
    runtime.dispose()
    store.close()
  }
})

test('disable is terminal during pending setup and concurrent enables install exactly once', async () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store),
    plugins = new PluginManager(store, runtime)
  let release: () => void = () => {},
    started: () => void = () => {},
    setups = 0,
    cleanups = 0
  const entered = new Promise<void>((resolve) => {
      started = resolve
    }),
    gate = new Promise<void>((resolve) => {
      release = resolve
    })
  try {
    await plugins.register({
      manifest: {
        id: 'race',
        name: 'Race',
        description: 'fixture',
        version: '1',
        capabilities: [],
        configSchema: { type: 'object', properties: {}, additionalProperties: false },
      },
      async setup(ctx) {
        setups++
        ctx.onDispose(() => {
          cleanups++
        })
        started()
        await gate
        ctx.registerAdminPage({ id: 'late', title: 'Late', entry: '/plugins/race/late.html' })
      },
    })
    const first = plugins.enable('race'),
      rejected = assert.rejects(first, /plugin_disabled/)
    await entered
    await plugins.disable('race')
    await rejected
    release()
    await new Promise((resolve) => setTimeout(resolve, 5))
    assert.equal(plugins.list()[0]?.enabled, false)
    assert.equal(plugins.list()[0]?.pages.length, 0)
    assert.equal(cleanups, 1)
    await Promise.all([plugins.enable('race'), plugins.enable('race')])
    assert.equal(setups, 2)
    assert.equal(plugins.list()[0]?.pages.length, 1)
  } finally {
    release()
    await plugins.dispose()
    runtime.dispose()
    store.close()
  }
})

test('in-flight hooks receive plugin cancellation and cannot continue sends after disable', async () => {
  const store = new Store(':memory:')
  let hook: import('../src/runtime.ts').SendHook | undefined,
    release: () => void = () => {},
    started: () => void = () => {},
    observed: AbortSignal | undefined
  const gate = new Promise<void>((resolve) => {
      release = resolve
    }),
    entered = new Promise<void>((resolve) => {
      started = resolve
    })
  const runtime = {
    beforeSend(value: import('../src/runtime.ts').SendHook) {
      hook = value
      return () => {}
    },
    addExtension() {
      return () => {}
    },
  } as unknown as Runtime
  const plugins = new PluginManager(store, runtime)
  try {
    await plugins.register({
      manifest: {
        id: 'hook',
        name: 'Hook',
        description: 'fixture',
        version: '1',
        capabilities: [],
        configSchema: { type: 'object', properties: {} },
      },
      setup(ctx) {
        ctx.beforeSend({
          priority: 0,
          async handler(segments, context) {
            observed = context.signal
            started()
            await gate
            return { action: 'continue', segments }
          },
        })
      },
    })
    await plugins.enable('hook')
    const context = {
      scene: {
        id: 'scene',
        kind: 'group' as const,
        adapterId: 'a',
        accountId: '42',
        platformSceneId: '9',
      },
      mode: 'real' as const,
      turnId: 'turn',
      signal: new AbortController().signal,
      dependencies: new Set<string>(),
      validations: [],
    }
    const pending = hook!.handler([{ parts: [{ type: 'text', text: 'Hello' }] }], context)
    await entered
    await plugins.disable('hook')
    assert.equal(observed?.aborted, true)
    release()
    assert.deepEqual(await pending, { action: 'cancel', reason: 'plugin_disabled' })
  } finally {
    release()
    await plugins.dispose()
    store.close()
  }
})

test('nested and array secrets stay redacted and survive omitted-secret configuration saves', async () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store),
    plugins = new PluginManager(store, runtime)
  let current: Record<string, unknown> = {}
  try {
    await plugins.register({
      manifest: {
        id: 'secrets',
        name: 'Secrets',
        description: 'fixture',
        version: '1',
        capabilities: [],
        configSchema: {
          type: 'object',
          properties: {
            nested: {
              type: 'object',
              properties: {
                label: { type: 'string' },
                token: { type: 'string', secret: true, default: 'never-expose-default' },
              },
              required: ['label', 'token'],
            },
            accounts: {
              type: 'array',
              items: {
                type: 'object',
                properties: { key: { type: 'string', secret: true }, name: { type: 'string' } },
                required: ['key', 'name'],
              },
            },
          },
          required: ['nested', 'accounts'],
          additionalProperties: false,
        },
      },
      setup(ctx) {
        current = ctx.config
      },
    })
    await plugins.configure('secrets', {
      nested: { label: 'A', token: 'hidden-a' },
      accounts: [{ name: 'B', key: 'hidden-b' }],
    })
    assert.deepEqual(plugins.config('secrets'), {
      nested: { label: 'A' },
      accounts: [{ name: 'B' }],
    })
    assert.equal(JSON.stringify(plugins.list()).includes('never-expose-default'), false)
    await plugins.configure('secrets', {
      nested: { label: 'Changed' },
      accounts: [{ name: 'Changed B' }],
    })
    await plugins.enable('secrets')
    assert.deepEqual(current, {
      nested: { label: 'Changed', token: 'hidden-a' },
      accounts: [{ name: 'Changed B', key: 'hidden-b' }],
    })
    await Promise.all([
      plugins.configure('secrets', {
        nested: { label: 'Next', token: 'rotated' },
        accounts: [{ name: 'Changed B' }],
      }),
      plugins.configure('secrets', {
        nested: { label: 'Latest' },
        accounts: [{ name: 'Changed B' }],
      }),
    ])
    assert.equal((current.nested as { token: string }).token, 'rotated')
  } finally {
    await plugins.dispose()
    runtime.dispose()
    store.close()
  }
})
