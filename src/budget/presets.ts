import type { BudgetSettings } from './settings.ts'
export type BudgetPresetName = 'strict_small' | 'estimated_small'
/** Draft settings only. Never enables spending, learning, a provider or a task. */
export function budgetPreset(
  name: BudgetPresetName,
): Omit<BudgetSettings, 'budgetExperimentEnabled'> {
  if (name !== 'strict_small' && name !== 'estimated_small') throw Error('budget_preset_unknown')
  return {
    budgetMode: name === 'strict_small' ? 'strict' : 'best_effort',
    budgetDailyUsd: 0.05,
    budgetMaxUnresolvedUsd: 0.005,
    budgetPerTurnUsd: 0.005,
    budgetPerTurnTokens: 4096,
    budgetPerTurnCalls: 2,
    budgetMaxOutputTokens: 512,
    budgetRealtimeRatio: 0.1,
    budgetMaintenanceRatio: 0.1,
  }
}
