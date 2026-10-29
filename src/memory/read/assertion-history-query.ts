import type { ProofParentReference } from '../domain/proofs.ts'
import type {
  AssertionHistoryQuery,
  AssertionHistoryPage,
  AssertionRevision,
} from '../storage/assertion-history.ts'

export interface HistoricalAssertionQuery {
  anchor: ProofParentReference
  asOfRecorded?: string
  validAt?: string
  limit?: number
}
const historicalQualifications = [
  'source_event_metadata_unavailable',
  'source_event_binding_unknown',
  'parent_revision_metadata_unavailable',
  'parent_binding_unknown',
  'proof_traversal_limited',
] as const
export type HistoricalQualification = (typeof historicalQualifications)[number]
export interface AssertionHistoricalReadPort {
  /** Must validate actual exposure, current scene/epoch/deletion and recovery readiness. */
  authorizeAnchor(anchor: ProofParentReference): string | undefined
  query(id: string, scopeKey: string, input: AssertionHistoryQuery): AssertionHistoryPage
  /** Current privacy plus historical AND/OR proof authority, not current-fact applicability. */
  authorizeRevision(revision: AssertionRevision): boolean
  /** Missing metadata qualifies authorized history; it is not itself a privacy retraction. */
  qualification?(revision: AssertionRevision): readonly HistoricalQualification[]
}
const time = (v: unknown): v is string =>
  typeof v === 'string' && Number.isFinite(Date.parse(v)) && new Date(v).toISOString() === v

/** Historical records are previously recorded evidence, never implicit current facts or new learning. */
export function queryAssertionHistory(
  input: HistoricalAssertionQuery,
  port: AssertionHistoricalReadPort,
) {
  const anchor = input.anchor
  const limit = input.limit ?? 20
  if (
    !anchor ||
    typeof anchor.id !== 'string' ||
    !anchor.id ||
    !Number.isSafeInteger(anchor.version) ||
    anchor.version < 1 ||
    !Number.isSafeInteger(anchor.deletionEpoch) ||
    anchor.deletionEpoch < 0 ||
    !Number.isSafeInteger(limit) ||
    limit < 1 ||
    limit > 20 ||
    (input.asOfRecorded !== undefined && !time(input.asOfRecorded)) ||
    (input.validAt !== undefined && !time(input.validAt))
  )
    throw new Error('memory_history_query_invalid')
  const scope = port.authorizeAnchor(anchor)
  if (!scope) throw new Error('memory_history_anchor_unavailable')
  const page = port.query(anchor.id, scope, {
    throughVersion: anchor.version,
    asOfRecorded: input.asOfRecorded,
    validAt: input.validAt,
    limit,
  })
  const reasons = new Set<string>()
  if (page.hasMore) reasons.add('result_limit')
  if (page.unknownRecordedTime) reasons.add('recorded_time_unknown')
  if (page.retentionIncomplete) reasons.add('retention_incomplete')
  const revisions = page.revisions.flatMap((revision) => {
    if (
      revision.assertionId !== anchor.id ||
      revision.version > anchor.version ||
      !port.authorizeRevision(revision)
    ) {
      reasons.add('authorization_or_proof_incomplete')
      return []
    }
    const qualifications = port.qualification?.(revision) ?? []
    if (
      !Array.isArray(qualifications) ||
      qualifications.some(
        (reason) => !(historicalQualifications as readonly string[]).includes(reason),
      )
    )
      throw new Error('memory_history_qualification_invalid')
    qualifications.forEach((reason) => reasons.add(reason))
    if (revision.event.legacyImport) reasons.add('legacy_import')
    if (revision.recordedAt === null) reasons.add('recorded_time_unknown')
    const validity =
      revision.validFrom === null || revision.validTo === null
        ? ('unknown' as const)
        : ('bounded' as const)
    if (validity === 'unknown') reasons.add('valid_time_unknown')
    if (
      revision.supports.some(
        (proof) =>
          proof.sources.some((source) => source.eventRef === undefined) ||
          (proof.parentReferences === undefined && proof.parents.length > 0),
      )
    )
      reasons.add('legacy_proof_binding_unknown')
    return [
      {
        ...structuredClone(revision),
        ref: {
          id: revision.assertionId,
          version: revision.version,
          deletionEpoch: anchor.deletionEpoch,
        },
        validityCoverage: validity,
      },
    ]
  })
  if (port.authorizeAnchor(anchor) !== scope) throw new Error('memory_history_anchor_unavailable')
  return {
    kind: 'assertion_history' as const,
    interpretation: 'previously_recorded_not_current_truth' as const,
    sourceBodyCoverage: 'unavailable' as const,
    asOfRecorded: input.asOfRecorded,
    validAt: input.validAt,
    revisions,
    coverage: {
      status: reasons.size ? ('limited' as const) : ('retained_scope_only' as const),
      reasons: [...reasons].sort(),
      returned: revisions.length,
      hasMore: page.hasMore,
    },
  }
}
