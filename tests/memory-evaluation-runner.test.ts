import assert from 'node:assert/strict'
import test from 'node:test'
import { execFileSync, spawnSync } from 'node:child_process'
import {
  runMemoryEvaluation,
  evaluationFixtureHash,
  type EvaluationAuthorization,
  type EvaluationOptions,
  type EvaluationProvider,
} from '../src/memory/evaluation/runner.ts'
import { serializeMemoryEvaluation } from '../src/memory/evaluation/reporter.ts'

function authorization(patch: Partial<EvaluationAuthorization> = {}): EvaluationAuthorization {
  return {
    confirmed: true,
    fixtureHash: evaluationFixtureHash,
    providerId: 'synthetic-local-provider',
    modelId: 'synthetic-model',
    maxCalls: 18,
    maxUsd: 1,
    timeoutMs: 1_000,
    ...patch,
  } as EvaluationAuthorization
}
function provider(patch: Partial<EvaluationProvider> = {}): EvaluationProvider {
  return {
    id: 'synthetic-local-provider',
    modelId: 'synthetic-model',
    maxCostUsd: () => 0.01,
    complete: async () => ({
      prediction: 'SCRIPTED_OUTPUT_NOT_SEMANTIC_GOLD',
      actualCostUsd: 0.005,
    }),
    ...patch,
  }
}

test('fixed synthetic dry-run exercises true service baselines and keeps semantic judgment pending', async () => {
  const first = await runMemoryEvaluation()
  const second = await runMemoryEvaluation()
  assert.equal(first.status, 'completed')
  assert.equal(first.calls, 0)
  assert.equal(first.results.length, 21)
  assert.equal(
    first.fixtureHash,
    '3b1fe8994cefd505f0aba47540cab84b294969af41fabe3e8c35bb018b06aa2c',
  )
  assert.equal(serializeMemoryEvaluation(first), serializeMemoryEvaluation(second))
  assert.ok(first.results.every((item) => item.mechanicalFailures.length === 0))
  assert.ok(
    first.results
      .filter((item) => item.kind === 'semantic-extraction')
      .every(
        (item) =>
          item.prediction === null &&
          item.semanticStatus === 'human_review_pending' &&
          item.cost.status === 'unknown',
      ),
  )
  assert.equal(first.results.find((item) => item.variant === 'no-memory')!.prediction, '[]')
  assert.ok(
    first.results
      .find((item) => item.variant === 'ordinary-lexical-rag')!
      .prediction!.includes('周一'),
  )
})

test('default CLI is dry-run and rejects real-run before any provider or environment use', () => {
  const stdout = execFileSync(
    process.execPath,
    ['--import', 'tsx', 'scripts/memory-evaluation.ts'],
    { encoding: 'utf8', env: { ...process.env, SYNTHETIC_API_KEY: 'SECRET_CANARY_NOT_READ' } },
  )
  assert.equal(JSON.parse(stdout).calls, 0)
  assert.equal(stdout.includes('SECRET_CANARY_NOT_READ'), false)
  const blocked = spawnSync(
    process.execPath,
    ['--import', 'tsx', 'scripts/memory-evaluation.ts', '--real-run'],
    { encoding: 'utf8' },
  )
  assert.equal(blocked.status, 2)
  assert.equal(blocked.stdout, '')
})

test('real-run requires matching explicit authority and dry-run forbids injectable provider', async () => {
  let calls = 0
  const p = provider({
    complete: async () => {
      calls++
      return { prediction: '', actualCostUsd: 0 }
    },
  })
  await assert.rejects(
    runMemoryEvaluation({ mode: 'dry-run', provider: p } as unknown as EvaluationOptions),
    /dry_run_provider_forbidden/,
  )
  for (const patch of [
    { confirmed: false },
    { fixtureHash: 'wrong' },
    { providerId: 'other' },
    { maxCalls: 0 },
    { maxUsd: 0 },
  ]) {
    await assert.rejects(
      runMemoryEvaluation({
        mode: 'real-run',
        provider: p,
        authorization: authorization(patch as Partial<EvaluationAuthorization>),
      }),
      /evaluation_authorization_invalid/,
    )
  }
  assert.equal(calls, 0)
})

