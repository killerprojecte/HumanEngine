import { Worker } from 'node:worker_threads'
import { createInterface } from 'node:readline'
import { randomUUID } from 'node:crypto'
const plugins = new Map()
const adapterRequests = new Map()
const settleAdapterRequest = (key, response) => {
  const request = adapterRequests.get(key)
  if (!request) return
  adapterRequests.delete(key)
  request.port.postMessage(response)
  const state = new Int32Array(request.state)
  Atomics.store(state, 0, 1)
  Atomics.notify(state, 0)
  request.port.close()
}
const cancelAdapterRequests = (plugin, invocationId) => {
  for (const [key, request] of adapterRequests)
    if (request.plugin === plugin && (!invocationId || request.invocationId === invocationId))
      settleAdapterRequest(key, { failed: true, error: 'plugin_disabled' })
}
let workerSource = ''
let modelProtocolSDK = ''
let modelProtocolModuleURLs = []
const protocolFailures = new WeakMap()
const configurationFailures = new WeakSet()
const failureMessage = (error) => {
  try {
    if (typeof error === 'string' && error) return error
    if (typeof error?.message === 'string' && error.message) return error.message
  } catch {
    // Thrown values may themselves have throwing accessors.
  }
  return 'plugin_failure'
}
const send = (value) => process.stdout.write(JSON.stringify(value) + '\n')
const request = (plugin, message) =>
  new Promise((resolve, reject) => {
    if (plugin.exited) {
      reject(Error('plugin_worker_exited'))
      return
    }
    const onMessage = (reply) => {
      if (reply.id !== message.id) return
      cleanup()
      if (reply.failed) {
        const failure = Error(failureMessage(reply.error))
        if (reply.protocolFailure) protocolFailures.set(failure, reply.protocolFailure)
        if (reply.configurationFailure) configurationFailures.add(failure)
        reject(failure)
      } else resolve(reply.result)
    }
    const onExit = () => {
      cleanup()
      reject(Error('plugin_worker_exited'))
    }
    const cleanup = () => {
      plugin.worker.off('message', onMessage)
      plugin.worker.off('exit', onExit)
      plugin.requests.delete(message.id)
    }
    plugin.requests.set(message.id, (error) => {
      cleanup()
      reject(error)
    })
    plugin.worker.on('message', onMessage)
    plugin.worker.once('exit', onExit)
    plugin.worker.postMessage(message)
  })
