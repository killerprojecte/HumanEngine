import { parentPort, workerData } from 'node:worker_threads'
import { ConfigReadConnection, type PublicationVerifyRequest } from './config-read.ts'
if (!parentPort) throw new Error('worker_parent_required')
const port = parentPort
try {
  const reader = new ConfigReadConnection(workerData.database)
  let closed = false
  const close = () => {
    if (closed) return
    closed = true
    reader.close()
  }
  port.on('close', close)
  port.on('message', (message) => {
    try {
      if (message.release !== undefined || message.cancel !== undefined) {
        reader.releasePublicationTicket(message.release ?? message.cancel)
        return
      }
      if (message.op === 'close') {
        close()
        port.postMessage({ id: message.id, result: null })
        return
      }
      if (closed) throw new Error('configuration_reader_closed')
      if (message.op === 'publication-info') {
        port.postMessage({ id: message.id, result: reader.publicationInfo() })
        return
      }
      if (message.op === 'publication-begin') {
        const result = reader.beginPublication(message.id)
        try {
          port.postMessage({ id: message.id, result })
        } catch (error) {
          reader.releasePublicationTicket(result.ticket)
          throw error
        }
        return
      }
      if (message.op === 'publication-abandon') {
        reader.abandonPublication(message.payload.ticket)
        port.postMessage({ id: message.id, result: null })
        return
      }
      if (message.op !== 'publication-verify') throw new Error('invalid_operation')
      const result = reader.verifyPublication(message.payload as PublicationVerifyRequest)
      // Both snapshots are released before sending, without a wire-size limit.
      port.postMessage({ id: message.id, result }, result.body ? [result.body.buffer] : [])
    } catch {
      try {
        port.postMessage({ id: message.id, error: true })
      } catch {
        try {
          close()
        } finally {
          port.close()
        }
      }
    }
  })
  try {
    port.postMessage({ ready: true })
  } catch (error) {
    close()
    throw error
  }
} catch {
  port.postMessage({ startupError: 'configuration_reader_unavailable' })
}
