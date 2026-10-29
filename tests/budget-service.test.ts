import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { OpenAPIHono } from '@hono/zod-openapi'
import { BudgetSettingsSchema } from '../src/budget/settings.ts'
import { BudgetService } from '../src/budget/service.ts'
import { registerBudgetRoutes, BudgetCoverageSchema } from '../src/budget/admin-routes.ts'
import { withBudgetContext } from '../src/budget/native-attempt.ts'

const descriptor = {
  providerId: 'synthetic',
  modelId: 'model',
  protocol: 'openai-chat',
  operation: 'complete' as const,
  role: 'replyer',
  prices: {
    inputUsdPerMillion: 1,
    outputUsdPerMillion: 1,
    cacheReadUsdPerMillion: 1,
    cacheWriteUsdPerMillion: 1,
    cacheWrite30mUsdPerMillion: 1,
    cacheWrite12hUsdPerMillion: 1,
  },
}
function fixture(settings: unknown = {}) {
  const db = new Database(':memory:')
  const store = { db, settings }
  return {
    db,
    store,
    service: new BudgetService(store, {
      bootId: 'boot',
      now: () => Date.parse('2026-10-28T12:00:00Z'),
    }),
  }
}
test('budget experiment defaults OFF, finite limits and foreground reservation validated', () => {
  const f = fixture()
  try {
    assert.equal(f.service.gate(), undefined)
    assert.equal(f.service.coverage().enabled, false)
    assert.equal(BudgetSettingsSchema.parse({}).budgetMode, 'strict')
    for (const settings of [
      { budgetDailyUsd: Infinity },
      { budgetRealtimeRatio: 0.4, budgetMaintenanceRatio: 0.2 },
      { budgetPerTurnUsd: 2 },
      { budgetMaxUnresolvedUsd: 2 },
    ])
      assert.equal(BudgetSettingsSchema.safeParse(settings).success, false)
  } finally {
    f.db.close()
  }
})
test('strict fails closed; reservations retain snapshots but new reservations check live configuration', () => {
  const f = fixture({ budgetExperimentEnabled: true })
  try {
    assert.throws(
      () => f.service.gate()!.prepare(descriptor, {}),
      /budget_verified_estimator_required/,
    )
    f.store.settings = {
      budgetExperimentEnabled: true,
      budgetMode: 'best_effort',
      budgetMaxOutputTokens: 9,
    }
    const gate = f.service.gate()!
    const attempt = withBudgetContext({ turnId: 'turn', lane: 'foreground' }, () =>
      gate.prepare(descriptor, {}),
    )
    assert.equal(attempt.body.max_completion_tokens, 9)
    f.store.settings = { budgetExperimentEnabled: false, budgetMaxOutputTokens: 99 }
    assert.throws(() => gate.prepare(descriptor, {}), /budget_configuration_changed/)
    assert.equal(f.service.coverage().pendingAttempts, 1)
    attempt.fail()
    assert.equal(f.service.coverage().pendingAttempts, 0)
    assert.equal(f.service.gate(), undefined)
  } finally {
    f.db.close()
  }
})
test('injected verified registry permits strict reservation without a pretend UI trust flag', () => {
  const f = fixture({ budgetExperimentEnabled: true })
  try {
    const service = new BudgetService(f.store, {
      bootId: 'verified',
      contract: () => ({
        version: 'synthetic-v1',
        outputCapHonored: true,
        inputCategories: ['inputUsdPerMillion'],
      }),
      estimator: ({ outputCap }) => ({
        source: 'verified_contract',
        enforceable: true,
        estimatorVersion: 'synthetic-v1',
        contractVersion: 'synthetic-v1',
        inputUpperTokens: 1,
        outputUpperTokens: outputCap,
        inputCategories: ['inputUsdPerMillion'],
      }),
    })
    const attempt = service.gate()!.prepare(descriptor, {})
    attempt.start()
    attempt.settle({
      inputTokens: 1,
      outputTokens: 1,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    })
    assert.equal(service.coverage().knownNanoUsd, 2000)
    assert.equal(service.coverage().coverage, 'settled')
  } finally {
    f.db.close()
  }
})
test('new boot never auto-releases pending old boot; API is aggregate-only and inherits gateway auth', async () => {
  const f = fixture({ budgetExperimentEnabled: true, budgetMode: 'best_effort' })
  try {
    const attempt = f.service.gate()!.prepare(descriptor, {})
    attempt.start()
    attempt.fail()
    const next = new BudgetService(f.store, { bootId: 'next' })
    assert.equal(next.coverage().unknownAttempts, 1)
    const app = new OpenAPIHono()
    app.use('/api/*', async (c, next) => {
      if (c.req.header('x-test-auth') !== 'yes') return c.json({ error: 'unauthorized' }, 401)
      await next()
    })
    registerBudgetRoutes(app, { budget: next })
    assert.equal((await app.request('/api/budget')).status, 401)
    const response = await app.request('/api/budget', { headers: { 'x-test-auth': 'yes' } })
    const data = await response.json()
    assert.equal(response.status, 200)
    assert.equal(BudgetCoverageSchema.safeParse(data).success, true)
    assert.equal(data.coverage, 'pending')
    assert.equal(JSON.stringify(data).includes('synthetic'), false)
    assert.equal(
      (await app.request('/api/budget', { method: 'POST', headers: { 'x-test-auth': 'yes' } }))
        .status,
      404,
    )
  } finally {
    f.db.close()
  }
})

