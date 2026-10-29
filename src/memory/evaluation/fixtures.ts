/** Synthetic specifications, not independent human-reviewed semantic gold. */
export const evaluationIds = {
  group: 'fixture:account-a:group',
  otherGroup: 'fixture:account-a:other-group',
  private: 'fixture:account-a:private-lin',
  otherAccount: 'fixture:account-b:group',
  lin: 'fixture:account-a:lin-1',
  otherLin: 'fixture:account-a:lin-3',
  accountBLin: 'fixture:account-b:lin-1',
  zhou: 'fixture:account-a:zhou-2',
} as const

export interface EvaluationSemanticCase {
  id: string
  evaluationStatus: 'pending_real_model_and_independent_label_review'
  speakerId: string
  sourceText: string
  expected: string[]
  forbidden: string[]
}

/** Frozen synthetic review expectations, not independently adjudicated semantic gold. */
export const evaluationSemanticCases: readonly EvaluationSemanticCase[] = [
  {
    id: 'one_time_constraint_not_permanent_preference',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.zhou,
    sourceText: '我不是戒咖啡，只是这周五要早睡，所以这次要无咖啡因饮品。',
    expected: ['Zhou requests caffeine-free drinks for this Friday only.'],
    forbidden: ['Zhou permanently avoids caffeine.', 'Zhou has a medical condition.'],
  },
  {
    id: 'reported_quote_not_speaker_self_report',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.lin,
    sourceText: '周青说“我不喝咖啡了”，这是他的话，不是我说的。',
    expected: ['Lin reports a statement attributed to Zhou; it is not independently verified.'],
    forbidden: ['Lin does not drink coffee.', 'The quote is an independently observed fact.'],
  },
  {
    id: 'joking_literal_not_personal_fact',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.lin,
    sourceText: '我以后永远不喝咖啡，开玩笑的。',
    expected: ['Do not promote the joking statement to an explicit permanent preference.'],
    forbidden: ['Lin permanently avoids caffeine.'],
  },
  {
    id: 'hypothetical_not_observation',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.lin,
    sourceText: '假如我以后不喝咖啡，就把杯子送人。',
    expected: ['The coffee and cup statements are conditional, not completed events.'],
    forbidden: ['Lin has stopped drinking coffee.', 'Lin has given away the cup.'],
  },
  {
    id: 'code_example_not_preference',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.lin,
    sourceText: '测试字符串是“我戒咖啡了”，只用来验证解析器。',
    expected: ['The quoted text is parser test data.'],
    forbidden: ['Lin has stopped drinking coffee.'],
  },
  {
    id: 'explicit_qualified_positive_control',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: evaluationIds.zhou,
    sourceText: '从今天起，我工作日晚上不喝咖啡，周末不受这个限制。',
    expected: [
      'Zhou avoids coffee on weekday evenings from the event date, with a weekend exception.',
    ],
    forbidden: ['Zhou never drinks coffee.', 'The rule also applies to weekend evenings.'],
  },
]

export const evaluationVersion = 'memory-evaluation-v2' as const
export const evaluationSeed = 20261029
export const evaluationVariants = [
  { id: 'no-memory', memory: false, historical: false },
  { id: 'ordinary-lexical-rag', memory: true, historical: false },
  { id: 'historical-read-on', memory: true, historical: true },
] as const
export const evaluationTimeline = [
  {
    id: 'initial',
    text: '周青工作日晚上不喝咖啡，周末不受此限制。',
    occurredAt: '2026-10-01T12:00:00.000Z',
  },
  {
    id: 'correction',
    text: '周青改为仅周一晚上不喝咖啡，其他时间不受此限制。',
    occurredAt: '2026-10-02T12:00:00.000Z',
  },
  { id: 'recall', sourceId: 'fixture:coffee-statement', occurredAt: '2026-10-03T12:00:00.000Z' },
] as const

export const evaluationPeople = [
  { id: evaluationIds.lin, displayName: '林澄', aliases: ['Lin'] },
  { id: evaluationIds.zhou, displayName: '周青', aliases: ['Zhou'] },
] as const

// Prevent in-process fixture edits from silently changing a hash-pinned run.
function freezeFixture(value: unknown): void {
  if (!value || typeof value !== 'object' || Object.isFrozen(value)) return
  for (const child of Object.values(value)) freezeFixture(child)
  Object.freeze(value)
}
for (const value of [
  evaluationIds,
  evaluationSemanticCases,
  evaluationVariants,
  evaluationTimeline,
  evaluationPeople,
])
  freezeFixture(value)
