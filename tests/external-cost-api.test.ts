import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
async function fixture(
  run: (f: {
    store: Store
    engine: Engine
    request: (path: string, body?: unknown, auth?: boolean) => Promise<Response>
  }) => Promise<void>,
) {
  const store = new Store(':memory:'),
    engine = new Engine(store),
    token = 'synthetic-external-cost-admin',
    app = createApp(store, { engine, token })
  const request = async (path: string, body?: unknown, auth = true) =>
    app.fetch(
      new Request('http://localhost' + path, {
        method: body === undefined ? 'GET' : 'POST',
        headers: {
          ...(auth ? { Authorization: `Bearer ${token}` } : {}),
          'Content-Type': 'application/json',
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      }),
    )
  try {
    await run({ store, engine, request })
  } finally {
    await engine.dispose()
    store.close()
  }
}
const receipt = () => ({
  id: 'synthetic-cost',
  category: 'tool',
  currency: 'USD',
  sourceId: 'synthetic-source',
  billingDate: new Date().toISOString().slice(0, 10),
  periodStart: '2026-01-01',
  periodEnd: '2026-12-31',
  status: 'unknown',
})
test('authenticated external-cost API retains unknown coverage, exact CAS correction and idempotent roots', () =>
  fixture(async ({ engine, request }) => {
    const input = receipt()
    assert.equal((await request('/api/budget/external-costs', input, false)).status, 401)
    assert.equal(engine.budget.externalCosts.list().total, 0)
    const first = await request('/api/budget/external-costs', input)
    assert.equal(first.status, 200)
    assert.equal((await first.json()).revision, 1)
    assert.equal((await request('/api/budget/external-costs', input)).status, 200)
    const unknown = await (await request('/api/budget')).json()
    assert.equal(unknown.coverage, 'external_incomplete')
    assert.equal(unknown.externalUnknownReceipts, 1)
    assert.equal(unknown.enabled, false)
    assert.equal(
      (
        await request('/api/budget/external-costs', {
          ...input,
          status: 'known',
          amountNanoUsd: 100,
        })
      ).status,
      409,
    )
    const correction = {
      id: input.id,
      expectedRevision: 1,
      correctionId: 'correction-1',
      replacement: { ...input, status: 'known', amountNanoUsd: 12345 },
    }
    assert.equal((await request('/api/budget/external-costs/corrections', correction)).status, 200)
    assert.equal((await request('/api/budget/external-costs/corrections', correction)).status, 200)
    assert.equal(
      (
        await request('/api/budget/external-costs/corrections', {
          ...correction,
          correctionId: 'correction-stale',
        })
      ).status,
      409,
    )
    const known = await (await request('/api/budget')).json()
    assert.equal(known.externalCosts.coverage, 'known_imported_only')
    assert.equal(known.externalKnownNanoUsd, 12345)
    assert.equal(known.knownNanoUsd, 12345)
    const rows = await (await request('/api/budget/external-costs?page=1&pageSize=1')).json()
    assert.equal(rows.total, 1)
    assert.equal(rows.items[0].revision, 2)
    assert.equal(rows.items[0].receipt.amountNanoUsd, 12345)
  }))
test('external-cost HTTP rejects prose, unknown amounts, unsafe integers, impossible dates and unbounded pages', () =>
  fixture(async ({ engine, request }) => {
    for (const input of [
      { ...receipt(), invoice: 'not-allowed' },
      { ...receipt(), amountNanoUsd: 0 },
      { ...receipt(), status: 'known', amountNanoUsd: 0.5 },
      { ...receipt(), status: 'known', amountNanoUsd: Number.MAX_SAFE_INTEGER + 1 },
      { ...receipt(), billingDate: '2026-02-30' },
      { ...receipt(), sourceId: 'https://example.invalid' },
    ])
      assert.equal((await request('/api/budget/external-costs', input)).status, 400)
    assert.equal((await request('/api/budget/external-costs?pageSize=51')).status, 400)
    assert.equal(engine.budget.externalCosts.list().total, 0)
  }))
