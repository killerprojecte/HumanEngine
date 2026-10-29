import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer, type ServerResponse } from 'node:http'
import Database from 'better-sqlite3'
import { Router, type RouterConfig, type ModelLog } from '../src/models.ts'
import { BudgetLedger } from '../src/budget/ledger.ts'
import {
  NativeBudgetGate,
  withBudgetContext,
  type NativeBudgetOptions,
} from '../src/budget/native-attempt.ts'
import { priceKeys } from '../src/budget/estimate.ts'

const rawUsage = {
  prompt_tokens: 10,
  completion_tokens: 2,
  total_tokens: 12,
  prompt_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 },
}
const chat = {
  choices: [{ finish_reason: 'stop', message: { content: 'synthetic reply' } }],
  usage: rawUsage,
}
async function server(
  handler: (body: Record<string, unknown>, res: ServerResponse, count: number) => void,
) {
  const bodies: Record<string, unknown>[] = []
  const app = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString())
    bodies.push(body)
    handler(body, res, bodies.length)
  })
  await new Promise<void>((resolve) => app.listen(0, '127.0.0.1', resolve))
  return {
    bodies,
    url: `http://127.0.0.1:${(app.address() as { port: number }).port}`,
    close: async () => {
      app.closeAllConnections()
      await new Promise<void>((resolve) => app.close(() => resolve()))
    },
  }
}
function json(res: ServerResponse, body: unknown) {
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
function config(url: string, protocol = 'openai-chat'): RouterConfig {
  return {
    providers: [
      {
        id: 'p',
        name: 'synthetic',
        baseUrl: url,
        protocol,
        credentialEnv: '',
        retryCount: 0,
        timeoutMs: 1000,
      },
    ],
    models: [
      {
        id: 'm',
        displayName: 'synthetic',
        providerId: 'p',
        apiModelName: 'synthetic',
        capabilities: ['text', 'embedding', 'vision'],
        embeddingBatchSize: 1,
        ...Object.fromEntries(priceKeys.map((key) => [key, 1])),
      },
    ],
    routes: [{ role: 'replyer', orderedModelIds: ['m'], timeoutMs: 10000 }],
  }
}
function fixture(options: Partial<NativeBudgetOptions> = {}) {
  const db = new Database(':memory:')
  const ledger = new BudgetLedger(db, { bootId: 'test-boot' })
  const gate = new NativeBudgetGate({
    ledger,
    policy: () => ({ dailyNanoUsd: 1_000_000_000, maxUnresolvedNanoUsd: 1_000_000_000 }),
    mode: 'strict',
    maxOutputTokens: 64,
    contract: () => ({
      version: 'synthetic-contract-v1',
      outputCapHonored: true,
      inputCategories: ['inputUsdPerMillion', 'cacheReadUsdPerMillion', 'cacheWriteUsdPerMillion'],
    }),
    estimator: ({ outputCap, contract }) => ({
      source: 'verified_contract',
      estimatorVersion: 'synthetic-estimator-v1',
      contractVersion: contract.version,
      enforceable: true,
      inputUpperTokens: 1000,
      outputUpperTokens: outputCap,
      inputCategories: contract.inputCategories,
    }),
    ...options,
  })
  const rows = () =>
    (db.prepare('SELECT id FROM budget_attempts ORDER BY rowid').all() as { id: string }[]).map(
      (row) => ledger.get(row.id),
    )
  return { db, ledger, gate, rows }
}
const messages = [{ role: 'user' as const, content: 'synthetic request' }]

test('OFF preserves native bodies; enabled Chat and Responses carry real output caps and settled snapshots', async () => {
  for (const protocol of ['openai-chat', 'openai-responses']) {
    const service = await server((_body, res) =>
      json(
        res,
        protocol === 'openai-chat'
          ? chat
          : {
              status: 'completed',
              output: [
                {
                  type: 'message',
                  status: 'completed',
                  content: [{ type: 'output_text', text: 'synthetic reply' }],
                },
              ],
              usage: {
                input_tokens: 10,
                output_tokens: 2,
                total_tokens: 12,
                input_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 },
              },
            },
      ),
    )
    const f = fixture()
    try {
      const cfg = config(service.url, protocol)
      await new Router(cfg).complete('replyer', messages)
      assert.equal('max_completion_tokens' in service.bodies[0]!, false)
      assert.equal('max_output_tokens' in service.bodies[0]!, false)
      const router = new Router(cfg, undefined, undefined, undefined, undefined, undefined, f.gate)
      await withBudgetContext({ turnId: 'turn-one', lane: 'foreground' }, () =>
        router.complete('replyer', messages),
      )
      const key = protocol === 'openai-chat' ? 'max_completion_tokens' : 'max_output_tokens'
      assert.equal(service.bodies[1]![key], 64)
      const attempt = f.rows()[0]!
      assert.equal(attempt.state, 'complete')
      assert.equal(attempt.request.turnId, 'turn-one')
      assert.equal(attempt.request.estimate.enforceable, true)
      assert.equal(attempt.knownNanoUsd, 12_000)
      assert.equal(attempt.heldNanoUsd, 0)
    } finally {
      f.db.close()
      await service.close()
    }
  }
})

