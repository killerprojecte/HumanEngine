import type { Store } from '../../store.ts'
import { parentBindingState } from '../domain/proofs.ts'
import type { RelationAssertionView } from '../domain/relations.ts'
import {
  semanticHash,
  type SemanticAssertion,
  type SemanticContext,
  type SemanticRef,
} from './contracts.ts'

/** One synchronous qualification capture. No row/permission cache survives this call. */
export function semanticAssertion(
  store: Store,
  current: (ctx: SemanticContext, ref: SemanticRef) => RelationAssertionView | undefined,
  ctx: SemanticContext,
  ref: SemanticRef,
): SemanticAssertion | undefined {
  store.assertRecoveryReady()
  if (ctx.simulation) return undefined
  let remaining = 512
  const visiting = new Set<string>(),
    snapshots = new Map<string, unknown>()
  const roots = new Set<string>()
  const visit = (reference: SemanticRef, depth: number): RelationAssertionView | undefined => {
    if (--remaining < 0 || depth > 32) throw new Error('semantic_provenance_limit')
    const key = JSON.stringify(reference)
    if (visiting.has(key)) throw new Error('semantic_provenance_cycle')
    const value = current(ctx, reference)
    if (!value || value.record.internalUse || !value.proofs.length) return undefined
    visiting.add(key)
    const sourceHeads: unknown[] = []
    for (const proof of value.proofs) {
      if (parentBindingState(proof) !== 'bound') return undefined
      for (const source of proof.sources) {
        if (--remaining < 0) throw new Error('semantic_provenance_limit')
        if (source.sceneId !== ctx.sceneId) return undefined
        roots.add(semanticHash([source.sceneId, source.messageId]))
        sourceHeads.push([
          source,
          store.sourceEvents.registered(source.sceneId, source.messageId) ?? null,
        ])
      }
      for (const parent of proof.parentReferences ?? [])
        if (!visit(parent, depth + 1)) return undefined
    }
    visiting.delete(key)
    snapshots.set(key, [reference, value.record, value.proofs, sourceHeads])
    return value
  }
  const value = visit(ref, 0)
  if (!value || !roots.size || roots.size > 128) return undefined
  return {
    ...value,
    dependencyKey: semanticHash([...snapshots].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))),
    roots: [...roots].sort(),
    rootCoverage: 'complete',
    derived: false,
  }
}
