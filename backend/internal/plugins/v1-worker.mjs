import { parentPort, workerData } from 'node:worker_threads'
import { pathToFileURL } from 'node:url'
// All console output is diagnostic; stdout belongs to the host protocol.
console.log = (...args) => console.error(...args)
const controller = new AbortController(),
  cleanups = [],
  callbacks = new Map(),
  invocations = new Map(),
  registrations = []
let sequence = 0,
  closing = false,
  disposing
const dispose = (reason) => {
  if (disposing) return disposing
  closing = true
  callbacks.clear()
  // Publish the promise before abort listeners resume setup. Both paths share it.
  disposing = Promise.resolve().then(async () => {
    const errors = []
    while (cleanups.length) {
      try {
        await cleanups.pop()()
      } catch (error) {
        errors.push(error.message)
      }
    }
    return errors
  })
  controller.abort(reason)
  invocations.clear()
  return disposing
}
const register = (kind, definition, handler) => {
  if (closing) throw Error('plugin_unloaded')
  const handle = String(++sequence)
  if (handler)
    callbacks.set(handle, {
      handler,
      kind,
      receiver: kind === 'tool' || kind === 'sendHook' ? definition : undefined,
    })
  const { execute, handler: ignored, ...metadata } = definition
  registrations.push({ ...metadata, kind, handle })
  return handle
}
parentPort.on('message', async (message) => {
  try {
    if (message.op === 'cancel_invoke') {
      invocations.get(message.invocationId)?.abort('caller_cancelled')
      invocations.delete(message.invocationId)
      return
    }
    if (message.op === 'dispose') {
      const errors = await dispose('plugin_disabled')
      parentPort.postMessage({ id: message.id, result: { disposed: true, errors } })
      return
    }
    if (closing) throw Error('plugin_unloaded')
    const callback = callbacks.get(message.handle)
    if (!callback) throw Error('plugin_callback_missing')
    const args = message.args ?? []
    const invocation = new AbortController()
    invocations.set(message.id, invocation)
    const signal = AbortSignal.any([controller.signal, invocation.signal])
    try {
      if (callback.kind === 'tool' || callback.kind === 'sendHook') {
        const context = args[1] ?? {}
        args[1] = {
          ...context,
          scene: Object.freeze({ ...context.scene }),
          dependencies: new Set(context.dependencies ?? []),
          validations: [],
          signal,
        }
      }
      if (callback.kind === 'event') {
        const freeze = (value) => {
          if (value && typeof value === 'object') {
            for (const child of Object.values(value)) freeze(child)
            Object.freeze(value)
          }
          return value
        }
        args[0] = freeze(args[0])
      }
      const result = await Reflect.apply(callback.handler, callback.receiver, args)
      if (signal.aborted) throw Error('plugin_call_cancelled')
      parentPort.postMessage({ id: message.id, result })
    } finally {
      if (callback.kind !== 'sendHook' || !message.retainTurnSignal || signal.aborted)
        invocations.delete(message.id)
      if (callback.kind === 'tool') invocation.abort('plugin_call_finished')
    }
  } catch (error) {
    parentPort.postMessage({ id: message.id, error: error.message })
  }
})

try {
  const loaded = await import(pathToFileURL(workerData.path).href)
  const plugin = loaded.default ?? loaded.plugin
  if (!plugin?.manifest || typeof plugin.setup !== 'function') throw Error('invalid_plugin_module')
  if (plugin.manifest.id !== workerData.id) throw Error('plugin_identity_mismatch')
  const config = workerData.config
  // Like the legacy manager, normalization validates but does not replace stored config.
  plugin.validateConfiguration?.(config)
  const context = {
    requestId: workerData.requestId,
    config,
    signal: controller.signal,
    registerTool: (tool) => register('tool', tool, tool.execute),
    beforeSend: (hook) => register('sendHook', hook, hook.handler),
    registerAdminPage: (page) => register('page', page),
    registerAdminAction: (name, handler) => register('action', { name }, handler),
    on: (type, handler) => register('event', { type }, handler),
    onDispose: (fn) => {
      if (!closing) cleanups.push(fn)
      else
        void Promise.resolve()
          .then(fn)
          .catch((error) =>
            parentPort.postMessage({
              event: 'log',
              code: 'late_dispose_failed',
              message: error.message,
            }),
          )
    },
    log: (code) => {
      if (!closing)
        parentPort.postMessage({
          event: 'log',
          code: /^[a-z0-9_.-]{1,100}$/i.test(code) ? code : 'plugin_event',
        })
    },
    // These capabilities must get full typed bridges before claiming V1 parity.
    registerModelProtocol: () => {
      throw Error('v1_model_protocol_bridge_pending')
    },
    registerAdapter: () => {
      throw Error('v1_adapter_bridge_pending')
    },
  }
  await plugin.setup(context)
  if (closing) throw Error('plugin_unloaded')
  parentPort.postMessage({
    ready: true,
    manifest: { ...plugin.manifest, apiVersion: 1 },
    registrations,
  })
} catch (error) {
  await dispose('plugin_setup_failed')
  parentPort.postMessage({ startupError: error.message })
}
