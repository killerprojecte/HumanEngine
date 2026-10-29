import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer, type ServerResponse } from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { createSessionGateway } from '../src/auth/session.ts'
import { withBudgetContext } from '../src/budget/native-attempt.ts'
import { BudgetCoverageSchema } from '../src/budget/admin-routes.ts'
import type { ChatMessage } from '../src/models.ts'
import type { Settings } from '../src/schema.ts'
import type { SceneRef } from '../src/onebot.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const origin = 'http://budget-fixture.local'
const adminToken = 'synthetic-budget-admin-token'
const credential = 'SYNTHETIC_PROVIDER_KEY_CANARY'
const prompt = 'SYNTHETIC_PROMPT_CANARY: no external model receives this fixture.'
const messages: ChatMessage[] = [{ role: 'user', content: prompt }]
const prices = {
  inputUsdPerMillion: 2,
  outputUsdPerMillion: 8,
  cacheReadUsdPerMillion: 0.5,
  cacheWriteUsdPerMillion: 4,
  cacheWrite5mUsdPerMillion: 2.5,
  cacheWrite1hUsdPerMillion: 4,
  cacheWrite30mUsdPerMillion: 3,
  cacheWrite12hUsdPerMillion: 5,
}
const usage = {
  prompt_tokens: 10,
  completion_tokens: 2,
  total_tokens: 12,
  prompt_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 },
}
const reply = {
  choices: [{ finish_reason: 'stop', message: { content: 'Synthetic local reply.' } }],
  usage,
}
function json(response: ServerResponse, body: unknown) {
  response.setHeader('Content-Type', 'application/json')
  response.end(JSON.stringify(body))
}
type NativeBody = Record<string, unknown>
type Handler = (body: NativeBody, response: ServerResponse, count: number) => void
async function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-budget-engine-'))
  const path = join(directory, 'synthetic.sqlite')
  const bodies: NativeBody[] = []
  const authorization: Array<string | undefined> = []
  let handler: Handler = (_body, response) => json(response, reply)
  const server = createServer(async (request, response) => {
    const chunks: Buffer[] = []
    for await (const chunk of request) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString()) as NativeBody
    bodies.push(body)
    authorization.push(request.headers.authorization)
    handler(body, response, bodies.length)
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const baseUrl = `http://127.0.0.1:${(server.address() as { port: number }).port}`
  let store: Store,
    engine: Engine,
    app: ReturnType<typeof createApp>,
    gateway: ReturnType<typeof createSessionGateway>
  let cookie = ''
  const open = () => {
    store = new Store(path)
    engine = new Engine(store)
    app = createApp(store, { token: adminToken, engine })
    gateway = createSessionGateway()
    cookie = ''
  }
  open()
  const raw = (route: string, init: RequestInit = {}) =>
    gateway(new Request(origin + route, init), async (request) => app.fetch(request), {
      token: adminToken,
    })
  const login = async () => {
    const response = await raw('/api/auth/session', {
      method: 'POST',
      headers: { 'X-HumanEngine-Session': '1', 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: adminToken }),
    })
    assert.equal(response.status, 200)
    cookie = response.headers.get('set-cookie')!.split(';')[0]!
  }
  const request = (route: string, init: RequestInit = {}) =>
    raw(route, {
      ...init,
      headers: {
        Cookie: cookie,
        'X-HumanEngine-Session': '1',
        'Content-Type': 'application/json',
        ...init.headers,
      },
    })
  const settings = async (patch: Partial<Settings>) => {
    const response = await request('/api/settings', {
      method: 'PUT',
      body: JSON.stringify({ ...store.settings, ...patch }),
    })
    assert.equal(response.status, 200, await response.clone().text())
    return response.json()
  }
  const configure = async (retryCount = 0) => {
    const response = await request('/api/models', {
      method: 'PUT',
      body: JSON.stringify({
        providers: [
          {
            id: 'loopback-provider',
            name: 'Synthetic loopback',
            protocol: 'openai-chat',
            baseUrl,
            credentialEnv: '',
            retryCount,
            timeoutMs: 2000,
          },
        ],
        models: [
          {
            id: 'loopback-model',
            displayName: 'Synthetic local model',
            providerId: 'loopback-provider',
            apiModelName: 'local-fixture',
            capabilities: ['text', 'tools'],
            ...prices,
          },
        ],
        routes: ['planner', 'replyer', 'learning'].map((role) => ({
          role,
          orderedModelIds: ['loopback-model'],
          timeoutMs: 5000,
        })),
        credentials: { 'loopback-provider': { action: 'set', value: credential } },
      }),
    })
    assert.equal(response.status, 200, await response.clone().text())
  }
  await login()
  await configure()
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    get cookie() {
      return cookie
    },
    bodies,
    authorization,
    raw,
    request,
    login,
    settings,
    configure,
    handler: (next: Handler) => {
      handler = next
    },
    rows: () =>
      (
        store.db.prepare('SELECT id FROM budget_attempts ORDER BY rowid').all() as { id: string }[]
      ).map(({ id }) => engine.budget.ledger.get(id)!),
    reopen: async () => {
      await engine.dispose()
      store.close()
      open()
    },
    close: async () => {
      await engine.dispose()
      store.close()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
      rmSync(directory, { recursive: true, force: true })
    },
  }
}
const hasCode = (code: string) => (error: unknown) =>
  !!error && typeof error === 'object' && 'code' in error && error.code === code
