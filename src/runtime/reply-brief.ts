import { SemanticStateEvidenceSchema } from '../memory/read/semantic-exposure.ts'
import { knowledgeEvidence } from './knowledge-evidence.ts'
import { stateEvidenceTools, validWorkspaceState } from './workspace-state-evidence.ts'
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
const stateReference = (value: unknown) => pick(value, ['id', 'version', 'deletionEpoch'])
const declaration = (value: unknown) => {
  const item = object(value)
  return {
    ...pick(item, ['kind', 'personId']),
    backing: stateReference(item?.backing),
    ...(item?.qualificationRef ? { qualificationRef: stateReference(item.qualificationRef) } : {}),
    ...(Array.isArray(item?.proofs)
      ? {
          observations: item.proofs.flatMap((proof) =>
            Array.isArray(object(proof)?.sourceObservations)
              ? (object(proof)!.sourceObservations as unknown[]).map((observation) => ({
                  ...pick(observation, ['sceneId', 'messageId', 'senderId', 'sentAt']),
                  ...(typeof object(observation)?.receivedAt === 'string'
                    ? { observedAt: object(observation)!.receivedAt }
                    : {}),
                }))
              : [],
          ),
          sources: item.proofs.flatMap((proof) =>
            Array.isArray(object(proof)?.sources)
              ? (object(proof)!.sources as unknown[]).map((source) => ({
                  ...pick(source, ['sceneId', 'messageId', 'senderId']),
                  ...(object(source)?.eventRef
                    ? {
                        eventRef: pick(object(source)!.eventRef, [
                          'eventId',
                          'revision',
                          'deletionEpoch',
                        ]),
                      }
                    : {}),
                }))
              : [],
          ),
        }
      : {}),
  }
}
function state(value: unknown): RecordValue | undefined {
  const item = object(value)
  if (item?.stateKind === 'semantic_view') {
    const parsed = SemanticStateEvidenceSchema.safeParse(item)
    return parsed.success ? structuredClone(parsed.data) : undefined
  }
  if (item?.stateKind === 'knowledge_graph') {
    if (!validWorkspaceState('memory.knowledge', item)) return undefined
    return knowledgeEvidence(item) as RecordValue
  }
  if (item?.stateKind === 'identity_view') {
    if (!validWorkspaceState('memory.identities', item)) return undefined
    const page = object(item.page)!
    return {
      stateKind: 'identity_view',
      page: {
        ...pick(page, ['sceneId', 'revision', 'personId', 'coverage', 'interpretation']),
        items: (page.items as unknown[]).map((value) => {
          const member = object(value)!
          return {
            personId: member.personId,
            aliases: (member.aliases as unknown[]).map((alias) =>
              pick(alias, ['label', 'validFrom', 'validUntil', 'sourceKind', 'sourceMessageId']),
            ),
          }
        }),
      },
    }
  }
  if (item?.stateKind === 'working_set' || item?.stateKind === 'experience_view') {
    const tool = item.stateKind === 'working_set' ? 'memory.workingSet' : 'memory.episodes'
    if (!validWorkspaceState(tool, item)) return undefined
    const page = object(item.page)!
    const entries = (page.items as unknown[]).map((entry) => {
      const e = object(entry)!
      if (e.kind === 'assertion') {
        const snapshot = object(e.snapshot)!
        return {
          kind: e.kind,
          snapshot: { ref: stateReference(snapshot.ref), record: memory(snapshot.record) },
          accessibility: pick(e.accessibility, [
            'state',
            'reviewDue',
            'pinned',
            'pinIntent',
            'pinStatus',
            'pinRevision',
            'policyCoverage',
            'metadataRevision',
            'contextSelectedCount',
            'lastSelectedAt',
            'protection',
          ]),
        }
      }
      if (e.kind === 'commitment')
        return {
          kind: e.kind,
          view: state({ stateKind: 'commitment', view: e.view })?.view,
          accessibility: pick(e.accessibility, [
            'state',
            'reviewDue',
            'pinned',
            'pinIntent',
            'pinStatus',
            'pinRevision',
            'policyCoverage',
            'metadataRevision',
            'contextSelectedCount',
            'lastSelectedAt',
            'protection',
          ]),
        }
      if (e.kind === 'reported_experience')
        return {
          kind: e.kind,
          assertionRef: stateReference(e.assertionRef),
          record: memory(e.record),
          time: pick(e.time, ['occurredAt', 'recordedAt']),
        }
      return {
        kind: e.kind,
        commitmentRef: {
          ...stateReference(e.commitmentRef),
          ...pick(e.commitmentRef, ['mutationVersion']),
        },
        view: state({ stateKind: 'commitment', view: e.view })?.view,
        completionBasis: e.completionBasis,
      }
    })
    return {
      stateKind: item.stateKind,
      page: {
        ...pick(page, [
          'enabled',
          'coverage',
          'omittedCold',
          'omittedEntries',
          'interpretation',
          'sourceBodyCoverage',
        ]),
        items: entries,
      },
    }
  }
  if (item?.stateKind === 'commitment') {
    const view = object(item.view)
    if (!view || !view.ref) return undefined
    return {
      stateKind: 'commitment',
      view: {
        ...pick(view, [
          'mutationVersion',
          'sceneId',
          'recordedAt',
          'confirmation',
          'lifecycle',
          'qualification',
          'sourceBodyCoverage',
          'changed',
          'confirmedParticipantIds',
          'missingParticipantIds',
          'completedParticipantIds',
          'cancelledParticipantIds',
        ]),
        ref: stateReference(view.ref),
        ...(view.terms
          ? {
              terms: pick(view.terms, [
                'kind',
                'title',
                'participantIds',
                'polarity',
                'modality',
                'temporalScope',
                'conditions',
                'timeQualification',
                'occurredAt',
                'validFrom',
                'validUntil',
              ]),
            }
          : {}),
        declarations: Array.isArray(view.evidence) ? view.evidence.map(declaration) : [],
        inheritedProposalGroups: Array.isArray(view.requiredProposalGroups)
          ? view.requiredProposalGroups.map((group) =>
              Array.isArray(group) ? group.map(declaration) : [],
            )
          : [],
        inheritedProposalSemantics:
          'All groups required; alternatives within each group are OR, not independent confirmations.',
      },
      ...pick(item, ['coverage']),
    }
  }
  if (item?.stateKind === 'commitment_timeline' && Array.isArray(item.recordedStates))
    return {
      stateKind: 'commitment_timeline',
      ...pick(item, [
        'coverage',
        'reasons',
        'interpretation',
        'sourceBodyCoverage',
        'stateTime',
        'asOfRecorded',
      ]),
      anchor: { ...stateReference(item.anchor), ...pick(item.anchor, ['mutationVersion']) },
      window: pick(item.window, [
        'maxTransitions',
        'firstRecordedAt',
        'firstMutationVersion',
        'prunedThroughMutationVersion',
        'throughMutationVersion',
      ]),
      recordedStates: item.recordedStates.map((row) => ({
        ...pick(row, [
          'recordedAt',
          'mutationVersion',
          'reason',
          'activeVersion',
          'latestVersion',
          'status',
        ]),
        states: Array.isArray(row.states)
          ? row.states.map((view: unknown) => state({ stateKind: 'commitment', view })?.view)
          : [],
      })),
    }
  if (item?.stateKind === 'commitment_history' && Array.isArray(item.revisions))
    return {
      stateKind: 'commitment_history',
      ...pick(item, ['coverage', 'interpretation', 'sourceBodyCoverage']),
      anchor: { ...stateReference(item.anchor), ...pick(item.anchor, ['mutationVersion']) },
      historyWindow: pick(item.historyWindow, [
        'maxRevisions',
        'omittedRevisions',
        'throughVersion',
      ]),
      revisions: item.revisions.map((view) => state({ stateKind: 'commitment', view })?.view),
    }
  if (item?.stateKind === 'assertion_history' && Array.isArray(item.revisions))
    return {
      stateKind: 'assertion_history',
      ...pick(item, [
        'kind',
        'interpretation',
        'sourceBodyCoverage',
        'asOfRecorded',
        'validAt',
        'coverage',
      ]),
      revisions: item.revisions.map((revision) => ({
        ...pick(revision, [
          'assertionId',
          'version',
          'recordedAt',
          'validFrom',
          'validTo',
          'validityCoverage',
        ]),
        ref: stateReference(object(revision)?.ref),
        record: memory(object(revision)?.record),
      })),
    }
  return undefined
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
      'parentBinding',
      'occurredAt',
      'learnedAt',
      'observedAt',
      'validUntil',
      'validFrom',
      'epistemicKind',
    ]),
    ...(object(record.scope)?.kind === 'scene' && typeof object(record.scope)?.personId === 'string'
      ? { personId: object(record.scope)!.personId }
      : {}),
    ...(typeof record.content === 'string'
      ? {
          content: record.content,
        }
      : {}),
    ...(record.sources ? { sources: provenance(record.sources) } : {}),
    ...(Array.isArray(record.relationEvidence)
      ? {
          relationCoverage: record.relationCoverage,
          relationEvidence: record.relationEvidence.map((edge) => ({
            ...pick(edge, [
              'id',
              'version',
              'predicate',
              'personId',
              'polarity',
              'modality',
              'temporalScope',
              'conditions',
              'occurredAt',
              'validFrom',
              'validUntil',
              'epistemicKind',
              'status',
              'admission',
              'autoReason',
              'applicability',
              'requiredMemoryIds',
            ]),
            target: pick(object(edge)?.target, ['entityId', 'kind', 'title']),
            sources: provenance(object(edge)?.sources),
          })),
        }
      : {}),
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
    ...(plan.stateEvidenceIds ?? []),
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
      if (stateEvidenceTools.includes(receipt.toolName ?? '')) {
        if (!(plan.stateEvidenceIds ?? []).includes(receipt.id))
          return { ...base, contentOmitted: 'state_not_selected' }
        const states = Array.isArray(receipt.value)
          ? receipt.value.map(state).filter((entry): entry is RecordValue => !!entry)
          : []
        return {
          ...base,
          states,
          caution:
            'State declarations and historical assertions are qualified evidence, not verified current truth. Working-set hot/cold/pin/access counts are accessibility policy, not truth, successful recall, satisfaction or event verification. Experience entries remain independent reports; shared plans are participant declarations, not observed events. Preserve all conditions, incomplete confirmation, historical time and coverage.',
        }
      }
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
            'Recalled information, not independently verified facts; preserve uncertainty and conflicts, including complete qualifications.',
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
