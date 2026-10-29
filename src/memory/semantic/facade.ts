import type { Store } from '../../store.ts'
import { recordSemanticMutation } from '../recovery/semantic.ts'
import { SemanticService } from './candidates.ts'
import type { SemanticAuthority } from './contracts.ts'

/** Compose trusted synchronous ports; no secondary transaction or authorization owner. */
export function createSemanticService(
  store: Store,
  current: SemanticAuthority['current'],
  anchorCurrent: SemanticAuthority['anchorCurrent'],
  episodeAnchor: SemanticAuthority['episodeAnchor'],
  partitionKey: SemanticAuthority['partitionKey'],
) {
  return new SemanticService(
    store.db,
    {
      assertReady: () => store.assertRecoveryReady(),
      current,
      anchorCurrent,
      episodeAnchor,
      partitionKey,
      enabled: () => store.settings.memorySemanticEnabled,
      autoSummary: () => store.settings.memorySemanticAutoAdmissionEnabled,
      learningEnabled: (ctx) => !!store.scene(ctx.sceneId)?.learningEnabled,
    },
    { beforeWrite: (mutation) => recordSemanticMutation(store.db, mutation) },
  )
}

export function semanticPolicyRevision(store: Store): number {
  const revision = store.get('memory-semantic-policy-revision', 0)
  if (!Number.isSafeInteger(revision) || revision < 0) throw new Error('semantic_policy_invalid')
  return revision
}
