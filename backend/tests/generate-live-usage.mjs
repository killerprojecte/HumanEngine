import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { normalizeProviderUsage } from '../../src/models.ts'
import { Store } from '../../src/store.ts'

const providerId = 'synthetic-meter'
const calls = [
  { role: 'planner', modelId: 'planner-test', raw: { prompt_tokens: 100, completion_tokens: 8,
    total_tokens: 108, prompt_cache_hit_tokens: 60, prompt_cache_miss_tokens: 40 } },
  { role: 'planner', modelId: 'planner-test', raw: { prompt_tokens: 100, completion_tokens: 9, total_tokens: 109 } },
  { role: 'replyer', modelId: 'replyer-test', raw: { prompt_tokens: 30, completion_tokens: 7, total_tokens: 37 } },
]
const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
try {
  const app = createApp(store, { token, engine })
  for (const call of calls) {
    store.recordModel({ event: 'model.completed', role: call.role, modelId: call.modelId, providerId,
      durationMs: 1, usage: normalizeProviderUsage(call.raw, 'openai', false),
      pricing: { apiModelName: call.modelId, inputUsdPerMillion: 2, outputUsdPerMillion: 4, cacheReadUsdPerMillion: 1 } })
  }
  const paths = ['/api/usage', '/api/usage?start=2000-01-01T00%3A00%3A00Z&end=2099-01-01T00%3A00%3A00Z']
  const views = []
  for (const path of paths) {
    const response = await app.request('http://fixture.invalid' + path, { headers: { Authorization: 'Bearer '+token } })
    assert.equal(response.status, 200)
    const body = await response.json()
    for (const row of body) {
      assert.match(row.day, /^\d{4}-\d\d-\d\d$/)
      delete row.day
      delete row.durationMs
    }
    const planner = body.find((row) => row.role === 'planner')
    assert.equal(planner.inputTokens, 200)
    assert.equal(planner.reportedInputTokens, 200)
    assert.equal(planner.cacheRateInputTokens, 100)
    assert.equal(planner.cacheRateReadTokens, 60)
    assert.equal(planner.ordinaryInputTokens, 40)
    assert.equal(planner.cacheRateKnownCalls, 1)
    assert.equal(body.find((row) => row.role === 'replyer').reportedInputTokens, 30)
    views.push({ path, body })
  }
  assert.deepEqual(views[0].body, views[1].body)
  const output = JSON.stringify({ node: process.version, providerId, calls, views }, null, 2) + '\n'
  const file = new URL('../internal/app/testdata/live-usage-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
  else writeFileSync(file, output)
  console.log('PASS actual Store.recordModel and authenticated all-time/scoped usage')
} finally {
  engine.dispose()
  store.close()
}