test('trusted fake adapter executes bounded prediction collection without inventing semantic scores', async () => {
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider(),
    authorization: authorization(),
  })
  assert.equal(report.status, 'completed')
  assert.equal(report.calls, 18)
  assert.ok(
    report.results
      .filter((r) => r.kind === 'semantic-extraction')
      .every(
        (r) =>
          r.prediction === 'SCRIPTED_OUTPUT_NOT_SEMANTIC_GOLD' &&
          r.semanticStatus === 'human_review_pending',
      ),
  )
  assert.ok(Math.abs(report.reportedCostUsd - 0.09) < 1e-9)
})

test('budget exhaustion prevents additional calls and unknown usage stops rather than assuming zero cost', async () => {
  const capped = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider(),
    authorization: authorization({ maxCalls: 1 }),
  })
  assert.equal(capped.calls, 1)
  assert.equal(capped.stopReason, 'authorization_budget_exhausted')
  const price = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider({ maxCostUsd: () => 2 }),
    authorization: authorization(),
  })
  assert.equal(price.calls, 0)
  const unknown = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider({ complete: async () => ({ prediction: 'synthetic' }) }),
    authorization: authorization(),
  })
  assert.equal(unknown.calls, 1)
  assert.equal(unknown.stopReason, 'provider_cost_unknown')
})

test('provider error secrets are not serialized and a nonsettling provider reaches a bounded unknown outcome', async () => {
  const failed = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider({
      complete: async () => {
        throw new Error('SECRET_CANARY_TOKEN_AND_URL')
      },
    }),
    authorization: authorization(),
  })
  assert.equal(failed.stopReason, 'provider_failed')
  assert.equal(serializeMemoryEvaluation(failed).includes('SECRET_CANARY'), false)
  let signal: AbortSignal | undefined
  const timed = await runMemoryEvaluation({
    mode: 'real-run',
    provider: provider({
      complete: async (_, current) => {
        signal = current
        return new Promise(() => {})
      },
    }),
    authorization: authorization({ timeoutMs: 10 }),
  })
  assert.equal(timed.stopReason, 'provider_timeout')
  assert.equal(timed.calls, 1)
  assert.equal(signal?.aborted, true)
})

test('authorization is snapshotted before adapter mutation and historical ablation changes actual context', async () => {
  const auth = authorization({ maxCalls: 1 })
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: auth,
    provider: provider({
      complete: async () => {
        auth.maxCalls = 100
        auth.maxUsd = 100
        return { prediction: 'synthetic', actualCostUsd: 0.005 }
      },
    }),
  })
  assert.equal(report.calls, 1)
  assert.equal(report.stopReason, 'authorization_budget_exhausted')
  const dry = await runMemoryEvaluation()
  const ordinary = dry.results.find((r) => r.variant === 'ordinary-lexical-rag')!
  const history = dry.results.find((r) => r.variant === 'historical-read-on')!
  assert.equal(ordinary.prediction!.includes('Previously recorded'), false)
  assert.equal(history.prediction!.includes('Previously recorded, not current truth:'), true)
})

test('requests carry synthetic speaker and mentioned-person attribution without semantic case names or gold', async () => {
  const requests: import('../src/memory/evaluation/runner.ts').EvaluationRequest[] = []
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: authorization({ maxCalls: 2 }),
    provider: provider({
      complete: async (request) => {
        requests.push(request)
        return { prediction: 'synthetic', actualCostUsd: 0.005 }
      },
    }),
  })
  assert.equal(requests[0]!.speaker.displayName, '周青')
  assert.equal(requests[1]!.speaker.displayName, '林澄')
  assert.deepEqual(requests[1]!.mentionedPeople, [
    { id: 'fixture:account-a:zhou-2', displayName: '周青' },
  ])
  assert.equal(requests[1]!.caseId, 'case-002')
  const payload = JSON.stringify(requests)
  assert.equal(payload.includes('reported_quote_not_speaker_self_report'), false)
  assert.equal(payload.includes('independently verified'), false)
  assert.equal(payload.includes('forbidden'), false)
  assert.deepEqual(report.execution, {
    providerId: 'synthetic-local-provider',
    modelId: 'synthetic-model',
    maxCalls: 2,
    maxUsd: 1,
    costMode: 'strict_usd',
    hardUsdCap: true,
    timeoutMs: 1_000,
  })
  assert.equal(report.runnerVersion, 'runner-v3')
  assert.equal(report.promptVersion, 'attribution-extraction-v2')
  assert.ok(
    report.results
      .filter((r) => r.prediction === 'synthetic')
      .every((r) => r.latencyMs !== null && r.latencyMs >= 0),
  )
})

