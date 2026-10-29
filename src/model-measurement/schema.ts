import { z } from '@hono/zod-openapi'

const count = z.number().int().min(0).max(Number.MAX_SAFE_INTEGER)
const coverage = z.enum(['complete', 'partial', 'unknown'])
/** Safe aggregate only: no request strings, arbitrary metadata, or provider calibration claim. */
export const RequestMeasurementSummarySchema = z
  .object({
    attempts: count,
    measuredBodyAttempts: count,
    bodyUtf8Bytes: count.nullable(),
    bytesCoverage: coverage,
    textCountedAttempts: count,
    serializedTextTokens: count.nullable(),
    textTokenCoverage: coverage,
    tokenizerContracts: z
      .array(z.string().regex(/^[A-Za-z0-9._-]{1,128}@[A-Za-z0-9._-]{1,128}$/))
      .max(16),
    contractCoverage: z.enum(['complete', 'limited']),
    providerTokenCount: z.literal('unknown'),
    providerFraming: z.literal('uncalibrated'),
    hardTokenCap: z.literal(false),
  })
  .strict()
