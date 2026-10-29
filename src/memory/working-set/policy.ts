import {
  DEFAULT_WORKING_SET_POLICY,
  type WorkingSetItem,
  type WorkingSetPolicy,
  type WorkingSetReference,
  type WorkingSetSelection,
  type WorkingSetSnapshot,
} from './contracts.ts'

export function workingSetPolicy(overrides: Partial<WorkingSetPolicy> = {}): WorkingSetPolicy {
  const policy = { ...DEFAULT_WORKING_SET_POLICY, ...overrides }
  if (
    Object.values(policy).some((value) => !Number.isSafeInteger(value) || value <= 0) ||
    policy.maxBatch > 200 ||
    policy.maxReceipts > 100_000 ||
    policy.receiptRetentionMs <= policy.selectionMaxAgeMs
  )
    throw new Error('working_set_policy_invalid')
  return policy
}
export const entityKey = (ref: WorkingSetReference) => JSON.stringify([ref.kind, ref.id])
export const referenceKey = (ref: WorkingSetReference) =>
  JSON.stringify([
    ref.kind,
    ref.id,
    ref.version,
    ref.deletionEpoch,
    ref.kind === 'commitment' ? ref.mutationVersion : null,
  ])
export const selectionKey = (value: WorkingSetSelection) =>
  JSON.stringify([referenceKey(value.ref), value.dependencyKey])
export const sameSelection = (a: WorkingSetSelection, b: WorkingSetSelection) =>
  selectionKey(a) === selectionKey(b)

/** Accessibility only. Neither time nor repeated selection changes evidence or truth. */
export function classifyWorkingSet(
  snapshot: WorkingSetSnapshot,
  item: WorkingSetItem | undefined,
  now: number,
  policy: WorkingSetPolicy,
  pinned = false,
  unresolvedPolicy = false,
) {
  const last = item?.lastSelectedAt ?? snapshot.recordedAt
  const age = last === null ? null : Math.max(0, now - last)
  return {
    state:
      pinned || unresolvedPolicy || snapshot.protection
        ? ('hot' as const)
        : !item || age === null
          ? ('unknown' as const)
          : age < policy.coldAfterMs
            ? ('hot' as const)
            : ('cold' as const),
    reviewDue:
      snapshot.recordedAt !== null &&
      ['inferred', 'hypothesis'].includes(snapshot.epistemicKind ?? '') &&
      Math.max(0, now - snapshot.recordedAt) >= policy.reviewAfterMs,
  }
}
