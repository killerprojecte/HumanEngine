import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { knownDiagnosticCode } from '../src/diagnostics/error-codes.ts'
import { sanitizeError } from '../src/diagnostics/errors.ts'
import { flowCatalog, localizedError } from '../src/diagnostics/flow-catalog.ts'
import { diagnosticLines } from '../src/diagnostics/format.ts'
import { validateReplyPlan } from '../src/runtime/reply-plan.ts'

const secret = 'PRIVATE_MODEL_ARGUMENT_TOKEN_CANARY'
const plan = {
  action: 'reply',
  targetMessageIds: [],
  contextMessageIds: [],
  strategy: secret,
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: [],
  actions: [],
  memorySelections: [{ evidenceId: 'memory-id', memoryIds: ['memory-id'] }],
}
const receipt = {
  id: 'receipt-id',
  toolName: 'memory.search',
  value: [{ id: 'memory-id', content: secret }],
}

test('actual memory receipt mismatch remains an actionable safe diagnostic in all locales', () => {
  let failure: unknown
  try {
    validateReplyPlan(plan, {
      targetMessageIds: [],
      contextMessageIds: [],
      evidence: [receipt],
      actionTools: [],
    })
  } catch (error) {
    failure = error
  }
  assert.ok(failure instanceof Error)
  assert.equal(failure.message, 'planner_unknown_memory_evidence')
  const diagnostic = sanitizeError(failure)
  assert.equal(diagnostic.errorCode, 'planner_unknown_memory_evidence')
  assert.equal(diagnostic.rootCause?.adviceCode, 'planner_unknown_memory_evidence')
  for (const language of ['en', 'zh-CN', 'zh-TW']) {
    const text = localizedError(diagnostic.errorCode, language)
    assert.notEqual(text.summary, localizedError('unknown_error', language).summary)
    assert.match(text.advice, /evidenceId/)
    assert.match(text.advice, /memoryIds/)
    assert.equal(flowCatalog(language).reasonLabels[diagnostic.errorCode!], text.summary)
    assert.doesNotMatch(
      JSON.stringify({ diagnostic, text, lines: diagnosticLines(diagnostic, language) }),
      new RegExp(secret),
    )
  }
  assert.equal(knownDiagnosticCode(`planner_unknown_memory_evidence:${secret}`), undefined)
  assert.equal(
    sanitizeError(new Error(`planner_unknown_memory_evidence:${secret}`)).errorCode,
    'unknown_error',
  )
})

test('actual incomplete relation closure retains its distinct safe cause without copying evidence', () => {
  assert.throws(
    () =>
      validateReplyPlan(
        { ...plan, memorySelections: [{ evidenceId: 'receipt-id', memoryIds: ['memory-id'] }] },
        {
          targetMessageIds: [],
          contextMessageIds: [],
          actionTools: [],
          evidence: [
            {
              ...receipt,
              toolName: 'memory.relations',
              value: [
                {
                  id: 'memory-id',
                  content: secret,
                  relationEvidence: [
                    {
                      target: { entityId: 'entity' },
                      requiredMemoryIds: ['memory-id', 'unselected'],
                    },
                  ],
                },
              ],
            },
          ],
        },
      ),
    (error: unknown) => {
      const diagnostic = sanitizeError(error)
      assert.equal(diagnostic.errorCode, 'planner_incomplete_relation_evidence')
      for (const language of ['en', 'zh-CN', 'zh-TW'])
        assert.notEqual(
          localizedError(diagnostic.errorCode, language).summary,
          localizedError('unknown_error', language).summary,
        )
      assert.doesNotMatch(JSON.stringify(diagnostic), new RegExp(secret))
      return true
    },
  )
})

test('all literal reply plan validation failures and relation closure have registered localized diagnostics', () => {
  const validator = readFileSync('src/runtime/reply-plan.ts', 'utf8').split(
    '/** A bounded, side-effect-free planning loop.',
  )[0]!
  const relation = readFileSync('src/runtime/relation-evidence.ts', 'utf8')
  const codes = new Set(
    [...`${validator}\n${relation}`.matchAll(/new Error\('(planner_[a-z_]+)'\)/g)].map(
      (match) => match[1]!,
    ),
  )
  assert.ok(codes.has('planner_unknown_memory_evidence'))
  assert.ok(codes.has('planner_incomplete_relation_evidence'))
  for (const code of codes) {
    assert.equal(knownDiagnosticCode(code), code)
    for (const language of ['en', 'zh-CN', 'zh-TW']) {
      const catalog = JSON.parse(
        readFileSync(`src/diagnostics/catalogs/errors-${language}.json`, 'utf8'),
      )
      assert.ok(catalog[code]?.summary && catalog[code]?.advice, `${language}:${code}`)
      assert.ok(flowCatalog(language).reasonLabels[code], `${language}:${code}`)
    }
  }
})
