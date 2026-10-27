import { randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { isAbsolute, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const requestIds = new WeakMap<Request, string>()
export function requestIdFor(request: Request): string {
  let value = requestIds.get(request)
  if (!value) {
    value = randomUUID()
    requestIds.set(request, value)
  }
  return value
}
const errorTypes = new Set([
  'Error',
  'TypeError',
  'RangeError',
  'SyntaxError',
  'ReferenceError',
  'URIError',
  'AggregateError',
  'AbortError',
  'TimeoutError',
  'OneBotError',
  'PluginStartupError',
  'PluginConfigurationError',
  'SqliteError',
  'ZodError',
])
const systemCodes = new Set([
  'ECONNREFUSED',
  'ECONNRESET',
  'ENOTFOUND',
  'EAI_AGAIN',
  'EHOSTUNREACH',
  'ENETUNREACH',
  'ETIMEDOUT',
  'EADDRINUSE',
  'EADDRNOTAVAIL',
  'EACCES',
  'EPERM',
  'EPIPE',
  'CERT_HAS_EXPIRED',
  'DEPTH_ZERO_SELF_SIGNED_CERT',
  'ERR_TLS_CERT_ALTNAME_INVALID',
  'UNABLE_TO_VERIFY_LEAF_SIGNATURE',
  'SELF_SIGNED_CERT_IN_CHAIN',
  'UND_ERR_CONNECT_TIMEOUT',
])
export interface ErrorCauseDiagnostic {
  errorType: string
  systemCode?: string
  httpStatus?: number
  stackFrames: string[]
}
export interface ErrorDiagnostic extends ErrorCauseDiagnostic {
  causes: ErrorCauseDiagnostic[]
}
function own(value: object, key: string): unknown {
  // Do not invoke getters or arbitrary toJSON/toString methods while handling failure.
  try {
    return Object.getOwnPropertyDescriptor(value, key)?.value
  } catch {
    return undefined
  }
}
function errorType(value: object): string {
  const ownName = own(value, 'name')
  if (typeof ownName === 'string' && errorTypes.has(ownName)) return ownName
  if (value instanceof TypeError) return 'TypeError'
  if (value instanceof SyntaxError) return 'SyntaxError'
  if (value instanceof RangeError) return 'RangeError'
  if (value instanceof Error) return 'Error'
  return 'UnknownError'
}
const nativeStackGetter = Object.getOwnPropertyDescriptor(new Error(), 'stack')?.get
function safeStack(value: object): unknown {
  try {
    const descriptor = Object.getOwnPropertyDescriptor(value, 'stack')
    if (descriptor && 'value' in descriptor) return descriptor.value
    // Node 24 exposes V8's lazily materialized stack through a native own getter.
    // Invoke only that exact captured getter, never a caller-defined accessor.
    if (value instanceof Error && descriptor?.get === nativeStackGetter && nativeStackGetter) {
      // V8 formats the first line through name/message. Check inherited values too,
      // so materializing a native stack cannot call application-defined accessors.
      for (const key of ['name', 'message']) {
        let current: object | null = value
        let depth = 0
        while (current && depth++ < 12) {
          const field = Object.getOwnPropertyDescriptor(current, key)
          if (field) {
            if (
              !('value' in field) ||
              (field.value !== undefined && typeof field.value !== 'string')
            )
              return undefined
            break
          }
          current = Object.getPrototypeOf(current)
        }
        if (current && depth > 12) return undefined
      }
      return nativeStackGetter.call(value)
    }
  } catch {
    /* Uninspectable failures still retain a safe error category. */
  }
  return undefined
}
function frames(value: unknown): string[] {
  if (typeof value !== 'string') return []
  const root = resolve(process.cwd())
  const result: string[] = []
  for (const line of value.slice(0, 16000).split('\n').slice(1, 40)) {
    const match = /^\s*at (?:[^\n()]* \()?((?:file:\/\/)?\/[^()\n]+):(\d{1,7}):(\d{1,7})\)?$/.exec(
      line,
    )
    if (!match) continue
    let file: string
    try {
      file = match[1]!.startsWith('file:') ? fileURLToPath(match[1]!) : match[1]!
    } catch {
      continue
    }
    const path = relative(root, resolve(file))
    // Only existing application/runtime source paths; never arbitrary URLs, absolute
    // deployment paths, function names or exception messages supplied by an upstream.
    if (
      isAbsolute(path) ||
      path.startsWith('..') ||
      !/^[A-Za-z0-9_./-]{1,180}\.(?:[cm]?js|ts)$/.test(path) ||
      !existsSync(resolve(root, path))
    )
      continue
    result.push(`${path}:${match[2]}:${match[3]}`)
    if (result.length === 8) break
  }
  return result
}
function describe(value: object): ErrorCauseDiagnostic {
  const details = own(value, 'details')
  const detailObject = details && typeof details === 'object' ? details : undefined
  const rawCode =
    own(value, 'code') ??
    own(value, 'systemCode') ??
    (detailObject ? own(detailObject, 'systemCode') : undefined)
  const status =
    own(value, 'httpStatus') ?? (detailObject ? own(detailObject, 'httpStatus') : undefined)
  return {
    errorType: errorType(value),
    ...(typeof rawCode === 'string' && systemCodes.has(rawCode) ? { systemCode: rawCode } : {}),
    ...(typeof status === 'number' && Number.isInteger(status) && status >= 100 && status <= 599
      ? { httpStatus: status }
      : {}),
    stackFrames: frames(safeStack(value)),
  }
}
/** Structural error evidence only. Messages, URLs, headers and payloads are never copied. */
export function sanitizeError(error: unknown): ErrorDiagnostic {
  if (!error || typeof error !== 'object')
    return { errorType: 'UnknownError', stackFrames: [], causes: [] }
  const result: ErrorDiagnostic = { ...describe(error), causes: [] }
  const seen = new Set<object>([error])
  let next = own(error, 'cause')
  while (next && typeof next === 'object' && !seen.has(next) && result.causes.length < 4) {
    seen.add(next)
    result.causes.push(describe(next))
    next = own(next, 'cause')
  }
  return result
}
export function requestOperation(method: string, route: string | undefined): string {
  const verb = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'HEAD'].includes(method)
    ? method
    : 'OTHER'
  // Hono's matched route template, never request URL/path/query or caller headers.
  return `${verb} ${route && /^\/api(?:\/[A-Za-z0-9_{}:*.-]+)*$/.test(route) && route.length <= 160 ? route : '/api/*'}`
}