const bestEffort: Partial<Settings> = {
  budgetExperimentEnabled: true,
  budgetMode: 'best_effort',
  budgetMaxOutputTokens: 17,
}

async function configureEmbedding(h: Awaited<ReturnType<typeof fixture>>, count = 3) {
  h.store.saveScene({
    id: 'budget-index-scene',
    adapterId: 'synthetic',
    accountId: 'synthetic',
    platformSceneId: 'budget-index-scene',
    kind: 'group',
    name: 'Synthetic index scene',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const response = await h.request('/api/models', {
    method: 'PUT',
    body: JSON.stringify({
      ...h.store.models,
      models: h.store.models.models.map((model) => ({
        ...model,
        capabilities: [...model.capabilities, 'embedding'],
        embeddingBatchSize: 1,
        embeddingDimensions: 2,
      })),
      routes: [
        ...h.store.models.routes,
        { role: 'embedding', orderedModelIds: ['loopback-model'], timeoutMs: 5000 },
      ],
    }),
  })
  assert.equal(response.status, 200, await response.clone().text())
  const records = Array.from({ length: count }, (_, index) =>
    h.engine.memory.create({
      scope: { kind: 'scene', sceneId: 'budget-index-scene' },
      kind: 'fact',
      content: `Synthetic indexed memory ${index}.`,
    }),
  )
  h.handler((body, response) => {
    assert.ok(Array.isArray(body.input), 'only synthetic native embeddings are expected')
    assert.equal(body.input.length, 1, 'the configured batch size must reach the real router')
    json(response, {
      data: body.input.map((_input, index) => ({ index, embedding: [1, 0.5] })),
      usage: { prompt_tokens: 4, total_tokens: 4 },
    })
  })
  return records
}

test('Engine budget OFF preserves its native request body and creates no budget attempts', async () => {
  const h = await fixture()
  try {
    assert.equal(h.store.settings.budgetExperimentEnabled, false)
    const result = await h.engine.router().complete('replyer', messages)
    assert.equal(result.text, 'Synthetic local reply.')
    assert.equal(h.bodies.length, 1)
    assert.equal('max_completion_tokens' in h.bodies[0]!, false)
    assert.equal('max_output_tokens' in h.bodies[0]!, false)
    assert.deepEqual(h.rows(), [])
    assert.equal(h.authorization[0], `Bearer ${credential}`)
    const status = await h.request('/api/budget')
    assert.equal(status.status, 200)
    assert.equal((await status.json()).enabled, false)
  } finally {
    await h.close()
  }
})

test('strict Engine and administrator probe reject before HTTP without an injected verified registry', async () => {
  const h = await fixture()
  try {
    await h.settings({ budgetExperimentEnabled: true })
    assert.equal(h.store.settings.budgetMode, 'strict')
    await assert.rejects(
      h.engine.router().complete('replyer', messages),
      hasCode('budget_verified_estimator_required'),
    )
    const probe = await h.request('/api/models/loopback-model/probe', {
      method: 'POST',
      body: JSON.stringify({ capability: 'text' }),
    })
    assert.equal(probe.status, 200)
    const receipt = await probe.json()
    assert.equal(receipt.ok, false)
    assert.equal(receipt.detail, 'budget_verified_estimator_required')
    assert.equal(h.bodies.length, 0)
    assert.deepEqual(h.rows(), [])
  } finally {
    await h.close()
  }
})

test('best-effort settings reach Engine native caps, preserve all eight rates and expose only aggregate session-authenticated status', async () => {
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    await withBudgetContext({ turnId: 'engine-priced-turn', lane: 'foreground' }, () =>
      h.engine.router().complete('replyer', messages),
    )
    assert.equal(h.bodies[0]!.max_completion_tokens, 17)
    const row = h.rows()[0]!
    assert.deepEqual(row.request.prices, prices)
    assert.equal(row.state, 'complete')
    assert.equal(row.knownNanoUsd, 39000)
    assert.equal(row.heldNanoUsd, 0)
    assert.equal(row.request.estimate.enforceable, false)
    assert.equal(row.request.estimate.source, 'conservative_estimate')
    assert.equal(row.request.turnId, 'engine-priced-turn')
    assert.equal((await h.raw('/api/budget')).status, 401)
    assert.equal((await h.raw('/api/budget', { headers: { Cookie: h.cookie } })).status, 401)
    assert.equal(
      (await h.request('/api/budget', { headers: { Origin: 'https://untrusted.invalid' } })).status,
      401,
    )
    const response = await h.request('/api/budget')
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('cache-control'), 'no-store')
    const status = await response.json()
    assert.equal(BudgetCoverageSchema.safeParse(status).success, true)
    assert.equal(status.knownNanoUsd, 39000)
    assert.equal(status.estimateKind, 'best_effort_not_hard_limit')
    assert.doesNotMatch(
      JSON.stringify(status),
      /SYNTHETIC_PROMPT_CANARY|SYNTHETIC_PROVIDER_KEY_CANARY|synthetic-budget-admin-token|engine-priced-turn|loopback-provider/,
    )
    assert.doesNotMatch(
      JSON.stringify(h.store.db.prepare('SELECT * FROM budget_attempts').all()),
      /SYNTHETIC_PROMPT_CANARY|SYNTHETIC_PROVIDER_KEY_CANARY/,
    )
    assert.equal((await h.request('/api/budget', { method: 'POST', body: '{}' })).status, 404)
  } finally {
    await h.close()
  }
})

