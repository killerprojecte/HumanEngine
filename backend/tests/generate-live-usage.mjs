import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { normalizeProviderUsage } from '../../src/models.ts'
import { Store } from '../../src/store.ts'

const providerId = 'synthetic-meter'
const jsonCalls = [
  { role: 'planner', modelId: 'planner-test', raw: { prompt_tokens: 100, completion_tokens: 8,
    total_tokens: 108, prompt_cache_hit_tokens: 60, prompt_cache_miss_tokens: 40 } },
  { role: 'planner', modelId: 'planner-test', raw: { prompt_tokens: 100, completion_tokens: 9, total_tokens: 109 } },
  { role: 'replyer', modelId: 'replyer-test', raw: { prompt_tokens: 30, completion_tokens: 7, total_tokens: 37 } },
]
const sseCalls = [
  jsonCalls[0],
  { role: 'planner', modelId: 'planner-test', raw: { prompt_tokens: 100, completion_tokens: 6,
    total_tokens: 106, prompt_cache_hit_tokens: 0, prompt_cache_miss_tokens: 100 } },
  jsonCalls[1],
  { role: 'planner', modelId: 'planner-test', raw: null },
  { role: 'replyer', modelId: 'replyer-test', raw: { ...jsonCalls[2].raw,
    prompt_cache_hit_tokens: 15, prompt_cache_miss_tokens: 15 } },
]
for (const [calls, stream] of [[jsonCalls, false], [sseCalls, true]]) {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const token = 'synthetic-token'
  try {
    const app = createApp(store, { token, engine })
    for (const call of calls) {
      store.recordModel({ event: 'model.completed', role: call.role, modelId: call.modelId, providerId,
        durationMs: 1, usage: call.raw === null ? undefined : normalizeProviderUsage(call.raw, 'openai', false),
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
      assert.equal(planner.inputTokens, stream ? 300 : 200)
      assert.equal(planner.reportedInputTokens, stream ? 300 : 200)
      assert.equal(planner.cacheRateInputTokens, stream ? 200 : 100)
      assert.equal(planner.cacheRateReadTokens, 60)
      assert.equal(planner.ordinaryInputTokens, stream ? 140 : 40)
      assert.equal(planner.cacheRateKnownCalls, stream ? 2 : 1)
      const replyer = body.find((row) => row.role === 'replyer')
      assert.equal(replyer.reportedInputTokens, 30)
      if (stream) {
        assert.equal(planner.calls, 4)
        assert.equal(planner.reportedUsageCalls, 3)
        assert.equal(planner.unknownUsageCalls, 1)
        assert.equal(replyer.cacheRateReadTokens, 15)
        assert.equal(replyer.cacheRateInputTokens, 30)
      }
      views.push({ path, body })
    }
    assert.deepEqual(views[0].body, views[1].body)
    const output = JSON.stringify({ node: process.version, providerId, calls, views }, null, 2) + '\n'
    const file = new URL(stream ? '../internal/app/testdata/live-usage-sse-source.json' :
      '../internal/app/testdata/live-usage-source.json', import.meta.url)
    if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
    else writeFileSync(file, output)
    console.log('PASS actual Store.recordModel and authenticated all-time/scoped usage')
  } finally {
    engine.dispose()
    store.close()
  }
}
