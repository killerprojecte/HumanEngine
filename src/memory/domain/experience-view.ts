import type { MemoryRecord } from './contracts.ts'
import type { ProofParentReference } from './proofs.ts'
import type { CommitmentExposure, CommitmentView } from './commitments.ts'

/** Read DTO only. These references do not grant authority or describe an observed real event. */
export interface ExperienceAssertionSnapshot {
  ref: ProofParentReference
  /** Trusted hash of full current proof/source-event/clock qualification; never model-provided. */
  qualificationKey: string
  record: MemoryRecord
}
export type ExperienceEntry =
  | {
      kind: 'reported_experience'
      assertionRef: ProofParentReference
      qualificationKey: string
      record: MemoryRecord
      time: { occurredAt: string | null; recordedAt: string }
    }
  | {
      kind: 'shared_plan_declaration'
      commitmentRef: CommitmentExposure
      view: CommitmentView
      completionBasis: 'participant_self_report_not_observed_event'
    }
export interface ExperienceViewCoverage {
  status: 'complete' | 'limited'
  reasons: Array<'upstream_limited' | 'entry_limit'>
  returned: number
  /** Only known supplied candidates omitted by the output cap, not unknown upstream results. */
  omittedEntries: number
}
export interface ExperienceView {
  items: ExperienceEntry[]
  coverage: ExperienceViewCoverage
  interpretation: 'independent_qualified_records_not_synthesized_episodes'
  sourceBodyCoverage: 'unavailable'
}
export interface ExperienceProjectionInput {
  sceneId: string
  simulation: boolean
  assertions: readonly ExperienceAssertionSnapshot[]
  commitments: readonly CommitmentView[]
  upstreamCoverage: { assertions: 'complete' | 'limited'; commitments: 'complete' | 'limited' }
  limit?: number
}
/** One owner-controlled, scope-authorized batch. Undefined means stale/ineligible, not no history. */
export interface ExperienceRefreshPort {
  resolveCurrentBatch(
    sceneId: string,
    references: {
      assertions: readonly ProofParentReference[]
      commitments: readonly CommitmentExposure[]
    },
  ):
    | { assertions: readonly ExperienceAssertionSnapshot[]; commitments: readonly CommitmentView[] }
    | undefined
}
