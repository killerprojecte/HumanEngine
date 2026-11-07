import { Worker } from 'node:worker_threads'
import { timingSafeEqual } from 'node:crypto'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import type { ServerResponse } from 'node:http'
import { WorkerRpc, readBody } from '../../src/server/worker-rpc.ts'
import {
  createOwnerFailureHandler,
  createVerifierFailureHandler,
} from '../../src/server/owner-supervision.ts'
import { configReadPaths, type ConfigReadPath } from '../../src/server/config-read.ts'
import type { HttpResult } from '../../src/server/http-result.ts'
import {
  PublicationGate,
  type PublicationInfo,
  type PublicationPlan,
} from '../../src/server/publication-gate.ts'
import { requestIdFor } from '../../src/diagnostics/errors.ts'
import { startupDiagnostic, startupUnavailable } from '../../src/diagnostics/startup.ts'

const results = new WeakMap<
  Response,
  {
    plan: PublicationPlan
    signal: AbortSignal
    release: () => void
    attach: (target: ServerResponse) => void
  }
>()
const guarded = new WeakSet<Response>()
const responseLeases = new Set<() => void>()
let pending: Promise<{ owner: WorkerRpc; reader: WorkerRpc; gate: PublicationGate }> | undefined
let closing: Promise<void> | undefined
let startupOwner: WorkerRpc | undefined, startupReader: WorkerRpc | undefined
function worker(name: 'engine-owner' | 'config-reader', source: string, database: string) {
  // Production bundles live beside index.mjs, independent of Nitro's chunk layout.
  // Development retains source loading only; no loader is needed in the shipped image.
  const production = process.env.NODE_ENV === 'production'
  const url = production
    ? pathToFileURL(resolve('.output/server', `${name}.mjs`))
    : pathToFileURL(resolve('src/server', `${source}.ts`))
  return new WorkerRpc(
    production
      ? new Worker(url, { workerData: { database } })
      : new Worker(
          `import { register } from 'tsx/esm/api'; register(); await import(${JSON.stringify(url.href)})`,
          { eval: true, workerData: { database } },
        ),
    {
      onUnexpectedExit:
        name === 'engine-owner' ? createOwnerFailureHandler() : createVerifierFailureHandler(),
    },
  )
}
export function getEngine() {
  if (closing) return Promise.reject(new Error('engine_closing'))
  pending ??= (async () => {
    const database = process.env.HUMANENGINE_DB || 'data/humanengine.sqlite'
    const owner = worker('engine-owner', 'owner-worker', database)
    startupOwner = owner
    let reader: WorkerRpc | undefined, gate: PublicationGate | undefined
    try {
      await owner.ready
      if (closing) throw new Error('engine_closing')
      reader = worker('config-reader', 'config-worker', database)
      startupReader = reader
      await reader.ready
      if (closing) throw new Error('engine_closing')
      const info = await reader.call<PublicationInfo>('publication-info', null)
      if (closing) throw new Error('engine_closing')
      gate = new PublicationGate(database, info, reader)
      publicationGate = gate
      return { owner, reader, gate }
    } catch (error) {
      try {
        await reader?.close()
      } finally {
        try {
          await owner.close()
        } finally {
          await gate?.close()
        }
      }
      throw error
    }
  })()
  return pending
}
export async function initializeEngine() {
  if (process.env.HUMANENGINE_DB === ':memory:') {
    await (await import('../../src/server/owner-runtime.ts')).initializeEngine()
    return
  }
  try {
    await getEngine()
  } catch (error) {
    console.error('[HumanEngine] Startup blocked:', startupDiagnostic(error))
  }
}
export function engineUnavailable(request?: Request) {
  const requestId = request ? requestIdFor(request) : undefined
  return new Response(
    JSON.stringify({
      error: 'Request unavailable; outcome may be unknown. Do not automatically retry writes.',
      ...(requestId ? { requestId } : {}),
    }),
    {
      status: 503,
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'no-store',
        'X-Content-Type-Options': 'nosniff',
        ...(requestId ? { 'X-Request-ID': requestId } : {}),
      },
    },
  )
}
export async function handleEngineRequest(request: Request): Promise<Response> {
  // Literal in-memory development databases cannot be shared across independent readers.
  if (process.env.HUMANENGINE_DB === ':memory:')
    return (await import('../../src/server/owner-runtime.ts')).handleEngineRequest(request)
  const token = process.env.HUMANENGINE_ADMIN_TOKEN
  const supplied = Buffer.from(request.headers.get('Authorization')?.replace(/^Bearer /, '') ?? '')
  const expected = Buffer.from(token ?? '')
  if (!token || supplied.length !== expected.length || !timingSafeEqual(supplied, expected))
    return startupUnavailable(request, new Error('engine_startup_failed'), token)
  let runtime: Awaited<ReturnType<typeof getEngine>>
  try {
    runtime = await getEngine()
  } catch (error) {
    return startupUnavailable(request, error, token)
  }
  const path = new URL(request.url).pathname
  const independentRead =
    request.method === 'GET' && configReadPaths.includes(path as ConfigReadPath)
  let retained = false
  let releaseRpc: (() => void) | undefined
  try {
    let result: HttpResult
    if (independentRead) {
      // Project pure configuration only inside the final pinned read snapshot.
      // There is no early body/head that can become stale during the IPC handshake.
      result = {
        status: 200,
        headers: [
          ['Content-Type', 'application/json'],
          ['X-Request-ID', requestIdFor(request)],
          ['Cache-Control', 'no-store'],
          ['X-Content-Type-Options', 'nosniff'],
        ],
        body: new Uint8Array(),
      }
    } else {
      const body = await readBody(request.body, { signal: request.signal })
      result = await runtime.owner.call<HttpResult>(
        'fetch',
        {
          requestId: requestIdFor(request),
          url: request.url,
          method: request.method,
          headers: [...request.headers],
          body,
        },
        { signal: request.signal, lease: true },
      )
      releaseRpc = () => runtime.owner.releaseResult(result)
    }
    if ((!independentRead && !result.observedHead) || request.signal.aborted)
      return engineUnavailable(request)
    // Opaque transport placeholder: only publishEngineResponse may emit its bound bytes.
    const response = new Response(null, { status: result.status, headers: result.headers })
    const cancellation = new AbortController()
    let released = false
    let transport: ServerResponse | undefined
    const cancel = () => {
      cancellation.abort()
      transport?.destroy()
      release()
    }
    const release = () => {
      if (released) return
      released = true
      results.delete(response)
      responseLeases.delete(cancel)
      request.signal.removeEventListener('abort', cancel)
      transport?.off('finish', release)
      transport?.off('close', cancel)
      transport?.off('error', cancel)
      releaseRpc?.()
    }
    retained = true
    guarded.add(response)
    results.set(response, {
      plan: { result, ...(independentRead ? { path: path as ConfigReadPath } : {}) },
      signal: cancellation.signal,
      release,
      attach: (target) => {
        transport = target
        target.once('finish', release)
        target.once('close', cancel)
        target.once('error', cancel)
      },
    })
    responseLeases.add(cancel)
    request.signal.addEventListener('abort', cancel, { once: true })
    return response
  } catch {
    return engineUnavailable(request)
  } finally {
    if (!retained) {
      releaseRpc?.()
    }
  }
}
/** Publishes only after the host owns the final authority read lock. */
export async function publishEngineResponse(
  response: Response,
  target: ServerResponse,
): Promise<boolean> {
  const entry = results.get(response)
  if (!entry) {
    if (guarded.has(response)) throw new Error('response_lease_expired')
    return false
  }
  // Consume before enqueueing bytes; a second publication must never succeed.
  results.delete(response)
  try {
    entry.attach(target)
    if (!publicationGate) throw new Error('response_proof_missing')
    await publicationGate.publish(entry.plan, target, entry.signal)
    return true
  } catch (error) {
    entry.release()
    throw error
  }
}
let publicationGate: PublicationGate | undefined
export function closeEngine(): Promise<void> {
  if (closing) return closing
  closing = Promise.resolve().then(async () => {
    try {
      if (process.env.HUMANENGINE_DB === ':memory:') {
        await (await import('../../src/server/owner-runtime.ts')).closeEngine()
        return
      }
      for (const release of responseLeases) release()
      // Reach workers immediately, not after potentially unbounded initialization.
      // getEngine checks closing before creating the next worker or publication gate.
      const stopped = await Promise.allSettled([
        publicationGate?.close(),
        startupReader?.close(),
        startupOwner?.close(),
      ])
      try {
        const runtime = await pending?.catch(() => undefined)
        await runtime?.gate.close()
      } finally {
        if (stopped.some((result) => result.status === 'rejected'))
          console.error('[HumanEngine] Worker shutdown cleanup failed')
      }
    } finally {
      pending = undefined
      publicationGate = undefined
      startupOwner = undefined
      startupReader = undefined
      closing = undefined
    }
  })
  return closing
}
