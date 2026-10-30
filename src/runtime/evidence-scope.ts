/** Retrieval scope is assigned by the server tool boundary, never inferred from a result count. */
export interface RetrievalCoverage {
  scope:
    | 'current_authorized_query_results'
    | 'current_authorized_person_results'
    | 'requested_message_ids'
    | 'bounded_message_window'
    | 'retained_history_window'
    | 'tool_defined_result'
  historyCoverage: 'not_established'
}
export function retrievalCoverage(toolName?: string): RetrievalCoverage {
  const scope =
    toolName === 'memory.search'
      ? 'current_authorized_query_results'
      : toolName === 'memory.person'
        ? 'current_authorized_person_results'
        : toolName === 'messages.get'
          ? 'requested_message_ids'
          : toolName === 'messages.around' || toolName === 'messages.byUser'
            ? 'bounded_message_window'
            : ['memory.history', 'memory.commitmentHistory', 'memory.commitmentTimeline'].includes(
                  toolName ?? '',
                )
              ? 'retained_history_window'
              : 'tool_defined_result'
  return { scope, historyCoverage: 'not_established' }
}
export const EVIDENCE_SCOPE_CONTRACT =
  'Delivery coverage describes only the returned tool result. Retrieval scope never establishes exhaustive history: historyCoverage not_established forbids inferring only, all, or none historically from result counts, even if the plan suggests it. Preserve narrower state qualifications. Coverage metadata does not verify the truth of declarations.'
