/** Explicit compatibility delta; the original source/oracle and schemas stay frozen. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { z } from 'zod'
const root = process.env.HUMANENGINE_LEGACY_SOURCE
assert.ok(root, 'HUMANENGINE_LEGACY_SOURCE must select the amended TypeScript checkout')
assert.equal(process.version, 'v24.22.1')
const { ReplyPlanSchema, validateReplyPlan, buildReplyPlan } = await import(
  pathToFileURL(resolve(root, 'src/runtime/reply-plan.ts')).href
)
const { buildReplyBrief } = await import(
  pathToFileURL(resolve(root, 'src/runtime/reply-brief.ts')).href
)
const check = process.argv.includes('--check')
const write = (path, value) => {
  const text = JSON.stringify(value, null, 2) + '\n'
  if (check) assert.equal(readFileSync(path, 'utf8'), text, path)
  else writeFileSync(path, text)
}
const frozen = JSON.parse(readFileSync('backend/internal/replyplan/testdata/node.json', 'utf8'))
const original = frozen.cases.find((c) => c.name === 'required-emoji')
assert.equal(original.expected, '{"error":"planner_invalid_plan"}')
const originalInput = JSON.parse(original.input)
const capture = (fn) => {
  try {
    return { value: fn() }
  } catch (e) {
    return { error: e.message }
  }
}
const cases = [
  {
    ...original,
    expected: JSON.stringify(
      capture(() => validateReplyPlan(originalInput.value, originalInput.input)),
    ),
  },
]
assert.ok(JSON.parse(cases[0].expected).value, 'source must accept omitted emoji')
const plan = {
  action: 'reply',
  actions: [],
  assets: [],
  constraints: ['Answer briefly.'],
  contextMessageIds: ['m1', 'm2'],
  expressionGoals: ['Offer a warm response.'],
  facts: [],
  strategy: 'Answer the request.',
  targetMessageIds: ['m2'],
}
const scope = {
  targetMessageIds: ['m2'],
  contextMessageIds: ['m1', 'm2'],
  evidence: [],
  actionTools: [],
}
for (const [name, patch] of [
  ['omitted', {}],
  ['empty', { emoji: [] }],
  ['null', { emoji: null }],
  ['string', { emoji: '🙂' }],
  ['number', { emoji: 0 }],
  ['invalid-item', { emoji: [1] }],
  ['empty-item', { emoji: [''] }],
  ['too-many', { emoji: Array(9).fill('🙂') }],
]) {
  const value = { ...plan, ...patch }
  cases.push({
    name,
    kind: 'validate',
    input: JSON.stringify({ value, input: scope }),
    expected: JSON.stringify(capture(() => validateReplyPlan(value, scope))),
  })
}
let contractDelta
const checkDelta = (name, current) => {
  const frozen = JSON.parse(readFileSync(`backend/internal/replyplan/schema/${name}.json`, 'utf8'))
  const schema = current.inputSchema ?? current
  const base = frozen.inputSchema ?? frozen
  const delta = { emoji: schema.properties.emoji, required: schema.required }
  if (contractDelta) assert.deepEqual(delta, contractDelta)
  else contractDelta = delta
  base.properties.emoji = delta.emoji
  base.required = delta.required
  assert.deepEqual(frozen, current, `${name}: unexpected change outside optional input defaults`)
}
for (const suggestions of [false, true]) {
  const suffix = suggestions ? '-suggestions' : ''
  checkDelta(
    `plan${suffix}`,
    z.toJSONSchema(
      suggestions ? ReplyPlanSchema : ReplyPlanSchema.omit({ replySuggestion: true }),
      { io: 'input' },
    ),
  )
  await buildReplyPlan({
    ...scope,
    messages: [],
    signal: new AbortController().signal,
    eligible: () => true,
    readTools: [],
    replySuggestionsEnabled: suggestions,
    execute: async () => {
      throw Error('unexpected read')
    },
    complete: async (_messages, tools) => {
      assert.equal(tools.length, 1)
      assert.equal(tools[0].name, 'reply.plan')
      assert.ok(!tools[0].inputSchema.required.includes('emoji'))
      checkDelta(`plan-tool${suffix}`, tools[0])
      const calls = [{ id: 'plan', name: 'reply.plan', arguments: plan }]
      return {
        text: '',
        toolCalls: calls,
        assistant: { role: 'assistant', content: '', toolCalls: calls },
      }
    },
  })
}
write('backend/internal/replyplan/schema/plan-input-defaults.json', contractDelta)
let completions = 0
const result = await buildReplyPlan({
  ...scope,
  messages: [],
  signal: new AbortController().signal,
  eligible: () => true,
  readTools: [],
  execute: async () => {
    throw Error('unexpected read')
  },
  complete: async () => {
    completions++
    assert.equal(completions, 1, 'omitted emoji must not invoke plan repair')
    const calls = [{ id: 'plan', name: 'reply.plan', arguments: plan }]
    return {
      text: '',
      toolCalls: calls,
      assistant: { role: 'assistant', content: '', toolCalls: calls },
    }
  },
})
const brief = buildReplyBrief(result.plan, [], [], [], [])
write('backend/internal/replyplan/testdata/optional-emoji.json', {
  node: process.version,
  reference: '67bede7445b1b35acaa214097873c40d18bd9958',
  source: {
    path: 'src/runtime/reply-plan.ts',
    sha256: createHash('sha256')
      .update(readFileSync(resolve(root, 'src/runtime/reply-plan.ts')))
      .digest('hex'),
  },
  frozenExpected: original.expected,
  cases,
  handoff: {
    input: JSON.stringify({ value: plan, input: scope }),
    expected: JSON.stringify({ result, brief, completions }),
  },
})
console.log(
  `${cases.length} optional-emoji validation cases; one complete planner-to-Replyer brief handoff`,
)
