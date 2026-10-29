import { appliedExperienceOperation } from './operation-eligibility.ts'

type Input = Parameters<typeof appliedExperienceOperation>[0]
type Operation = ReturnType<typeof appliedExperienceOperation>
interface Fixture {
  id: string
  input: Input
  expected: Operation
}
const person: Input = {
  action: 'exact_person_before_semantic',
  condition: { kind: 'person_question', personId: 'fixed-holdout-person' },
  tool: 'memory.person',
  arguments: { personId: 'fixed-holdout-person' },
  value: [{ id: 'fixed-authorized-record' }],
  semanticRead: false,
  turnValid: true,
}
const plan: Input = {
  action: 'verify_plan_qualifiers',
  condition: { kind: 'plan_question' },
  tool: 'memory.relations',
  arguments: {},
  value: [
    {
      relationCoverage: 'complete',
      relationEvidence: [{ polarity: 'negative', conditions: ['only if explicitly confirmed'] }],
    },
  ],
  semanticRead: false,
  turnValid: true,
}
const fixtures: Fixture[] = [
  { id: 'scoped-person-first', input: person, expected: 'memory_person' },
  {
    id: 'other-person',
    input: { ...person, arguments: { personId: 'other-person' } },
    expected: undefined,
  },
  {
    id: 'semantic-read-already-occurred',
    input: { ...person, semanticRead: true },
    expected: undefined,
  },
  { id: 'person-empty-result', input: { ...person, value: [] }, expected: undefined },
  { id: 'person-tool-mismatch', input: { ...person, tool: 'memory.search' }, expected: undefined },
  { id: 'invalidated-turn-person', input: { ...person, turnValid: false }, expected: undefined },
  {
    id: 'person-condition-mismatch',
    input: { ...person, condition: { kind: 'plan_question' } },
    expected: undefined,
  },
  { id: 'qualified-plan-read', input: plan, expected: 'plan_qualifiers_checked' },
  {
    id: 'plan-limited-coverage',
    input: {
      ...plan,
      value: [{ relationCoverage: 'limited', relationEvidence: [{ polarity: 'positive' }] }],
    },
    expected: undefined,
  },
  {
    id: 'plan-mixed-limited-packet',
    input: {
      ...plan,
      value: [
        { relationCoverage: 'complete', relationEvidence: [{ polarity: 'positive' }] },
        { relationCoverage: 'limited', relationEvidence: [] },
      ],
    },
    expected: undefined,
  },
  {
    id: 'plan-no-qualifications',
    input: { ...plan, value: [{ relationCoverage: 'complete', relationEvidence: [] }] },
    expected: undefined,
  },
  {
    id: 'plan-unstructured-result',
    input: { ...plan, value: [{ id: 'record-only' }] },
    expected: undefined,
  },
  { id: 'invalidated-turn-plan', input: { ...plan, turnValid: false }, expected: undefined },
  {
    id: 'plan-condition-mismatch',
    input: { ...plan, condition: { kind: 'person_question', personId: 'fixed-holdout-person' } },
    expected: undefined,
  },
  {
    id: 'style-is-not-an-observed-operation',
    input: {
      ...person,
      action: 'conclusion_before_detail',
      condition: { kind: 'response_style', personId: 'fixed-holdout-person' },
    },
    expected: undefined,
  },
  {
    id: 'transport-ack-is-not-application',
    input: { ...plan, tool: 'message.sent', value: [{ status: 'sent', messageId: 'fixed-ack' }] },
    expected: undefined,
  },
]
const freeze = (value: unknown): void => {
  if (!value || typeof value !== 'object') return
  for (const child of Object.values(value)) freeze(child)
  Object.freeze(value)
}
freeze(fixtures)

/** Fixed synthetic behavior contexts; never evidence of language quality or causal benefit. */
export const experienceBehaviorHoldouts: readonly Fixture[] = fixtures
export function runExperienceBehaviorHoldouts() {
  return fixtures.map(({ id, input, expected }) => ({
    id,
    passed: appliedExperienceOperation(input) === expected,
  }))
}
