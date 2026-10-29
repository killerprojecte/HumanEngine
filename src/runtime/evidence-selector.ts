/** Pure packing of already-authorized, complete evidence groups; never an authority source. */
export type JsonValue =
  null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue }
export interface EvidenceGroup<T extends JsonValue> {
  id: string
  rank: number
  units?: number
  mandatory?: boolean
  complete: boolean
  value: T
}
/** Tokenizer verification is a trusted server capability, not a user claim or calibration proof. */
export type EvidenceBudget =
  | { mode: 'bytes'; limit: number }
  | {
      mode: 'tokens'
      limit: number
      tokenizer: {
        verified: true
        version: string
        modelIdentity: string
        count(serialized: string): number
      }
    }
export type OmissionReason =
  'not_selected' | 'incomplete' | 'budget' | 'group_limit' | 'record_limit'
export interface SelectionResult<T extends JsonValue> {
  status: 'selected' | 'insufficient' | 'invalid'
  groups: readonly EvidenceGroup<T>[]
  serialized?: string
  usage?: {
    unit: 'bytes' | 'tokens'
    count: number
    limit: number
    tokenizerVersion?: string
    modelIdentity?: string
  }
  omitted: readonly { id: string; reason: OmissionReason }[]
  coverage: { candidates: number; selected: number; omitted: number }
  reason?: string
}
function immutableJson<T extends JsonValue>(value: T): T {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error('invalid_value')
    return value
  }
  if (Array.isArray(value)) return Object.freeze(value.map(immutableJson)) as unknown as T
  if (typeof value !== 'object' || Object.getPrototypeOf(value) !== Object.prototype)
    throw new Error('invalid_value')
  const copy = Object.fromEntries(
    Object.entries(value).map(([key, item]) => [key, immutableJson(item)]),
  )
  return Object.freeze(copy) as T
}
export function selectEvidenceGroups<T extends JsonValue>(input: {
  candidates: readonly EvidenceGroup<T>[]
  selectedIds?: readonly string[]
  budget: EvidenceBudget
  /** Trusted final prompt projection. Must preserve complete group meaning, and be deterministic. */
  serialize(groups: readonly T[]): string
}): SelectionResult<T> {
  const countCandidates = Array.isArray(input.candidates) ? input.candidates.length : 0
  const failed = (status: 'invalid' | 'insufficient', reason: string): SelectionResult<T> => ({
    status,
    reason,
    groups: [],
    omitted: [],
    coverage: {
      candidates: countCandidates,
      selected: 0,
      omitted: countCandidates,
    },
  })
  try {
    if (!Array.isArray(input.candidates) || countCandidates > 40)
      return failed('invalid', 'candidate_limit')
    const budget = input.budget
    if (!Number.isSafeInteger(budget.limit) || budget.limit < 0)
      return failed('invalid', 'invalid_budget')
    if (
      budget.mode !== 'bytes' &&
      (budget.mode !== 'tokens' ||
        budget.tokenizer?.verified !== true ||
        !budget.tokenizer.version ||
        !budget.tokenizer.modelIdentity)
    )
      return failed('invalid', 'unverified_tokenizer')
    const ids = new Set<string>()
    const candidates = input.candidates.map((group) => {
      if (
        !group.id ||
        ids.has(group.id) ||
        !Number.isFinite(group.rank) ||
        (group.units !== undefined &&
          (!Number.isSafeInteger(group.units) || group.units < 1 || group.units > 40)) ||
        typeof group.complete !== 'boolean'
      )
        throw new Error('invalid_candidate')
      ids.add(group.id)
      return Object.freeze({ ...group, value: immutableJson(group.value) })
    })
    const selected = input.selectedIds === undefined ? undefined : new Set(input.selectedIds)
    if (
      selected &&
      (selected.size !== input.selectedIds!.length || [...selected].some((id) => !ids.has(id)))
    )
      return failed('invalid', 'invalid_selection')
    if (selected && candidates.some((group) => group.mandatory && !selected.has(group.id)))
      return failed('invalid', 'mandatory_not_selected')
    const measure = (groups: readonly EvidenceGroup<T>[]) => {
      const serialized = input.serialize(Object.freeze(groups.map((group) => group.value)))
      if (typeof serialized !== 'string') throw new Error('invalid_serializer')
      JSON.parse(serialized)
      const count =
        budget.mode === 'bytes'
          ? new TextEncoder().encode(serialized).byteLength
          : budget.tokenizer.count(serialized)
      if (!Number.isSafeInteger(count) || count < 0 || (serialized.length > 0 && count === 0))
        throw new Error('invalid_count')
      return { serialized, count }
    }
    if (measure([]).count > budget.limit) return failed('insufficient', 'envelope_budget')
    const omitted: { id: string; reason: OmissionReason }[] = []
    const ordered = candidates.sort(
      (a, b) =>
        Number(!!b.mandatory) - Number(!!a.mandatory) ||
        b.rank - a.rank ||
        (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
    )
    const groups: EvidenceGroup<T>[] = []
    for (const group of ordered) {
      if (!group.complete) {
        if (group.mandatory) return failed('insufficient', 'mandatory_incomplete')
        omitted.push({ id: group.id, reason: 'incomplete' })
        continue
      }
      if (selected && !selected.has(group.id) && !group.mandatory) {
        omitted.push({ id: group.id, reason: 'not_selected' })
        continue
      }
      if (
        groups.reduce((sum, item) => sum + (item.units ?? 1), 0) + (group.units ?? 1) > 12 &&
        (group.units !== undefined || groups.some((item) => item.units !== undefined))
      ) {
        if (group.mandatory) return failed('insufficient', 'mandatory_record_limit')
        omitted.push({ id: group.id, reason: 'record_limit' })
        continue
      }
      if (groups.length === 12) {
        if (group.mandatory) return failed('insufficient', 'mandatory_group_limit')
        omitted.push({ id: group.id, reason: 'group_limit' })
        continue
      }
      if (measure([...groups, group]).count > budget.limit) {
        if (group.mandatory) return failed('insufficient', 'mandatory_budget')
        omitted.push({ id: group.id, reason: 'budget' })
        continue
      }
      groups.push(group)
    }
    if (!groups.length && countCandidates > 0)
      return {
        ...failed('insufficient', 'no_complete_evidence'),
        omitted: Object.freeze(omitted),
      }
    const final = measure(groups)
    // Reject nondeterministic projection/counters rather than reporting an unsafe budget guarantee.
    const repeated = measure(groups)
    if (final.serialized !== repeated.serialized || final.count !== repeated.count)
      return failed('invalid', 'unstable_measurement')
    if (final.count > budget.limit) return failed('insufficient', 'final_budget')
    return {
      status: 'selected',
      groups: Object.freeze(groups),
      serialized: final.serialized,
      usage: {
        unit: budget.mode,
        count: final.count,
        limit: budget.limit,
        ...(budget.mode === 'tokens'
          ? {
              tokenizerVersion: budget.tokenizer.version,
              modelIdentity: budget.tokenizer.modelIdentity,
            }
          : {}),
      },
      omitted: Object.freeze(omitted),
      coverage: {
        candidates: countCandidates,
        selected: groups.length,
        omitted: omitted.length,
      },
    }
  } catch {
    return failed('invalid', 'invalid_input_or_measurement')
  }
}