test('the administrator probe uses the same enabled Engine budget gate instead of bypassing output caps or accounting', async () => {
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    const response = await h.request('/api/models/loopback-model/probe', {
      method: 'POST',
      body: JSON.stringify({ capability: 'text' }),
    })
    assert.equal(response.status, 200)
    assert.equal((await response.json()).ok, true)
    assert.equal(h.bodies.length, 1)
    assert.equal(h.bodies[0]!.max_completion_tokens, 17)
    assert.equal(h.rows().length, 1)
    assert.equal(h.rows()[0]!.state, 'complete')
  } finally {
    await h.close()
  }
})

test('distinct logical Engine requests in one turn share the call allowance rather than resetting it per router', async () => {
  const h = await fixture()
  try {
    await h.settings({ ...bestEffort, budgetPerTurnCalls: 2 })
    await withBudgetContext({ turnId: 'shared-call-turn', lane: 'foreground' }, async () => {
      await h.engine.router().complete('replyer', messages)
      await h.engine.router().complete('replyer', messages)
      await assert.rejects(
        h.engine.router().complete('replyer', messages),
        hasCode('budget_turn_calls'),
      )
    })
    assert.equal(h.bodies.length, 2)
    const rows = h.rows()
    assert.equal(rows.length, 2)
    assert.notEqual(rows[0]!.request.requestId, rows[1]!.request.requestId)
    assert.ok(rows.every((row) => row.request.turnId === 'shared-call-turn'))
    await withBudgetContext({ turnId: 'fresh-call-turn', lane: 'foreground' }, () =>
      h.engine.router().complete('replyer', messages),
    )
    assert.equal(h.bodies.length, 3)
  } finally {
    await h.close()
  }
})

