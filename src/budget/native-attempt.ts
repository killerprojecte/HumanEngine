import { safeBudgetFailureCode } from './errors.ts'
import { AsyncLocalStorage } from 'node:async_hooks'
import { randomUUID } from 'node:crypto'
import type { CostUsage, ModelCostPrices } from '../model-cost.ts'
import type { BudgetEstimate, BudgetLane, BudgetPolicy, InputPriceCategory } from './contracts.ts'
import { BudgetLedger } from './ledger.ts'
import { priceKeys } from './estimate.ts'
import { effectiveModelPriority } from '../scheduler.ts'

export interface BudgetContext {
  turnId: string
  lane: BudgetLane
}
const context = new AsyncLocalStorage<BudgetContext>()
const requestContext = new AsyncLocalStorage<{ id: string; attempt: number }>()
export function withBudgetRequest<T>(task: () => T): T {
  return requestContext.run({ id: randomUUID(), attempt: 0 }, task)
}
export function withBudgetContext<T>(value: BudgetContext, task: () => T): T {
  const outer = context.getStore()
  const rank = { foreground: 0, realtime: 1, maintenance: 2 }
  return context.run(
    { ...value, lane: outer && rank[outer.lane] > rank[value.lane] ? outer.lane : value.lane },
    task,
  )
}
/** Explicit trusted work context chooses its lane; outer background scopes can only demote it. */
export function currentBudgetPriority(defaultPriority: number): number {
  const active = context.getStore()
  const priorities = { foreground: 0, realtime: 1, maintenance: 2 }
  return effectiveModelPriority(active ? priorities[active.lane] : defaultPriority)
}
export interface NativeAttemptDescriptor {
  providerId: string
  modelId: string
  protocol: string
  operation: 'complete' | 'embedding'
  role: string
  prices: ModelCostPrices
  opaqueInput?: boolean
}
export interface BudgetProviderContract {
  version: string
  outputCapHonored: boolean
  inputCategories: InputPriceCategory[]
}
export type BudgetEstimator = (input: {
  descriptor: Readonly<NativeAttemptDescriptor>
  body: unknown
  outputCap: number
  contract: BudgetProviderContract
}) => BudgetEstimate
export class BudgetAdmissionError extends Error {
  readonly code: string
  constructor(code: unknown) {
    const safeCode = safeBudgetFailureCode(code)
    super(safeCode)
    this.code = safeCode
  }
}
export interface NativeBudgetOptions {
  ledger: BudgetLedger
  policy: () => BudgetPolicy
  mode: 'strict' | 'best_effort'
  maxOutputTokens: number
  estimator?: BudgetEstimator
  contract?: (descriptor: Readonly<NativeAttemptDescriptor>) => BudgetProviderContract | undefined
}
export class NativeBudgetGate {
  private readonly options: NativeBudgetOptions
  constructor(options: NativeBudgetOptions) {
    if (!Number.isSafeInteger(options.maxOutputTokens) || options.maxOutputTokens < 1)
      throw new BudgetAdmissionError('budget_output_cap_required')
    this.options = { ...options }
  }
  runRequest<T>(task: () => T): T {
    return withBudgetRequest(task)
  }
  assertSupported(protocol: string): void {
    if (!['openai-chat', 'openai-responses'].includes(protocol))
      throw new BudgetAdmissionError('budget_protocol_unsupported')
  }
  prepare(descriptor: NativeAttemptDescriptor, body: unknown) {
    try {
      return this.prepareAttempt(descriptor, body)
    } catch (error) {
      throw new BudgetAdmissionError(error)
    }
  }
  private prepareAttempt(descriptor: NativeAttemptDescriptor, body: unknown) {
    this.assertSupported(descriptor.protocol)
    if (descriptor.opaqueInput) throw new BudgetAdmissionError('budget_opaque_input_unsupported')
    const options = this.options
    const contract = options.contract?.(descriptor)
    const outputCap = descriptor.operation === 'embedding' ? 0 : options.maxOutputTokens
    if (options.mode === 'strict' && (!contract || !options.estimator))
      throw new BudgetAdmissionError('budget_verified_estimator_required')
    if (options.mode === 'strict' && outputCap > 0 && !contract?.outputCapHonored)
      throw new BudgetAdmissionError('budget_output_cap_unverified')
    const limitedBody = {
      ...(body as Record<string, unknown>),
      ...(outputCap > 0
        ? {
            [descriptor.protocol === 'openai-responses'
              ? 'max_output_tokens'
              : 'max_completion_tokens']: outputCap,
          }
        : {}),
    }
    let estimate: BudgetEstimate
    if (options.mode === 'strict') {
      estimate = options.estimator!({
        descriptor,
        body: limitedBody,
        outputCap,
        contract: contract!,
      })
      if (
        !estimate.enforceable ||
        estimate.source === 'conservative_estimate' ||
        estimate.outputUpperTokens !== outputCap ||
        estimate.contractVersion !== contract!.version
      )
        throw new BudgetAdmissionError('budget_estimate_contract_mismatch')
      if (
        JSON.stringify([...new Set(estimate.inputCategories)].sort()) !==
        JSON.stringify([...new Set(contract!.inputCategories)].sort())
      )
        throw new BudgetAdmissionError('budget_estimate_contract_mismatch')
    } else {
      // An explicitly labeled heuristic, not an accurate tokenizer or universal upper bound.
      const bytes = Buffer.byteLength(JSON.stringify(limitedBody), 'utf8')
      estimate = {
        source: 'conservative_estimate',
        estimatorVersion: 'utf8-plus-1024-v1',
        contractVersion: contract?.version ?? 'unverified-native-text-v1',
        enforceable: false,
        inputUpperTokens: bytes + 1024,
        outputUpperTokens: outputCap,
        inputCategories:
          descriptor.operation === 'embedding'
            ? ['inputUsdPerMillion']
            : (contract?.inputCategories ?? [
                'inputUsdPerMillion',
                'cacheReadUsdPerMillion',
                'cacheWriteUsdPerMillion',
                'cacheWrite30mUsdPerMillion',
                'cacheWrite12hUsdPerMillion',
              ]),
      }
    }
    const scope = requestContext.getStore() ?? { id: randomUUID(), attempt: 0 }
    const active = context.getStore()
    const priority = currentBudgetPriority(
      ['planner', 'replyer', 'vision'].includes(descriptor.role) ? 0 : 1,
    )
    const priorityLane: BudgetLane =
      priority === 0 ? 'foreground' : priority === 1 ? 'realtime' : 'maintenance'
    const laneRank = { foreground: 0, realtime: 1, maintenance: 2 }
    const lane =
      active && laneRank[active.lane] > laneRank[priorityLane] ? active.lane : priorityLane
    const id = randomUUID()
    const prices: ModelCostPrices = {}
    for (const key of priceKeys)
      if (descriptor.prices[key] !== undefined) prices[key] = descriptor.prices[key]
    const policy = { ...options.policy(), mode: options.mode }
    try {
      options.ledger.reserve(
        {
          id,
          requestId: scope.id,
          attempt: ++scope.attempt,
          providerId: descriptor.providerId,
          modelId: descriptor.modelId,
          operation: descriptor.operation,
          lane,
          turnId: active?.turnId ?? scope.id,
          estimate,
          prices,
        },
        policy,
      )
    } catch (error) {
      throw new BudgetAdmissionError(
        error instanceof Error ? error.message : 'budget_admission_failed',
      )
    }
    let started = false,
      settled = false
    return {
      body: limitedBody,
      start: () => {
        if (!options.ledger.start(id))
          throw new BudgetAdmissionError('budget_attempt_already_started')
        started = true
      },
      settle: (usage: CostUsage) => {
        options.ledger.settle(id, usage)
        settled = true
      },
      fail: () => {
        if (settled) return
        if (started) options.ledger.unknown(id)
        else options.ledger.release(id)
        settled = true
      },
    }
  }
}