test('strict unsupported estimator/cap and opaque/custom inputs refuse before transmission', async () => {
  const service = await server((_body, res) => json(res, chat))
  try {
    for (const options of [
      { estimator: undefined },
      {
        contract: () => ({
          version: 'synthetic',
          outputCapHonored: false,
          inputCategories: ['inputUsdPerMillion'] as const,
        }),
      },
    ]) {
      const f = fixture(options as Partial<NativeBudgetOptions>)
      const logs: ModelLog[] = []
      try {
        await assert.rejects(
          new Router(
            config(service.url),
            (e) => logs.push(e),
            undefined,
            undefined,
            undefined,
            undefined,
            f.gate,
          ).complete('replyer', messages),
        )
        assert.equal(f.rows().length, 0)
        assert.ok(logs.some((e) => e.code?.startsWith('budget_')))
      } finally {
        f.db.close()
      }
    }
    const f = fixture()
    try {
      await assert.rejects(
        new Router(
          config(service.url),
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          f.gate,
        ).complete('replyer', [
          { role: 'user', content: 'image', images: ['data:image/png;base64,synthetic'] },
        ]),
      )
      await assert.rejects(
        new Router(
          config(service.url, 'custom-fixture'),
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          f.gate,
        ).complete('replyer', messages),
      )
      assert.equal(f.rows().length, 0)
    } finally {
      f.db.close()
    }
    assert.equal(service.bodies.length, 0)
  } finally {
    await service.close()
  }
})

test('each provider retry has its own reservation and failed transport retains unknown hold', async () => {
  const service = await server((_body, res, count) => {
    if (count === 1) {
      res.statusCode = 429
      res.setHeader('Retry-After', '0')
      res.end('synthetic throttle')
    } else json(res, chat)
  })
  const f = fixture()
  try {
    const cfg = config(service.url)
    cfg.providers[0]!.retryCount = 1
    await new Router(cfg, undefined, undefined, undefined, undefined, undefined, f.gate).complete(
      'replyer',
      messages,
    )
    const rows = f.rows()
    assert.equal(rows.length, 2)
    assert.equal(rows[0]!.state, 'unknown')
    assert.ok(rows[0]!.heldNanoUsd > 0)
    assert.equal(rows[1]!.state, 'complete')
    assert.equal(rows[0]!.request.requestId, rows[1]!.request.requestId)
    assert.deepEqual(
      rows.map((r) => r.request.attempt),
      [1, 2],
    )
  } finally {
    f.db.close()
    await service.close()
  }
})

