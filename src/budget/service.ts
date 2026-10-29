import { ExternalCostService } from './external-cost/service.ts'
import { assertDomainReady } from '../memory/recovery/ledger.ts'
import { recordExternalCostReceipt } from '../memory/recovery/external-cost.ts'
import {
  readRestoredModelHold,
  acknowledgeRestoredModelHold,
} from '../memory/recovery/model-hold.ts'
import { randomUUID } from 'node:crypto'
import { BudgetLedger } from './ledger.ts'
import { roundedNanoUsd } from './estimate.ts'
import {
  NativeBudgetGate,
  BudgetAdmissionError,
  type NativeBudgetOptions,
} from './native-attempt.ts'
import { BudgetSettingsSchema, type BudgetSettings } from './settings.ts'
import type Database from 'better-sqlite3'
import type { BudgetPolicy } from './contracts.ts'

export interface BudgetStorePort {
  db: Database.Database
  readonly settings: unknown
}
export interface BudgetServiceOptions {
  bootId?: string
  now?: () => number
  estimator?: NativeBudgetOptions['estimator']
  contract?: NativeBudgetOptions['contract']
}
export class BudgetService {
  readonly ledger: BudgetLedger
  readonly externalCosts: ExternalCostService
  private readonly options: BudgetServiceOptions
  constructor(
    private readonly store: BudgetStorePort,
    options: BudgetServiceOptions = {},
  ) {
    this.options = { ...options }
    this.externalCosts = new ExternalCostService(store.db, {
      assertReady: () => assertDomainReady(store.db),
      beforeWrite: (receipt) => recordExternalCostReceipt(store.db, receipt),
      now: options.now,
    })
    this.ledger = new BudgetLedger(store.db, {
      bootId: options.bootId ?? randomUUID(),
      now: options.now,
      externalCoverage: (day) => this.externalCosts.coverage(day),
    })
    // No automatic recovery: another boot may still be active.
  }
  private settings(): BudgetSettings {
    return BudgetSettingsSchema.parse(this.store.settings)
  }
  private policy(settings: BudgetSettings): BudgetPolicy {
    const dailyNanoUsd = roundedNanoUsd(settings.budgetDailyUsd)
    return {
      mode: settings.budgetMode,
      dailyNanoUsd,
      maxUnresolvedNanoUsd: roundedNanoUsd(settings.budgetMaxUnresolvedUsd),
      perTurnNanoUsd: roundedNanoUsd(settings.budgetPerTurnUsd),
      perTurnTokens: settings.budgetPerTurnTokens,
      perTurnCalls: settings.budgetPerTurnCalls,
      laneDailyNanoUsd: {
        realtime: Math.floor(dailyNanoUsd * settings.budgetRealtimeRatio),
        maintenance: Math.floor(dailyNanoUsd * settings.budgetMaintenanceRatio),
      },
    }
  }
  recovery() {
    const state = readRestoredModelHold(this.store.db)
    return {
      restoreId: state?.restoreId,
      held: state?.status === 'held',
      restorationCoverage: !state
        ? ('none' as const)
        : state.status === 'held'
          ? ('unknown' as const)
          : ('manually_acknowledged' as const),
    }
  }
  acknowledgeRecovery(restoreId: string, acknowledgment: string) {
    acknowledgeRestoredModelHold(this.store.db, restoreId, acknowledgment)
    return this.recovery()
  }
  private assertRestorationAdmission() {
    if (this.recovery().held) throw new BudgetAdmissionError('budget_restore_accounting_hold')
  }
  gate(): NativeBudgetGate | undefined {
    this.assertRestorationAdmission()
    const settings = this.settings()
    if (!settings.budgetExperimentEnabled) return undefined
    return new NativeBudgetGate({
      ledger: this.ledger,
      policy: () => {
        this.assertRestorationAdmission()
        const current = this.settings()
        if (
          !current.budgetExperimentEnabled ||
          current.budgetMode !== settings.budgetMode ||
          current.budgetMaxOutputTokens !== settings.budgetMaxOutputTokens
        )
          throw new BudgetAdmissionError('budget_configuration_changed')
        return this.policy(current)
      },
      mode: settings.budgetMode,
      maxOutputTokens: settings.budgetMaxOutputTokens,
      estimator: this.options.estimator,
      contract: this.options.contract,
    })
  }
  coverage() {
    const restoration = this.recovery()
    const settings = this.settings()
    const coverage = this.ledger.coverage(this.policy(settings))
    const counts = this.store.db
      .prepare(
        "SELECT state, COUNT(*) AS count FROM budget_attempts WHERE state IN ('reserved','started','partial','unknown') GROUP BY state",
      )
      .all() as Array<{ state: string; count: number }>
    const count = (state: string) => counts.find((row) => row.state === state)?.count ?? 0
    return {
      ...coverage,
      externalCosts: this.externalCosts.coverage(coverage.windowStart),
      admissionHold: restoration.held,
      restorationCoverage: restoration.restorationCoverage,
      restorationId: restoration.restoreId,
      enabled: settings.budgetExperimentEnabled,
      mode: settings.budgetMode,
      estimateKind:
        settings.budgetMode === 'strict'
          ? ('verified_contract_required' as const)
          : ('best_effort_not_hard_limit' as const),
      amountUnit: 'nanoUSD' as const,
      usdPerNano: 0.000000001,
      knownUsd: coverage.knownNanoUsd / 1e9,
      heldUsd: coverage.heldNanoUsd / 1e9,
      remainingUsd: coverage.remainingNanoUsd / 1e9,
      partialAttempts: count('partial'),
      unknownAttempts: count('unknown'),
      pendingAttempts: count('reserved') + count('started'),
      coverage: coverage.externalUnknownReceipts
        ? ('external_incomplete' as const)
        : coverage.unboundedAttempts
          ? ('unbounded' as const)
          : count('partial') || count('unknown') || count('reserved') || count('started')
            ? ('pending' as const)
            : ('settled' as const),
    }
  }
}