test('provider/model report labels reject endpoint or recognizable credential forms before calls', async () => {
  let calls = 0
  for (const identity of ['https://example.invalid/?token=SECRET_CANARY', 'sk-SECRET_CANARY']) {
    await assert.rejects(
      runMemoryEvaluation({
        mode: 'real-run',
        authorization: authorization({ modelId: identity }),
        provider: provider({
          modelId: identity,
          complete: async () => {
            calls++
            return { prediction: 'synthetic', actualCostUsd: 0 }
          },
        }),
      }),
      /evaluation_authorization_invalid/,
    )
  }
  assert.equal(calls, 0)
})

test('request-bounded mode explicitly permits unknown upper prices but stops at first unknown actual cost', async () => {
  let calls = 0
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: authorization({
      costMode: 'request_bounded',
      acceptUnknownCostRisk: true,
      maxUsd: undefined,
      maxCalls: 2,
    }),
    provider: provider({
      maxCostUsd: () => undefined,
      complete: async () => {
        calls++
        return { prediction: 'synthetic' }
      },
    }),
  })
  assert.equal(calls, 1)
  assert.equal(report.stopReason, 'provider_cost_unknown')
  assert.equal(report.execution!.costMode, 'request_bounded')
  assert.equal(report.execution!.hardUsdCap, false)
  assert.equal(report.execution!.maxUsd, null)
  assert.equal(
    report.results.find((item) => item.prediction === 'synthetic')!.cost.status,
    'unknown',
  )
})

test('strict missing upper bound still refuses before calling provider', async () => {
  let calls = 0
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: authorization(),
    provider: provider({
      maxCostUsd: () => undefined,
      complete: async () => {
        calls++
        return { prediction: 'synthetic', actualCostUsd: 0 }
      },
    }),
  })
  assert.equal(calls, 0)
  assert.equal(report.stopReason, 'provider_budget_contract_invalid')
  assert.equal(report.execution!.hardUsdCap, true)
})

test('request-bounded risk acceptance is mandatory and malformed modes or prices fail before calls', async () => {
  let calls = 0
  for (const patch of [
    { costMode: 'request_bounded', acceptUnknownCostRisk: undefined, maxUsd: undefined },
    { costMode: 'request_bounded', acceptUnknownCostRisk: false },
    { costMode: 'unknown' },
    { costMode: 'request_bounded', acceptUnknownCostRisk: true, maxUsd: -1 },
    { maxUsd: undefined },
  ]) {
    await assert.rejects(
      runMemoryEvaluation({
        mode: 'real-run',
        authorization: authorization(patch as Partial<EvaluationAuthorization>),
        provider: provider({
          complete: async () => {
            calls++
            return { prediction: 'synthetic' }
          },
        }),
      }),
      /evaluation_authorization_invalid/,
    )
  }
  assert.equal(calls, 0)
})

test('request-bounded mode retains call limit while optional dollar reference is explicitly not a hard cap', async () => {
  const report = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: authorization({
      costMode: 'request_bounded',
      acceptUnknownCostRisk: true,
      maxUsd: 0.001,
      maxCalls: 1,
    }),
    provider: provider({
      maxCostUsd: () => undefined,
      complete: async () => ({ prediction: 'synthetic', actualCostUsd: 0.005 }),
    }),
  })
  assert.equal(report.calls, 1)
  assert.equal(report.reportedCostUsd, 0.005)
  assert.equal(report.stopReason, 'authorization_budget_exhausted')
  assert.equal(report.execution!.hardUsdCap, false)
  assert.equal(report.execution!.maxUsd, 0.001)
})

test('request-bounded mode retains deadline and invalid provider amounts are not converted to zero', async () => {
  const auth = authorization({
    costMode: 'request_bounded',
    acceptUnknownCostRisk: true,
    maxUsd: undefined,
    timeoutMs: 5,
  })
  const timed = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: auth,
    provider: provider({
      maxCostUsd: () => undefined,
      complete: async () => new Promise(() => {}),
    }),
  })
  assert.equal(timed.calls, 1)
  assert.equal(timed.stopReason, 'provider_timeout')
  const invalid = await runMemoryEvaluation({
    mode: 'real-run',
    authorization: auth,
    provider: provider({ maxCostUsd: () => NaN }),
  })
  assert.equal(invalid.calls, 0)
  assert.equal(invalid.stopReason, 'provider_budget_contract_invalid')
})
