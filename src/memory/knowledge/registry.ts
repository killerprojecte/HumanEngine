import type { KnowledgeEdge, KnowledgeEndpoint, KnowledgeProposal } from './contracts.ts'
export const KNOWLEDGE_REGISTRY_VERSION = 1 as const
/** An immutable named version. A future same-name schema must use another version. */
export const KNOWLEDGE_PREDICATES = Object.freeze({
  participates_in: { subjects: ['person'], objects: ['plan', 'event'] },
  prefers_topic: { subjects: ['person'], objects: ['topic', 'literal:text'] },
  uses_tool: { subjects: ['person'], objects: ['tool', 'literal:text'] },
  member_of: { subjects: ['person'], objects: ['organization'] },
  part_of: { subjects: ['concept', 'task'], objects: ['concept', 'task'] },
  depends_on: { subjects: ['concept', 'task'], objects: ['concept', 'task'] },
  has_property: {
    subjects: ['tool', 'concept', 'task'],
    objects: ['literal:text', 'literal:number', 'literal:boolean'],
  },
} as const)
export function endpointKind(endpoint: KnowledgeEndpoint): string {
  return endpoint.kind === 'entity'
    ? endpoint.entity.kind
    : endpoint.kind === 'literal'
      ? 'literal:' + endpoint.valueType
      : 'person'
}
export function validateKnowledgePredicate(
  predicate: KnowledgeProposal['predicate'],
  subject: KnowledgeEndpoint,
  object: KnowledgeEndpoint,
  property?: KnowledgeProposal['property'],
): void {
  const rule = KNOWLEDGE_PREDICATES[predicate]
  if (
    !rule ||
    !(rule.subjects as readonly string[]).includes(endpointKind(subject)) ||
    !(rule.objects as readonly string[]).includes(endpointKind(object))
  )
    throw new Error('knowledge_endpoint_type')
  if (predicate === 'has_property') {
    const valid =
      object.kind === 'literal' &&
      ((property === 'version' && object.valueType === 'text') ||
        (property === 'enabled' && object.valueType === 'boolean') ||
        (property === 'count' &&
          object.valueType === 'number' &&
          Number.isSafeInteger(object.value) &&
          object.value >= 0 &&
          object.unit === null) ||
        (property === 'duration' &&
          object.valueType === 'number' &&
          object.value >= 0 &&
          ['seconds', 'minutes', 'hours'].includes(object.unit ?? '')))
    if (!valid) throw new Error('knowledge_property_type')
  }
}
export function knowledgeNodeKey(endpoint: KnowledgeEndpoint): string {
  return endpoint.kind === 'person'
    ? 'person:' + endpoint.personId
    : endpoint.kind === 'entity'
      ? 'entity:' + endpoint.entity.id
      : 'literal:' + JSON.stringify(endpoint)
}
/** Include all alternative values/negative claims for a subject+predicate, not just positive matches. */
export function conflictKey(
  edge: Pick<KnowledgeEdge, 'subject' | 'predicate' | 'property'>,
): string {
  return JSON.stringify([knowledgeNodeKey(edge.subject), edge.predicate, edge.property ?? null])
}

export function knowledgeAutomaticPolicy(
  predicate: KnowledgeProposal['predicate'],
): 'self_explicit' | 'direct_explicit_or_observed' {
  return ['participates_in', 'prefers_topic', 'uses_tool', 'member_of'].includes(predicate)
    ? 'self_explicit'
    : 'direct_explicit_or_observed'
}
