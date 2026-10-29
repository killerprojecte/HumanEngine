import { isDeepStrictEqual } from 'node:util'
import type {
  ExperienceEntry,
  ExperienceProjectionInput,
  ExperienceRefreshPort,
  ExperienceView,
} from '../domain/experience-view.ts'
import type { ProofParentReference } from '../domain/proofs.ts'

const validRef = (ref: ProofParentReference) =>
  ref &&
  typeof ref.id === 'string' &&
  ref.id.length > 0 &&
  Number.isSafeInteger(ref.version) &&
  ref.version > 0 &&
  Number.isSafeInteger(ref.deletionEpoch) &&
  ref.deletionEpoch >= 0
const refKey = (ref: ProofParentReference) =>
  JSON.stringify([ref.id, ref.version, ref.deletionEpoch])
const entryKey = (entry: ExperienceEntry) =>
  entry.kind === 'reported_experience'
    ? `assertion:${refKey(entry.assertionRef)}`
    : `commitment:${refKey(entry.commitmentRef)}`
const freeze = <T>(value: T, seen = new WeakSet<object>()): T => {
  if (value && typeof value === 'object' && !seen.has(value)) {
    seen.add(value)
    for (const item of Object.values(value)) freeze(item, seen)
    Object.freeze(value)
  }
  return value
}
function invalid(): never {
  throw new Error('experience_projection_invalid_input')
}

/**
 * Deterministic read projection, not episode synthesis and not an authorization layer.
 * The owner supplies already-authorized current snapshots. Order is the supplied record
 * order (assertions, then plans), not an inferred chronology or joint participation.
 */
export function projectExperienceView(input: ExperienceProjectionInput): ExperienceView {
  if (!input || typeof input !== 'object') invalid()
  const limit = input.limit ?? 12
  if (
    !input.sceneId ||
    typeof input.simulation !== 'boolean' ||
    !Number.isSafeInteger(limit) ||
    limit < 1 ||
    limit > 12 ||
    !Array.isArray(input.assertions) ||
    !Array.isArray(input.commitments) ||
    input.assertions.length + input.commitments.length > 40 ||
    !['complete', 'limited'].includes(input.upstreamCoverage?.assertions) ||
    !['complete', 'limited'].includes(input.upstreamCoverage?.commitments)
  )
    invalid()
  const entries: ExperienceEntry[] = []
  const seen = new Set<string>()
  const append = (entry: ExperienceEntry) => {
    const key = entryKey(entry)
    if (seen.has(key)) invalid()
    seen.add(key)
    entries.push(entry)
  }
  if (!input.simulation) {
    for (const snapshot of input.assertions) {
      const record = snapshot?.record
      if (
        !record ||
        !record.scope ||
        typeof record.content !== 'string' ||
        typeof record.learnedAt !== 'string' ||
        !Array.isArray(record.sources) ||
        !validRef(snapshot.ref) ||
        snapshot.ref.id !== record.id ||
        !/^[a-f0-9]{64}$/.test(snapshot.qualificationKey)
      )
        invalid()
      // These are view filters, never a scope grant or a claim that excluded history is absent.
      if (
        record.scope.kind === 'sharedPublicKnowledge' ||
        record.internalUse ||
        record.kind !== 'experience' ||
        !['active', 'disputed'].includes(record.status)
      )
        continue
      if (record.scope.kind !== 'scene' || record.scope.sceneId !== input.sceneId) invalid()
      append({
        kind: 'reported_experience',
        assertionRef: snapshot.ref,
        qualificationKey: snapshot.qualificationKey,
        record,
        time: { occurredAt: record.occurredAt ?? null, recordedAt: record.learnedAt },
      })
    }
    for (const view of input.commitments) {
      if (
        !view ||
        !validRef(view.ref) ||
        view.sceneId !== input.sceneId ||
        !Number.isSafeInteger(view.mutationVersion) ||
        view.mutationVersion < 1 ||
        view.qualification !== 'declared_not_verified' ||
        view.sourceBodyCoverage !== 'unavailable'
      )
        invalid()
      append({
        kind: 'shared_plan_declaration',
        commitmentRef: { ...view.ref, mutationVersion: view.mutationVersion },
        view,
        completionBasis: 'participant_self_report_not_observed_event',
      })
    }
  }
  const reasons: ExperienceView['coverage']['reasons'] = []
  if (!input.simulation && Object.values(input.upstreamCoverage).includes('limited'))
    reasons.push('upstream_limited')
  if (entries.length > limit) reasons.push('entry_limit')
  try {
    return freeze(
      structuredClone({
        items: entries.slice(0, limit),
        coverage: {
          status: reasons.length ? 'limited' : 'complete',
          reasons,
          returned: Math.min(entries.length, limit),
          omittedEntries: Math.max(0, entries.length - limit),
        },
        interpretation: 'independent_qualified_records_not_synthesized_episodes',
        sourceBodyCoverage: 'unavailable',
      } as ExperienceView),
    )
  } catch {
    return invalid()
  }
}

/**
 * One batch re-resolves included references only. No dependency registration, cache,
 * read metric, persistence or model call occurs here. The owner still checks feature
 * flags/simulation/cancellation and registers exposure only after actual budget retention.
 */
export function verifyExperienceView(
  sceneId: string,
  page: ExperienceView,
  ports: ExperienceRefreshPort,
): boolean {
  try {
    if (!sceneId || !Array.isArray(page.items) || page.items.length > 12) return false
    if (!page.items.length) return true
    const keys = new Set(page.items.map(entryKey))
    if (keys.size !== page.items.length) return false
    const assertions = page.items.flatMap((item) =>
      item.kind === 'reported_experience' ? [item.assertionRef] : [],
    )
    const commitments = page.items.flatMap((item) =>
      item.kind === 'shared_plan_declaration' ? [item.commitmentRef] : [],
    )
    const current = ports.resolveCurrentBatch(sceneId, { assertions, commitments })
    if (
      !current ||
      current.assertions.length !== assertions.length ||
      current.commitments.length !== commitments.length
    )
      return false
    const projected = projectExperienceView({
      sceneId,
      simulation: false,
      ...current,
      upstreamCoverage: { assertions: 'complete', commitments: 'complete' },
      limit: 12,
    })
    if (projected.items.length !== page.items.length) return false
    const byKey = new Map(projected.items.map((item) => [entryKey(item), item]))
    return page.items.every((item) => isDeepStrictEqual(item, byKey.get(entryKey(item))))
  } catch {
    return false
  }
}
