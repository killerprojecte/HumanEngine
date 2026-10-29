import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { Store } from '../src/store.ts'
import Database from 'better-sqlite3'
import { EvaluationSessionLedger } from '../src/memory/evaluation/session-ledger.ts'
import { markRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import {
  createNativeEvaluationProvider,
  discoverEvaluationModels,
  EphemeralEvaluationCredential,
} from '../src/memory/evaluation/native-provider.ts'
import {
  runMemoryEvaluation,
  evaluationFixtureHash,
  type EvaluationRequest,
} from '../src/memory/evaluation/runner.ts'

const secret = 'synthetic-secret-never-a-real-key'
async function requests() {
  const values: EvaluationRequest[] = []
  await runMemoryEvaluation({
    mode: 'real-run',
    authorization: {
      confirmed: true,
      fixtureHash: evaluationFixtureHash,
      providerId: 'fixture',
      modelId: 'fixture',
      maxCalls: 18,
      maxUsd: 1,
      timeoutMs: 1000,
    },
    provider: {
      id: 'fixture',
      modelId: 'fixture',
      maxCostUsd: () => 0,
      async complete(value) {
        values.push(value)
        return { prediction: 'fixture', actualCostUsd: 0 }
      },
    },
  })
  return values
}
async function fixture(mode: 'ok' | 'unknown' | 'error' | 'malformed' = 'ok') {
  let beforeResponse: (() => void) | undefined
  const wires: { path: string; auth: string | undefined; body: string }[] = []
  const server = createServer(async (req, res) => {
    let body = ''
    for await (const chunk of req) body += chunk.toString()
    wires.push({ path: req.url!, auth: req.headers.authorization, body })
    beforeResponse?.()
    res.setHeader('content-type', 'application/json')
    if (mode === 'malformed') {
      res.end(`{\"private\":\"${secret}\" invalid`)
      return
    }
    if (mode === 'error') {
      res.writeHead(503)
      res.end(JSON.stringify({ error: secret }))
      return
    }
    if (req.url === '/v1/models') {
      res.end(JSON.stringify({ data: [{ id: 'model/a' }, { id: secret }, { id: 'bad\nmodel' }] }))
      return
    }
    res.end(
      JSON.stringify({
        choices: [{ message: { content: `OK ${secret}` }, finish_reason: 'stop' }],
        ...(mode === 'unknown'
          ? {}
          : {
              usage: {
                prompt_tokens: 3,
                completion_tokens: 2,
                prompt_tokens_details: { cached_tokens: 0, cache_write_tokens: 0 },
              },
            }),
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const endpoint = `http://127.0.0.1:${(server.address() as { port: number }).port}/v1`
  const store = new Store(':memory:')
  const sessionDb = new Database(':memory:')
  const sessions = new EvaluationSessionLedger(sessionDb)
  store.set('settings', {
    ...store.settings,
    budgetExperimentEnabled: true,
    budgetMode: 'strict',
    budgetPerTurnUsd: 0.1,
    budgetPerTurnCalls: 18,
    budgetMaxOutputTokens: 20,
  })
  const bytes = Buffer.from(secret)
  const credential = new EphemeralEvaluationCredential(bytes)
  assert.ok(bytes.every((value) => value === 0))
  const options = {
    store,
    credential,
    sessions,
    approval: {
      confirmed: true as const,
      syntheticOnly: true as const,
      endpoint,
      providerId: 'fixture',
      modelId: 'fixture',
      runId: 'approved-run',
      maxCalls: 18,
      maxUsd: 0.1,
      maxOutputTokens: 20,
      durationMs: 30000,
      requestTimeoutMs: 1000,
    },
    model: {
      id: 'fixture',
      displayName: 'fixture',
      providerId: 'fixture',
      apiModelName: 'fixture-wire',
      capabilities: ['text' as const],
      inputUsdPerMillion: 1,
      outputUsdPerMillion: 1,
      cacheReadUsdPerMillion: 1,
      cacheWriteUsdPerMillion: 1,
    },
    verified: {
      contract: () => ({
        version: 'synthetic-only-v1',
        outputCapHonored: true,
        inputCategories: ['inputUsdPerMillion' as const],
      }),
      estimator: ({ outputCap }: { outputCap: number }) => ({
        source: 'verified_contract' as const,
        estimatorVersion: 'mock',
        contractVersion: 'synthetic-only-v1',
        enforceable: true,
        inputUpperTokens: 1000,
        outputUpperTokens: outputCap,
        inputCategories: ['inputUsdPerMillion' as const],
      }),
      maxCostUsd: () => 0.002,
    },
  }
  return {
    store,
    credential,
    options,
    wires,
    beforeResponse(callback: () => void) {
      beforeResponse = callback
    },
    async close() {
      credential.dispose()
      store.close()
      sessionDb.close()
      server.close()
      await once(server, 'close')
    },
  }
}
test('native adapter uses existing strict ledger, final output cap, synthetic-only body and redacted output', async () => {
  const f = await fixture()
  try {
    const provider = createNativeEvaluationProvider(f.options)
    const request = (await requests())[0]!
    const response = await provider.complete(request, new AbortController().signal)
    assert.equal(response.prediction, 'OK [REDACTED]')
    assert.ok(Math.abs(response.actualCostUsd! - 0.000005) < 1e-15)
    assert.equal(f.wires.length, 1)
    assert.equal(f.wires[0]!.auth, `Bearer ${secret}`)
    const body = JSON.parse(f.wires[0]!.body)
    assert.equal(body.max_completion_tokens, 20)
    assert.equal(body.model, 'fixture-wire')
    assert.equal(JSON.parse(body.messages[1].content).occurredAt, '2026-10-02T13:00:00.000Z')
    const rows = f.store.db.prepare('SELECT * FROM budget_attempts').all()
    assert.equal(rows.length, 1)
    assert.ok(!JSON.stringify(rows).includes(secret))
    assert.ok(!JSON.stringify(rows).includes('2026-10-02T13:00:00.000Z'))
    await assert.rejects(
      provider.complete(
        { ...request, utterance: 'NOT THE SYNTHETIC FIXTURE' },
        new AbortController().signal,
      ),
      /synthetic_fixture/,
    )
    assert.equal(f.wires.length, 1)
    provider.dispose()
    await assert.rejects(
      provider.complete(request, new AbortController().signal),
      /session_stopped/,
    )
  } finally {
    await f.close()
  }
})
test('unknown usage and provider errors stop without retries, body/header/cause leakage or free accounting', async () => {
  for (const mode of ['unknown', 'error'] as const) {
    const f = await fixture(mode)
    try {
      const provider = createNativeEvaluationProvider(f.options)
      const request = (await requests())[0]!
      if (mode === 'unknown')
        assert.equal(
          (await provider.complete(request, new AbortController().signal)).actualCostUsd,
          undefined,
        )
      else
        await assert.rejects(provider.complete(request, new AbortController().signal), (error) => {
          assert.equal((error as Error).message, 'evaluation_provider_failed')
          assert.equal((error as Error).cause, undefined)
          assert.ok(!JSON.stringify(error).includes(secret))
          return true
        })
      await assert.rejects(
        provider.complete(request, new AbortController().signal),
        /session_stopped/,
      )
      assert.equal(f.wires.length, 1)
      const rows = f.store.db.prepare('SELECT state FROM budget_attempts').all() as {
        state: string
      }[]
      assert.ok(rows.some((row) => ['unknown', 'partial'].includes(row.state)))
    } finally {
      await f.close()
    }
  }
})
test('HOLD, budget OFF and missing verified contract fail before transport', async () => {
  const f = await fixture()
  try {
    const request = (await requests())[0]!
    assert.throws(
      () => createNativeEvaluationProvider({ ...f.options, verified: undefined as never }),
      /contract_required/,
    )
    const provider = createNativeEvaluationProvider(f.options)
    f.store.set('settings', { ...f.store.settings, budgetExperimentEnabled: false })
    await assert.rejects(
      provider.complete(request, new AbortController().signal),
      /budget_configuration/,
    )
    f.store.db.transaction(() => markRestoredModelHold(f.store.db))()
    await assert.rejects(
      provider.complete(request, new AbortController().signal),
      /budget_restore_accounting_hold/,
    )
    assert.equal(f.wires.length, 0)
  } finally {
    await f.close()
  }
})
test('separate metadata approval reuses Router models, excludes malformed/secret identifiers and never generates', async () => {
  const f = await fixture()
  try {
    const args = {
      store: f.store,
      credential: f.credential,
      approval: {
        confirmed: true as const,
        operation: 'models' as const,
        endpoint: f.options.approval.endpoint,
        providerId: 'fixture',
        timeoutMs: 1000,
      },
    }
    assert.deepEqual(await discoverEvaluationModels(args), ['model/a'])
    assert.equal(f.wires.length, 1)
    assert.equal(f.wires[0]!.path, '/v1/models')
    assert.equal(f.wires[0]!.body, '')
    f.store.db.transaction(() => markRestoredModelHold(f.store.db))()
    await assert.rejects(discoverEvaluationModels(args), /discovery_failed/)
    assert.equal(f.wires.length, 1)
    await assert.rejects(
      discoverEvaluationModels({
        ...args,
        approval: { ...args.approval, confirmed: false as never },
      }),
      /approval_required/,
    )
  } finally {
    await f.close()
  }
})
test('credential input has no environment/path fallback and serializes only a marker', () => {
  const credential = new EphemeralEvaluationCredential(Buffer.from(secret))
  assert.equal(JSON.stringify(credential), '"[EPHEMERAL_CREDENTIAL]"')
  assert.throws(() => EphemeralEvaluationCredential.fromPipe(0), /pipe_required/)
  credential.dispose()
  assert.throws(() => credential.resolve(), /disposed/)
})

test('explicit request-bounded mode transmits cap with unknown prices, stops unknown and persists across reopen', async () => {
  const { default: Database } = await import('better-sqlite3')
  const { mkdtempSync, rmSync } = await import('node:fs')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const { EvaluationSessionLedger } = await import('../src/memory/evaluation/session-ledger.ts')
  const { createBoundedNativeEvaluationProvider } =
    await import('../src/memory/evaluation/native-provider.ts')
  const directory = mkdtempSync(join(tmpdir(), 'he-eval-session-'))
  let db = new Database(join(directory, 'session.sqlite'))
  const f = await fixture()
  try {
    f.store.set('settings', { ...f.store.settings, budgetExperimentEnabled: false })
    const { maxUsd, ...rest } = f.options.approval
    const approval = {
      ...rest,
      costMode: 'request_bounded' as const,
      acceptUnknownCostRisk: true as const,
      maxCalls: 2,
    }
    const model = {
      id: 'fixture',
      displayName: 'fixture',
      providerId: 'fixture',
      apiModelName: 'fixture-wire',
      capabilities: ['text' as const],
    }
    let sessions = new EvaluationSessionLedger(db)
    const provider = createBoundedNativeEvaluationProvider({
      ...f.options,
      approval,
      model,
      sessions,
    })
    const request = (await requests())[0]!
    assert.equal(provider.maxCostUsd(request), undefined)
    const result = await provider.complete(request, new AbortController().signal)
    assert.equal(result.actualCostUsd, undefined)
    assert.equal(JSON.parse(f.wires[0]!.body).max_completion_tokens, 20)
    assert.equal(sessions.status(approval.runId)!.state, 'unknown')
    assert.equal(sessions.status(approval.runId)!.hardUsdCap, false)
    assert.ok(!JSON.stringify(db.prepare('SELECT * FROM evaluation_runs').all()).includes(secret))
    db.close()
    db = new Database(join(directory, 'session.sqlite'))
    sessions = new EvaluationSessionLedger(db)
    const reopened = createBoundedNativeEvaluationProvider({
      ...f.options,
      approval,
      model,
      sessions,
    })
    await assert.rejects(reopened.complete(request, new AbortController().signal), /run_not_ready/)
    assert.equal(f.wires.length, 1)
    assert.throws(
      () =>
        createBoundedNativeEvaluationProvider({
          ...f.options,
          approval: { ...approval, maxCalls: 3 },
          model,
          sessions,
        }),
      /approval_conflict/,
    )
  } finally {
    await f.close()
    db.close()
    rmSync(directory, { recursive: true, force: true })
  }
})
test('request-bounded counters survive a new adapter and enforce calls/deadline without touching event time', async () => {
  const { default: Database } = await import('better-sqlite3')
  const { EvaluationSessionLedger } = await import('../src/memory/evaluation/session-ledger.ts')
  const { createBoundedNativeEvaluationProvider } =
    await import('../src/memory/evaluation/native-provider.ts')
  const db = new Database(':memory:'),
    f = await fixture()
  try {
    f.store.set('settings', { ...f.store.settings, budgetExperimentEnabled: false })
    const sessions = new EvaluationSessionLedger(db)
    const approval = {
      ...f.options.approval,
      maxCalls: 2,
      maxUsd: 0.00000001,
      costMode: 'request_bounded' as const,
      acceptUnknownCostRisk: true as const,
    }
    const opts = { ...f.options, approval, sessions }
    const request = (await requests())[0]!
    await createBoundedNativeEvaluationProvider(opts).complete(
      request,
      new AbortController().signal,
    )
    await createBoundedNativeEvaluationProvider(opts).complete(
      request,
      new AbortController().signal,
    )
    await assert.rejects(
      createBoundedNativeEvaluationProvider(opts).complete(request, new AbortController().signal),
      /run_not_ready/,
    )
    assert.equal(f.wires.length, 2)
    const other = { ...opts, approval: { ...approval, runId: 'other-approved-run' } }
    const provider = createBoundedNativeEvaluationProvider(other)
    db.prepare('UPDATE evaluation_runs SET deadline_ms=0 WHERE run_id=?').run('other-approved-run')
    await assert.rejects(provider.complete(request, new AbortController().signal), /run_not_ready/)
    assert.equal(f.wires.length, 2)
  } finally {
    await f.close()
    db.close()
  }
})
test('bounded mode requires explicit risk acceptance; an existing strict gate and HOLD are never bypassed', async () => {
  const { default: Database } = await import('better-sqlite3')
  const { EvaluationSessionLedger } = await import('../src/memory/evaluation/session-ledger.ts')
  const { createBoundedNativeEvaluationProvider } =
    await import('../src/memory/evaluation/native-provider.ts')
  const db = new Database(':memory:'),
    f = await fixture()
  try {
    const sessions = new EvaluationSessionLedger(db)
    const approval = {
      ...f.options.approval,
      costMode: 'request_bounded' as const,
      acceptUnknownCostRisk: true as const,
    }
    assert.throws(
      () =>
        createBoundedNativeEvaluationProvider({
          ...f.options,
          sessions,
          approval: { ...approval, acceptUnknownCostRisk: false as never },
        }),
      /bounded_approval_required/,
    )
    const provider = createBoundedNativeEvaluationProvider({ ...f.options, sessions, approval })
    await assert.rejects(
      provider.complete((await requests())[0]!, new AbortController().signal),
      /provider_failed/,
    )
    assert.equal(f.wires.length, 0)
    assert.equal(sessions.status(approval.runId)!.state, 'unknown')
    f.store.set('settings', { ...f.store.settings, budgetExperimentEnabled: false })
    f.store.db.transaction(() => markRestoredModelHold(f.store.db))()
    const second = createBoundedNativeEvaluationProvider({
      ...f.options,
      sessions,
      approval: { ...approval, runId: 'second-run' },
    })
    await assert.rejects(
      second.complete((await requests())[0]!, new AbortController().signal),
      /budget_restore_accounting_hold/,
    )
    assert.equal(f.wires.length, 0)
  } finally {
    await f.close()
    db.close()
  }
})

test('actual frozen runner traverses all native synthetic variants and bounded unknown stops after one request', async () => {
  const f = await fixture()
  try {
    const provider = createNativeEvaluationProvider(f.options)
    const report = await runMemoryEvaluation({
      mode: 'real-run',
      provider,
      authorization: {
        confirmed: true,
        fixtureHash: evaluationFixtureHash,
        providerId: 'fixture',
        modelId: 'fixture',
        maxCalls: 18,
        maxUsd: 0.1,
        timeoutMs: 1000,
      },
    })
    assert.equal(report.status, 'completed')
    assert.equal(report.calls, 18)
    assert.equal(f.wires.length, 18)
    assert.ok(!JSON.stringify(report).includes(secret))
  } finally {
    await f.close()
  }
  const { default: Database } = await import('better-sqlite3')
  const { EvaluationSessionLedger } = await import('../src/memory/evaluation/session-ledger.ts')
  const { createBoundedNativeEvaluationProvider } =
    await import('../src/memory/evaluation/native-provider.ts')
  const unknown = await fixture('unknown'),
    db = new Database(':memory:')
  try {
    unknown.store.set('settings', { ...unknown.store.settings, budgetExperimentEnabled: false })
    const provider = createBoundedNativeEvaluationProvider({
      ...unknown.options,
      sessions: new EvaluationSessionLedger(db),
      approval: {
        ...unknown.options.approval,
        costMode: 'request_bounded',
        acceptUnknownCostRisk: true,
      },
    })
    const report = await runMemoryEvaluation({
      mode: 'real-run',
      provider,
      authorization: {
        confirmed: true,
        fixtureHash: evaluationFixtureHash,
        providerId: 'fixture',
        modelId: 'fixture',
        maxCalls: 18,
        timeoutMs: 1000,
        costMode: 'request_bounded',
        acceptUnknownCostRisk: true,
      },
    })
    assert.equal(report.calls, 1)
    assert.equal(report.stopReason, 'provider_cost_unknown')
    assert.equal(report.execution!.hardUsdCap, false)
    assert.equal(unknown.wires.length, 1)
  } finally {
    await unknown.close()
    db.close()
  }
})
test('explicit inherited pipe accepts synthetic credential without argv/env/file input or output', async () => {
  const { spawnSync } = await import('node:child_process')
  const child = spawnSync(
    'bash',
    [
      '-c',
      `printf 'synthetic-pipe-only' | node --import tsx --input-type=module -e '
    import {EphemeralEvaluationCredential as Credential} from "./src/memory/evaluation/native-provider.ts";
    const value=Credential.fromPipe(3);
    if(value.resolve()!=="synthetic-pipe-only") process.exit(2);
    value.dispose(); console.log("pipe-consumed-disposed");
  ' 3<&0`,
    ],
    { cwd: process.cwd(), encoding: 'utf8', timeout: 5000 },
  )
  assert.equal(child.status, 0, child.stderr)
  assert.equal(child.stdout.trim(), 'pipe-consumed-disposed')
  assert.equal(child.stderr, '')
})

test('strict sessions also retain original deadline across factory recreation', async () => {
  const f = await fixture(),
    db = new Database(':memory:')
  try {
    const sessions = new EvaluationSessionLedger(db),
      opts = { ...f.options, sessions }
    createNativeEvaluationProvider(opts)
    db.prepare('UPDATE evaluation_runs SET deadline_ms=0').run()
    const recreated = createNativeEvaluationProvider(opts)
    await assert.rejects(
      recreated.complete((await requests())[0]!, new AbortController().signal),
      /run_not_ready/,
    )
    assert.equal(f.wires.length, 0)
  } finally {
    await f.close()
    db.close()
  }
})
test('post-response ledger failure retains a fixed error without leaking cleanup causes', async () => {
  const { createBoundedNativeEvaluationProvider } =
    await import('../src/memory/evaluation/native-provider.ts')
  for (const bounded of [false, true]) {
    const f = await fixture(),
      db = new Database(':memory:')
    try {
      const sessions = new EvaluationSessionLedger(db)
      if (bounded) f.store.set('settings', { ...f.store.settings, budgetExperimentEnabled: false })
      const provider = bounded
        ? createBoundedNativeEvaluationProvider({
            ...f.options,
            sessions,
            approval: {
              ...f.options.approval,
              costMode: 'request_bounded',
              acceptUnknownCostRisk: true,
            },
          })
        : createNativeEvaluationProvider({ ...f.options, sessions })
      f.beforeResponse(() => db.close())
      await assert.rejects(
        provider.complete((await requests())[0]!, new AbortController().signal),
        (error) => {
          assert.equal((error as Error).message, 'evaluation_provider_failed')
          assert.equal((error as Error).cause, undefined)
          return true
        },
      )
      assert.equal(f.wires.length, 1)
    } finally {
      await f.close()
      if (db.open) db.close()
    }
  }
})

test('recreated strict request uses persisted deadline in its actual transport AbortSignal', async () => {
  const f = await fixture(),
    db = new Database(':memory:')
  const originalFetch = globalThis.fetch
  try {
    const sessions = new EvaluationSessionLedger(db),
      opts = { ...f.options, sessions }
    createNativeEvaluationProvider(opts)
    const request = (await requests())[0]!
    db.prepare('UPDATE evaluation_runs SET deadline_ms=?').run(
      performance.timeOrigin + performance.now() + 80,
    )
    let physical = 0,
      aborted = false
    globalThis.fetch = async (_input, init) => {
      physical++
      const signal = init!.signal!
      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(
          () => reject(new Error('fixture_transport_deadline_not_enforced')),
          400,
        )
        const done = () => {
          aborted = true
          clearTimeout(timer)
          resolve()
        }
        if (signal.aborted) done()
        else signal.addEventListener('abort', done, { once: true })
      })
      throw new Error('fixture_aborted')
    }
    await assert.rejects(
      createNativeEvaluationProvider(opts).complete(request, new AbortController().signal),
      /provider_failed/,
    )
    assert.equal(physical, 1)
    assert.equal(aborted, true)
  } finally {
    globalThis.fetch = originalFetch
    await f.close()
    db.close()
  }
})

test('known strict cost-contract violation persists stopped with known subtotal across adapter recreation', async () => {
  const f = await fixture()
  try {
    const opts = { ...f.options, verified: { ...f.options.verified, maxCostUsd: () => 0 } }
    const request = (await requests())[0]!
    const first = await createNativeEvaluationProvider(opts).complete(
      request,
      new AbortController().signal,
    )
    assert.ok(first.actualCostUsd! > 0)
    assert.equal(f.options.sessions.status(opts.approval.runId)!.state, 'stopped')
    assert.ok(f.options.sessions.status(opts.approval.runId)!.knownUsd > 0)
    await assert.rejects(
      createNativeEvaluationProvider(opts).complete(request, new AbortController().signal),
      /run_not_ready/,
    )
    assert.equal(f.wires.length, 1)
  } finally {
    await f.close()
  }
})
test('all hash-pinned synthetic roster objects and aliases are deeply frozen', async () => {
  const { evaluationPeople } = await import('../src/memory/evaluation/fixtures.ts')
  assert.ok(Object.isFrozen(evaluationPeople))
  assert.ok(Object.isFrozen(evaluationPeople[0]))
  assert.ok(Object.isFrozen(evaluationPeople[0].aliases))
  assert.throws(() => {
    ;(evaluationPeople[0] as { displayName: string }).displayName = 'unapproved-canary'
  }, TypeError)
  assert.equal((await requests())[1]!.speaker.displayName, '林澄')
})
test('inherited diagnostic writer receives no raw native error chain or provider body', async () => {
  const { withFlowContext } = await import('../src/diagnostics/flow.ts')
  const f = await fixture('malformed')
  try {
    const recorded: unknown[] = []
    const provider = createNativeEvaluationProvider(f.options)
    const request = (await requests())[0]!
    await assert.rejects(
      withFlowContext(
        {},
        () =>
          provider.complete(
            { ...request, utterance: 'unapproved-synthetic-canary' },
            new AbortController().signal,
          ),
        {
          log(...args) {
            recorded.push(args)
          },
        },
      ),
      /synthetic_fixture/,
    )
    await assert.rejects(
      withFlowContext({}, () => provider.complete(request, new AbortController().signal), {
        log(...args) {
          recorded.push(args)
        },
      }),
      /provider_failed/,
    )
    assert.deepEqual(recorded, [])
    assert.equal(f.wires.length, 1)
  } finally {
    await f.close()
  }
})
