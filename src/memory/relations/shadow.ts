import type { MemoryRecord } from '../domain/contracts.ts'
import type { RelationRecord } from '../domain/relations.ts'

/** Cardinalities only. This comparison neither answers the user nor measures quality. */
export function compareRelationRetrieval(
  baseline: readonly MemoryRecord[],
  graph: { items: readonly RelationRecord[]; coverage: 'complete' | 'limited' },
) {
  const baselineIds = new Set(baseline.map((record) => record.id))
  const graphIds = new Set(graph.items.flatMap((edge) => [edge.backing.id, edge.target.backing.id]))
  return {
    baselineCount: baselineIds.size,
    graphCount: graphIds.size,
    overlapCount: [...graphIds].filter((id) => baselineIds.has(id)).length,
    graphCoverage: graph.coverage,
    interpretation: 'bounded_set_comparison_not_quality_evidence' as const,
  }
}
