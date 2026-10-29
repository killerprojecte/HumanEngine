import { createHash } from 'node:crypto'
import type { RuntimeMessage } from '../runtime.ts'
import type { Store } from '../store.ts'
import type { SourceEventIdentity, SourceEventRef } from '../memory/domain/events.ts'

/** Hash only normalized evidence; never persist media locators or original chat here. */
export function sourceBodyHash(message: RuntimeMessage): string {
  return createHash('sha256')
    .update(JSON.stringify([message.sentAt, message.replyToId ?? null, message.parts]))
    .digest('hex')
}

export function sourceIdentity(
  scene: { id: string; adapterId: string; accountId: string },
  message: Pick<RuntimeMessage, 'id' | 'sceneId' | 'senderId'>,
): SourceEventIdentity {
  if (scene.id !== message.sceneId) throw new Error('source_event_scope_mismatch')
  return {
    sceneId: scene.id,
    adapterId: scene.adapterId,
    accountId: scene.accountId,
    messageId: message.id,
    senderId: message.senderId,
  }
}

/** Qualified library/cache reference, not an adapter locator or a source identity. */
export function sourceMediaReference(reference: string, ref?: SourceEventRef): string {
  return ref ? JSON.stringify([reference, ref.eventId, ref.revision, ref.deletionEpoch]) : reference
}

/** Exact original snapshot validation; a repeated body never upgrades its captured revision. */
export function currentSourceSnapshot(store: Store, message: RuntimeMessage): boolean {
  if (message.origin === 'simulation') return true
  if (store.recalled(message.sceneId, message.id)) return false
  const scene = store.scene(message.sceneId)
  if (!scene) return !message.sourceEventRef
  const identity = sourceIdentity(scene, message)
  // An unversioned captured body cannot acquire authority from later enrollment.
  if (!message.sourceEventRef)
    return (
      !store.sourceEvents.registered(message.sceneId, message.id) &&
      !store.sourceEvents.hasEdits(identity)
    )
  return !!store.sourceEvents.capture(identity, sourceBodyHash(message), message.sourceEventRef)
}
