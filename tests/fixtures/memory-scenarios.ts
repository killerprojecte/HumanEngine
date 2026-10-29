/** Synthetic specifications, not independent human-reviewed semantic gold. */
export const memoryScenarioIds = {
  group: 'fixture:account-a:group',
  otherGroup: 'fixture:account-a:other-group',
  private: 'fixture:account-a:private-lin',
  otherAccount: 'fixture:account-b:group',
  lin: 'fixture:account-a:lin-1',
  otherLin: 'fixture:account-a:lin-3',
  accountBLin: 'fixture:account-b:lin-1',
  zhou: 'fixture:account-a:zhou-2',
} as const

export interface SemanticMemoryScenario {
  id: string
  evaluationStatus: 'pending_real_model_and_independent_label_review'
  speakerId: string
  sourceText: string
  expected: string[]
  forbidden: string[]
}

/** Future evaluation inputs: these are not fed into a fake model and scored as accuracy. */
export const semanticMemoryScenarios: readonly SemanticMemoryScenario[] = [
  {
    id: 'one_time_constraint_not_permanent_preference',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.zhou,
    sourceText: '我不是戒咖啡，只是这周五要早睡，所以这次要无咖啡因饮品。',
    expected: ['Zhou requests caffeine-free drinks for this Friday only.'],
    forbidden: ['Zhou permanently avoids caffeine.', 'Zhou has a medical condition.'],
  },
  {
    id: 'reported_quote_not_speaker_self_report',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.lin,
    sourceText: '周青说“我不喝咖啡了”，这是他的话，不是我说的。',
    expected: ['Lin reports a statement attributed to Zhou; it is not independently verified.'],
    forbidden: ['Lin does not drink coffee.', 'The quote is an independently observed fact.'],
  },
  {
    id: 'joking_literal_not_personal_fact',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.lin,
    sourceText: '我以后永远不喝咖啡，开玩笑的。',
    expected: ['Do not promote the joking statement to an explicit permanent preference.'],
    forbidden: ['Lin permanently avoids caffeine.'],
  },
  {
    id: 'hypothetical_not_observation',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.lin,
    sourceText: '假如我以后不喝咖啡，就把杯子送人。',
    expected: ['The coffee and cup statements are conditional, not completed events.'],
    forbidden: ['Lin has stopped drinking coffee.', 'Lin has given away the cup.'],
  },
  {
    id: 'code_example_not_preference',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.lin,
    sourceText: '测试字符串是“我戒咖啡了”，只用来验证解析器。',
    expected: ['The quoted text is parser test data.'],
    forbidden: ['Lin has stopped drinking coffee.'],
  },
  {
    id: 'explicit_qualified_positive_control',
    evaluationStatus: 'pending_real_model_and_independent_label_review',
    speakerId: memoryScenarioIds.zhou,
    sourceText: '从今天起，我工作日晚上不喝咖啡，周末不受这个限制。',
    expected: [
      'Zhou avoids coffee on weekday evenings from the event date, with a weekend exception.',
    ],
    forbidden: ['Zhou never drinks coffee.', 'The rule also applies to weekend evenings.'],
  },
]
