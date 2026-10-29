import { isDeepStrictEqual } from 'node:util'
import type { BudgetDatabase } from '../storage.ts'
import { safeNumber } from '../estimate.ts'
import {
  ExternalCostInputSchema,
  ExternalCostCorrectionSchema,
  ExternalCostReceiptSchema,
  ExternalCostListSchema,
  type ExternalCostInput,
  type ExternalCostCorrection,
  type ExternalCostReceipt,
  type ExternalCostCoverage,
} from './contracts.ts'
import { ExternalCostStorage } from './storage.ts'
export interface ExternalCostPorts {
  assertReady(): void
  beforeWrite(receipt: ExternalCostReceipt): void
  now?: () => number
}
export class ExternalCostService {
  readonly storage: ExternalCostStorage
  constructor(
    private readonly db: BudgetDatabase,
    private readonly ports: ExternalCostPorts,
  ) {
    this.storage = new ExternalCostStorage(db)
  }
  import(input: ExternalCostInput): ExternalCostReceipt {
    const receipt = ExternalCostInputSchema.parse(input)
    return this.db
      .transaction(() => {
        this.ports.assertReady()
        const prior = this.storage.latest(receipt.id)
        if (prior) {
          if (!isDeepStrictEqual(prior.receipt, receipt)) throw new Error('external_cost_conflict')
          return prior
        }
        if (this.storage.current().length >= 10000) throw new Error('external_cost_capacity')
        return this.write({
          schemaVersion: 1,
          revision: 1,
          receipt,
          correctionId: null,
          recordedAt: this.time(),
        })
      })
      .immediate()
  }
  correct(input: ExternalCostCorrection): ExternalCostReceipt {
    const correction = ExternalCostCorrectionSchema.parse(input)
    return this.db
      .transaction(() => {
        this.ports.assertReady()
        const existing = this.storage.correction(correction.correctionId)
        if (existing) {
          if (
            existing.revision !== correction.expectedRevision + 1 ||
            !isDeepStrictEqual(existing.receipt, correction.replacement)
          )
            throw new Error('external_cost_conflict')
          return existing
        }
        const prior = this.storage.latest(correction.id)
        if (!prior || prior.revision !== correction.expectedRevision)
          throw new Error('external_cost_revision_conflict')
        return this.write({
          schemaVersion: 1,
          revision: prior.revision + 1,
          receipt: correction.replacement,
          correctionId: correction.correctionId,
          recordedAt: this.time(),
        })
      })
      .immediate()
  }
  private time() {
    return new Date((this.ports.now ?? Date.now)()).toISOString()
  }
  private write(input: ExternalCostReceipt) {
    const value = ExternalCostReceiptSchema.parse(input)
    this.ports.beforeWrite(value)
    this.storage.put(value)
    return structuredClone(value)
  }
  list(input: { page?: number; pageSize?: number } = {}) {
    this.ports.assertReady()
    const { page, pageSize } = ExternalCostListSchema.parse(input)
    const rows = this.storage
      .current()
      .sort(
        (a, b) =>
          b.recordedAt.localeCompare(a.recordedAt) || a.receipt.id.localeCompare(b.receipt.id),
      )
    return {
      items: rows.slice((page - 1) * pageSize, page * pageSize),
      total: rows.length,
      page,
      pageSize,
    }
  }
  coverage(windowStart: string): ExternalCostCoverage {
    this.ports.assertReady()
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(windowStart) ||
      new Date(windowStart + 'T00:00:00.000Z').toISOString().slice(0, 10) !== windowStart
    )
      throw new Error('external_cost_date_invalid')
    const rows = this.storage.current(),
      known = rows.filter((r) => r.receipt.status === 'known'),
      unknownReceipts = rows.length - known.length
    const sum = (values: typeof rows) =>
      safeNumber(
        values.reduce(
          (n, r) => n + BigInt(r.receipt.status === 'known' ? r.receipt.amountNanoUsd : 0),
          0n,
        ),
      )
    return {
      currency: 'USD',
      windowStart,
      knownTodayNanoUsd: sum(known.filter((r) => r.receipt.billingDate === windowStart)),
      knownAllTimeNanoUsd: sum(known),
      unknownReceipts,
      knownReceipts: known.length,
      coverage: unknownReceipts ? 'incomplete' : 'known_imported_only',
    }
  }
}
