import test from 'node:test'
import assert from 'node:assert/strict'
import { budgetPreset } from '../src/budget/presets.ts'
import { BudgetSettingsSchema } from '../src/budget/settings.ts'
test('small presets only fill bounded draft limits, never enable spending and never mutate a shared default', () => {
  for (const name of ['strict_small', 'estimated_small'] as const) {
    const value = budgetPreset(name)
    assert.equal('budgetExperimentEnabled' in value, false)
    assert.equal(value.budgetMode, name === 'strict_small' ? 'strict' : 'best_effort')
    const parsed = BudgetSettingsSchema.parse(value)
    assert.equal(parsed.budgetExperimentEnabled, false)
    assert.equal(parsed.budgetDailyUsd, 0.05)
    assert.equal(parsed.budgetPerTurnCalls, 2)
    assert.equal(parsed.budgetMaxOutputTokens, 512)
    value.budgetDailyUsd = 999
    assert.equal(budgetPreset(name).budgetDailyUsd, 0.05)
  }
  assert.throws(() => budgetPreset('unknown' as never), /budget_preset_unknown/)
})
