import { parentPort, workerData } from 'node:worker_threads'
import { pathToFileURL } from 'node:url'
import { registerHooks } from 'node:module'
// Resolve only deployment-supplied exact module URLs, never a basename or suffix.
// All aliases and the bridge itself load this one source-derived SDK instance.
const sdkURL = 'humanengine:v1-model-protocols'
const sdkAliases = new Set(
  (workerData.modelProtocolModuleURLs ?? []).map((url) => new URL(url).href),
)
registerHooks({
  resolve(specifier, context, nextResolve) {
    let candidate
    try {
      candidate = /^(?:\.{1,2}\/|\/)/.test(specifier)
        ? new URL(specifier, context.parentURL).href
        : new URL(specifier).href
    } catch {}
    if (specifier === sdkURL || sdkAliases.has(candidate))
      return { url: sdkURL, shortCircuit: true }
    const resolved = nextResolve(specifier, context)
    return sdkAliases.has(resolved.url) ? { url: sdkURL, shortCircuit: true } : resolved
  },
  load(url, context, nextLoad) {
    return url === sdkURL
      ? {
          format: 'module',
          source: workerData.modelProtocolSDK,
          shortCircuit: true,
        }
      : nextLoad(url, context)
  },
})
const { ModelProtocolError } = await import(sdkURL)
const privateProtocolFailure = (error) => {
  const value = classifyProtocolFailure(error)
  if (!value) return undefined
  // Runtime mutations retain the source retry decision, but arbitrary status
  // strings/objects cannot smuggle provider URLs, response bodies or credentials.
  const result = {
    code: 'provider_request_failed',
    retryable: value.retryable === true,
  }
  if (Number.isSafeInteger(value.status)) {
    result.status = value.status
    if (value.status) result.code = `provider_http_${value.status}`
  }
  if (
    [
      'protocol_configuration_error',
      'provider_timeout',
      'memory_read_disabled',
      'memory_read_revoked',
      'personalization_disabled',
    ].includes(value.code)
  )
    result.code = value.code
  // Strings and objects are ignored by source retryDelay, not coerced. Preserve
  // nonfinite numbers privately without JSON's NaN/Infinity -> null conversion.
  if (typeof value.retryAfterMs === 'number') result.retryAfterMs = String(value.retryAfterMs)
  return result
}
// All console output is diagnostic; stdout belongs to the host protocol.
console.log = (...args) => console.error(...args)
const failureMessage = (error) => {
  try {
    if (typeof error === 'string' && error) return error
    if (typeof error?.message === 'string' && error.message) return error.message
  } catch {
    // Thrown values may themselves have throwing accessors.
  }
  return 'plugin_failure'
}
let controller = new AbortController()
let activationSerial = 0
const cleanups = [],
  callbacks = new Map(),
  invocations = new Map(),
  pendingContinuations = new Map(),
  continuations = new Map(),
  registrations = [],
  protocols = []
let setupConfigurationFailed = false
let activationGeneration=workerData.activation
let activePlugin
let sequence = 0,
  closing = false,
  disposing
