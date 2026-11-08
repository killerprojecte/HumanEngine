// This RPC preserves the synchronous AdapterContext API. A transferred MessagePort
// carries the unbounded response; the shared word is only a wake-up flag, never a
// payload buffer or size cap. The host event loop remains free during Atomics.wait.
import { MessageChannel, receiveMessageOnPort } from 'node:worker_threads'
import { AsyncLocalStorage } from 'node:async_hooks'
const adapterInvocation = new AsyncLocalStorage()
const adapterDefinitions = []
const adapterInstances = new Map()
let adapterSequence = 0
const adapterRPC = (
  adapterId,
  operation,
  args,
  invocationId = adapterInvocation.getStore()?.id,
  activation = adapterInvocation.getStore()?.activation ?? activationGeneration,
) => {
  const { port1, port2 } = new MessageChannel()
  const state = new Int32Array(new SharedArrayBuffer(4))
  const requestId = String(++adapterSequence)
  parentPort.postMessage(
    {
      event: 'adapter_rpc',
      activation,
      requestId,
      adapterId,
      operation,
      args,
      invocationId,
      state: state.buffer,
      port: port2,
    },
    [port2],
  )
  try {
    while (Atomics.load(state, 0) === 0) Atomics.wait(state, 0, 0)
    const response = receiveMessageOnPort(port1)?.message
    if (!response) throw Error('adapter_bridge_response_missing')
    if (response.failed) {
      // Source receive/recall become no-ops after disposal. A native retirement
      // can race the last synchronous request before the dispose frame arrives.
      if (response.error === 'plugin_disabled' && ['receive', 'recall'].includes(operation)) return
      throw Error(response.error || 'adapter_service_failed')
    }
    return response.result
  } finally {
    port1.close()
  }
}
const installV1Adapters = () => {
  const starts = []
  const owner = activationSerial, signal = controller.signal, generation = activationGeneration
  const guard = () => { if (owner !== activationSerial || signal.aborted || closing) throw Error("plugin_unloaded") }
  for (const { name, factory } of adapterDefinitions) {
    if (closing) throw Error('plugin_unloaded')
    const id = workerData.id + ':' + name
    const ownedRPC = (id,operation,args) => { guard(); return adapterRPC(id,operation,args,undefined,generation) }
    let installed
    const ownedHandles = new Set()
    const remove = () => {
      const adapter = adapterInstances.get(id)
      if (!adapter || owner !== activationSerial) return
      adapterInstances.delete(id)
      for (const handle of ownedHandles) callbacks.delete(handle)
      for (let i = registrations.length - 1; i >= 0; i--)
        if (ownedHandles.has(registrations[i].handle)) registrations.splice(i, 1)
      // Cleanup is lease-owned, never tied to the invocation that failed.
      try {
        adapterRPC(id, 'remove', [], null, generation)
      } finally {
        adapter.dispose()
      }
    }
    installed = installAdapter(
      id,
      (context) =>
        factory({
          ...context,
          // Ingress belongs to the transport lease even when a transport emits it
          // from a send callback. Its source validation/tombstone reads must share
          // that lease scope, rather than inheriting the outbound call's lifetime.
          receive: (...args) => adapterInvocation.exit(() => context.receive(...args)),
        }),
      {
        addAdapter(adapterId, adapter) {
          guard()
          // Ownership starts when source gives us its wrapper, before any
          // fallible native install or handle registration. If installAdapter
          // throws before returning, this fallback still disposes exactly once.
          adapterInstances.set(adapterId, adapter)
          cleanups.push(() => (installed ? installed.dispose() : remove()))
          const handles = {}
          for (const operation of ['send', 'history', 'getMessage']) {
            handles[operation] = register(
              'adapterMethod',
              { name, operation },
              adapter[operation],
              adapter,
            )
            ownedHandles.add(handles[operation])
          }
          handles.status = register(
            'adapterMethod',
            { name, operation: 'status' },
            () => adapter.status,
          )
          ownedHandles.add(handles.status)
          adapterRPC(adapterId, 'install', [handles], null, generation)
          ownedHandles.add(register('adapter', { name, handles }))
        },
        removeAdapter: remove,
        ingest(message, scene) {
          ownedRPC(id, 'receive', [scene, message])
        },
        edit(message, scene, change) {
          return ownedRPC(id, 'edit', [scene, message, change])
        },
        recall(sceneId, messageId) {
          ownedRPC(id, 'recall', [sceneId, messageId])
        },
      },
      {
        recalled(sceneId, messageId) {
          return ownedRPC(id, 'isRecalled', [sceneId, messageId])
        },
      },
      signal,
      {
        media(assetId, sceneId) {
          return ownedRPC(id, 'media', [assetId, sceneId])
        },
      },
    )
    starts.push(installed.start)
  }
  return starts
}