for await (const line of createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
})) {
  let message
  try {
    message = JSON.parse(line)
  } catch {
    continue
  }
  // Do not await here: plugin lifecycle/control must progress independently of calls.
  void (async () => {
    try {
      if (message.op === 'initialize') {
        workerSource = message.source
        modelProtocolSDK = message.modelProtocolSDK
        modelProtocolModuleURLs = message.modelProtocolModuleURLs
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'tool_response' || message.op === 'tool_stream_abort') {
 const plugin = plugins.get(message.plugin)
 if (plugin && !plugin.exited) plugin.worker.postMessage(message)
 return
 }
 if (message.op === 'adapter_response') {
        settleAdapterRequest(message.plugin + '/' + message.requestId, message)
        return
      }
      if (message.op === 'status') {
        send({
          id: message.id,
          result: {
            workers: plugins.size,
            pendingRequests: [...plugins.values()].reduce((sum, p) => sum + p.requests.size, 0),
          },
        })
        return
      }
      if (message.op === 'enable') {
        if (plugins.has(message.plugin)) throw Error('plugin_already_loaded')
        const requestId = message.requestId || randomUUID()
        const worker = new Worker(workerSource, {
          eval: true,
          workerData: {
            id: message.pluginId,
            activation: message.activation,
            path: message.path,
            config: message.config,
            requestId,
            deferAdapterStart: message.deferAdapterStart === true,
            managementOnly: message.managementOnly === true,
            modelProtocolSDK,
            modelProtocolModuleURLs,
          },
          stdout: true,
          stderr: true,
        })
        worker.stdout.pipe(process.stderr)
        worker.stderr.pipe(process.stderr)
        worker.on('message', (event) => {
          if(event.event==='module_ready')send({event:'module_ready',plugin:message.plugin})
          if (event.event === 'tool_rpc') {
 if (plugin.closing) plugin.worker.postMessage({op:'tool_response',requestId:event.requestId,failed:true,error:'plugin_disabled'})
 else send({...event,plugin:message.plugin})
 }
 if (event.event === 'adapter_rpc') {
            const key = message.plugin + '/' + event.requestId
            adapterRequests.set(key, { ...event, plugin: message.plugin })
            // Cleanup is allowed after closing; all other native callbacks are fenced.
            if (plugin.closing && event.operation !== 'remove') {
              settleAdapterRequest(key, {
                failed: true,
                error: 'plugin_disabled',
              })
            } else {
              const { port, state, ...payload } = event
              send({ ...payload, plugin: message.plugin })
            }
          }
          if (event.event === 'log')
            process.stderr.write(
              JSON.stringify({
                timestamp: new Date().toISOString(),
                level: event.code === 'late_dispose_failed' ? 'error' : 'info',
                module: `plugin:${message.pluginId}`,
                code: event.code,
                requestId,
              }) + '\n',
            )
        })
        const plugin = {
          worker,
          activation: 0,
          closing: message.managementOnly === true,
          exited: false,
          requests: new Map(),
        }
        worker.once('exit', () => {
          plugin.exited = true
          cancelAdapterRequests(message.plugin)
          if (plugins.get(message.plugin) === plugin) plugins.delete(message.plugin)
          send({ event: 'worker_exit', plugin: message.plugin })
        })
        plugins.set(message.plugin, plugin)
        try {
          const ready = await new Promise((resolve, reject) => {
            worker.on('message', (m) => {
              if (m.ready) resolve(m)
              if (m.startup && m.failed) {const error=Error(failureMessage(m.error));if(m.configurationFailure)configurationFailures.add(error);reject(error)}
            })
            worker.once('error', reject)
            worker.once('exit', () => reject(Error('plugin_worker_exited')))
          })
          send({
            id: message.id,
            result: {
              manifest: ready.manifest,
              registrations: ready.registrations,
            },
          })
        } catch (error) {
          // Once disable/terminate owns the worker, startup failure must not
          // race its disposal acknowledgement with a second termination. The
          // source startup path can report its failure before a concurrently
          // waiting dispose request has posted the shared cleanup result.
          if (!plugin.closing && !message.deferAdapterStart) {
            plugin.closing = true
            if (plugins.get(message.plugin) === plugin) plugins.delete(message.plugin)
            cancelAdapterRequests(message.plugin)
            await worker.terminate()
          }
          throw error
        }
        return
      }
      const plugin = plugins.get(message.plugin)
      if (message.op === 'cancel_invoke') {
        cancelAdapterRequests(message.plugin, message.invocationId)
        plugin?.requests.get(message.invocationId)?.(Error('plugin_call_cancelled'))
        if (plugin && !plugin.exited)
          plugin.worker.postMessage({
            op: 'cancel_invoke',
            invocationId: message.invocationId,
          })
        // Acknowledge control admission without waiting on user callback code.
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'release_continuation') {
        if (plugin && !plugin.exited) plugin.worker.postMessage(message)
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'terminate') {
        if (plugin) {
          plugin.closing = true
          cancelAdapterRequests(message.plugin)
          plugins.delete(message.plugin)
          await plugin.worker.terminate()
        }
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'disable' || message.op === 'retire') {
        if (!plugin) {
          send({ id: message.id, result: true })
          return
        }
        plugin.closing = true
        cancelAdapterRequests(message.plugin)
        try {
          const disposal = await request(plugin, {
            id: message.id,
            op: 'dispose',
            keepConfigurations: message.op === 'retire',
          })
          for (const error of disposal.errors ?? [])
            process.stderr.write(
              JSON.stringify({
                timestamp: new Date().toISOString(),
                level: 'error',
                module: `plugin:${message.plugin}`,
                code: 'dispose_failed',
                message: String(error),
              }) + '\n',
            )
        } finally {
          if (message.op !== 'retire') {
            plugins.delete(message.plugin)
            await plugin.worker.terminate()
          }
        }
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'close') {
        await Promise.all([...plugins.values()].map((p) => p.worker.terminate()))
        plugins.clear()
        send({ id: message.id, result: true })
        process.exitCode = 0
        process.stdin.destroy()
        return
      }
      if(message.op==='reactivate'){
       if(!plugin||!plugin.closing)throw Error('plugin_module_not_quiescent')
       const activation=++plugin.activation
       plugin.closing=false
       message.requestId ||= randomUUID()
       try{send({id:message.id,result:await request(plugin,message)})}catch(error){if(plugin.activation===activation)plugin.closing=true;throw error}
       return
      }
      if (!plugin || (plugin.closing && !(message.op === 'inspect_plugin' && (['configuration_validate','configuration_prepare','configuration_release'].includes(message.input?.operation)||(message.input?.operation==='configuration_validate_first'&&message.input.currentModule===true))))) throw Error('plugin_not_loaded')
      send({ id: message.id, result: await request(plugin, message) })
    } catch (error) {
      send({
        id: message.id,
        failed: true,
        error: failureMessage(error),
        protocolFailure: protocolFailures.get(error),
 configurationFailure:configurationFailures.has(error),
      })
    }
  })()
}
await Promise.all([...plugins.values()].map((p) => p.worker.terminate()))