test('per-turn money includes completed earlier logical requests plus the next reservation', async () => {
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    await withBudgetContext({ turnId: 'shared-money-turn', lane: 'foreground' }, () =>
      h.engine.router().complete('replyer', messages),
    )
    const first = h.rows()[0]!
    const cap = (first.reservedNanoUsd + Math.floor(first.knownNanoUsd / 2)) / 1e9
    await h.settings({ budgetPerTurnUsd: cap })
    await withBudgetContext({ turnId: 'shared-money-turn', lane: 'foreground' }, () =>
      assert.rejects(
        h.engine.router().complete('replyer', messages),
        hasCode('budget_turn_amount'),
      ),
    )
    assert.equal(h.bodies.length, 1)
    assert.equal(h.rows().length, 1)
    await withBudgetContext({ turnId: 'fresh-money-turn', lane: 'foreground' }, () =>
      h.engine.router().complete('replyer', messages),
    )
    assert.equal(
      h.bodies.length,
      2,
      'the individual next estimate fits; only the same-turn aggregate was rejected',
    )
  } finally {
    await h.close()
  }
})

test('provider retry attempts are separately charged and their unknown liability survives Engine and SQLite restart', async () => {
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    await h.configure(1)
    h.handler((_body, response, count) => {
      if (count === 1) {
        response.statusCode = 429
        response.setHeader('Retry-After', '0')
        response.end('Synthetic throttle')
      } else json(response, reply)
    })
    await withBudgetContext({ turnId: 'retry-turn', lane: 'foreground' }, () =>
      h.engine.router().complete('replyer', messages),
    )
    const rows = h.rows()
    assert.equal(h.bodies.length, 2)
    assert.deepEqual(
      rows.map((row) => row.state),
      ['unknown', 'complete'],
    )
    assert.deepEqual(
      rows.map((row) => row.request.attempt),
      [1, 2],
    )
    assert.equal(rows[0]!.request.requestId, rows[1]!.request.requestId)
    assert.ok(rows[0]!.heldNanoUsd > 0)
    const persisted = JSON.stringify(rows)
    const before = await (await h.request('/api/budget')).json()
    const oldCookie = h.cookie
    await h.reopen()
    assert.equal(
      (await h.raw('/api/budget', { headers: { Cookie: oldCookie, 'X-HumanEngine-Session': '1' } }))
        .status,
      401,
    )
    await h.login()
    assert.equal(JSON.stringify(h.rows()), persisted)
    const after = await (await h.request('/api/budget')).json()
    assert.equal(after.unknownAttempts, 1)
    assert.equal(after.heldNanoUsd, before.heldNanoUsd)
    assert.equal(after.knownNanoUsd, before.knownNanoUsd)
    assert.equal(after.coverage, 'pending')
    assert.equal(
      h.bodies.length,
      2,
      'opening Engine and viewing status must not retry unresolved attempts',
    )
  } finally {
    await h.close()
  }
})

test('settings save and reload retain opt-in limits while illegal lane ratios reject atomically without model traffic', async () => {
  const h = await fixture()
  try {
    await h.settings({
      ...bestEffort,
      budgetDailyUsd: 2,
      budgetPerTurnUsd: 0.2,
      budgetRealtimeRatio: 0.3,
      budgetMaintenanceRatio: 0.15,
      budgetMaxOutputTokens: 23,
    })
    const before = h.store.settings
    const invalid = await h.request('/api/settings', {
      method: 'PUT',
      body: JSON.stringify({ ...before, budgetRealtimeRatio: 0.4, budgetMaintenanceRatio: 0.2 }),
    })
    assert.equal(invalid.status, 400)
    assert.deepEqual(h.store.settings, before)
    await h.reopen()
    await h.login()
    const loaded = await (await h.request('/api/settings')).json()
    assert.deepEqual(loaded, before)
    assert.equal(h.bodies.length, 0)
    await h.engine.router().complete('replyer', messages)
    assert.equal(h.bodies[0]!.max_completion_tokens, 23)
    await h.settings({ budgetExperimentEnabled: false })
    const attempts = h.rows().length
    await h.engine.router().complete('replyer', messages)
    assert.equal('max_completion_tokens' in h.bodies[1]!, false)
    assert.equal(h.rows().length, attempts)
  } finally {
    await h.close()
  }
})

