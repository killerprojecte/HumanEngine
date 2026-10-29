import { isDeepStrictEqual } from 'node:util'
import {
  SemanticSelectionSchema,
  semanticHash,
  type SemanticAssertion,
  type SemanticSelection,
  type SemanticAnchorDependency,
  type SemanticAnchorQualification,
} from './contracts.ts'

export const SEMANTIC_ALGORITHM_VERSION = 1 as const
export const SEMANTIC_PROMPT_VERSION = 'original-declarations-v1'
export interface SemanticDirty {
  sceneId: string
  personId: string | null
  episodeAnchor?: { id: string; version: number }
  trigger: SemanticSelection
  reason: 'assertion_committed' | 'source_qualification_changed'
}
export interface SemanticResolvedInput {
  coverage: 'complete' | 'limited'
  /** First authorized subject membership/source-head snapshot, not a commit-time replacement. */
  partitionKey?: string
  /** Trusted exact anchor independently evidenced by every original declaration. */
  episodeAnchor?: { id: string; version: number }
  episodeMembershipKey?: string
  anchorDependencies?: SemanticAnchorDependency[]
  anchorQualifications?: SemanticAnchorQualification[]
  entries: readonly { selection: SemanticSelection; assertion: SemanticAssertion }[]
}
export interface SemanticInput {
  sceneId: string
  personId: string | null
  episodeOnly: boolean
  episodeMembershipKey?: string
  anchorDependencies?: SemanticAnchorDependency[]
  anchorQualifications?: SemanticAnchorQualification[]
  partitionKey: string | null
  episodeAnchor?: { id: string; version: number }
  selections: SemanticSelection[]
  assertions: SemanticAssertion[]
  roots: string[]
  inputKey: string
}

/** Capture whole authorized declarations, never truncate a conflict/condition to fit the bound. */
export function captureSemanticInput(
  sceneId: string,
  resolved: SemanticResolvedInput,
): SemanticInput | undefined {
  if (
    resolved.coverage !== 'complete' ||
    resolved.entries.length < 2 ||
    resolved.entries.length > 12
  )
    return
  const entries = [...resolved.entries].sort((a, b) =>
    a.selection.ref.id < b.selection.ref.id ? -1 : a.selection.ref.id > b.selection.ref.id ? 1 : 0,
  )
  if (new Set(entries.map((entry) => entry.selection.ref.id)).size !== entries.length) return
  for (const { selection, assertion } of entries) {
    if (
      !SemanticSelectionSchema.safeParse(selection).success ||
      assertion.derived ||
      assertion.record.internalUse ||
      assertion.record.id !== selection.ref.id ||
      assertion.record.scope.kind !== 'scene' ||
      assertion.record.scope.sceneId !== sceneId ||
      assertion.dependencyKey !== selection.dependencyKey ||
      assertion.rootCoverage !== 'complete' ||
      !assertion.proofs.length ||
      !assertion.roots.length ||
      assertion.roots.some((root) => !/^[a-f0-9]{64}$/.test(root))
    )
      return
  }
  const people = new Set(
    entries.map(({ assertion }) =>
      assertion.record.scope.kind === 'scene' ? (assertion.record.scope.personId ?? null) : null,
    ),
  )
  const anchor = resolved.episodeAnchor
  if (anchor && (!anchor.id || !Number.isSafeInteger(anchor.version) || anchor.version < 1)) return
  if (people.size !== 1 && !anchor) return
  if (
    anchor &&
    (!resolved.episodeMembershipKey || !/^[a-f0-9]{64}$/.test(resolved.episodeMembershipKey))
  )
    return
  if (anchor && (!resolved.anchorDependencies?.length || !resolved.anchorQualifications?.length))
    return
  const partitionKey = anchor ? null : resolved.partitionKey
  if (!anchor && (!partitionKey || !/^[a-f0-9]{64}$/.test(partitionKey))) return
  const roots = [...new Set(entries.flatMap(({ assertion }) => assertion.roots))].sort()
  if (roots.length < 2) return
  const selections = structuredClone(entries.map((entry) => entry.selection))
  const assertions = structuredClone(entries.map((entry) => entry.assertion))
  return {
    sceneId,
    personId: people.size === 1 ? [...people][0]! : null,
    episodeOnly: !!anchor,
    partitionKey: partitionKey ?? null,
    ...(anchor
      ? {
          episodeAnchor: structuredClone(anchor),
          episodeMembershipKey: resolved.episodeMembershipKey,
          anchorDependencies: structuredClone(resolved.anchorDependencies),
          anchorQualifications: structuredClone(resolved.anchorQualifications),
        }
      : {}),
    selections,
    assertions,
    roots,
    inputKey: semanticHash([
      SEMANTIC_ALGORITHM_VERSION,
      SEMANTIC_PROMPT_VERSION,
      sceneId,
      selections,
      assertions,
      anchor ?? null,
      resolved.episodeMembershipKey ?? null,
      resolved.anchorDependencies ?? null,
      resolved.anchorQualifications ?? null,
      partitionKey ?? null,
    ]),
  }
}

/** Re-resolution may revoke a snapshot but can never upgrade its original versions or bodies. */
export function semanticInputUnchanged(
  snapshot: SemanticInput,
  resolved: SemanticResolvedInput,
): boolean {
  const current = captureSemanticInput(snapshot.sceneId, resolved)
  return !!current && current.inputKey === snapshot.inputKey && isDeepStrictEqual(current, snapshot)
}
