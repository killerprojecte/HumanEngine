import type { PlanEvidence, ReplyPlan } from './reply-plan.ts'

type RecordValue = Record<string, unknown>
const object = (value: unknown): RecordValue | undefined =>
  value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : undefined
const pick = (value: unknown, keys: readonly string[]): RecordValue => {
  const record = object(value)
  return record
    ? Object.fromEntries(
        keys.filter((key) => record[key] !== undefined).map((key) => [key, record[key]]),
      )
    : {}
}
const provenance = (value: unknown) =>
  Array.isArray(value) ? value.map((source) => pick(source, ['messageId', 'senderId'])) : []
const memory = (value: unknown) => {
  const record = object(value)
  if (!record) return undefined
  return {
    ...pick(record, [
      'id',
      'kind',
      'status',
      'lineageStatus',
      'occurredAt',
      'learnedAt',
      'validUntil',
    ]),
    ...(typeof record.content === 'string'
      ? {
          content: record.content.slice(0, 1200),
          ...(record.content.length > 1200 ? { contentTruncated: true } : {}),
        }
      : {}),
    ...(record.sources ? { sources: provenance(record.sources) } : {}),
  }
}
const metadata = ['trust', 'coverage', 'reason', 'status', 'note', 'truncated'] as const
const webSource = (value: unknown) =>
  pick(value, ['title', 'url', 'sourceUrl', 'snippet', 'publishedAt'])
const vision = (value: RecordValue, allowed: Set<string>) => ({
  ...pick(value, ['trust', 'framePolicy']),
  images: Array.isArray(value.images)
    ? value.images
        .filter((image) => allowed.has(String(object(image)?.messageId)))
        .map((image) =>
          pick(image, ['messageId', 'status', 'description', 'reason', 'framePolicy']),
        )
    : [],
  omitted: Array.isArray(value.omitted)
    ? value.omitted
        .filter((image) => allowed.has(String(object(image)?.messageId)))
        .map((image) => pick(image, ['messageId', 'reason']))
    : [],
})
const imageBriefs = (contexts: unknown[], allowed: Set<string>): ReturnType<typeof vision>[] =>
  contexts.flatMap((value) => {
    if (Array.isArray(value)) return imageBriefs(value, allowed)
    const record = object(value)
    if (!record) return []
    if (Array.isArray(record.images) || Array.isArray(record.omitted)) {
      const projected = vision(record, allowed)
      return projected.images.length || projected.omitted.length ? [projected] : []
    }
    return Array.isArray(record.imageContext) ? imageBriefs(record.imageContext, allowed) : []
  })

/** A language-module handoff, not a replacement for the runtime's canonical receipts.
 * Call ContextProjection.stringify on the returned brief together with selected original
 * messages, the current clock and timezone. Never pass this back into execution guards.
 */
