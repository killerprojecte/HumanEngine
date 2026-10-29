import type { Store } from '../../store.ts'
import { domainSourceCurrent } from '../read/domain-source-events.ts'
import { recordKnowledgeWrite } from '../recovery/knowledge.ts'
import type { KnowledgeAuthority } from './contracts.ts'
import { KnowledgeService } from './service.ts'

/** Trusted transient source parts, never a submit-time preview/latest-source fallback. */
export function createKnowledgeService(store: Store, current: KnowledgeAuthority['current']) {
  return new KnowledgeService(
    store.db,
    {
      assertReady: () => store.assertRecoveryReady(),
      current,
      enabled: () => store.settings.memoryKnowledgeEnabled,
      autoAdmission: () => store.settings.memoryKnowledgeAutoAdmissionEnabled,
      learningEnabled: (ctx) => !!store.scene(ctx.sceneId)?.learningEnabled,
      source: (ctx, messageId) => {
        const evidence = ctx.sourceEvidence?.find(
          (item) => item.sceneId === ctx.sceneId && item.messageId === messageId,
        )
        if (!evidence?.available || evidence.isBot || !evidence.parts) return undefined
        const source = {
          sceneId: evidence.sceneId,
          messageId,
          senderId: evidence.senderId,
          ...(evidence.eventRef ? { eventRef: evidence.eventRef } : {}),
        }
        if (
          (!source.eventRef && store.sourceEvents.registered(ctx.sceneId, messageId)) ||
          !domainSourceCurrent(store, source)
        )
          return undefined
        return { source, parts: evidence.parts }
      },
    },
    { beforeWrite: () => recordKnowledgeWrite(store.db) },
  )
}
