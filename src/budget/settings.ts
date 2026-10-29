import { z } from '@hono/zod-openapi'

// User-set spending limits, not provider prices. The experiment is opt-in.
const usd = (fallback: number) => z.number().finite().min(0.000001).max(100000).default(fallback)
export const budgetSettingsShape = {
  budgetExperimentEnabled: z.boolean().default(false),
  budgetMode: z.enum(['strict', 'best_effort']).default('strict'),
  budgetDailyUsd: usd(1),
  budgetMaxUnresolvedUsd: usd(0.25),
  budgetPerTurnUsd: usd(0.1),
  budgetPerTurnTokens: z.number().int().min(1).max(10000000).default(32768),
  budgetPerTurnCalls: z.number().int().min(1).max(1000).default(8),
  budgetMaxOutputTokens: z.number().int().min(1).max(1000000).default(2048),
  budgetRealtimeRatio: z.number().finite().min(0).max(0.5).default(0.2),
  budgetMaintenanceRatio: z.number().finite().min(0).max(0.5).default(0.1),
}
export type BudgetSettings = z.infer<z.ZodObject<typeof budgetSettingsShape>>
export function refineBudgetSettings(value: BudgetSettings, ctx: z.RefinementCtx): void {
  if (value.budgetRealtimeRatio + value.budgetMaintenanceRatio > 0.5)
    ctx.addIssue({
      code: 'custom',
      path: ['budgetMaintenanceRatio'],
      message: 'Reserve at least 50% of the daily budget for foreground requests',
    })
  if (value.budgetMaxUnresolvedUsd > value.budgetDailyUsd)
    ctx.addIssue({
      code: 'custom',
      path: ['budgetMaxUnresolvedUsd'],
      message: 'Unresolved limit must not exceed daily limit',
    })
  if (value.budgetPerTurnUsd > value.budgetDailyUsd)
    ctx.addIssue({
      code: 'custom',
      path: ['budgetPerTurnUsd'],
      message: 'Turn limit must not exceed daily limit',
    })
}
export const BudgetSettingsSchema = z.object(budgetSettingsShape).superRefine(refineBudgetSettings)
