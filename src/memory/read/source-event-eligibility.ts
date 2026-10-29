import type { MemorySource } from '../domain/contracts.ts'
import {
  SourceEventRefSchema,
  type SourceEventIdentity,
  type SourceEventRef,
  type SourceEventEligibility,
} from '../domain/events.ts'

export interface SourceEventEligibilityPort {
  /** Resolve account/adapter from trusted scene metadata, never candidate fields. */
  identity(source: MemorySource): SourceEventIdentity | undefined
  recalled(source: MemorySource): boolean
  current(ref: SourceEventRef, identity: SourceEventIdentity): SourceEventEligibility
  historical(ref: SourceEventRef, identity: SourceEventIdentity): SourceEventEligibility
  legacyEdited(identity: SourceEventIdentity): boolean
}
export interface MemorySourceEligibility {
  eligible: boolean
  binding: 'bound' | 'legacy_unknown' | 'legacy_edited_unknown' | 'invalid'
  reason: string
}

/** Current revision drift is a read exclusion, not a command to erase old authorized evidence.
 * Historical eligibility is metadata/proof eligibility only, never permission to invent source text.
 */
export function sourceEventEligibility(
  source: MemorySource,
  mode: 'current' | 'historical',
  port: SourceEventEligibilityPort,
): MemorySourceEligibility {
  if (port.recalled(source)) return { eligible: false, binding: 'invalid', reason: 'recalled' }
  const identity = port.identity(source)
  if (
    !identity ||
    identity.sceneId !== source.sceneId ||
    identity.messageId !== source.messageId ||
    identity.senderId !== source.senderId
  )
    return { eligible: false, binding: 'invalid', reason: 'scope_mismatch' }
  if (source.eventRef === undefined) {
    if (port.legacyEdited(identity))
      return {
        eligible: mode === 'historical',
        binding: 'legacy_edited_unknown',
        reason: 'legacy_edited_unknown',
      }
    return { eligible: true, binding: 'legacy_unknown', reason: 'legacy_unknown' }
  }
  const parsed = SourceEventRefSchema.safeParse(source.eventRef)
  if (!parsed.success) return { eligible: false, binding: 'invalid', reason: 'invalid_ref' }
  const result =
    mode === 'current'
      ? port.current(parsed.data, identity)
      : port.historical(parsed.data, identity)
  return { eligible: result.eligible, binding: 'bound', reason: result.reason }
}