test('coverage exposes unbounded strict liability without a false finite remaining allowance', () => {
  const f = fixture({ budgetExperimentEnabled: true })
  try {
    const service = new BudgetService(f.store, {
      bootId: 'verified',
      contract: () => ({
        version: 'v1',
        outputCapHonored: true,
        inputCategories: ['inputUsdPerMillion'],
      }),
      estimator: ({ outputCap }) => ({
        source: 'verified_contract',
        enforceable: true,
        estimatorVersion: 'v1',
        contractVersion: 'v1',
        inputUpperTokens: 1000,
        outputUpperTokens: outputCap,
        inputCategories: ['inputUsdPerMillion'],
      }),
    })
    const attempt = service
      .gate()!
      .prepare({ ...descriptor, prices: { inputUsdPerMillion: 1, outputUsdPerMillion: 1 } }, {})
    attempt.start()
    attempt.settle({
      inputTokens: 1000,
      outputTokens: 1,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 1000,
    })
    const view = service.coverage()
    assert.equal(view.coverage, 'unbounded')
    assert.equal(view.unboundedAttempts, 1)
    assert.equal(view.partialAttempts, 1)
    assert.equal(view.remainingUsd, 0)
    assert.equal(view.knownNanoUsd, 1000)
    assert.equal(BudgetCoverageSchema.safeParse(view).success, true)
  } finally {
    f.db.close()
  }
})

test('an existing gate applies lowered per-turn quota to new reservations without rewriting old policy', () => {
  const f = fixture({
    budgetExperimentEnabled: true,
    budgetMode: 'best_effort',
    budgetPerTurnCalls: 2,
  })
  try {
    const gate = f.service.gate()!
    withBudgetContext({ turnId: 'one-turn', lane: 'foreground' }, () => {
      const first = gate.prepare(descriptor, {})
      first.start()
      f.store.settings = {
        budgetExperimentEnabled: true,
        budgetMode: 'best_effort',
        budgetPerTurnCalls: 1,
      }
      assert.throws(() => gate.prepare(descriptor, {}), /budget_turn_calls/)
      first.settle({
        inputTokens: 1,
        outputTokens: 1,
        cacheKnown: true,
        cacheReadInputTokens: 0,
        cacheWriteInputTokens: 0,
      })
    })
    const row = f.db.prepare('SELECT id FROM budget_attempts').get() as { id: string }
    assert.equal(f.service.ledger.get(row.id)!.policy.perTurnCalls, 2)
    assert.equal(f.service.ledger.get(row.id)!.state, 'complete')
  } finally {
    f.db.close()
  }
})