test('simulation creation, inspection and deletion do not spontaneously request a model or create budget attempts', async () => {
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    const created = await h.request('/api/simulations', { method: 'POST' })
    assert.equal(created.status, 200)
    const { id } = await created.json()
    assert.deepEqual(await (await h.request(`/api/simulations/${id}`)).json(), [])
    await h.request('/api/budget')
    await immediate()
    assert.equal(h.bodies.length, 0)
    assert.deepEqual(h.rows(), [])
    assert.equal((await h.request(`/api/simulations/${id}`, { method: 'DELETE' })).status, 200)
    await immediate()
    assert.equal(h.bodies.length, 0)
    assert.deepEqual(h.rows(), [])
  } finally {
    await h.close()
  }
})

test(
  'a real foreground Runtime turn shares its budget across decision and Planner before blocking Replyer transport',
  { timeout: 15000 },
  async () => {
    const h = await fixture()
    try {
      await h.settings({
        ...bestEffort,
        budgetDailyUsd: 5,
        budgetMaxUnresolvedUsd: 1,
        budgetPerTurnUsd: 1,
        budgetPerTurnTokens: 1000000,
        budgetPerTurnCalls: 2,
        debounceMs: 0,
        maxWaitMs: 100,
      })
      const scene: SceneRef = {
        id: 'budget-runtime-scene',
        kind: 'group',
        adapterId: 'synthetic-transport',
        accountId: 'offline-bot',
        platformSceneId: 'synthetic-group',
      }
      h.store.saveScene({
        ...scene,
        name: 'Synthetic runtime',
        participationEnabled: true,
        learningEnabled: false,
        proactiveEnabled: false,
      })
      let sends = 0
      h.engine.runtime.addAdapter(scene.adapterId, {
        send: async () => {
          sends++
          throw new Error('Unexpected synthetic platform send')
        },
        getMessage: async () => ({ items: [], coverage: 'complete' }),
        history: async () => ({ items: [], coverage: 'complete' }),
        dispose() {},
      })
      h.handler((body, response) => {
        const tools = body.tools as Array<{ function: { name: string } }>
        let name: string, args: unknown
        if (tools.some((tool) => tool.function.name === 'participation__decide')) {
          name = 'participation__decide'
          args = { action: 'reply', reasonCode: 'synthetic_budget_turn' }
        } else {
          name = 'reply__plan'
          args = fixtureReplyPlan(body.messages as ChatMessage[])
        }
        json(response, {
          choices: [
            {
              finish_reason: 'tool_calls',
              message: {
                tool_calls: [
                  {
                    id: 'synthetic-tool',
                    type: 'function',
                    function: { name, arguments: JSON.stringify(args) },
                  },
                ],
              },
            },
          ],
          usage,
        })
      })
      let completed!: () => void
      const turnDone = new Promise<void>((resolve) => {
        completed = resolve
      })
      const log = h.store.log.bind(h.store)
      h.store.log = (...args) => {
        const result = log(...args)
        if (args[3]?.stage === 'turn_cleanup' && args[3]?.status === 'completed') completed()
        return result
      }
      h.engine.runtime.ingest(
        {
          id: 'runtime-budget-source',
          sceneId: scene.id,
          senderId: 'synthetic-person',
          sentAt: new Date().toISOString(),
          receivedAt: new Date().toISOString(),
          origin: 'live',
          status: 'available',
          parts: [{ type: 'text', text: 'Synthetic runtime turn input.' }],
        },
        scene,
      )
      await turnDone
      assert.equal(h.bodies.length, 2)
      const rows = h.rows()
      assert.equal(rows.length, 2)
      assert.ok(rows[0]!.request.turnId)
      assert.equal(rows[0]!.request.turnId, rows[1]!.request.turnId)
      assert.notEqual(rows[0]!.request.requestId, rows[1]!.request.requestId)
      assert.ok(rows.every((row) => row.request.lane === 'foreground'))
      assert.ok(
        h.store.logs.some(
          (entry) => entry.errorCode === 'budget_turn_calls' && entry.role === 'replyer',
        ),
      )
      assert.equal(sends, 0)
    } finally {
      await h.close()
    }
  },
)

