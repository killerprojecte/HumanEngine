const object = (value: unknown): Record<string, unknown> | undefined =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
const edges = (value: unknown): Record<string, unknown>[] => {
  const values = object(value)?.relationEvidence
  return Array.isArray(values)
    ? values.flatMap((value) => (object(value) ? [object(value)!] : []))
    : []
}
/** A selected entity group must retain every returned qualification and counterclaim.
 * This checks structural closure, not the truth or entailment of model-written facts.
 */
export function validateRelationSelection(
  records: readonly unknown[],
  ids: readonly string[],
): void {
  const selected = new Set(ids)
  const targets = new Set(
    records
      .filter((item) => selected.has(String(object(item)?.id)))
      .flatMap((item) => edges(item).map((edge) => object(edge.target)?.entityId)),
  )
  for (const item of records) {
    const record = object(item)
    if (!record) throw new Error('planner_incomplete_relation_evidence')
    const linked = edges(item)
    const relevant =
      selected.has(String(record.id)) ||
      linked.some((edge) => targets.has(object(edge.target)?.entityId))
    if (!relevant) continue
    if (
      !selected.has(String(record.id)) ||
      !linked.length ||
      linked.some(
        (edge) =>
          typeof object(edge.target)?.entityId !== 'string' ||
          !Array.isArray(edge.requiredMemoryIds) ||
          edge.requiredMemoryIds.some((id) => typeof id !== 'string' || !selected.has(id)),
      )
    )
      throw new Error('planner_incomplete_relation_evidence')
  }
}