export function buildReplyBrief(
  plan: ReplyPlan & {
    expressionGoals?: string[]
    memorySelections?: Array<{ evidenceId: string; memoryIds: string[] }>
    replySuggestion?: string
  },
  evidence: PlanEvidence[],
  actionReceipts: PlanEvidence[],
  imageContext: unknown[] = [],
  relatedMessageIds: string[] = [],
) {
  const imageMessageIds = new Set([
    ...plan.contextMessageIds,
    ...plan.targetMessageIds,
    ...relatedMessageIds,
  ])
  const memorySelections = new Map(
    (plan.memorySelections ?? []).map((selection) => [
      selection.evidenceId,
      new Set(selection.memoryIds),
    ]),
  )
  const selected = new Set([
    ...plan.facts.flatMap((fact) => fact.evidenceIds),
    ...plan.assets.map((asset) => asset.evidenceId),
    ...memorySelections.keys(),
  ])
  const sources = evidence
    .filter(
      (receipt) =>
        selected.has(receipt.id) ||
        ['images', 'omitted'].some(
          (key) =>
            Array.isArray(object(receipt.value)?.[key]) &&
            (object(receipt.value)![key] as unknown[]).some((image) =>
              imageMessageIds.has(String(object(image)?.messageId)),
            ),
        ),
    )
    .map((receipt) => {
      const value = object(receipt.value)
      const base = { id: receipt.id, ...(receipt.toolName ? { toolName: receipt.toolName } : {}) }
      if (receipt.toolName?.startsWith('memory.')) {
        const selectedMemoryIds = memorySelections.get(receipt.id)
        if (!selectedMemoryIds)
          return { ...base, ...pick(value, metadata), contentOmitted: 'memory_not_selected' }
        const records = Array.isArray(receipt.value)
          ? receipt.value
          : Array.isArray(value?.items)
            ? value.items
            : value?.content !== undefined
              ? [value]
              : []
        return {
          ...base,
          ...pick(value, metadata),
          caution:
            'Recalled information, not independently verified facts; preserve uncertainty and conflicts. Truncated excerpts may omit qualifications.',
          memories: records
            .filter((record) => selectedMemoryIds.has(String(object(record)?.id)))
            .map(memory)
            .filter(Boolean),

          ...(value?.sources ? { sources: provenance(value.sources) } : {}),
        }
      }
      if (value && (Array.isArray(value.images) || Array.isArray(value.omitted)))
        return { ...base, ...vision(value, imageMessageIds) }
      if (receipt.toolName === 'memes.search') {
        const assets = Array.isArray(value?.items) ? value.items : []
        const ids = new Set(
          plan.assets
            .filter((asset) => asset.evidenceId === receipt.id)
            .map((asset) => asset.assetId),
        )
        return {
          ...base,
          ...pick(value, metadata),
          assets: assets
            .filter((asset) => ids.has(String(object(asset)?.id)))
            .map((asset) => ({
              assetId: object(asset)?.id,
              ...pick(asset, ['available', 'disabled']),
              meaning: pick(object(asset)?.tags, ['emotion', 'intent', 'memeMeaning']),
            })),
        }
      }
      if (value && typeof value.senderId === 'string' && Array.isArray(value.parts)) {
        return plan.contextMessageIds.includes(String(value.id))
          ? { ...base, messageId: value.id }
          : {
              ...base,
              message: pick(value, [
                'id',
                'senderId',
                'senderDisplayName',
                'sentAt',
                'parts',
                'replyToId',
                'status',
              ]),
            }
      }
      if (receipt.toolName?.startsWith('messages.')) {
        const items = Array.isArray(value?.items) ? value.items : []
        return {
          ...base,
          ...pick(value, metadata),
          messageIds: items.map((item) => object(item)?.id).filter((id) => typeof id === 'string'),
        }
      }
      // Web sources give citations, not raw page bodies, headers, fetch details or tool arguments.
      const results = Array.isArray(value?.results)
        ? value.results
        : Array.isArray(value?.items)
          ? value.items
          : []
      return {
        ...base,
        ...pick(value, metadata),
        ...webSource(value),
        ...(results.length ? { sources: results.map(webSource) } : {}),
      }
    })
  return {
    plan: {
      action: plan.action,
      deliveryTool: plan.deliveryTool,
      targetMessageIds: plan.targetMessageIds,
      contextMessageIds: plan.contextMessageIds,
      strategy: plan.strategy,
      constraints: plan.constraints,
      facts: plan.facts,
      assets: plan.assets,
      emoji: plan.emoji,
      expressionGoals: plan.expressionGoals ?? [],
      ...(plan.replySuggestion !== undefined ? { replySuggestion: plan.replySuggestion } : {}),
    },
    evidence: sources,
    imageContext: imageBriefs(imageContext, imageMessageIds),
    actionReceipts: actionReceipts.map((receipt) => {
      const value = object(receipt.value)
      const status =
        value?.ok === false || value?.error !== undefined
          ? 'failed'
          : typeof value?.status === 'string'
            ? value.status
            : value?.ok === true
              ? 'confirmed'
              : 'unconfirmed'
      return {
        id: receipt.id,
        toolName: receipt.toolName,
        ...pick(value, [
          'ok',
          'actionTaken',
          'scope',
          'assetId',
          'sourceMessageId',
          'positive',
          'disabled',
          'reason',
        ]),
        status,
        ...(object(value?.memory)?.id ? { memoryId: object(value?.memory)!.id } : {}),
        ...(object(value?.habit)?.id ? { habitId: object(value?.habit)!.id } : {}),
      }
    }),
  }
}