test(
  'actual silent background batches have independent realtime budget identities rather than inheriting a foreground turn',
  { timeout: 15000 },
  async () => {
    const h = await fixture()
    try {
      await h.settings({
        ...bestEffort,
        budgetPerTurnCalls: 1,
        learningBatchMs: 100,
        learningMaxWaitMs: 100,
        learningMinIntervalMs: 0,
      })
      const scene: SceneRef = {
        id: 'budget-learning-scene',
        kind: 'group',
        adapterId: 'offline',
        accountId: 'offline-bot',
        platformSceneId: 'synthetic-group',
      }
      h.store.saveScene({
        ...scene,
        name: 'Synthetic background learning',
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      })
      const completedBatches: string[] = []
      let completeBatch: (() => void) | undefined
      const log = h.store.log.bind(h.store)
      h.store.log = (...args) => {
        const result = log(...args)
        if (args[3]?.stage === 'learning.batch' && args[3]?.status === 'completed') {
          if (args[3].batchId) completedBatches.push(args[3].batchId)
          completeBatch?.()
        }
        return result
      }
      for (const id of ['first-background-source', 'second-background-source']) {
        const done = new Promise<void>((resolve) => {
          completeBatch = resolve
        })
        withBudgetContext({ turnId: 'synthetic-outer-foreground', lane: 'foreground' }, () =>
          h.engine.runtime.ingest(
            {
              id,
              sceneId: scene.id,
              senderId: 'synthetic-person',
              sentAt: new Date().toISOString(),
              receivedAt: new Date().toISOString(),
              origin: 'live',
              status: 'available',
              parts: [{ type: 'text', text: 'Synthetic neutral chatter.' }],
            },
            scene,
          ),
        )
        await done
        await immediate()
      }
      const rows = h.rows()
      assert.equal(h.bodies.length, 2)
      assert.equal(rows.length, 2)
      assert.equal(completedBatches.length, 2)
      assert.ok(rows.every((row) => row.request.lane === 'realtime'))
      assert.deepEqual(
        rows.map((row) => row.request.turnId),
        completedBatches,
      )
      assert.notEqual(rows[0]!.request.turnId, rows[1]!.request.turnId)
      assert.ok(rows.every((row) => row.request.turnId !== 'synthetic-outer-foreground'))
      assert.equal(h.engine.learningStatus().completed, 2)
      assert.equal(h.engine.memory.adminList().length, 0)
    } finally {
      await h.close()
    }
  },
)

test('actual embedding rebuild with no maintenance allocation fails before HTTP or ledger reservation', async () => {
  const h = await fixture()
  try {
    await h.settings({ ...bestEffort, budgetMaintenanceRatio: 0 })
    await configureEmbedding(h)
    const job = h.engine.embeddingIndex.start()
    await h.engine.embeddingIndex.settled()
    const status = h.engine.embeddingIndex.status()
    assert.equal(status.job?.id, job.id)
    assert.equal(status.job?.state, 'failed')
    assert.equal(status.job?.error, 'budget_lane_limit')
    assert.equal(status.job?.processed, 0)
    assert.equal(status.active, undefined)
    assert.equal(h.bodies.length, 0)
    assert.deepEqual(h.rows(), [])
  } finally {
    await h.close()
  }
})