test('embedding batches settle before vector validation without charging twice', async () => {
  const service = await server((_body, res, count) =>
    json(res, {
      data: [{ index: 0, embedding: count === 2 ? [1, 2, 3] : [1, 2] }],
      usage: { prompt_tokens: 4, total_tokens: 4 },
    }),
  )
  const f = fixture()
  try {
    await assert.rejects(
      new Router(
        config(service.url),
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        f.gate,
      ).embeddings('m', ['one', 'two']),
    )
    assert.equal(service.bodies.length, 2)
    assert.ok(service.bodies.every((body) => !('max_completion_tokens' in body)))
    const rows = f.rows()
    assert.equal(rows.length, 2)
    assert.ok(rows.every((row) => row.state === 'complete' && row.knownNanoUsd === 4000))
    assert.equal(rows[0]!.request.requestId, rows[1]!.request.requestId)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('partial cache-write reporting remains subtotal plus unresolved exposure', async () => {
  const service = await server((_body, res) =>
    json(res, {
      ...chat,
      usage: {
        prompt_tokens: 10,
        completion_tokens: 2,
        total_tokens: 12,
        prompt_tokens_details: { cached_tokens: 2 },
      },
    }),
  )
  const f = fixture()
  try {
    await new Router(
      config(service.url),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      f.gate,
    ).complete('replyer', messages)
    assert.equal(f.rows()[0]!.state, 'partial')
    assert.equal(f.rows()[0]!.knownNanoUsd, 4000)
    assert.ok(f.rows()[0]!.heldNanoUsd > 0)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('cancellation before transport creates no attempt; after transmission is unknown, not refunded', async () => {
  let entered!: () => void
  const received = new Promise<void>((resolve) => (entered = resolve))
  const service = await server((_body, _res) => entered())
  const f = fixture()
  try {
    const router = new Router(
      config(service.url),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      f.gate,
    )
    const before = new AbortController()
    before.abort()
    await assert.rejects(router.complete('replyer', messages, [], before.signal))
    assert.equal(f.rows().length, 0)
    const after = new AbortController()
    const call = router.complete('replyer', messages, [], after.signal)
    const rejected = assert.rejects(call)
    await received
    after.abort()
    await rejected
    assert.equal(f.rows()[0]!.state, 'unknown')
    assert.ok(f.rows()[0]!.heldNanoUsd > 0)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('best-effort explicitly records heuristic evidence and sends a cap without claiming a hard bound', async () => {
  const service = await server((_body, res) => json(res, chat))
  const f = fixture({ mode: 'best_effort', contract: undefined, estimator: undefined })
  try {
    await new Router(
      config(service.url),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      f.gate,
    ).complete('replyer', messages)
    const row = f.rows()[0]!
    assert.equal(row.request.estimate.source, 'conservative_estimate')
    assert.equal(row.request.estimate.enforceable, false)
    assert.equal(row.policy.mode, 'best_effort')
    assert.deepEqual(row.request.estimate.inputCategories, [
      'cacheReadUsdPerMillion',
      'cacheWrite12hUsdPerMillion',
      'cacheWrite30mUsdPerMillion',
      'cacheWriteUsdPerMillion',
      'inputUsdPerMillion',
    ])
    assert.equal(service.bodies[0]!.max_completion_tokens, 64)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('paid malformed business output is settled before parser rejects it, and next overrun call is blocked', async () => {
  const service = await server((_body, res) =>
    json(res, {
      choices: [
        {
          finish_reason: 'tool_calls',
          message: {
            tool_calls: [
              { id: 'bad', type: 'function', function: { name: 'unoffered', arguments: '{}' } },
            ],
          },
        },
      ],
      usage: { ...rawUsage, prompt_tokens: 1001, total_tokens: 1003 },
    }),
  )
  const f = fixture()
  try {
    const router = new Router(
      config(service.url),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      f.gate,
    )
    await assert.rejects(router.complete('replyer', messages))
    assert.equal(f.rows()[0]!.state, 'complete')
    assert.equal(f.rows()[0]!.overrun, true)
    await assert.rejects(router.complete('replyer', messages))
    assert.equal(service.bodies.length, 1)
    assert.equal(f.rows().length, 1)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('candidate fallback receives a distinct paid attempt and immutable model price snapshot', async () => {
  const service = await server((body, res) => {
    if (body.model === 'first') {
      res.statusCode = 400
      res.end('synthetic unavailable')
    } else json(res, chat)
  })
  const f = fixture()
  try {
    const cfg = config(service.url)
    cfg.models[0]!.apiModelName = 'first'
    cfg.models.push({
      ...cfg.models[0]!,
      id: 'second',
      apiModelName: 'second',
      inputUsdPerMillion: 2,
    })
    cfg.routes[0]!.orderedModelIds.push('second')
    const router = new Router(cfg, undefined, undefined, undefined, undefined, undefined, f.gate)
    cfg.models[1]!.inputUsdPerMillion = 999
    await router.complete('replyer', messages)
    const rows = f.rows()
    assert.deepEqual(
      rows.map((row) => row.request.modelId),
      ['m', 'second'],
    )
    assert.deepEqual(
      rows.map((row) => row.state),
      ['unknown', 'complete'],
    )
    assert.equal(rows[0]!.request.requestId, rows[1]!.request.requestId)
    assert.equal(rows[1]!.request.prices.inputUsdPerMillion, 2)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('abort between reservation and transport releases only the unstarted reservation', async () => {
  const service = await server((_body, res) => json(res, chat))
  const controller = new AbortController()
  const f = fixture({
    estimator: ({ outputCap, contract }) => {
      controller.abort()
      return {
        source: 'verified_contract',
        estimatorVersion: 'synthetic-v1',
        contractVersion: contract.version,
        enforceable: true,
        inputUpperTokens: 1000,
        outputUpperTokens: outputCap,
        inputCategories: contract.inputCategories,
      }
    },
  })
  try {
    await assert.rejects(
      new Router(
        config(service.url),
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        f.gate,
      ).complete('replyer', messages, [], controller.signal),
    )
    assert.equal(service.bodies.length, 0)
    assert.equal(f.rows().length, 1)
    assert.equal(f.rows()[0]!.state, 'released')
    assert.equal(f.rows()[0]!.heldNanoUsd, 0)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('budget denial surfaces a safe actionable code without retry/fallback or HTTP', async () => {
  const service = await server((_body, res) => json(res, chat))
  const f = fixture({ policy: () => ({ dailyNanoUsd: 0, maxUnresolvedNanoUsd: 0 }) })
  try {
    const cfg = config(service.url)
    cfg.providers[0]!.retryCount = 2
    cfg.models.push({ ...cfg.models[0]!, id: 'fallback' })
    cfg.routes[0]!.orderedModelIds.push('fallback')
    await assert.rejects(
      new Router(cfg, undefined, undefined, undefined, undefined, undefined, f.gate).complete(
        'replyer',
        messages,
      ),
      (error: unknown) =>
        !!error &&
        typeof error === 'object' &&
        'code' in error &&
        error.code === 'budget_daily_limit',
    )
    assert.equal(service.bodies.length, 0)
    assert.equal(f.rows().length, 0)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('arbitrary estimator exception text is not propagated as a budget reason or cause', async () => {
  const service = await server((_body, res) => json(res, chat))
  const f = fixture({
    estimator: () => {
      throw new Error('PRIVATE_SECRET_CANARY')
    },
  })
  try {
    await assert.rejects(
      new Router(
        config(service.url),
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        f.gate,
      ).complete('replyer', messages),
      (error: unknown) => {
        assert.ok(error instanceof Error)
        assert.equal(error.message, 'budget_admission_failed')
        assert.ok(!String(error.cause).includes('PRIVATE_SECRET_CANARY'))
        return true
      },
    )
    assert.equal(service.bodies.length, 0)
  } finally {
    f.db.close()
    await service.close()
  }
})

test('one Router re-resolves budget supplier across logical calls and OFF-to-ON retry', async () => {
  let enabled = false
  const f = fixture()
  const service = await server((_body, res, count) => {
    if (count === 3) {
      enabled = true
      res.statusCode = 429
      json(res, { error: 'synthetic retry' })
      return
    }
    json(res, chat)
  })
  try {
    const cfg = config(service.url)
    cfg.providers[0]!.retryCount = 1
    const router = new Router(cfg, undefined, undefined, undefined, undefined, undefined, () =>
      enabled ? f.gate : undefined,
    )
    await router.complete('replyer', messages)
    enabled = true
    await router.complete('replyer', messages)
    enabled = false
    await router.complete('replyer', messages)
    assert.deepEqual(
      service.bodies.map((body) => body.max_completion_tokens),
      [undefined, 64, undefined, 64],
    )
    assert.equal(f.rows().length, 2)
    assert.ok(f.rows().every((row) => row!.state === 'complete'))
    assert.notEqual(f.rows()[0]!.request.requestId, f.rows()[1]!.request.requestId)
  } finally {
    await service.close()
    f.db.close()
  }
})

test('queued physical attempt sees enabled budget and lowered current limits before HTTP', async () => {
  const { modelScheduler } = await import('../src/scheduler.ts')
  const { BudgetService } = await import('../src/budget/service.ts')
  const db = new Database(':memory:')
  const store = { db, settings: {} as Record<string, unknown> }
  const budget = new BudgetService(store)
  const service = await server((_body, res) => json(res, chat))
  let release!: () => void
  const blocked = new Promise<void>((resolve) => {
    release = resolve
  })
  let entered = 0,
    allEntered!: () => void
  const enteredPromise = new Promise<void>((resolve) => {
    allEntered = resolve
  })
  const holders = Array.from({ length: 4 }, () =>
    modelScheduler.run(0, undefined, async () => {
      if (++entered === 4) allEntered()
      await blocked
    }),
  )
  try {
    await enteredPromise
    const router = new Router(
      config(service.url),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      () => budget.gate(),
    )
    const pending = router.complete('replyer', messages)
    // Logical work is queued behind all four occupied physical slots.
    store.settings = {
      budgetExperimentEnabled: true,
      budgetMode: 'best_effort',
      budgetDailyUsd: 0.000001,
      budgetMaxUnresolvedUsd: 0.000001,
      budgetPerTurnUsd: 0.000001,
    }
    release()
    await Promise.all(holders)
    await assert.rejects(pending, /budget_daily_limit/)
    assert.equal(service.bodies.length, 0)
  } finally {
    release()
    await Promise.all(holders)
    await service.close()
    db.close()
  }
})

test('retry uses updated output cap and quota while started receipt retains original snapshot', async () => {
  const { BudgetService } = await import('../src/budget/service.ts')
  const db = new Database(':memory:')
  const store = {
    db,
    settings: {
      budgetExperimentEnabled: true,
      budgetMode: 'best_effort',
      budgetMaxOutputTokens: 64,
    } as Record<string, unknown>,
  }
  const budget = new BudgetService(store)
  const service = await server((_body, res, count) => {
    if (count === 1) {
      store.settings = { ...store.settings, budgetMaxOutputTokens: 16 }
      res.statusCode = 429
      json(res, { error: 'retry' })
      return
    }
    json(res, chat)
  })
  try {
    const cfg = config(service.url)
    cfg.providers[0]!.retryCount = 1
    await new Router(cfg, undefined, undefined, undefined, undefined, undefined, () =>
      budget.gate(),
    ).complete('replyer', messages)
    assert.deepEqual(
      service.bodies.map((body) => body.max_completion_tokens),
      [64, 16],
    )
    const rows = (
      db.prepare('SELECT id FROM budget_attempts ORDER BY rowid').all() as { id: string }[]
    ).map((row) => budget.ledger.get(row.id)!)
    assert.equal(rows[0]!.state, 'unknown')
    assert.equal(rows[1]!.state, 'complete')
    assert.equal(rows[0]!.request.estimate.outputUpperTokens, 64)
    assert.equal(rows[1]!.request.estimate.outputUpperTokens, 16)
    assert.equal(rows[0]!.request.requestId, rows[1]!.request.requestId)
    assert.deepEqual(
      rows.map((row) => row.request.attempt),
      [1, 2],
    )
  } finally {
    await service.close()
    db.close()
  }
})

test('custom completion and embeddings queued while OFF cannot execute after budget is enabled', async () => {
  const { modelScheduler } = await import('../src/scheduler.ts')
  const { ModelProtocolRegistry } = await import('../src/model-protocols.ts')
  for (const operation of ['complete', 'embeddings'] as const) {
    let enabled = false,
      calls = 0
    const f = fixture()
    const registry = new ModelProtocolRegistry()
    registry.register(
      'fixture:protocol',
      'Synthetic',
      {
        async complete() {
          calls++
          return {
            text: 'unused',
            toolCalls: [],
            usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
          }
        },
        async embeddings() {
          calls++
          return { vectors: [[1, 2]], usage: { inputTokens: 1, outputTokens: 0, totalTokens: 1 } }
        },
      },
      new AbortController().signal,
    )
    let release!: () => void,
      ready!: () => void,
      entered = 0
    const block = new Promise<void>((resolve) => {
      release = resolve
    })
    const all = new Promise<void>((resolve) => {
      ready = resolve
    })
    const holders = Array.from({ length: 4 }, () =>
      modelScheduler.run(0, undefined, async () => {
        if (++entered === 4) ready()
        await block
      }),
    )
    try {
      await all
      const router = new Router(
        config('http://127.0.0.1:1', 'fixture:protocol'),
        undefined,
        undefined,
        registry,
        undefined,
        undefined,
        () => (enabled ? f.gate : undefined),
      )
      const pending =
        operation === 'complete'
          ? router.complete('replyer', messages)
          : router.embeddings('m', ['synthetic'])
      for (let spin = 0; modelScheduler.status.queued === 0 && spin < 100; spin++)
        await new Promise<void>((resolve) => setImmediate(resolve))
      assert.equal(modelScheduler.status.queued, 1)
      enabled = true
      release()
      await Promise.all(holders)
      await assert.rejects(pending, /budget_protocol_unsupported/)
      assert.equal(calls, 0)
      assert.equal(f.rows().length, 0)
    } finally {
      release()
      await Promise.all(holders)
      f.db.close()
    }
  }
})

test('foreground query embeddings use reserved foreground slot; other context lanes remain bounded', async () => {
  const { modelScheduler, withModelPriority } = await import('../src/scheduler.ts')
  const { currentBudgetPriority } = await import('../src/budget/native-attempt.ts')
  assert.equal(currentBudgetPriority(1), 1)
  assert.equal(
    withBudgetContext({ turnId: 'q', lane: 'foreground' }, () => currentBudgetPriority(1)),
    0,
  )
  assert.equal(
    withModelPriority(2, () =>
      withBudgetContext({ turnId: 'q', lane: 'foreground' }, () => currentBudgetPriority(1)),
    ),
    2,
  )
  const f = fixture({
    contract: () => ({
      version: 'embedding-v1',
      outputCapHonored: true,
      inputCategories: ['inputUsdPerMillion'],
    }),
  })
  const service = await server((_body, res) =>
    json(res, {
      data: [{ index: 0, embedding: [1, 2] }],
      usage: { prompt_tokens: 1, total_tokens: 1 },
    }),
  )
  const router = new Router(
    config(service.url),
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    f.gate,
  )
  let release!: () => void,
    ready!: () => void,
    entered = 0
  const block = new Promise<void>((resolve) => {
    release = resolve
  })
  const all = new Promise<void>((resolve) => {
    ready = resolve
  })
  const holders = Array.from({ length: 3 }, () =>
    modelScheduler.run(1, undefined, async () => {
      if (++entered === 3) ready()
      await block
    }),
  )
  try {
    await all
    await withBudgetContext({ turnId: 'query', lane: 'foreground' }, () =>
      router.embeddings('m', ['synthetic']),
    )
    assert.equal(service.bodies.length, 1)
    assert.equal(f.rows()[0]!.request.lane, 'foreground')
    release()
    await Promise.all(holders)
    await withBudgetContext({ turnId: 'index', lane: 'realtime' }, () =>
      router.embeddings('m', ['synthetic']),
    )
    await withModelPriority(2, () =>
      withBudgetContext({ turnId: 'maintenance', lane: 'foreground' }, () =>
        router.embeddings('m', ['synthetic']),
      ),
    )
    await router.embeddings('m', ['synthetic'])
    assert.deepEqual(
      f.rows().map((row) => row!.request.lane),
      ['foreground', 'realtime', 'maintenance', 'realtime'],
    )
  } finally {
    release()
    await Promise.all(holders)
    await service.close()
    f.db.close()
  }
})
