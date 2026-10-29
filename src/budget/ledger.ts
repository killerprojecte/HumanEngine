import { createHash } from 'node:crypto'
import {
  calculateModelCost,
  cachePartition,
  validCostUsage,
  type CostUsage,
} from '../model-cost.ts'
import type {
  BudgetAttempt,
  BudgetCoverage,
  BudgetPolicy,
  BudgetRequest,
  BudgetState,
} from './contracts.ts'
import {
  nonnegativeInteger,
  priceKeys,
  quoteEstimate,
  exactTokenSubtotalNanoUsd,
  safeNumber,
} from './estimate.ts'
import { migrateBudget, type BudgetDatabase } from './storage.ts'

const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const lanes = ['foreground', 'realtime', 'maintenance'] as const
const tokenKeys = [
  'inputTokens',
  'outputTokens',
  'ordinaryInputTokens',
  'cacheReadInputTokens',
  'cacheWriteInputTokens',
  'cacheWrite5mInputTokens',
  'cacheWrite1hInputTokens',
  'cacheWrite30mInputTokens',
  'cacheWrite12hInputTokens',
] as const
function identity(value: string): string {
  if (
    typeof value !== 'string' ||
    !/^[a-zA-Z0-9_.:@/-]{1,160}$/.test(value) ||
    value.includes('://')
  )
    throw new Error('invalid_budget_identity')
  return value
}
function onlyKeys(value: object, keys: readonly string[]) {
  if (Object.keys(value).some((key) => !keys.includes(key)))
    throw new Error('invalid_budget_metadata')
}
function canonicalRequest(value: BudgetRequest): BudgetRequest {
  onlyKeys(value, [
    'id',
    'requestId',
    'attempt',
    'providerId',
    'modelId',
    'operation',
    'lane',
    'turnId',
    'estimate',
    'prices',
  ])
  onlyKeys(value.estimate, [
    'source',
    'estimatorVersion',
    'contractVersion',
    'enforceable',
    'inputUpperTokens',
    'outputUpperTokens',
    'inputCategories',
  ])
  onlyKeys(value.prices, priceKeys)
  nonnegativeInteger(value.attempt)
  if (
    value.attempt < 1 ||
    !lanes.includes(value.lane) ||
    !['complete', 'embedding'].includes(value.operation)
  )
    throw new Error('invalid_budget_request')
  const prices: BudgetRequest['prices'] = {}
  for (const key of priceKeys)
    if (value.prices[key] !== undefined) {
      const rate = value.prices[key]!
      if (!Number.isFinite(rate) || rate < 0) throw new Error('invalid_budget_price')
      prices[key] = rate
    }
  if (value.operation === 'embedding' && value.estimate.outputUpperTokens !== 0)
    throw new Error('invalid_budget_estimate')
  return {
    id: identity(value.id),
    requestId: identity(value.requestId),
    attempt: value.attempt,
    providerId: identity(value.providerId),
    modelId: identity(value.modelId),
    operation: value.operation,
    lane: value.lane,
    ...(value.turnId ? { turnId: identity(value.turnId) } : {}),
    estimate: {
      source: value.estimate.source,
      estimatorVersion: identity(value.estimate.estimatorVersion),
      contractVersion: identity(value.estimate.contractVersion),
      enforceable: value.estimate.enforceable,
      inputUpperTokens: value.estimate.inputUpperTokens,
      outputUpperTokens: value.estimate.outputUpperTokens,
      inputCategories: [...new Set(value.estimate.inputCategories)].sort(),
    },
    prices,
  }
}
function policyValid(policy: BudgetPolicy) {
  onlyKeys(policy, [
    'mode',
    'dailyNanoUsd',
    'maxUnresolvedNanoUsd',
    'laneDailyNanoUsd',
    'perTurnNanoUsd',
    'perTurnTokens',
    'perTurnCalls',
  ])
  if (policy.laneDailyNanoUsd) onlyKeys(policy.laneDailyNanoUsd, lanes)
  if (policy.mode !== undefined && !['strict', 'best_effort'].includes(policy.mode))
    throw new Error('invalid_budget_mode')
  nonnegativeInteger(policy.dailyNanoUsd)
  nonnegativeInteger(policy.maxUnresolvedNanoUsd)
  for (const value of [
    policy.perTurnNanoUsd,
    policy.perTurnTokens,
    policy.perTurnCalls,
    ...Object.values(policy.laneDailyNanoUsd ?? {}),
  ])
    if (value !== undefined) nonnegativeInteger(value)
}
interface Row {
  id: string
  payload_hash: string
  payload: string
  policy: string
  boot_id: string
  state: BudgetState
  window_start: string
  lane: BudgetRequest['lane']
  turn_id: string | null
  reserved_nano: number
  known_nano: number
  held_nano: number
  created_at: string
  started_at: string | null
  settled_at: string | null
  usage: string | null
  receipt_hash: string | null
  overrun: number
  unbounded_exposure: number
}
const sum = (values: number[]) => safeNumber(values.reduce((a, b) => a + BigInt(b), 0n))
function usageSnapshot(value: CostUsage): CostUsage {
  if (
    (value.known !== undefined && typeof value.known !== 'boolean') ||
    (value.cacheKnown !== undefined && typeof value.cacheKnown !== 'boolean')
  )
    return { inputTokens: 0, outputTokens: 0, known: false }
  const result: CostUsage = { inputTokens: value.inputTokens, outputTokens: value.outputTokens }
  for (const key of tokenKeys) if (value[key] !== undefined) result[key] = value[key]
  if (value.known !== undefined) result.known = value.known
  if (value.cacheKnown !== undefined) result.cacheKnown = value.cacheKnown
  if (!validCostUsage(result)) return { inputTokens: 0, outputTokens: 0, known: false }
  return result
}
export class BudgetLedger {
  private readonly bootId: string
  private readonly now: () => number
  private readonly externalCoverage?: (windowStart: string) => {
    knownTodayNanoUsd: number
    unknownReceipts: number
  }
  constructor(
    private readonly db: BudgetDatabase,
    options: {
      bootId: string
      now?: () => number
      externalCoverage?: (windowStart: string) => {
        knownTodayNanoUsd: number
        unknownReceipts: number
      }
    },
  ) {
    this.externalCoverage = options.externalCoverage
    this.bootId = identity(options.bootId)
    this.now = options.now ?? Date.now
    migrateBudget(db)
  }
  private time() {
    return new Date(this.now()).toISOString()
  }
  private rows() {
    return this.db.prepare('SELECT * FROM budget_attempts').all() as Row[]
  }
  private row(id: string) {
    const row = this.db.prepare('SELECT * FROM budget_attempts WHERE id=?').get(id) as
      Row | undefined
    if (!row) throw new Error('budget_attempt_missing')
    return row
  }
  private view(row: Row): BudgetAttempt {
    return {
      request: JSON.parse(row.payload),
      bootId: row.boot_id,
      currency: 'USD',
      policy: JSON.parse(row.policy),
      state: row.state,
      windowStart: row.window_start,
      createdAt: row.created_at,
      ...(row.started_at ? { startedAt: row.started_at } : {}),
      ...(row.settled_at ? { settledAt: row.settled_at } : {}),
      reservedNanoUsd: row.reserved_nano,
      knownNanoUsd: row.known_nano,
      heldNanoUsd: row.held_nano,
      ...(row.usage ? { usage: JSON.parse(row.usage) } : {}),
      overrun: !!row.overrun,
      unboundedExposure: !!row.unbounded_exposure,
    }
  }
  get(id: string): BudgetAttempt {
    return this.view(this.row(id))
  }
  coverage(policy: BudgetPolicy): BudgetCoverage {
    policyValid(policy)
    const windowStart = this.time().slice(0, 10),
      rows = this.rows()
    const external = this.externalCoverage?.(windowStart) ?? {
      knownTodayNanoUsd: 0,
      unknownReceipts: 0,
    }
    nonnegativeInteger(external.knownTodayNanoUsd)
    nonnegativeInteger(external.unknownReceipts)
    const current = rows.filter((row) => row.window_start === windowStart)
    const knownNanoUsd = sum([...current.map((row) => row.known_nano), external.knownTodayNanoUsd]),
      heldNanoUsd = sum(current.map((row) => row.held_nano))
    const carriedNanoUsd = sum(
      rows.filter((row) => row.window_start !== windowStart).map((row) => row.held_nano),
    )
    const unresolvedNanoUsd = sum(
      rows.filter((row) => ['partial', 'unknown'].includes(row.state)).map((row) => row.held_nano),
    )
    const exposure = sum([knownNanoUsd, heldNanoUsd, carriedNanoUsd])
    const laneExposureNanoUsd = { foreground: 0, realtime: 0, maintenance: 0 }
    for (const lane of lanes)
      laneExposureNanoUsd[lane] = sum(
        rows
          .filter((row) => row.lane === lane)
          .map((row) => row.held_nano + (row.window_start === windowStart ? row.known_nano : 0)),
      )
    return {
      currency: 'USD',
      externalKnownNanoUsd: external.knownTodayNanoUsd,
      externalUnknownReceipts: external.unknownReceipts,
      windowStart,
      knownNanoUsd,
      heldNanoUsd,
      carriedNanoUsd,
      unresolvedNanoUsd,
      admissionExposureNanoUsd: exposure,
      remainingNanoUsd:
        external.unknownReceipts > 0 ||
        (policy.mode !== 'best_effort' && rows.some((row) => row.unbounded_exposure))
          ? 0
          : Math.max(0, policy.dailyNanoUsd - exposure),
      laneExposureNanoUsd,
      overrunAttempts: rows.filter((row) => row.overrun).length,
      unboundedAttempts: rows.filter((row) => row.unbounded_exposure).length,
    }
  }
  reserve(input: BudgetRequest, policy: BudgetPolicy): BudgetAttempt {
    const request = canonicalRequest(input),
      payload = JSON.stringify(request),
      payloadHash = hash(request)
    const amount = quoteEstimate(request.estimate, request.prices, policy.mode)
    policyValid(policy)
    return this.db
      .transaction(() => {
        const existing = this.db
          .prepare('SELECT * FROM budget_attempts WHERE id=?')
          .get(request.id) as Row | undefined
        if (existing) {
          if (existing.payload_hash !== payloadHash) throw new Error('budget_idempotency_conflict')
          return this.view(existing)
        }
        const duplicateAttempt = this.db
          .prepare(
            "SELECT id FROM budget_attempts WHERE json_extract(payload,'$.requestId')=? AND json_extract(payload,'$.attempt')=?",
          )
          .get(request.requestId, request.attempt)
        if (duplicateAttempt) throw new Error('budget_attempt_identity_conflict')
        const coverage = this.coverage(policy)
        if (coverage.externalUnknownReceipts) throw new Error('budget_external_cost_unknown')
        if (policy.mode !== 'best_effort' && coverage.unboundedAttempts)
          throw new Error('budget_unbounded_exposure')
        const brokenContract = this.rows().some((row) => {
          if (!row.overrun) return false
          const prior = JSON.parse(row.payload) as BudgetRequest
          return (
            prior.providerId === request.providerId &&
            prior.modelId === request.modelId &&
            prior.estimate.contractVersion === request.estimate.contractVersion &&
            prior.estimate.estimatorVersion === request.estimate.estimatorVersion
          )
        })
        if (brokenContract) throw new Error('budget_contract_overrun')
        if (coverage.unresolvedNanoUsd > policy.maxUnresolvedNanoUsd)
          throw new Error('budget_unresolved_limit')
        if (amount > coverage.remainingNanoUsd) throw new Error('budget_daily_limit')
        const laneCap = policy.laneDailyNanoUsd?.[request.lane]
        if (
          laneCap !== undefined &&
          sum([coverage.laneExposureNanoUsd[request.lane], amount]) > laneCap
        )
          throw new Error('budget_lane_limit')
        if (request.turnId) {
          const prior = this.rows().filter(
            (row) => row.turn_id === request.turnId && row.state !== 'released',
          )
          if (policy.perTurnCalls !== undefined && prior.length >= policy.perTurnCalls)
            throw new Error('budget_turn_calls')
          if (
            policy.perTurnNanoUsd !== undefined &&
            sum([...prior.map((row) => row.known_nano + row.held_nano), amount]) >
              policy.perTurnNanoUsd
          )
            throw new Error('budget_turn_amount')
          const tokenTotal = sum([
            request.estimate.inputUpperTokens,
            request.estimate.outputUpperTokens,
            ...prior.flatMap((row) => {
              const r = JSON.parse(row.payload) as BudgetRequest
              return [r.estimate.inputUpperTokens, r.estimate.outputUpperTokens]
            }),
          ])
          if (policy.perTurnTokens !== undefined && tokenTotal > policy.perTurnTokens)
            throw new Error('budget_turn_tokens')
        } else if (
          policy.perTurnCalls !== undefined ||
          policy.perTurnNanoUsd !== undefined ||
          policy.perTurnTokens !== undefined
        )
          throw new Error('budget_turn_identity_required')
        const at = this.time()
        this.db
          .prepare(
            'INSERT INTO budget_attempts(id,payload_hash,payload,policy,boot_id,state,window_start,lane,turn_id,reserved_nano,known_nano,held_nano,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)',
          )
          .run(
            request.id,
            payloadHash,
            payload,
            JSON.stringify(policy),
            this.bootId,
            'reserved',
            at.slice(0, 10),
            request.lane,
            request.turnId ?? null,
            amount,
            0,
            amount,
            at,
          )
        return this.get(request.id)
      })
      .immediate()
  }
  start(id: string): boolean {
    return this.db
      .transaction(() => {
        const row = this.row(id)
        if (row.boot_id !== this.bootId) throw new Error('budget_attempt_owner')
        if (row.state !== 'reserved') return false
        // Reservation already contributes to exposure. Re-read external receipts at
        // the last synchronous admission boundary without reserving it a second time.
        const policy = JSON.parse(row.policy) as BudgetPolicy
        const coverage = this.coverage(policy)
        if (coverage.externalUnknownReceipts) throw new Error('budget_external_cost_unknown')
        if (coverage.admissionExposureNanoUsd > policy.dailyNanoUsd)
          throw new Error('budget_daily_limit')
        this.db
          .prepare("UPDATE budget_attempts SET state='started',started_at=? WHERE id=?")
          .run(this.time(), id)
        return true
      })
      .immediate()
  }
  release(id: string): BudgetAttempt {
    return this.db
      .transaction(() => {
        const row = this.row(id)
        if (row.state === 'released') return this.view(row)
        if (row.boot_id !== this.bootId || row.state !== 'reserved')
          throw new Error('budget_release_started')
        this.db
          .prepare(
            "UPDATE budget_attempts SET state='released',held_nano=0,settled_at=? WHERE id=?",
          )
          .run(this.time(), id)
        return this.get(id)
      })
      .immediate()
  }
  unknown(id: string): BudgetAttempt {
    return this.db
      .transaction(() => {
        const row = this.row(id)
        if (row.state === 'unknown') return this.view(row)
        if (row.state !== 'started') throw new Error('budget_invalid_transition')
        this.db
          .prepare("UPDATE budget_attempts SET state='unknown',settled_at=? WHERE id=?")
          .run(this.time(), id)
        return this.get(id)
      })
      .immediate()
  }
  settle(id: string, usage: CostUsage): BudgetAttempt {
    const snapshot = usageSnapshot(usage),
      receiptHash = hash(snapshot)
    return this.db
      .transaction(() => {
        const row = this.row(id)
        if (row.receipt_hash) {
          if (row.receipt_hash !== receiptHash) throw new Error('budget_receipt_conflict')
          return this.view(row)
        }
        if (!['started', 'unknown'].includes(row.state))
          throw new Error('budget_invalid_transition')
        const request = JSON.parse(row.payload) as BudgetRequest
        const cost = calculateModelCost(snapshot, request.prices)
        const partition = cachePartition(snapshot)
        const ttlKeys = [
          'cacheWrite5mInputTokens',
          'cacheWrite1hInputTokens',
          'cacheWrite30mInputTokens',
          'cacheWrite12hInputTokens',
        ] as const
        const ttlPrices = [
          'cacheWrite5mUsdPerMillion',
          'cacheWrite1hUsdPerMillion',
          'cacheWrite30mUsdPerMillion',
          'cacheWrite12hUsdPerMillion',
        ] as const
        const ttlTotal = ttlKeys.reduce((sum, key) => sum + (snapshot[key] ?? 0), 0)
        const buckets: Array<readonly [number, number | undefined]> = []
        if (cost?.ordinaryInputUsd !== undefined)
          buckets.push([partition?.ordinaryInputTokens ?? 0, request.prices.inputUsdPerMillion])
        if (cost?.outputUsd !== undefined)
          buckets.push([snapshot.outputTokens, request.prices.outputUsdPerMillion])
        if (cost?.cacheReadUsd !== undefined)
          buckets.push([snapshot.cacheReadInputTokens ?? 0, request.prices.cacheReadUsdPerMillion])
        if (cost?.cacheWriteUsd !== undefined) {
          buckets.push([
            (snapshot.cacheWriteInputTokens ?? 0) - ttlTotal,
            request.prices.cacheWriteUsdPerMillion,
          ])
          for (const [index, key] of ttlKeys.entries())
            buckets.push([snapshot[key] ?? 0, request.prices[ttlPrices[index]!]])
        }
        const known = exactTokenSubtotalNanoUsd(buckets)
        const allowed = new Set(request.estimate.inputCategories)
        const categoryViolation =
          validCostUsage(snapshot) &&
          (((partition?.ordinaryInputTokens ?? snapshot.ordinaryInputTokens ?? 0) > 0 &&
            !allowed.has('inputUsdPerMillion')) ||
            ((snapshot.cacheReadInputTokens ?? 0) > 0 && !allowed.has('cacheReadUsdPerMillion')) ||
            ((snapshot.cacheWriteInputTokens ?? 0) - ttlTotal > 0 &&
              !allowed.has('cacheWriteUsdPerMillion')) ||
            ttlKeys.some(
              (key, index) => (snapshot[key] ?? 0) > 0 && !allowed.has(ttlPrices[index]!),
            ))
        const complete = cost?.fullyPriced === true
        const held = complete ? 0 : Math.max(0, row.reserved_nano - known)
        // A broken upper-bound contract is visible; never clamp the actual bill to the reservation.
        const overrun =
          categoryViolation ||
          known > row.reserved_nano ||
          snapshot.inputTokens > request.estimate.inputUpperTokens ||
          snapshot.outputTokens > request.estimate.outputUpperTokens
        this.db
          .prepare(
            'UPDATE budget_attempts SET state=?,known_nano=?,held_nano=?,usage=?,receipt_hash=?,settled_at=?,overrun=?,unbounded_exposure=? WHERE id=?',
          )
          .run(
            complete ? 'complete' : cost ? 'partial' : 'unknown',
            known,
            held,
            JSON.stringify(snapshot),
            receiptHash,
            this.time(),
            overrun ? 1 : 0,
            overrun && !complete ? 1 : 0,
            id,
          )
        return this.get(id)
      })
      .immediate()
  }
  /** Privileged caller supplies an independently verified receipt identity, never an estimate. */
  reconcile(id: string, usage: CostUsage, evidenceId: string): BudgetAttempt {
    identity(evidenceId)
    const snapshot = usageSnapshot(usage)
    const receiptHash = hash(snapshot)
    return this.db
      .transaction(() => {
        const prior = this.db
          .prepare('SELECT attempt_id,receipt_hash FROM budget_reconciliations WHERE evidence_id=?')
          .get(evidenceId) as { attempt_id: string; receipt_hash: string } | undefined
        if (prior) {
          if (prior.attempt_id !== id || prior.receipt_hash !== receiptHash)
            throw new Error('budget_reconciliation_conflict')
          return this.get(id)
        }
        const row = this.row(id)
        if (!['unknown', 'partial'].includes(row.state))
          throw new Error('budget_invalid_transition')
        const request = JSON.parse(row.payload) as BudgetRequest
        if (!calculateModelCost(snapshot, request.prices)?.fullyPriced)
          throw new Error('budget_reconciliation_incomplete')
        this.db
          .prepare("UPDATE budget_attempts SET state='unknown',receipt_hash=NULL WHERE id=?")
          .run(id)
        const result = this.settle(id, snapshot)
        this.db
          .prepare('INSERT INTO budget_reconciliations VALUES(?,?,?,?)')
          .run(evidenceId, id, receiptHash, this.time())
        return result
      })
      .immediate()
  }
  /** Caller must establish that this boot is dead; elapsed time alone is not authority. */
  recoverDeadBoot(confirmedDeadBootId: string): number {
    identity(confirmedDeadBootId)
    if (confirmedDeadBootId === this.bootId) throw new Error('budget_active_boot')
    return this.db
      .transaction(() => {
        const released = this.db
          .prepare(
            "UPDATE budget_attempts SET state='released',held_nano=0,settled_at=? WHERE boot_id=? AND state='reserved'",
          )
          .run(this.time(), confirmedDeadBootId).changes
        const unknown = this.db
          .prepare(
            "UPDATE budget_attempts SET state='unknown',settled_at=? WHERE boot_id=? AND state='started'",
          )
          .run(this.time(), confirmedDeadBootId).changes
        return released + unknown
      })
      .immediate()
  }
}
