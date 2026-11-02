import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { Readable, Duplex, pipeline } from 'node:stream'
import {
  createGunzip,
  createInflate,
  createInflateRaw,
  createBrotliDecompress,
  createZstdDecompress,
  constants,
} from 'node:zlib'
import type { OutboundLease } from './outbound.ts'

const flush = { flush: constants.Z_SYNC_FLUSH, finishFlush: constants.Z_SYNC_FLUSH }
function deflateDecoder() {
  return Duplex.from(async function* (source: AsyncIterable<Buffer>) {
    const iterator = source[Symbol.asyncIterator]()
    const first = await iterator.next()
    if (first.done) return
    const decoder = (first.value[0]! & 15) === 8 ? createInflate(flush) : createInflateRaw(flush)
    const input = Readable.from(
      (async function* () {
        yield first.value
        while (true) {
          const next = await iterator.next()
          if (next.done) break
          yield next.value
        }
      })(),
    )
    pipeline(input, decoder, () => {})
    try {
      for await (const chunk of decoder) yield chunk
    } finally {
      input.destroy()
      decoder.destroy()
      await iterator.return?.()
    }
  })
}
function decodedResponse(incoming: import('node:http').IncomingMessage): Readable {
  const codings =
    incoming.headers['content-encoding']
      ?.toLowerCase()
      .split(',')
      .map((value) => value.trim()) ?? []
  if (codings.length > 5) throw new Error('http_response_encoding_limit')
  if (codings.some((coding) => !['gzip', 'x-gzip', 'deflate', 'br', 'zstd'].includes(coding)))
    return incoming
  const decoders = codings.reverse().map((coding) =>
    coding === 'br'
      ? createBrotliDecompress({
          flush: constants.BROTLI_OPERATION_FLUSH,
          finishFlush: constants.BROTLI_OPERATION_FLUSH,
        })
      : coding === 'zstd'
        ? createZstdDecompress({ params: { [constants.ZSTD_d_windowLogMax]: 23 } })
        : coding === 'deflate'
          ? deflateDecoder()
          : createGunzip(flush),
  )
  if (!decoders.length) return incoming
  // pipeline propagates cancellation/errors in either direction. The caller caps decoded bytes.
  const output = decoders.at(-1)!
  pipeline([incoming, ...decoders], () => {})
  return output
}

export interface PreparedResponse {
  status: number
  ok: boolean
  body: ReadableStream<Uint8Array> | null
}

/** No fetch BodyInit copy: transfer ownership through the native writable callback/close. */
export function preparedHttpRequest(
  url: URL,
  headers: Record<string, string>,
  lease: OutboundLease,
  signal: AbortSignal,
): { response: Promise<PreparedResponse>; release(): void } {
  let settled = false,
    written = false,
    released = false
  const releaseOnce = () => {
    if (released) return
    released = true
    lease.release()
  }
  let request: ReturnType<typeof httpRequest> | undefined
  const release = () => {
    settled = true
    if (written) releaseOnce()
    else request?.destroy() // An early terminal response must not leave an upload blocked forever.
  }
  const writeDone = () => {
    written = true
    if (settled) releaseOnce()
  }
  const response = new Promise<PreparedResponse>((resolve, reject) => {
    let received = false
    try {
      // URL validation, authorization, cancellation and the final source gate run in the caller
      // immediately before this synchronous function. Standard TLS verification is unchanged.
      request = (url.protocol === 'https:' ? httpsRequest : httpRequest)(
        url,
        {
          method: 'POST',
          headers: { ...headers, 'Content-Length': String(lease.bytes.byteLength) },
          signal,
        },
        (incoming) => {
          received = true
          const status = incoming.statusCode ?? 0
          try {
            resolve({
              status,
              ok: status >= 200 && status < 300,
              body: Readable.toWeb(decodedResponse(incoming), {
                strategy: {
                  highWaterMark: 64 * 1024,
                  size: (chunk: Uint8Array) => chunk.byteLength,
                },
              }) as ReadableStream<Uint8Array>,
            })
          } catch (error) {
            incoming.destroy()
            reject(error)
          }
        },
      )
      request.once('error', reject)
      request.once('close', () => {
        writeDone()
        if (!received) reject(new Error('http_request_closed'))
      })
      const bytes = Buffer.from(lease.bytes.buffer, lease.bytes.byteOffset, lease.bytes.byteLength)
      request.write(bytes, (error) => {
        writeDone()
        if (error) reject(error)
      })
      request.end()
    } catch (error) {
      if (request) request.destroy()
      else writeDone()
      reject(error)
    }
  })
  return { response, release }
}