const dispose = (reason, keepConfigurations = false) => {
  if (disposing) return disposing
  closing = true
  callbacks.clear()
  if (!keepConfigurations) pluginConfigurationOperations.clear()
  pendingContinuations.clear()
  continuations.clear()
  // Publish the promise before abort listeners resume setup. Both paths share it.
  disposing = Promise.resolve().then(async () => {
    const errors = []
    while (cleanups.length) {
      try {
        await cleanups.pop()()
      } catch (error) {
        errors.push(failureMessage(error))
      }
    }
    return errors
  })
  controller.abort(reason)
  invocations.clear()
  return disposing
}
const register = (kind, definition, handler, receiver) => {
  if (closing) throw Error('plugin_unloaded')
  const handle = String(++sequence)
  if (handler)
    callbacks.set(handle, {
      handler,
      kind,
      operation: definition.operation,
      name: definition.name,
      receiver: receiver ?? (kind === 'tool' || kind === 'sendHook' ? definition : undefined),
    })
  const { execute, handler: ignored, ...metadata } = definition
  registrations.push({ ...metadata, kind, handle })
  return handle
}
const nativeToolRequests = new Map()
let nativeToolSequence = 0
const toolRPC = (invocationId, signal, operation, args) => new Promise((resolve,reject) => {
 if(signal.aborted || closing) { reject(Error('plugin_disabled')); return }
 // Fail non-JSON payloads in this invocation, never in the shared control host.
 const wireArgs = JSON.parse(JSON.stringify(args))
 const requestId = `tool:${++nativeToolSequence}`
 const cancel = () => { nativeToolRequests.delete(requestId); reject(Error('plugin_disabled')) }
 const settle = (reply) => { signal.removeEventListener('abort',cancel); reply.failed ? reject(Error(reply.error)) : resolve(reply.result) }
 nativeToolRequests.set(requestId,settle)
 signal.addEventListener('abort',cancel,{once:true})
 parentPort.postMessage({event:'tool_rpc',activation:activationGeneration,requestId,invocationId,operation,args:wireArgs})
})
const toolStreams = new Map()
const cacheToolResult = async (invocationId, signal, source, options = {}) => {
 const token = `stream:${++nativeToolSequence}`
 const controller = new AbortController()
 const lifetime = AbortSignal.any([signal,controller.signal])
 toolStreams.set(token,controller)
 let iterator, completed = false
 try {
  await toolRPC(invocationId,lifetime,'cache_start',[token,{...options,encoding:options.encoding ?? (typeof source === 'string' ? 'utf8' : 'base64')}])
  const chunks = typeof source === 'string' || source instanceof Uint8Array ? [typeof source === 'string' ? Buffer.from(source) : source] : source
  iterator = Symbol.asyncIterator in chunks ? chunks[Symbol.asyncIterator]() : chunks[Symbol.iterator]()
  for (;;) {
   const item = await new Promise((resolve,reject) => {
    const abort = () => reject(Error('tool_result_cancelled'))
    lifetime.addEventListener('abort',abort,{once:true})
    if(lifetime.aborted) {lifetime.removeEventListener('abort',abort);abort();return}
    Promise.resolve().then(()=>{lifetime.throwIfAborted();return iterator.next()}).then(resolve,reject).finally(()=>lifetime.removeEventListener('abort',abort)).catch(()=>{})
   })
   if(item.done) {completed=true;break}
   if(!(item.value instanceof Uint8Array)) throw Error('invalid_tool_result_chunk')
   await toolRPC(invocationId,lifetime,'cache_chunk',[token,Buffer.from(item.value).toString('base64')])
  }
  return await toolRPC(invocationId,lifetime,'cache_finish',[token])
 } finally {
  toolStreams.delete(token)
  if(!completed) {
   try {if(source && typeof source === 'object' && typeof source.destroy === 'function') source.destroy()} catch {}
   try {void Promise.resolve(iterator?.return?.()).catch(()=>{})} catch {}
   try {void toolRPC(invocationId,signal,'cache_abort',[token]).catch(()=>{})} catch {}
  }
 }
}
let pendingAdapterStarts = null
const startRegisteredAdapters = async () => {
 if (!pendingAdapterStarts) throw Error('plugin_adapters_already_started')
 const starts = pendingAdapterStarts
 const owner = activationSerial, signal = controller.signal
 pendingAdapterStarts = null
 for (const start of starts) {
  if (closing || owner !== activationSerial || signal.aborted) throw Error('plugin_unloaded')
  await untilAbort(start(), AbortSignal.any([signal, AbortSignal.timeout(30000)]))
  if (closing || owner !== activationSerial || signal.aborted) throw Error('plugin_unloaded')
 }
}
parentPort.on('message', async (message) => {
  try {
    if (message.op === 'tool_stream_abort') {toolStreams.get(message.stream)?.abort('tool_result_cancelled');return}
    if (message.op === 'tool_response') {
      const settle = nativeToolRequests.get(message.requestId)
      nativeToolRequests.delete(message.requestId)
      settle?.(message)
      return
    }
    if (message.op === 'cancel_invoke') {
      invocations.get(message.invocationId)?.abort('caller_cancelled')
      invocations.delete(message.invocationId)
      pendingContinuations.delete(message.invocationId)
      return
    }
    if (message.op === 'continuation_status') {
      parentPort.postMessage({
        id: message.id,
        result: {
          pending: pendingContinuations.size,
          claimed: continuations.size,
        },
      })
      return
    }
    if (message.op === 'release_continuation') {
      pendingContinuations.delete(message.invocationId)
      continuations.delete(message.invocationId)
      return
    }
    if (message.op === 'dispose') {
      const errors = await dispose('plugin_disabled',message.keepConfigurations === true)
      parentPort.postMessage({
        id: message.id,
        result: { disposed: true, errors },
      })
      return
    }
    if(message.op==='reactivate'){
     if(!closing||pluginConfigurationOperations.size)throw Error('plugin_module_not_quiescent')
     const result=await activateModule(message.config,message.requestId,true,message.activation)
     parentPort.postMessage({id:message.id,result})
     return
    }
    if (closing && !(message.op === 'inspect_plugin' && ((['configuration_validate','configuration_prepare','configuration_release'].includes(message.input?.operation) && pluginConfigurationOperations.has(message.input.token)) || (message.input?.operation==='configuration_validate_first'&&message.input.currentModule===true)))) throw Error('plugin_unloaded')
    if (message.op === 'abort_plugin') {controller.abort('plugin_reconfigured');parentPort.postMessage({id:message.id,result:true});return}
    if (message.op === 'start_adapters') {
      await startRegisteredAdapters()
      if (closing || controller.signal.aborted) throw Error('plugin_unloaded')
      parentPort.postMessage({id:message.id,result:true})
      return
    }
    if (message.op === 'inspect_plugin') {
      parentPort.postMessage({id:message.id,result:inspectPluginConfiguration(activePlugin,message.input)})
      return
    }
    if (message.op === 'claim_continuation') {
      const pending = pendingContinuations.get(message.invocationId)
      if (!pending || pending.modelId !== message.modelId || pending.handle !== message.handle)
        throw Error('model_continuation_not_pending')
      pendingContinuations.delete(message.invocationId)
      // Read the opaque value only after Go has validated visible output. Never
      // structured-clone, JSON-encode, inspect or normalize this original value.
      const value = pending.result.continuation
      if (pending.signal.aborted) throw Error('plugin_call_cancelled')
      continuations.set(message.invocationId, {
        value,
        modelId: pending.modelId,
        handle: pending.handle,
      })
      parentPort.postMessage({ id: message.id, result: true })
      return
    }
    const callback = callbacks.get(message.handle)
    if (!callback) throw Error('plugin_callback_missing')
    const completion = callback.kind === 'modelMethod' && callback.operation === 'complete'
    if (completion !== (message.op === 'complete_model'))
      throw Error('typed_model_completion_required')
    if (callback.kind === 'adapterMethod' && message.op !== 'invoke_adapter')
      throw Error('typed_adapter_operation_required')
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
          ...(callback.kind === 'tool' && message.toolServices ? {send: (input) => toolRPC(message.id, signal, 'send', [input]),cacheResult:(source,options)=>cacheToolResult(message.id,signal,source,options)} : {}),
        }
      }
      if (callback.kind === 'adapterMethod') {
        args[2] = signal
        if (callback.operation === 'send' && message.beforeTransmit)
          args[3] = () =>
            adapterRPC(workerData.id + ':' + callback.name, 'beforeTransmit', [], message.id)
      }
      if (callback.kind === 'modelMethod') args[0] = { ...args[0], signal }
      if (completion) {
        const previous = continuations.get(message.previous)
        args[3] =
          previous?.modelId === message.modelId && previous.handle === message.handle
            ? previous.value
            : undefined
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
      if (callback.kind === 'tool' && message.toolServices) validateConfig(args[0], callback.receiver.inputSchema)
      let result
      try {
        // AsyncLocalStorage follows source-owned awaits, including history's
        // normalization/tombstone callbacks, without contaminating another call
        // or transport ingress scheduled outside this invocation.
        result = await adapterInvocation.run({ id: message.id, activation: activationGeneration }, () =>
          Reflect.apply(callback.handler, callback.receiver, args),
        )
      } catch (error) {
        if (callback.kind === 'adapterMethod' && callback.operation === 'send') {
          // Validation outside installAdapter's send try/catch is a rejection,
          // not an uncertain transport receipt. Preserve that separate boundary.
          parentPort.postMessage({
            id: message.id,
            result: { rejected: failureMessage(error) },
          })
          return
        }
        if (callback.kind !== 'modelMethod' || signal.aborted) throw error
        // Only adapter execution is inside legacy invokeProtocol's catch. Output
        // serialization and continuation getters below are outside that boundary.
        const protocolFailure = privateProtocolFailure(error)
        parentPort.postMessage({
          id: message.id,
          failed: true,
          error: 'model_protocol_failure',
          protocolFailure,
        })
        return
      }
      if (callback.kind === 'tool' && message.toolServices && callback.receiver.outputSchema) validateConfig(result,callback.receiver.outputSchema)
      // The source send wrapper owns settlement and returns an authoritative
      // receipt even when its transport synchronously aborts while settling.
      if (signal.aborted && !(callback.kind === 'adapterMethod' && callback.operation === 'send'))
        throw Error('plugin_call_cancelled')
      if (callback.kind === 'adapterMethod' && callback.operation === 'send') {
        const failure = outcomeError(result)
        parentPort.postMessage({
          id: message.id,
          result: {
            receipt: result,
            ...(failure === undefined ? {} : { failure: { message: failureMessage(failure) } }),
          },
        })
      } else if (completion) {
        if (!result || typeof result !== 'object' || Array.isArray(result))
          throw Error('invalid_provider_response')
        const visible = JSON.stringify({
          text: result.text,
          toolCalls: result.toolCalls,
          usage: result.usage,
        })
        if (signal.aborted) throw Error('plugin_call_cancelled')
        pendingContinuations.set(message.id, {
          result,
          signal,
          modelId: message.modelId,
          handle: message.handle,
        })
        try {
          parentPort.postMessage({ id: message.id, result: { visible } })
        } catch (error) {
          pendingContinuations.delete(message.id)
          throw error
        }
      } else parentPort.postMessage({ id: message.id, result })
    } finally {
      if (
        !['sendHook', 'modelMethod'].includes(callback.kind) ||
        !message.retainTurnSignal ||
        signal.aborted
      )
        invocations.delete(message.id)
      if (callback.kind === 'tool') invocation.abort('plugin_call_finished')
    }
  } catch (error) {
    parentPort.postMessage({
      id: message.id,
      failed: true,
      error: failureMessage(error),
      configurationFailure: message.op === 'reactivate' && setupConfigurationFailed,
    })
  }
})

