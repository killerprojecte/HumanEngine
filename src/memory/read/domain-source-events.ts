import type { Store } from '../../store.ts'
import type { MemorySource } from '../domain/contracts.ts'

/** Current-use qualification only. Never erases a source or alters historical support. */
export function domainSourceCurrent(
  store: Pick<Store, 'recalled' | 'sourceEvents' | 'scene'>,
  source: MemorySource,
): boolean {
  if (store.recalled(source.sceneId, source.messageId)) return false
  const registered = store.sourceEvents.registered(source.sceneId, source.messageId)
  if (!source.eventRef && !registered) return true
  const scene = store.scene(source.sceneId)
  if (!scene) return false
  const identity = {
    sceneId: source.sceneId,
    adapterId: scene.adapterId,
    accountId: scene.accountId,
    messageId: source.messageId,
    senderId: source.senderId,
  }
  if (
    registered &&
    (registered.identity.adapterId !== identity.adapterId ||
      registered.identity.accountId !== identity.accountId ||
      registered.identity.senderId !== identity.senderId)
  )
    return false
  if (source.eventRef) return store.sourceEvents.current(source.eventRef, identity).eligible
  return !store.sourceEvents.hasEdits(identity)
}