test('embedding rebuild chunks and explicit same-job retries retain the turn call cap across restart', async () => {
  const h = await fixture()
  try {
    await h.settings({ ...bestEffort, budgetPerTurnCalls: 2 })
    await configureEmbedding(h)
    const job = h.engine.embeddingIndex.start()
    await h.engine.embeddingIndex.settled()
    const assertCapped = () => {
      const status = h.engine.embeddingIndex.status()
      assert.equal(status.job?.id, job.id)
      assert.equal(status.job?.state, 'failed')
      assert.equal(status.job?.error, 'budget_turn_calls')
      assert.equal(status.job?.processed, 2)
      assert.equal(status.job?.total, 3)
      assert.equal(status.active, undefined, 'an incomplete generation cannot be published')
      assert.equal(h.bodies.length, 2)
      const rows = h.rows()
      assert.equal(rows.length, 2)
      assert.equal(new Set(rows.map((row) => row.request.requestId)).size, 2)
      assert.ok(rows.every((row) => row.state === 'complete'))
      assert.ok(rows.every((row) => row.request.lane === 'maintenance'))
      assert.ok(rows.every((row) => row.request.turnId === `embedding-rebuild:${job.id}`))
    }
    assertCapped()
    const before = h.rows()
    assert.equal(h.engine.embeddingIndex.resume(true, job.id).id, job.id)
    await h.engine.embeddingIndex.settled()
    assertCapped()
    assert.deepEqual(h.rows(), before)
    await h.reopen()
    assertCapped()
    assert.equal(h.engine.embeddingIndex.resume(true, job.id).id, job.id)
    await h.engine.embeddingIndex.settled()
    assertCapped()
    assert.deepEqual(h.rows(), before)
  } finally {
    await h.close()
  }
})

test('successful multi-chunk rebuild uses one maintenance identity while query embedding retains foreground scope and simulation is lexical only', async () => {
  const h = await fixture()
  try {
    await h.settings({ ...bestEffort, budgetPerTurnCalls: 3 })
    const records = await configureEmbedding(h)
    const job = h.engine.embeddingIndex.start()
    await h.engine.embeddingIndex.settled()
    const status = h.engine.embeddingIndex.status()
    assert.equal(status.state, 'ready')
    assert.equal(status.job?.state, 'completed')
    assert.equal(status.active?.generationId, job.id)
    assert.deepEqual(status.coverage, { eligible: 3, indexed: 3, pending: 0 })
    assert.equal(h.bodies.length, 3)
    assert.equal(h.rows().length, 3)
    assert.equal(new Set(h.rows().map((row) => row.request.requestId)).size, 3)
    assert.ok(h.rows().every((row) => row.request.turnId === `embedding-rebuild:${job.id}`))
    assert.ok(h.rows().every((row) => row.request.lane === 'maintenance'))
    await h.settings({ budgetMaintenanceRatio: 0, budgetRealtimeRatio: 0 })
    const results = await withBudgetContext(
      { turnId: 'synthetic-foreground-query', lane: 'foreground' },
      () =>
        h.engine.searchMemory(
          { sceneId: 'budget-index-scene', simulation: false },
          'Synthetic indexed memory',
        ),
    )
    assert.deepEqual(new Set(results.map((record) => record.id)), new Set(records.map((r) => r.id)))
    assert.equal(
      h.bodies.length,
      4,
      'foreground search must not fall back after a background-lane denial',
    )
    const query = h.rows().at(-1)!
    assert.equal(query.request.operation, 'embedding')
    assert.equal(query.request.turnId, 'synthetic-foreground-query')
    assert.equal(query.request.lane, 'foreground')
    assert.equal(query.state, 'complete')
    const before = h.rows()
    assert.deepEqual(
      await withBudgetContext({ turnId: 'synthetic-simulation-query', lane: 'foreground' }, () =>
        h.engine.searchMemory(
          { sceneId: 'budget-index-scene', simulation: true },
          'Synthetic indexed memory',
        ),
      ),
      [],
    )
    assert.equal(h.bodies.length, 4)
    assert.deepEqual(h.rows(), before)
  } finally {
    await h.close()
  }
})