async function activateModule(config,requestId,deferAdapterStart,generation=workerData.activation){
 activationGeneration=generation
 const plugin=activePlugin
 const activation=++activationSerial
 controller=new AbortController()
 const setupController=controller
 closing=false;disposing=undefined;setupConfigurationFailed=false
 cleanups.length=0;callbacks.clear();registrations.length=0;protocols.length=0
 adapterDefinitions.length=0;adapterInstances.clear()
 pendingContinuations.clear();continuations.clear()
 try {
  // Like the legacy manager, normalization validates but does not replace stored config.
  // Its separate store reads also isolate in-place validator mutations from setup.
  const validationConfig = structuredClone(config)
  try {
  const normalized = plugin.validateConfiguration?.(validationConfig)
  validateConfig(normalized ?? validationConfig, plugin.manifest.configSchema)
  } catch (error) {setupConfigurationFailed = true;throw error}
  const context = {
    requestId,
    config,
    signal: setupController.signal,
    registerTool: (tool) => {
      if (!tool.name.startsWith(`${workerData.id}.`) || registrations.some(r => r.kind === 'tool' && r.name === tool.name)) throw Error('invalid_plugin_tool')
      return register('tool', tool, tool.execute)
    },
    beforeSend: (hook) => register('sendHook', hook, hook.handler),
    registerAdminPage: (page) => {
      if (!/^[a-z0-9-]+$/.test(page.id) || !page.entry.startsWith(`/plugins/${workerData.id}/`) || page.entry.includes('..') || !/^\/[a-zA-Z0-9/_\-.]+$/.test(page.entry)) throw Error('invalid_admin_page')
      return register('page', page)
    },
    registerAdminAction: (name, handler) => {
      if (!/^[a-z0-9-]+$/.test(name)) throw Error('invalid_admin_action')
      return register('action', { name }, handler)
    },
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
              message: failureMessage(error),
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
    registerModelProtocol: (name, label, protocol) => {
      if (closing) throw Error('plugin_unloaded')
      if (
        !plugin.manifest.capabilities?.includes('model-protocol') ||
        !/^[a-z0-9-]{1,32}$/.test(name) ||
        protocols.some((entry) => entry.name === name)
      )
        throw Error('invalid_protocol_registration')
      protocols.push({ name, label, protocol })
    },
    registerAdapter: (name, factory) => {
      if (closing) throw Error('plugin_unloaded')
      if (
        !plugin.manifest.capabilities.includes('adapter') ||
        !/^[a-z0-9-]{1,32}$/.test(name) ||
        (workerData.id + ':' + name).length > 64 ||
        adapterDefinitions.some((entry) => entry.name === name)
      )
        throw Error('invalid_adapter_registration')
      adapterDefinitions.push({ name, factory })
    },
  }
  const contextGuard=()=>{if(closing||activationSerial!==activation||setupController.signal.aborted)throw Error('plugin_unloaded')}
  for(const name of ['registerTool','beforeSend','registerAdminPage','registerAdminAction','on','registerModelProtocol','registerAdapter']){
   const original=context[name]
   context[name]=(...args)=>{contextGuard();return original(...args)}
  }
  const onDispose=context.onDispose
  context.onDispose=(fn)=>{
   if(activationSerial!==activation||setupController.signal.aborted){
    void Promise.resolve().then(fn).catch(()=>parentPort.postMessage({event:'lifecycle',code:'late_dispose_failed',requestId}))
   }else onDispose(fn)
  }
  await untilAbort(Promise.resolve().then(()=>{contextGuard();return plugin.setup(context)}),setupController.signal)
  contextGuard()
  if (closing) throw Error('plugin_unloaded')
  // The legacy manager validates and snapshots adapters only after setup resolves.
  for (const { name, label, protocol } of protocols) {
    if (`${workerData.id}:${name}`.length > 160) throw Error('invalid_or_duplicate_protocol_id')
    if (
      !label ||
      label.length > 100 ||
      typeof protocol.complete !== 'function' ||
      ['embeddings', 'listModels'].some(
        (op) => protocol[op] !== undefined && typeof protocol[op] !== 'function',
      )
    )
      throw Error('invalid_protocol_definition')
    const handles = {}
    for (const operation of ['complete', 'embeddings', 'listModels'])
      if (protocol[operation])
        handles[operation] = register(
          'modelMethod',
          { name, operation },
          protocol[operation],
          protocol,
        )
    register('modelProtocol', { name, label, handles })
  }
  pendingAdapterStarts = installV1Adapters()
  if (!deferAdapterStart) await startRegisteredAdapters()

  return {manifest:{...plugin.manifest,apiVersion:1},registrations}
 }catch(error){
  if(activationSerial===activation)await dispose('plugin_setup_failed',true)
  throw error
 }
}
try {
 const loaded=await import(pathToFileURL(workerData.path).href)
 activePlugin=loaded.default??loaded.plugin
 if(!activePlugin?.manifest||typeof activePlugin.setup!=='function')throw Error('invalid_plugin_module')
 if(activePlugin.manifest.id!==workerData.id)throw Error('plugin_identity_mismatch')
 if(!/^[a-z0-9-]+$/.test(activePlugin.manifest.id))throw Error('invalid_plugin_id')
 parentPort.postMessage({event:'module_ready'})
 let result
 if(workerData.managementOnly){closing=true;result={manifest:{...activePlugin.manifest,apiVersion:1},registrations:[]}}
 else result=await activateModule(workerData.config,workerData.requestId,workerData.deferAdapterStart)
 parentPort.postMessage({ready:true,...result})
}catch(error){
 await dispose('plugin_setup_failed',workerData.deferAdapterStart === true)
 parentPort.postMessage({startup:true,configurationFailure:setupConfigurationFailed,failed:true,error:failureMessage(error)})
}
