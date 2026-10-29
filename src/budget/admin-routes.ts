import { RESTORATION_REVIEW_ACK } from '../memory/recovery/model-hold.ts'
import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { BudgetService } from './service.ts'

const amount = z.number().finite().nonnegative()
const count = z.number().int().nonnegative()
export const BudgetCoverageSchema = z
  .object({
    admissionHold: z.boolean(),
    restorationCoverage: z.enum(['none', 'unknown', 'manually_acknowledged']),
    restorationId: z.string().optional(),
    currency: z.literal('USD'),
    windowStart: z.string(),
    knownNanoUsd: amount,
    externalKnownNanoUsd: amount,
    externalUnknownReceipts: count,
    externalCosts: z.object({
      currency: z.literal('USD'),
      windowStart: z.string(),
      knownTodayNanoUsd: amount,
      knownAllTimeNanoUsd: amount,
      unknownReceipts: count,
      knownReceipts: count,
      coverage: z.enum(['known_imported_only', 'incomplete']),
    }),
    heldNanoUsd: amount,
    carriedNanoUsd: amount,
    unresolvedNanoUsd: amount,
    admissionExposureNanoUsd: amount,
    remainingNanoUsd: amount,
    laneExposureNanoUsd: z.object({ foreground: amount, realtime: amount, maintenance: amount }),
    overrunAttempts: count,
    unboundedAttempts: count,
    enabled: z.boolean(),
    mode: z.enum(['strict', 'best_effort']),
    estimateKind: z.enum(['verified_contract_required', 'best_effort_not_hard_limit']),
    amountUnit: z.literal('nanoUSD'),
    usdPerNano: z.literal(0.000000001),
    knownUsd: amount,
    heldUsd: amount,
    remainingUsd: amount,
    partialAttempts: count,
    unknownAttempts: count,
    pendingAttempts: count,
    coverage: z.enum(['settled', 'pending', 'unbounded', 'external_incomplete']),
  })
  .openapi('BudgetCoverage')
/** Authenticated management only. Manual review permits admission, never reconciles accounting. */
export function registerBudgetRoutes(
  app: OpenAPIHono,
  { budget }: { budget: Pick<BudgetService, 'coverage' | 'recovery' | 'acknowledgeRecovery'> },
) {
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/budget/recovery',
      responses: {
        200: {
          description: 'Restoration accounting status',
          content: { 'application/json': { schema: RecoverySchema } },
        },
      },
    }),
    (c) => c.json(budget.recovery(), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/budget/recovery/acknowledge',
      request: {
        body: {
          required: true,
          content: {
            'application/json': {
              schema: z
                .object({
                  restoreId: z.string().uuid(),
                  acknowledgment: z.literal(RESTORATION_REVIEW_ACK),
                })
                .strict(),
            },
          },
        },
      },
      responses: {
        200: {
          description: 'Manual review acknowledged; historical accounting remains incomplete',
          content: { 'application/json': { schema: RecoverySchema } },
        },
        409: {
          description: 'Restoration identifier changed',
          content: { 'application/json': { schema: z.object({ error: z.string() }) } },
        },
      },
    }),
    (c) => {
      const input = c.req.valid('json')
      try {
        return c.json(budget.acknowledgeRecovery(input.restoreId, input.acknowledgment), 200)
      } catch (error) {
        if (error instanceof Error && error.message === 'recovery_model_review_conflict')
          return c.json({ error: 'recovery_model_review_conflict' }, 409)
        throw error
      }
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/budget',
      responses: {
        200: {
          description: 'Budget accounting coverage; held amounts are not confirmed invoices',
          content: { 'application/json': { schema: BudgetCoverageSchema } },
        },
        401: { description: 'Authentication required' },
      },
    }),
    (c) => c.json(budget.coverage(), 200),
  )
}

const RecoverySchema = z.object({
  restoreId: z.string().optional(),
  held: z.boolean(),
  restorationCoverage: z.enum(['none', 'unknown', 'manually_acknowledged']),
})
