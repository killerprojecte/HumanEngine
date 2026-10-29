/** Bounded public reason codes only; arbitrary estimator/SQLite exception text is never forwarded. */
export const budgetFailureCodes = [
  'budget_admission_failed',
  'budget_external_cost_unknown',
  'budget_restore_accounting_hold',
  'budget_amount_overflow',
  'budget_estimate_unknown',
  'budget_price_unknown',
  'budget_contract_overrun',
  'budget_daily_limit',
  'budget_lane_limit',
  'budget_turn_amount',
  'budget_turn_calls',
  'budget_turn_tokens',
  'budget_turn_identity_required',
  'budget_unbounded_exposure',
  'budget_unresolved_limit',
  'budget_output_cap_required',
  'budget_output_cap_unverified',
  'budget_protocol_unsupported',
  'budget_opaque_input_unsupported',
  'budget_verified_estimator_required',
  'budget_estimate_contract_mismatch',
  'budget_attempt_already_started',
  'budget_attempt_identity_conflict',
  'budget_idempotency_conflict',
  'budget_invalid_transition',
  'budget_attempt_owner',
  'budget_attempt_missing',
  'budget_release_started',
  'budget_receipt_conflict',
  'budget_configuration_invalid',
  'budget_configuration_changed',
] as const
export function isBudgetFailureCode(value: string): boolean {
  return (budgetFailureCodes as readonly string[]).includes(value)
}
export function safeBudgetFailureCode(value: unknown): string {
  const code = value instanceof Error ? value.message : value
  if (typeof code === 'string' && isBudgetFailureCode(code)) return code
  if (
    typeof code === 'string' &&
    [
      'invalid_budget_category',
      'invalid_budget_integer',
      'invalid_budget_price',
      'invalid_budget_estimate',
      'invalid_budget_identity',
      'invalid_budget_metadata',
      'invalid_budget_mode',
      'invalid_budget_request',
    ].includes(code)
  )
    return 'budget_configuration_invalid'
  return 'budget_admission_failed'
}
