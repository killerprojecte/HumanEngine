import type { ModelCostPrices, CostUsage } from '../model-cost.ts'
export type BudgetLane = 'foreground' | 'realtime' | 'maintenance'
export type BudgetState = 'reserved' | 'started' | 'complete' | 'partial' | 'unknown' | 'released'
export type InputPriceCategory = Exclude<keyof ModelCostPrices, 'outputUsdPerMillion'>
export interface BudgetEstimate {
  source: 'verified_contract' | 'verified_tokenizer' | 'conservative_estimate'
  estimatorVersion: string
  contractVersion: string
  enforceable: boolean
  inputUpperTokens: number
  outputUpperTokens: number
  inputCategories: InputPriceCategory[]
}
export interface BudgetRequest {
  id: string
  requestId: string
  attempt: number
  providerId: string
  modelId: string
  operation: 'complete' | 'embedding'
  lane: BudgetLane
  turnId?: string
  estimate: BudgetEstimate
  prices: ModelCostPrices
}
export interface BudgetPolicy {
  mode?: 'strict' | 'best_effort'
  dailyNanoUsd: number
  maxUnresolvedNanoUsd: number
  laneDailyNanoUsd?: Partial<Record<BudgetLane, number>>
  perTurnNanoUsd?: number
  perTurnTokens?: number
  perTurnCalls?: number
}
export interface BudgetAttempt {
  request: BudgetRequest
  bootId: string
  currency: 'USD'
  policy: BudgetPolicy
  state: BudgetState
  windowStart: string
  createdAt: string
  startedAt?: string
  settledAt?: string
  reservedNanoUsd: number
  knownNanoUsd: number
  heldNanoUsd: number
  usage?: CostUsage
  overrun: boolean
  unboundedExposure: boolean
}
export interface BudgetCoverage {
  externalKnownNanoUsd: number
  externalUnknownReceipts: number
  currency: 'USD'
  windowStart: string
  knownNanoUsd: number
  heldNanoUsd: number
  carriedNanoUsd: number
  unresolvedNanoUsd: number
  admissionExposureNanoUsd: number
  remainingNanoUsd: number
  laneExposureNanoUsd: Record<BudgetLane, number>
  overrunAttempts: number
  unboundedAttempts: number
}
