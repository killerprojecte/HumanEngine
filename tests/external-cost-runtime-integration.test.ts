import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { createServer, type ServerResponse } from 'node:http'
import { mkdtempSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { modelScheduler } from '../src/scheduler.ts'
import type { ExternalCostInput } from '../src/budget/external-cost/contracts.ts'
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
  const directory = mkdtempSync(join(tmpdir(), 'he-external-cost-runtime-'))
  let path = join(directory, 'synthetic.sqlite')
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
    directory,
    get path() {
      return path
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
    reopen: async (replacement?: string) => {
      await engine.dispose()
      store.close()
      if (replacement) path = replacement
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

// Wall/billing Date is controlled separately from synthetic chat event timestamps.
function billingClock(t: TestContext) {
  t.mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-29T13:00:00Z') })
}
const invoice = (
  id: string,
  status: 'known' | 'unknown' = 'known',
  amountNanoUsd = 1000000000,
): ExternalCostInput => ({
  id,
  category: 'compute',
  currency: 'USD',
  billingDate: '2026-10-29',
  periodStart: '2026-10-01',
  periodEnd: '2026-10-31',
  sourceId: 'synthetic_compute_vendor',
  ...(status === 'known' ? { status, amountNanoUsd } : { status }),
})
const post = (
  h: Awaited<ReturnType<typeof fixture>>,
  value: unknown,
  route = '/api/budget/external-costs',
) => h.request(route, { method: 'POST', body: JSON.stringify(value) })
const complete = (h: Awaited<ReturnType<typeof fixture>>, id: string) =>
  withBudgetContext({ turnId: id, lane: 'foreground' }, () =>
    h.engine.router().complete('planner', messages),
  )

async function coverage(h: Awaited<ReturnType<typeof fixture>>) {
  const response = await h.request('/api/budget')
  assert.equal(response.status, 200)
  return response.json()
}

test('actual authenticated cost import counts current known spend once and blocks native Engine transport until an explicit CAS correction', async (t) => {
  billingClock(t)
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    assert.equal((await h.raw('/api/budget/external-costs')).status, 401)
    const initial = await post(h, invoice('known-cap'))
    assert.equal(initial.status, 200)
    const first = await initial.json()
    assert.equal(first.revision, 1)
    assert.deepEqual(await (await post(h, invoice('known-cap'))).json(), first)
    assert.equal((await post(h, invoice('known-cap', 'known', 7))).status, 409)
    assert.equal((await post(h, { ...invoice('bad'), invoiceBody: prompt })).status, 400)
    const before = await coverage(h)
    assert.equal(before.externalCosts.knownTodayNanoUsd, 1000000000)
    assert.equal(before.externalKnownNanoUsd, 1000000000)
    assert.equal(before.knownNanoUsd, 1000000000)
    await assert.rejects(() => complete(h, 'known-blocked'), hasCode('budget_daily_limit'))
    assert.equal(h.bodies.length, 0)
    const correction = {
      id: 'known-cap',
      expectedRevision: 1,
      correctionId: 'explicit-correction',
      replacement: invoice('known-cap', 'known', 1000),
    }
    assert.equal((await post(h, correction, '/api/budget/external-costs/corrections')).status, 200)
    assert.equal(
      (
        await post(
          h,
          { ...correction, correctionId: 'stale-correction' },
          '/api/budget/external-costs/corrections',
        )
      ).status,
      409,
    )
    await complete(h, 'known-allowed')
    assert.equal(h.bodies.length, 1)
    const after = await coverage(h)
    assert.equal(after.externalKnownNanoUsd, 1000)
    assert.ok(after.knownNanoUsd > 1000)
    assert.equal(after.externalCosts.coverage, 'known_imported_only')
    assert.equal(
      (
        h.store.db.prepare('SELECT COUNT(*) n FROM budget_external_cost_revisions').get() as {
          n: number
        }
      ).n,
      2,
    )
    assert.doesNotMatch(
      JSON.stringify(after),
      /SYNTHETIC_PROVIDER_KEY_CANARY|SYNTHETIC_PROMPT_CANARY/,
    )
  } finally {
    await h.close()
  }
})

test('unknown imported debt survives billing-day rollover and restart; experiment OFF still displays incomplete coverage rather than zero', async (t) => {
  billingClock(t)
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    assert.equal((await post(h, invoice('unknown-debt', 'unknown'))).status, 200)
    await assert.rejects(
      () => complete(h, 'unknown-today'),
      hasCode('budget_external_cost_unknown'),
    )
    t.mock.timers.tick(86400000)
    await assert.rejects(
      () => complete(h, 'unknown-tomorrow'),
      hasCode('budget_external_cost_unknown'),
    )
    await h.reopen()
    await h.login()
    await assert.rejects(
      () => complete(h, 'unknown-restarted'),
      hasCode('budget_external_cost_unknown'),
    )
    const live = await coverage(h)
    assert.equal(live.windowStart, '2026-10-30')
    assert.equal(live.externalCosts.unknownReceipts, 1)
    assert.equal(live.coverage, 'external_incomplete')
    await h.settings({ budgetExperimentEnabled: false })
    const off = await coverage(h)
    assert.equal(off.enabled, false)
    assert.equal(off.externalCosts.unknownReceipts, 1)
    assert.equal(off.coverage, 'external_incomplete')
    assert.equal(h.bodies.length, 0)
    await h.settings(bestEffort)
    assert.equal(
      (
        await post(
          h,
          {
            id: 'unknown-debt',
            expectedRevision: 1,
            correctionId: 'resolved-explicitly',
            replacement: invoice('unknown-debt', 'known', 0),
          },
          '/api/budget/external-costs/corrections',
        )
      ).status,
      200,
    )
    await complete(h, 'after-declared-resolution')
    assert.equal(h.bodies.length, 1)
  } finally {
    await h.close()
  }
})

test('synthetic message event dates cannot move the real billing window or reset imported known spending', async (t) => {
  billingClock(t)
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    await post(h, invoice('real-billing-cap'))
    const scene: SceneRef = {
      id: 'synthetic-date-scene',
      kind: 'group',
      adapterId: 'offline',
      accountId: 'bot',
      platformSceneId: 'scene',
    }
    h.store.saveScene({
      ...scene,
      name: 'Synthetic timeline',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    for (const [id, eventDate] of [
      ['past', '1999-01-01T00:00:00Z'],
      ['future', '2099-12-31T23:59:59Z'],
    ]) {
      h.engine.runtime.ingest(
        {
          id: id!,
          sceneId: scene.id,
          senderId: 'person',
          origin: 'live',
          status: 'available',
          sentAt: eventDate!,
          receivedAt: eventDate!,
          parts: [{ type: 'text', text: 'Synthetic event clock, not invoice clock.' }],
        },
        scene,
        false,
      )
      const result = await coverage(h)
      assert.equal(result.windowStart, '2026-10-29')
      assert.equal(result.externalKnownNanoUsd, 1000000000)
      await assert.rejects(() => complete(h, `billing-${id}`), hasCode('budget_daily_limit'))
    }
    assert.equal(h.bodies.length, 0)
    const list = await (await h.request('/api/budget/external-costs')).json()
    assert.equal(list.items[0].receipt.billingDate, '2026-10-29')
    assert.equal(list.items[0].recordedAt, '2026-10-29T13:00:00.000Z')
  } finally {
    await h.close()
  }
})

test('actual queued native model attempt observes a newly imported unknown receipt at physical admission', async (t) => {
  billingClock(t)
  const h = await fixture()
  let release: () => void = () => {},
    blockers: Promise<void>[] = []
  try {
    await h.settings(bestEffort)
    const waiting = new Promise<void>((resolve) => {
      release = resolve
    })
    blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
      modelScheduler.run(0, undefined, () => waiting),
    )
    for (let n = 0; n < 4; n++) await immediate()
    const pending = complete(h, 'queued-before-external-import')
    const rejected = assert.rejects(() => pending, hasCode('budget_external_cost_unknown'))
    for (let n = 0; n < 8; n++) await immediate()
    assert.equal(modelScheduler.status.queued, 1)
    await post(h, invoice('late-unknown', 'unknown'))
    release()
    await Promise.all(blockers)
    await rejected
    assert.equal(h.bodies.length, 0)
  } finally {
    release()
    await Promise.all(blockers)
    await h.close()
  }
})

test('an actual complete restore preserves imported uncertainty and adds unconditional model HOLD; acknowledgment is not external reconciliation', async (t) => {
  billingClock(t)
  const h = await fixture()
  try {
    await h.settings(bestEffort)
    await post(h, invoice('restore-unknown', 'unknown'))
    const mediaRoot = join(h.directory, 'media')
    mkdirSync(mediaRoot)
    const backupRoot = join(h.directory, 'backup')
    await backup({ database: h.path, mediaRoot, destination: backupRoot })
    const restored = await restore({
      backupRoot,
      destination: join(h.directory, 'restored'),
      authorityDirectory: metadata(h.store.db)!.authority,
    })
    await h.reopen(restored.database)
    await h.login()
    await h.settings({ budgetExperimentEnabled: false })
    await assert.rejects(
      () => complete(h, 'restore-held-even-off'),
      hasCode('budget_restore_accounting_hold'),
    )
    const recovery = await (await h.request('/api/budget/recovery')).json()
    assert.equal(recovery.held, true)
    assert.equal(
      (
        await post(
          h,
          {
            restoreId: recovery.restoreId,
            acknowledgment: 'I_reviewed_accounting_and_accept_incomplete_history',
          },
          '/api/budget/recovery/acknowledge',
        )
      ).status,
      200,
    )
    await h.settings(bestEffort)
    await assert.rejects(
      () => complete(h, 'ack-not-reconciled'),
      hasCode('budget_external_cost_unknown'),
    )
    const visible = await coverage(h)
    assert.equal(visible.restorationCoverage, 'manually_acknowledged')
    assert.equal(visible.externalCosts.unknownReceipts, 1)
    assert.equal(visible.coverage, 'external_incomplete')
    assert.equal(h.bodies.length, 0)
  } finally {
    await h.close()
  }
})
