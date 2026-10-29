import { z } from 'zod'
const opaque = z.string().regex(/^[A-Za-z0-9_-]{1,160}$/)
const day = z
  .string()
  .regex(/^\d{4}-\d{2}-\d{2}$/)
  .refine(
    (v) =>
      Number.isFinite(Date.parse(v + 'T00:00:00.000Z')) &&
      new Date(v + 'T00:00:00.000Z').toISOString().slice(0, 10) === v,
  )
const base = {
  id: opaque,
  category: z.enum(['tool', 'storage', 'compute']),
  currency: z.literal('USD'),
  billingDate: day,
  periodStart: day,
  periodEnd: day,
  sourceId: opaque,
}
export const ExternalCostInputSchema = z
  .discriminatedUnion('status', [
    z
      .object({
        ...base,
        status: z.literal('known'),
        amountNanoUsd: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
      })
      .strict(),
    z.object({ ...base, status: z.literal('unknown') }).strict(),
  ])
  .refine((v) => v.periodStart <= v.periodEnd)
export type ExternalCostInput = z.infer<typeof ExternalCostInputSchema>
export const ExternalCostReceiptSchema = z
  .object({
    schemaVersion: z.literal(1),
    revision: z.number().int().min(1).max(1000),
    receipt: ExternalCostInputSchema,
    correctionId: opaque.nullable(),
    recordedAt: z.iso.datetime(),
  })
  .strict()
export type ExternalCostReceipt = z.infer<typeof ExternalCostReceiptSchema>
export const ExternalCostCorrectionSchema = z
  .object({
    id: opaque,
    expectedRevision: z.number().int().positive(),
    replacement: ExternalCostInputSchema,
    correctionId: opaque,
  })
  .strict()
  .refine((v) => v.id === v.replacement.id)
export type ExternalCostCorrection = z.infer<typeof ExternalCostCorrectionSchema>
export interface ExternalCostCoverage {
  currency: 'USD'
  windowStart: string
  knownTodayNanoUsd: number
  knownAllTimeNanoUsd: number
  unknownReceipts: number
  knownReceipts: number
  coverage: 'known_imported_only' | 'incomplete'
}
export const EXTERNAL_COST_TABLE = 'budget_external_cost_revisions'
export const EXTERNAL_COST_COLUMNS = [
  'id',
  'revision',
  'payload',
  'payload_hash',
  'correction_id',
  'recorded_at',
] as const
export const ExternalCostListSchema = z
  .object({
    page: z.number().int().min(1).max(10000).default(1),
    pageSize: z.number().int().min(1).max(50).default(25),
  })
  .strict()
