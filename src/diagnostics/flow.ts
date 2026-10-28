import { AsyncLocalStorage } from 'node:async_hooks'
import { randomUUID } from 'node:crypto'
import { performance } from 'node:perf_hooks'
import type { Log } from '../schema.ts'
import { sanitizeError } from './errors.ts'

export type FlowStatus =
  'started' | 'completed' | 'failed' | 'skipped' | 'cancelled' | 'cancel_requested' | 'partial'
export interface FlowContext {
  traceId: string
  spanId?: string
  turnId?: string
  sceneId?: string
  batchId?: string
  parentTraceId?: string
  linkedTraceIds?: string[]
}
export interface FlowDetails extends Partial<FlowContext> {
  reason?: string
  nextStep?: string
  counts?: Record<string, number>
  ruleIds?: string[]
  role?: string
  modelId?: string
  providerId?: string
  apiModel?: string
  toolName?: string
  action?: string
  outputKind?: string
  receiptState?: string
  mode?: string
}
export interface FlowWriter {
  log(
    level: Log['level'],
    module: string,
    code: string,
    context?: Partial<Omit<Log, 'id' | 'at' | 'level' | 'module' | 'code'>>,
  ): unknown
}
const context = new AsyncLocalStorage<{ context: FlowContext; writer?: FlowWriter }>()
export const currentFlowContext = (): Partial<FlowContext> => context.getStore()?.context ?? {}
export const currentFlowWriter = (): FlowWriter | undefined => context.getStore()?.writer
export function withFlowContext<T>(
  value: Partial<FlowContext>,
  fn: () => T,
  writer?: FlowWriter,
): T {
  return context.run(
    {
      context: {
        ...currentFlowContext(),
        ...value,
        traceId: value.traceId ?? currentFlowContext().traceId ?? randomUUID(),
      },
      writer: writer ?? currentFlowWriter(),
    },
    fn,
  )
}
const label = (value: unknown) =>
  typeof value === 'string' && /^[a-zA-Z0-9_.:/-]{1,128}$/.test(value) && !value.includes('://')
    ? value
    : undefined
function safeDetails(details: FlowDetails): FlowDetails {
  const result: FlowDetails = {}
  for (const key of [
    'traceId',
    'spanId',
    'turnId',
    'sceneId',
    'batchId',
    'parentTraceId',
    'reason',
    'nextStep',
    'role',
    'modelId',
    'providerId',
    'apiModel',
    'toolName',
    'action',
    'outputKind',
    'receiptState',
    'mode',
  ] as const) {
    const value = label(details[key])
    if (value !== undefined) result[key] = value
  }
  if (details.linkedTraceIds)
    result.linkedTraceIds = [
      ...new Set(
        details.linkedTraceIds.filter(
          (value) => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(value),
        ),
      ),
    ].slice(0, 60)
  if (details.ruleIds)
    result.ruleIds = [
      ...new Set(
        details.ruleIds.map(label).filter((value): value is string => value !== undefined),
      ),
    ].slice(0, 32)
  if (details.counts)
    result.counts = Object.fromEntries(
      Object.entries(details.counts)
        .filter(
          ([key, value]) =>
            /^[a-zA-Z][a-zA-Z0-9]{0,39}$/.test(key) &&
            typeof value === 'number' &&
            Number.isFinite(value) &&
            value >= 0,
        )
        .slice(0, 32)
        .map(([key, value]) => [key, Math.round(value * 100) / 100]),
    )
  return result
}
/** A span represents real work only. Call finish in every outcome; repeated finish is ignored. */
export function startFlow(
  writer: FlowWriter | undefined,
  stage: string,
  details: FlowDetails = {},
) {
  const safe = safeDetails(details)
  const parent = currentFlowContext()
  const scope: FlowContext = {
    ...parent,
    traceId: safe.traceId ?? parent.traceId ?? randomUUID(),
    spanId: randomUUID(),
  }
  for (const key of ['turnId', 'sceneId', 'batchId', 'parentTraceId'] as const)
    if (safe[key] !== undefined) scope[key] = safe[key]
  if (safe.linkedTraceIds) scope.linkedTraceIds = safe.linkedTraceIds
  const parentSpanId = parent.spanId
  const started = performance.now()
  let ended = false
  let cancellationRequested = false
  let removeAbort: (() => void) | undefined
  const emit = (status: FlowStatus, extra: FlowDetails, error?: unknown) => {
    const additional = safeDetails(extra)
    const merged = {
      ...safe,
      ...additional,
      ...(safe.counts || additional.counts
        ? { counts: { ...safe.counts, ...additional.counts } }
        : {}),
    }
    try {
      context.run({ context: scope, writer }, () =>
        writer?.log(
          status === 'failed'
            ? 'error'
            : ['partial', 'cancelled', 'cancel_requested'].includes(status)
              ? 'warn'
              : 'info',
          'flow',
          `flow.${label(stage) ?? 'operation'}.${status}`,
          {
            ...scope,
            ...merged,
            spanId: scope.spanId,
            parentSpanId,
            stage: label(stage) ?? 'operation',
            status,
            ...(status === 'started'
              ? {}
              : { durationMs: Math.max(0, Math.round((performance.now() - started) * 100) / 100) }),
            ...(error === undefined
              ? {}
              : sanitizeError(error, {
                  stage: label(stage),
                  operation: label(stage),
                  role: merged.role,
                  modelId: merged.modelId,
                  providerId: merged.providerId,
                  apiModel: merged.apiModel,
                  attempt: merged.counts?.attempt,
                  timeoutMs: merged.counts?.timeoutMs,
                  remainingCandidates: merged.counts?.remainingCandidates,
                })),
          },
        ),
      )
    } catch {
      /* Observability must never alter business work or cause a paid retry. */
    }
  }
  emit('started', {})
  const finish = (
    status: Exclude<FlowStatus, 'started' | 'cancel_requested'>,
    extra: FlowDetails = {},
    error?: unknown,
  ) => {
    if (ended) return
    ended = true
    removeAbort?.()
    emit(status, extra, error)
  }
  return {
    context: scope,
    run<T>(fn: () => T): T {
      return withFlowContext(scope, fn, writer)
    },
    finish,
    bindSignal(signal?: AbortSignal) {
      if (!signal || ended) return
      removeAbort?.()
      const abort = () => {
        if (ended || cancellationRequested) return
        cancellationRequested = true
        emit('cancel_requested', {
          reason: 'cancellation_requested',
          nextStep: 'wait_for_cleanup',
        })
      }
      removeAbort = () => signal.removeEventListener('abort', abort)
      if (signal.aborted) abort()
      else signal.addEventListener('abort', abort, { once: true })
    },
    get ended() {
      return ended
    },
  }
}
