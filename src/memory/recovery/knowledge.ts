import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import { KnowledgeStorage } from '../knowledge/storage.ts'
import { validateKnowledgePredicate } from '../knowledge/registry.ts'
import type { KnowledgeEdge } from '../knowledge/contracts.ts'
import { requireKnowledgeReader, type Revocation } from './ledger.ts'

/** Compatibility barrier only: derived admission is not a new independent grant. */
export function recordKnowledgeWrite(db: Database.Database): void {
  requireKnowledgeReader(db)
}

/** No speculative rebase or reconstructed model extraction during offline recovery. */
export function createRecoveryKnowledgePort(store: Store) {
  const storage = new KnowledgeStorage(store.db, () => store.assertRecoveryReady())
  function assertSupported(): KnowledgeEdge[] {
    const tables = store.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name LIKE 'memory_knowledge_%'",
      )
      .all() as { name: string; type: string }[]
    if (!tables.length) return []
    if (
      tables.length !== 1 ||
      tables[0]!.type !== 'table' ||
      tables[0]!.name !== 'memory_knowledge_edges'
    )
      throw new Error('recovery_unknown_knowledge_schema')
    const columns = (
      store.db.prepare('PRAGMA table_xinfo(memory_knowledge_edges)').all() as { name: string }[]
    )
      .map((c) => c.name)
      .sort()
    if (
      JSON.stringify(columns) !==
      JSON.stringify(['admission_key', 'assertion_id', 'id', 'scene_id', 'value'])
    )
      throw new Error('recovery_unknown_knowledge_schema')
    try {
      const rows = storage.rows()
      for (const edge of rows) {
        if ((edge.predicate === 'has_property') !== (edge.property !== undefined))
          throw new Error('knowledge_property_invalid')
        validateKnowledgePredicate(edge.predicate, edge.subject, edge.object, edge.property)
      }
      return rows
    } catch {
      throw new Error('recovery_knowledge_material_invalid')
    }
  }
  function verify(events: readonly Revocation[]) {
    const rows = assertSupported(),
      byId = new Map(rows.map((e) => [e.id, e]))
    const deleted = new Set(
      events
        .filter(
          (e): e is Extract<Revocation, { kind: 'assertion' }> =>
            e.kind === 'assertion' && e.terminal,
        )
        .map((e) => e.id),
    )
    for (const edge of rows) {
      const proofs = [
        ...edge.proofs,
        ...edge.dependencies.flatMap((d) => d.proofs),
        ...edge.readDependencies.flatMap((d) => d.proofs),
      ]
      const refs = [
        edge.backing,
        ...edge.dependencies.map((d) => d.entity.backing),
        ...edge.readDependencies.map((d) => d.backing),
        ...proofs.flatMap((p) => p.parentReferences ?? []),
      ]
      if (
        refs.some((r) => deleted.has(r.id)) ||
        proofs.some((p) => p.parents.some((id) => deleted.has(id)))
      )
        throw new Error('recovery_knowledge_purge_incomplete')
      if (
        proofs.some((p) => p.sources.some((s) => store.recalled(s.sceneId, s.messageId))) ||
        edge.spans.some((s) => store.recalled(s.source.sceneId, s.source.messageId))
      )
        throw new Error('recovery_knowledge_purge_incomplete')
      // Existence/scope only: a historical version drift is not privacy deletion or rebase.
      for (const dependency of edge.readDependencies) {
        if (!dependency.edgeId) continue
        const origin = byId.get(dependency.edgeId)
        if (
          !origin ||
          origin.sceneId !== edge.sceneId ||
          origin.backing.id !== dependency.backing.id
        )
          throw new Error('recovery_knowledge_dependency_missing')
      }
      for (const dependency of edge.dependencies) {
        const origin = byId.get(dependency.entity.originEdgeId)
        if (!origin || origin.sceneId !== edge.sceneId)
          throw new Error('recovery_knowledge_dependency_missing')
      }
    }
  }
  return { assertSupported, verify }
}
