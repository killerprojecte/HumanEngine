import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import { ref, computed, watch, nextTick, effectScope, createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'

// Executes the real page setup with transport/lifecycle stubs; not rendered browser QA.
const filename = new URL('../app/pages/models.vue', import.meta.url)
const descriptor = parse(readFileSync(filename, 'utf8'), { filename: filename.pathname }).descriptor
const code = transformSync(compileScript(descriptor, { id: 'usage-page-test' }).content, {
  loader: 'ts',
  format: 'cjs',
}).code
const require = createRequire(filename)
function page(get: (...args: any[]) => Promise<any>) {
  const module = { exports: {} as any }
  const noop = () => undefined
  new Function(
    'require',
    'module',
    'exports',
    'ref',
    'computed',
    'watch',
    'onMounted',
    'onBeforeUnmount',
    'onBeforeRouteLeave',
    'useI18n',
    'useApi',
    code,
  )(
    require,
    module,
    module.exports,
    ref,
    computed,
    watch,
    noop,
    noop,
    noop,
    () => ({ t: (key: string) => key, locale: ref('en') }),
    () => ({ client: { GET: get } }),
  )
  const scope = effectScope()
  return {
    state: scope.run(() => module.exports.default.setup({}, { expose: noop })),
    close: () => scope.stop(),
  }
}
function reply(calls: number, incomplete = false) {
  return {
    data: [
      {
        day: '2026-11-04',
        calls,
        inputTokens: calls * 100,
        outputTokens: 20,
        cacheReadInputTokens: calls * 50,
        cacheWriteInputTokens: 0,
        unknownUsageCalls: 0,
        cacheReadKnownCalls: calls,
        cacheWriteKnownCalls: calls,
        estimatedUsd: 0.1,
        pricedCalls: calls,
        partialPricedCalls: 0,
      },
    ],
    response: new Response(null, { headers: { 'X-Usage-Incomplete': String(incomplete) } }),
  }
}

test('range switching ignores out-of-order results and keeps totals/timeline on the latest response', async () => {
  const pending: Array<(value: any) => void> = []
  const queries: any[] = []
  const { state, close } = page(async (_path, options) => {
    queries.push(options.params.query)
    return new Promise((resolve) => pending.push(resolve))
  })
  try {
    const first = state.loadUsage()
    state.usagePreset.value = '7d'
    await nextTick()
    assert.equal(pending.length, 4)
    assert.equal(queries[1].bucket, 'hour')
    assert.equal(queries[3].bucket, 'day')
    pending[2]!(reply(7))
    pending[3]!(reply(7))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(state.usage.value[0].calls, 7)
    assert.equal(state.usageBusy.value, false)
    pending[0]!(reply(24, true))
    pending[1]!(reply(24, true))
    await first
    assert.equal(state.usage.value[0].calls, 7)
    assert.equal(state.timeline.value[0].calls, 7)
    assert.equal(state.usageIncomplete.value, false)
  } finally {
    close()
  }
})

test('custom ranges validate before fetching and propagate incomplete/unknown accounting', async () => {
  let calls = 0
  const { state, close } = page(async () => {
    calls++
    return reply(2, true)
  })
  try {
    state.usagePreset.value = 'custom'
    await nextTick()
    await state.loadUsage()
    assert.equal(calls, 0)
    assert.equal(state.usageError.value, 'usageInvalidRange')
    state.customStart.value = '2026-11-03T00:00'
    state.customEnd.value = '2026-11-04T00:00'
    await state.loadUsage()
    assert.equal(calls, 2)
    assert.equal(state.usageIncomplete.value, true)
    assert.equal(state.timelineIncomplete.value, true)
    assert.equal(
      state.cacheRate({ input: 200, read: 100, calls: 2, known: 0, readKnown: 2 }),
      '50.0%',
    )
    assert.equal(
      state.cacheRate({ input: 200, read: 100, calls: 2, known: 0, readKnown: 1 }),
      'usageCounterUnknown',
    )
    assert.equal(
      state.cacheRate({ input: 0, read: 0, calls: 0, known: 0, readKnown: 0 }),
      'usageCounterUnknown',
    )
  } finally {
    close()
  }
})

test('transport failure clears stale rows and loading state', async () => {
  const { state, close } = page(async () => {
    throw new Error('synthetic network failure')
  })
  try {
    state.usage.value = reply(1).data
    await state.loadUsage()
    assert.deepEqual(state.usage.value, [])
    assert.deepEqual(state.timeline.value, [])
    assert.equal(state.usageBusy.value, false)
    assert.equal(state.usageError.value, 'networkError')
  } finally {
    close()
  }
})

test('rendered chart marks unknown counters instead of drawing zero bars, and labels partial subtotals', async () => {
  const template = compileTemplate({
    source: descriptor.template!.content,
    filename: filename.pathname,
    id: 'usage-render-test',
  })
  assert.deepEqual(template.errors, [])
  const output = { exports: {} as any }
  new Function(
    'require',
    'module',
    'exports',
    transformSync(template.code, { loader: 'ts', format: 'cjs' }).code,
  )(require, output, output.exports)
  const { state, close } = page(async () => reply(2))
  try {
    state.section.value = 'usage'
    state.chartMode.value = 'bar'
    const unknown = {
      ...reply(2).data[0],
      inputTokens: 999,
      outputTokens: 999,
      unknownUsageCalls: 2,
      cacheReadKnownCalls: 0,
      cacheWriteKnownCalls: 0,
      cacheReadInputTokens: 0,
    }
    state.timeline.value = [unknown]
    const render = async () => {
      const app = createSSRApp({ setup: () => ({ ...state }), render: output.exports.render })
      app.config.warnHandler = () => undefined
      app.component('UModal', { render: () => null })
      return renderToString(app)
    }
    let html = await render()
    assert.doesNotMatch(html, /data-segment=/)
    assert.match(html, /title="[^"]*usageInput: usageCounterUnknown/)
    assert.equal(
      state.timelineMax.value,
      1,
      'unknown token counts must not distort the chart scale',
    )
    assert.equal((html.match(/data-unknown="true"/g) ?? []).length, 1)
    state.timeline.value = [
      { ...unknown, unknownUsageCalls: 0, cacheReadKnownCalls: 1, cacheReadInputTokens: 50 },
    ]
    html = await render()
    assert.match(html, /data-segment="input"/)
    assert.doesNotMatch(html, /data-segment="read"/)
    assert.doesNotMatch(html, /data-segment="write"/)
    assert.match(html, /title="[^"]*usageCacheRead: usagePartialCounter/)
    state.timeline.value = [
      {
        ...reply(2).data[0],
        inputTokens: 10000,
        outputTokens: 2000,
        cacheReadInputTokens: 6000,
        cacheWriteInputTokens: 1000,
      },
    ]
    html = await render()
    assert.equal(state.timelineMax.value, 10000)
    assert.equal((html.match(/data-segment=/g) ?? []).length, 4)
    assert.match(html, /data-segment="input" style="height:100%;"/)
    assert.match(html, /role="img"[^>]*aria-label="[^"]*usageInput: 10000/)
    assert.match(html, /title="10000">10K/)
    state.chartMode.value = 'line'
    html = await render()
    assert.equal((html.match(/data-series=/g) ?? []).length, 4)
    assert.doesNotMatch(html, /data-segment=/)
    assert.match(html, /polyline/)
    state.timeline.value = Array.from({ length: 49 }, (_, i) => ({
      ...reply(2).data[0],
      day: new Date(Date.UTC(2026, 10, 1, i)).toISOString(),
    }))
    html = await render()
    assert.match(html, /min-width:1764px/)
    state.timeline.value = [
      {
        ...reply(2).data[0],
        inputTokens: 10000,
        outputTokens: 2000,
        cacheReadInputTokens: 6000,
        cacheWriteInputTokens: 1000,
      },
    ]

    state.chartMode.value = 'bar'
    html = await render()
    assert.equal((html.match(/data-segment=/g) ?? []).length, 4)
  } finally {
    close()
  }
})

