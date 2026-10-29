/** Retain every semantic field and AND/OR group; internal hashes and canonical proof IDs are not evidence prose. */
export function knowledgeEvidence(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(knowledgeEvidence)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(
    Object.entries(value)
      .filter(([key]) => !['proofId', 'backingFingerprint', 'spanHash'].includes(key))
      .map(([key, item]) => [key, knowledgeEvidence(item)]),
  )
}
