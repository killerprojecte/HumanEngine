import { Worker } from 'node:worker_threads'
import { createInterface } from 'node:readline'
import { randomUUID } from 'node:crypto'
const plugins = new Map()
let workerSource = ''
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
      reply.error ? reject(Error(reply.error)) : resolve(reply.result)
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
for await (const line of createInterface({ input: process.stdin, crlfDelay: Infinity })) {
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
        send({ id: message.id, result: true })
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
        const requestId = message.requestId ?? randomUUID()
        const worker = new Worker(workerSource, {
          eval: true,
          workerData: {
            id: message.pluginId,
            path: message.path,
            config: message.config,
            requestId,
          },
          stdout: true,
          stderr: true,
        })
        worker.stdout.pipe(process.stderr)
        worker.stderr.pipe(process.stderr)
        worker.on('message', (event) => {
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
        const plugin = { worker, closing: false, exited: false, requests: new Map() }
        worker.once('exit', () => {
          plugin.exited = true
        })
        plugins.set(message.plugin, plugin)
        try {
          const ready = await new Promise((resolve, reject) => {
            worker.on('message', (m) => {
              if (m.ready) resolve(m)
              if (m.startupError) reject(Error(m.startupError))
            })
            worker.once('error', reject)
            worker.once('exit', () => reject(Error('plugin_worker_exited')))
          })
          send({
            id: message.id,
            result: { manifest: ready.manifest, registrations: ready.registrations },
          })
        } catch (error) {
          plugins.delete(message.plugin)
          await worker.terminate()
          throw error
        }
        return
      }
      const plugin = plugins.get(message.plugin)
      if (message.op === 'cancel_invoke') {
        plugin?.requests.get(message.invocationId)?.(Error('plugin_call_cancelled'))
        if (plugin && !plugin.exited)
          plugin.worker.postMessage({ op: 'cancel_invoke', invocationId: message.invocationId })
        // Acknowledge control admission without waiting on user callback code.
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'terminate') {
        if (plugin) {
          plugin.closing = true
          plugins.delete(message.plugin)
          await plugin.worker.terminate()
        }
        send({ id: message.id, result: true })
        return
      }
      if (message.op === 'disable') {
        if (!plugin) {
          send({ id: message.id, result: true })
          return
        }
        plugin.closing = true
        try {
          const disposal = await request(plugin, { id: message.id, op: 'dispose' })
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
          plugins.delete(message.plugin)
          await plugin.worker.terminate()
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
      if (!plugin || plugin.closing) throw Error('plugin_not_loaded')
      send({ id: message.id, result: await request(plugin, message) })
    } catch (error) {
      send({ id: message.id, error: error.message })
    }
  })()
}
await Promise.all([...plugins.values()].map((p) => p.worker.terminate()))
