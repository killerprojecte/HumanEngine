import type { Log } from '../schema.ts'
import {
  diagnosticLocale,
  flowCatalog,
  localizedError,
  presentationCatalog,
} from './flow-catalog.ts'

export interface ReadableErrorContext {
  operation?: string
  stage?: string
  role?: string
  modelId?: string
  providerId?: string
  apiModel?: string
  stageBudgetMs?: number
  maxAttempts?: number
  attempt?: number
  timeoutMs?: number
  remainingCandidates?: number
  retryable?: boolean
}
export interface ReadableError {
  errorType?: string
  errorCode?: string
  errorMessage?: string
  adviceCode?: string
  systemCode?: string
  httpStatus?: number
  stackFrames?: string[]
  errorContext?: ReadableErrorContext
}
export type ReadableLog = Partial<Log> &
  ReadableError & {
    rootCause?: ReadableError
    causes?: ReadableError[]
  }

/** Only serializer-approved fields enter this browser-safe presentation layer. */
export function diagnosticLines(log: ReadableLog, locale = 'zh-CN'): string[] {
  const language = diagnosticLocale(locale),
    w = presentationCatalog(language).words,
    catalog = flowCatalog(language)
  const root =
    log.rootCause ??
    (log.errorType || log.errorCode || log.errorMessage
      ? log
      : log.level === 'error' || log.status === 'failed'
        ? { errorType: 'UnknownError', errorCode: 'cause_not_captured' }
        : undefined)
  if (!root) return []
  const code =
    root.errorCode ??
    root.systemCode ??
    (root.httpStatus ? `provider_http_${root.httpStatus}` : undefined)
  const translated = localizedError(code, language)
  const context = {
    operation: log.operation,
    stage: log.stage ?? log.phase,
    role: log.role,
    modelId: log.modelId,
    providerId: log.providerId,
    apiModel: log.apiModel,
    ...log.errorContext,
    ...root.errorContext,
  }
  const result = [`${w.root}: ${translated.summary}${code ? ` [${code}]` : ''}`]
  if (root.errorType) result.push(`${w.type}=${root.errorType}`)
  if (root.errorMessage && root.errorMessage !== code && root.errorMessage !== translated.summary)
    result.push(`${w.technical}: ${root.errorMessage}`)
  if (root.httpStatus !== undefined) result.push(`HTTP=${root.httpStatus}`)
  if (root.systemCode) result.push(`${w.system}=${root.systemCode}`)
  if (context.operation) result.push(`${w.operation}=${context.operation}`)
  if (context.stage)
    result.push(`${w.stage}=${catalog.stageLabels[context.stage] ?? context.stage}`)
  if (context.role)
    result.push(
      `${w.role}=${(presentationCatalog(language).roles as Record<string, string>)[context.role] ?? context.role}`,
    )
  if (context.apiModel || context.modelId)
    result.push(`${w.model}=${context.apiModel ?? context.modelId}`)
  if (context.providerId) result.push(`${w.provider}=${context.providerId}`)
  if (context.attempt !== undefined) result.push(`${w.attempt}=${context.attempt}`)
  if (context.maxAttempts !== undefined) result.push(`${w.maxAttempts}=${context.maxAttempts}`)
  if (context.stageBudgetMs !== undefined)
    result.push(`${w.stageBudget}=${context.stageBudgetMs}ms`)
  if (context.timeoutMs !== undefined) result.push(`${w.timeout}=${context.timeoutMs}ms`)
  if (context.remainingCandidates !== undefined)
    result.push(`${w.remaining}=${context.remainingCandidates}`)
  if (context.retryable !== undefined)
    result.push(`${w.retryable}=${context.retryable ? w.yes : w.no}`)
  result.push(`${w.advice}: ${localizedError(root.adviceCode ?? code, language).advice}`)
  return result
}
export function readableLog(log: ReadableLog, locale = 'zh-CN'): string {
  const language = diagnosticLocale(locale),
    w = presentationCatalog(language).words
  const { stageLabels, statusLabels, reasonLabels, nextLabels, countLabels } = flowCatalog(language)
  const result = [
    log.stage && log.status
      ? `${statusLabels[log.status] ?? log.status}: ${stageLabels[log.stage] ?? log.stage}`
      : [log.module, log.code].filter(Boolean).join(' · '),
  ]
  if (log.role)
    result.push(
      `${w.role}=${(presentationCatalog(language).roles as Record<string, string>)[log.role] ?? log.role}`,
    )
  if (log.apiModel || log.modelId) result.push(`${w.model}=${log.apiModel ?? log.modelId}`)
  if (log.providerId) result.push(`${w.provider}=${log.providerId}`)
  if (log.toolName) result.push(`${w.tool}=${log.toolName}`)
  if (log.reason)
    result.push(
      `${w.reason}: ${reasonLabels[log.reason] ?? (/^provider_http_[1-5]\d\d$/.test(log.reason) ? localizedError(log.reason, language).summary : `${w.unknown} (${log.reason})`)}`,
    )
  for (const [key, value] of Object.entries(log.counts ?? {}))
    result.push(
      `${log.mode === 'simulation' && key === 'sentSegments' ? w.simulated : (countLabels[key] ?? key)}=${value}`,
    )
  if (log.outputKind)
    result.push(
      `${w.result}=${(presentationCatalog(language).outputs as Record<string, string>)[log.outputKind] ?? log.outputKind}`,
    )
  if (log.durationMs !== undefined) result.push(`${w.duration}=${log.durationMs}ms`)
  const error = diagnosticLines(log, language)
  if (error.length) result.push(...error)
  else {
    if (log.systemCode) result.push(`${w.system}=${log.systemCode}`)
    if (log.httpStatus !== undefined) result.push(`HTTP=${log.httpStatus}`)
  }
  if (log.nextStep) result.push(`${w.next}: ${nextLabels[log.nextStep] ?? log.nextStep}`)
  return result.filter(Boolean).join(language === 'en' ? '; ' : '；')
}
export function stdoutLog(log: Log): string {
  const w = presentationCatalog('zh-CN').words
  const correlation = [
    log.traceId ? `${w.trace}=${log.traceId}` : undefined,
    log.turnId ? `${w.turn}=${log.turnId}` : undefined,
    log.batchId ? `${w.batch}=${log.batchId}` : undefined,
  ]
    .filter(Boolean)
    .join(' ')
  return `${log.at} ${log.level.toUpperCase()} ${correlation ? `[${correlation}] ` : ''}${readableLog(log, 'zh-CN')} | ${JSON.stringify(log)}\n`
}
