import { z } from 'zod'
import type {
  CommitmentAuthority,
  CommitmentContext,
  CommitmentExposure,
  CommitmentRevision,
  CommitmentView,
} from '../domain/commitments.ts'
import { currentEvidence } from './transitions.ts'
import {
  temporalHash,
  qualifyRecordedState,
  type CommitmentTimeline,
} from './temporal-contracts.ts'
export interface TemporalHistoryResult {
  recordedStates: {
    recordedAt: string
    mutationVersion: number
    reason: string
    activeVersion: number | null
    latestVersion: number
    status: 'available' | 'redacted'
    states: CommitmentView[]
  }[]
  coverage: 'limited'
  reasons: (
    | 'legacy_history_unknown'
    | 'retention_incomplete'
    | 'recorded_clock_nonmonotonic'
    | 'no_recorded_state'
    | 'privacy_or_proof_unavailable'
    | 'result_limit'
    | 'anchor_version_bound'
  )[]
  interpretation: 'previously_recorded_declaration_state_not_current_truth'
  sourceBodyCoverage: 'unavailable'
  stateTime: 'server_recorded_not_occurred'
  window: {
    maxTransitions: 64
    firstRecordedAt: string | null
    firstMutationVersion: number | null
    prunedThroughMutationVersion: number
    throughMutationVersion: number
  }
}
export function readCommitmentTimeline(
  ctx: CommitmentContext,
  anchor: CommitmentExposure,
  timeline: CommitmentTimeline | undefined,
  input: { asOfRecorded?: string; limit?: number },
  ports: {
    revision(version: number): CommitmentRevision | undefined
    authority: CommitmentAuthority
  },
): TemporalHistoryResult {
  const limit = input.limit ?? 20
  if (
    !Number.isSafeInteger(limit) ||
    limit < 1 ||
    limit > 20 ||
    (input.asOfRecorded !== undefined &&
      !z.iso.datetime({ offset: true }).safeParse(input.asOfRecorded).success)
  )
    throw new Error('commitment_temporal_query_invalid')
  const result: TemporalHistoryResult = {
    recordedStates: [],
    coverage: 'limited',
    reasons: [],
    interpretation: 'previously_recorded_declaration_state_not_current_truth',
    sourceBodyCoverage: 'unavailable',
    stateTime: 'server_recorded_not_occurred',
    window: {
      maxTransitions: 64,
      firstRecordedAt: timeline?.window.firstRecordedAt ?? null,
      firstMutationVersion: timeline?.window.firstMutationVersion ?? null,
      prunedThroughMutationVersion: timeline?.window.prunedThroughMutationVersion ?? 0,
      throughMutationVersion: anchor.mutationVersion,
    },
  }
  if (!timeline) {
    result.reasons.push('legacy_history_unknown')
    return result
  }
  if (timeline.window.firstMutationVersion > 1) result.reasons.push('legacy_history_unknown')
  if (timeline.window.prunedThroughMutationVersion) result.reasons.push('retention_incomplete')
  if (timeline.window.clockNonmonotonic) {
    result.reasons.push('recorded_clock_nonmonotonic')
    if (input.asOfRecorded) return result
  }
  const rows = timeline.transitions.filter(
    (t) =>
      t.mutationVersion <= anchor.mutationVersion &&
      (!input.asOfRecorded || Date.parse(t.recordedAt) <= Date.parse(input.asOfRecorded)),
  )
  const selected = input.asOfRecorded ? rows.slice(-1) : rows.slice(-limit)
  if (!selected.length) {
    result.reasons.push('no_recorded_state')
    return result
  }
  if (!input.asOfRecorded && rows.length > limit) result.reasons.push('result_limit')
  for (const row of selected) {
    const states: CommitmentView[] = []
    let redacted = row.states === null
    let privacyRedacted = row.states === null
    for (const state of row.states ?? []) {
      if (state.ref.version > anchor.version) {
        redacted = true
        if (!result.reasons.includes('anchor_version_bound'))
          result.reasons.push('anchor_version_bound')
        continue
      }
      const revision = ports.revision(state.ref.version)
      if (!revision || !state.termsHash || temporalHash(revision.terms) !== state.termsHash) {
        privacyRedacted = true
        redacted = true
        continue
      }
      const qualified = qualifyRecordedState(state, (e) =>
        currentEvidence(ctx, e, {
          ...ports.authority,
          current: (c, r) => ports.authority.historical?.(c, r),
        }),
      )
      if (!qualified) {
        privacyRedacted = true
        redacted = true
        continue
      }
      const { termsHash: _hash, termsRecordedAt, ...view } = qualified
      states.push({
        ...view,
        terms: structuredClone(revision.terms),
        recordedAt: termsRecordedAt,
        evidence: qualified.evidence,
        requiredProposalGroups: qualified.requiredProposalGroups,
      })
    }
    if (privacyRedacted && !result.reasons.includes('privacy_or_proof_unavailable'))
      result.reasons.push('privacy_or_proof_unavailable')
    result.recordedStates.push({
      recordedAt: row.recordedAt,
      mutationVersion: row.mutationVersion,
      reason: row.reason,
      activeVersion: row.head.activeVersion,
      latestVersion: row.head.latestVersion,
      status: redacted ? 'redacted' : 'available',
      states,
    })
  }
  return result
}
