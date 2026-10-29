import test from 'node:test'
import { once } from 'node:events'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { BudgetService } from '../src/budget/service.ts'
import { Store } from '../src/store.ts'
import {
  markRestoredModelHold,
  readRestoredModelHold,
  acknowledgeRestoredModelHold,
  RESTORATION_REVIEW_ACK,
} from '../src/memory/recovery/model-hold.ts'

test('restoration hold survives restart and day rollover even when budget is OFF', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-model-hold-'))
  const path = join(root, 'fixture.sqlite')
  // File-backed coverage now reads the external-cost domain under real recovery authority.
  let store = new Store(path)
  let db = store.db
  try {
    const service = new BudgetService({ db, settings: {} })
    assert.equal(service.gate(), undefined)
    const marked = db.transaction(() => markRestoredModelHold(db))()
    assert.throws(() => service.gate(), /budget_restore_accounting_hold/)
    assert.equal(service.coverage().admissionHold, true)
    assert.equal(service.coverage().restorationCoverage, 'unknown')
    store.close()
    store = new Store(path)
    db = store.db
    const restarted = new BudgetService(
      { db, settings: {} },
      { now: () => Date.parse('2030-01-02T00:00:00Z') },
    )
    assert.throws(() => restarted.gate(), /budget_restore_accounting_hold/)
    assert.equal(restarted.recovery().restoreId, marked.restoreId)
    assert.throws(() => restarted.acknowledgeRecovery(marked.restoreId, 'okay'), /ack_required/)
    assert.throws(
      () => restarted.acknowledgeRecovery('wrong', RESTORATION_REVIEW_ACK),
      /review_conflict/,
    )
    const before = db.prepare('SELECT * FROM budget_attempts').all()
    restarted.acknowledgeRecovery(marked.restoreId, RESTORATION_REVIEW_ACK)
    restarted.acknowledgeRecovery(marked.restoreId, RESTORATION_REVIEW_ACK)
    assert.equal(restarted.gate(), undefined)
    assert.equal(restarted.coverage().admissionHold, false)
    assert.equal(restarted.coverage().restorationCoverage, 'manually_acknowledged')
    assert.deepEqual(db.prepare('SELECT * FROM budget_attempts').all(), before)
    const again = db.transaction(() => markRestoredModelHold(db))()
    assert.notEqual(again.restoreId, marked.restoreId)
    assert.throws(
      () => restarted.acknowledgeRecovery(marked.restoreId, RESTORATION_REVIEW_ACK),
      /review_conflict/,
    )
    assert.throws(() => restarted.gate(), /budget_restore_accounting_hold/)
  } finally {
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('hold is atomic, guarded against malformed persistence, and acknowledgment never creates missing state', () => {
  const db = new Database(':memory:')
  try {
    assert.equal(readRestoredModelHold(db), undefined)
    assert.throws(() => markRestoredModelHold(db), /transaction_required/)
    assert.throws(
      () => acknowledgeRestoredModelHold(db, 'missing', RESTORATION_REVIEW_ACK),
      /review_conflict/,
    )
    assert.equal(readRestoredModelHold(db), undefined)
    assert.throws(
      () =>
        db.transaction(() => {
          markRestoredModelHold(db)
          throw new Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(readRestoredModelHold(db), undefined)
    db.exec('CREATE TABLE recovery_model_hold(id,restore_id,status)')
    assert.throws(() => readRestoredModelHold(db), /hold_invalid/)
    assert.throws(() => db.transaction(() => markRestoredModelHold(db))(), /hold_invalid/)
  } finally {
    db.close()
  }
})

test('store-backed optional-budget probes block native HTTP both initially and after queueing', async () => {
  const { createServer } = await import('node:http')
  const { Store } = await import('../src/store.ts')
  const { probe } = await import('../src/probes.ts')
  const { modelScheduler } = await import('../src/scheduler.ts')
  let requests = 0
  const server = createServer((_req, res) => {
    requests++
    res.setHeader('content-type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [{ message: { content: 'OK' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1, completion_tokens: 1 },
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const store = new Store(':memory:')
  let release: (() => void) | undefined
  let blockers: Promise<void>[] = []
  try {
    store.set('models', {
      providers: [
        {
          id: 'p',
          name: 'Synthetic',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${address.port}/v1`,
          credentialEnv: '',
        },
      ],
      models: [
        {
          id: 'm',
          providerId: 'p',
          displayName: 'Synthetic',
          apiModelName: 'fixture',
          capabilities: ['text', 'embedding'],
        },
      ],
      routes: [],
    })
    store.secrets.apply('model', 'p', { action: 'set', value: 'synthetic-only' })
    const first = store.db.transaction(() => markRestoredModelHold(store.db))()
    for (const capability of ['text', 'tools', 'vision', 'embedding'] as const) {
      const result = await probe(store, 'm', capability)
      assert.equal(result.ok, false)
      assert.equal(result.detail, 'budget_restore_accounting_hold')
    }
    assert.equal(requests, 0)
    acknowledgeRestoredModelHold(store.db, first.restoreId, RESTORATION_REVIEW_ACK)
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    blockers = Array.from({ length: 4 }, () => modelScheduler.run(0, undefined, () => gate))
    await new Promise<void>((resolve) => setImmediate(resolve))
    const pending = probe(store, 'm', 'text', undefined, () => undefined)
    await new Promise<void>((resolve) => setImmediate(resolve))
    assert.equal(modelScheduler.status.queued, 1)
    store.db.transaction(() => markRestoredModelHold(store.db))()
    release!()
    await Promise.all(blockers)
    const result = await pending
    assert.equal(result.detail, 'budget_restore_accounting_hold')
    assert.equal(requests, 0)
  } finally {
    release?.()
    await Promise.all(blockers)
    store.close()
    server.close()
    await once(server, 'close')
  }
})

test('store-backed custom probe cannot omit or override the mandatory hold supplier', async () => {
  const { Store } = await import('../src/store.ts')
  const { probe } = await import('../src/probes.ts')
  const { ModelProtocolRegistry } = await import('../src/model-protocols.ts')
  const store = new Store(':memory:')
  const registry = new ModelProtocolRegistry()
  let calls = 0
  registry.register(
    'fixture:protocol',
    'Synthetic',
    {
      async complete() {
        calls++
        return {
          text: 'OK',
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
  try {
    store.set('models', {
      providers: [
        {
          id: 'p',
          name: 'Synthetic',
          protocol: 'fixture:protocol',
          baseUrl: 'http://127.0.0.1',
          credentialEnv: '',
        },
      ],
      models: [
        {
          id: 'm',
          displayName: 'Synthetic',
          providerId: 'p',
          apiModelName: 'm',
          capabilities: ['text', 'embedding'],
        },
      ],
      routes: [],
    })
    store.db.transaction(() => markRestoredModelHold(store.db))()
    for (const capability of ['text', 'embedding'] as const) {
      assert.equal(
        (await probe(store, 'm', capability, registry)).detail,
        'budget_restore_accounting_hold',
      )
      assert.equal(
        (await probe(store, 'm', capability, registry, () => undefined)).detail,
        'budget_restore_accounting_hold',
      )
    }
    assert.equal(calls, 0)
  } finally {
    store.close()
  }
})

test('SQLite non-string hold statuses cannot be coerced into an acknowledged admission', () => {
  for (const status of [
    Buffer.from('held'),
    Buffer.from('manually_acknowledged'),
    null,
    1,
    0,
    'unknown',
  ]) {
    const db = new Database(':memory:')
    try {
      const service = new BudgetService({ db, settings: {} })
      // A malformed copied table may omit affinities and constraints. Never trust coercion.
      db.exec('CREATE TABLE recovery_model_hold(id,restore_id,status)')
      db.prepare('INSERT INTO recovery_model_hold VALUES(1,?,?)').run(
        '463a2db2-051b-40f4-94aa-c841c6cde0a7',
        status,
      )
      assert.throws(() => readRestoredModelHold(db), /recovery_model_hold_invalid/)
      assert.throws(() => service.recovery(), /recovery_model_hold_invalid/)
      assert.throws(() => service.gate(), /recovery_model_hold_invalid/)
      assert.throws(() => service.coverage(), /recovery_model_hold_invalid/)
      assert.throws(
        () =>
          service.acknowledgeRecovery(
            '463a2db2-051b-40f4-94aa-c841c6cde0a7',
            RESTORATION_REVIEW_ACK,
          ),
        /recovery_model_hold_invalid/,
      )
    } finally {
      db.close()
    }
  }
  // SQLite permits BLOB in the actual TEXT column too: cover the reported production shape.
  const db = new Database(':memory:')
  try {
    const service = new BudgetService({ db, settings: {} })
    db.transaction(() => markRestoredModelHold(db))()
    for (const value of ['held', 'manually_acknowledged']) {
      db.prepare('UPDATE recovery_model_hold SET status=?').run(Buffer.from(value))
      assert.throws(() => service.gate(), /recovery_model_hold_invalid/)
    }
  } finally {
    db.close()
  }
})