test('Planner cache metrics stay role-scoped and token-weighted while costs and token charts include every role', () => {
  const { state, close } = page(async () => reply(1))
  const base = reply(1).data[0]!
  const plannerA = {
    ...base,
    role: 'planner',
    modelId: 'shared-model',
    providerId: 'provider-a',
    apiModelName: 'api-model',
    day: '2026-11-04T08:00:00Z',
    inputTokens: 100,
    cacheReadInputTokens: 90,
    estimatedUsd: 1,
  }
  const plannerB = {
    ...plannerA,
    providerId: 'provider-b',
    inputTokens: 900,
    cacheReadInputTokens: 90,
    estimatedUsd: 2,
  }
  const replyer = {
    ...plannerA,
    role: 'replyer',
    inputTokens: 10000,
    cacheReadInputTokens: 0,
    estimatedUsd: 3,
  }
  const vision = {
    ...replyer,
    role: 'vision',
    day: '2026-11-04T09:00:00Z',
    inputTokens: 20000,
    estimatedUsd: 4,
  }
  const setRows = (rows: (typeof plannerA)[]) => {
    state.usage.value = rows
    state.timeline.value = rows
  }
  try {
    setRows([plannerA, plannerB, replyer, vision])
    assert.equal(state.usageTotals.value.usd, 10)
    assert.equal(state.usageTotals.value.input, 31000)
    assert.equal(state.usageTotals.value.calls, 4)
    assert.equal(state.plannerTotals.value.input, 1000)
    assert.equal(state.plannerTotals.value.read, 180)
    assert.equal(state.plannerTotals.value.calls, 2)
    assert.equal(state.plannerCacheRate(state.plannerTotals.value), '18.0%')
    assert.equal(state.plannerGroups.value.length, 2)
    assert.deepEqual(
      state.plannerGroups.value.map((group: any) => [
        group.providerId,
        state.plannerCacheRate(group),
      ]),
      [
        ['provider-a', '90.0%'],
        ['provider-b', '10.0%'],
      ],
    )
    const [first, second] = state.timelineRows.value
    assert.equal(first.input, 11000)
    assert.equal(state.plannerCacheRate(first.planner), '18.0%')
    assert.match(state.timelineTooltip(first), /usagePlannerCacheRate: 18\.0%/)
    assert.equal(second.input, 20000)
    assert.equal(state.plannerCacheRate(second.planner), 'usagePlannerNoCalls')
    assert.match(state.timelineTooltip(second), /usagePlannerCacheRate: usagePlannerNoCalls/)
    assert.equal(state.plannerCacheRate(state.plannerTotals.value, true), 'usagePlannerIncomplete')

    setRows([plannerA, { ...plannerB, cacheReadKnownCalls: 0, cacheReadInputTokens: 0 }, replyer])
    assert.equal(state.plannerCacheRate(state.plannerTotals.value), 'usageCounterUnknown')
    assert.equal(state.plannerCacheRate(state.timelineRows.value[0].planner), 'usageCounterUnknown')
    setRows([{ ...plannerA, unknownUsageCalls: 1 }])
    assert.equal(state.plannerCacheRate(state.plannerTotals.value), 'usageCounterUnknown')
    setRows([replyer, vision])
    assert.equal(state.usageTotals.value.usd, 7)
    assert.equal(state.plannerCacheRate(state.plannerTotals.value), 'usagePlannerNoCalls')
    assert.deepEqual(state.plannerGroups.value, [])
    setRows([{ ...plannerA, cacheReadInputTokens: 0 }])
    assert.equal(
      state.plannerCacheRate(state.plannerTotals.value),
      '0.0%',
      'explicit known zero remains distinct from missing data',
    )
  } finally {
    close()
  }
})

test('Planner coverage headers do not inherit unrelated historical gaps and chart requests carry browser timezone', async () => {
  const queries: any[] = []
  const { state, close } = page(async (_path, options) => {
    queries.push(options.params.query)
    const result = reply(1, true)
    result.response.headers.set('X-Usage-Planner-Incomplete', 'false')
    return result
  })
  try {
    await state.loadUsage()
    assert.equal(state.usageIncomplete.value, true)
    assert.equal(state.timelineIncomplete.value, true)
    assert.equal(state.plannerUsageIncomplete.value, false)
    assert.equal(state.plannerTimelineIncomplete.value, false)
    assert.equal(queries[1].timeZone, Intl.DateTimeFormat().resolvedOptions().timeZone)
    assert.equal(queries[0].timeZone, undefined, 'summary preserves original historical-day totals')
  } finally {
    close()
  }
})
