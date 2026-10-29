import {
  stateEvidenceTools,
  workspaceStateTools,
  validWorkspaceState,
} from './workspace-state-evidence.ts'
import type { buildReplyBrief } from './reply-brief.ts'
import {
  selectEvidenceGroups,
  type EvidenceBudget,
  type JsonValue,
  type SelectionResult,
} from './evidence-selector.ts'
export type ReplyBrief = ReturnType<typeof buildReplyBrief>
export interface MemoryOmission {
  evidenceId: string
  reason:
    | 'missing_memory_selection'
    | 'missing_state_selection'
    | 'budget_or_group_limit'
    | 'budget'
    | 'record_limit'
    | 'group_limit'
    | 'planner_evidence_budget'
  omittedRecords?: number
}
export interface PackedReplyEvidence {
  status: 'selected' | 'insufficient' | 'invalid'
  brief?: ReplyBrief
  serialized?: string
  usage?: SelectionResult<JsonValue>['usage']
  omitted: readonly MemoryOmission[]
  reason?: string
}
const object = (value: unknown): Record<string, unknown> | undefined =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined

/** Packs authorized complete memories and fact dependencies, not semantic entailment or authority. */
export function packReplyEvidence(input: {
  brief: ReplyBrief
  envelope: Record<string, unknown>
  serialize(payload: Record<string, unknown>): string
  budget: EvidenceBudget
  mandatoryEvidenceIds?: readonly string[]
  upstreamOmissions?: readonly {
    evidenceId: string
    reason: 'planner_evidence_budget'
    omittedRecords: number
  }[]
}): PackedReplyEvidence {
  const fail = (reason: string): PackedReplyEvidence => ({ status: 'invalid', omitted: [], reason })
  try {
    const upstream = (input.upstreamOmissions ?? []).map((item) => ({ ...item }))
    if (
      upstream.length > 12 ||
      new Set(upstream.map((item) => item.evidenceId)).size !== upstream.length ||
      upstream.some(
        (item) =>
          typeof item.evidenceId !== 'string' ||
          !item.evidenceId ||
          item.reason !== 'planner_evidence_budget' ||
          !Number.isSafeInteger(item.omittedRecords) ||
          item.omittedRecords < 1,
      )
    )
      return fail('invalid_upstream_omission')
    const upstreamIds = new Set(upstream.map((item) => item.evidenceId))
    const brief = structuredClone(input.brief)
    const envelope = structuredClone(input.envelope)
    const freeze = (value: unknown): void => {
      if (value && typeof value === 'object' && !Object.isFrozen(value)) {
        Object.values(value).forEach(freeze)
        Object.freeze(value)
      }
    }
    const memory = brief.evidence.filter((receipt) => receipt.toolName?.startsWith('memory.'))
    if (memory.length > 40 || new Set(memory.map((item) => item.id)).size !== memory.length)
      return fail('candidate_limit_or_duplicate')
    const ids = new Set(memory.map((item) => item.id))
    const mandatory = new Set(input.mandatoryEvidenceIds ?? [])
    if ([...mandatory].some((id) => !ids.has(id))) return fail('invalid_mandatory_selection')
    const missing = new Set<string>()
    const sizes = new Map<string, number>()
    let recordCount = 0
    for (const receipt of memory) {
      const raw = object(receipt)!
      const isState = stateEvidenceTools.includes(receipt.toolName ?? '')
      const records = isState
        ? Array.isArray(raw.states)
          ? raw.states
          : []
        : Array.isArray(raw.memories)
          ? raw.memories
          : []
      if (
        isState &&
        (workspaceStateTools.includes(receipt.toolName ?? '')
          ? records.length !== 1 || !validWorkspaceState(receipt.toolName!, records[0])
          : receipt.toolName === 'memory.commitments'
            ? records.length > 12
            : records.some(
                (record) =>
                  !Array.isArray(object(record)?.revisions) ||
                  (object(record)!.revisions as unknown[]).length > 20,
              ))
      )
        return fail('state_record_limit')
      if (
        isState &&
        ['memory.history', 'memory.commitmentHistory'].includes(receipt.toolName ?? '') &&
        records.reduce(
          (sum, record) =>
            sum +
            (Array.isArray(object(record)?.revisions)
              ? (object(record)!.revisions as unknown[]).length
              : 0),
          0,
        ) > 20
      )
        return fail('state_record_limit')
      // A bounded state receipt is one indivisible selection unit, not one memory record.
      recordCount += isState ? 1 : records.length
      if (
        raw.contentOmitted ||
        !records.length ||
        (!isState &&
          records.some(
            (record) =>
              typeof object(record)?.content !== 'string' || object(record)?.contentTruncated,
          ))
      )
        missing.add(receipt.id)
      sizes.set(receipt.id, isState ? 1 : records.length)
    }
    if (recordCount > 40) return fail('candidate_record_limit')
    if ([...mandatory].some((id) => missing.has(id)))
      return {
        status: 'insufficient',
        omitted: [...missing].map((evidenceId) => ({
          evidenceId,
          reason: 'missing_memory_selection',
        })),
        reason: 'mandatory_incomplete',
      }
    // Union all memory receipts jointly supporting a fact; never retain half a dependency group.
    const roots = new Map([...ids].map((id) => [id, id]))
    const root = (id: string): string => {
      let next = id
      while (roots.get(next) !== next) next = roots.get(next)!
      return next
    }
    const facts = brief.plan.facts.filter((fact) => !fact.evidenceIds.some((id) => missing.has(id)))
    for (const fact of facts) {
      const refs = fact.evidenceIds.filter((id) => ids.has(id))
      for (const id of refs.slice(1)) roots.set(root(id), root(refs[0]!))
    }
    const groups = new Map<string, string[]>()
    for (const receipt of memory)
      if (!missing.has(receipt.id)) {
        const key = root(receipt.id)
        groups.set(key, [...(groups.get(key) ?? []), receipt.id])
      }
    const project = (kept: ReadonlySet<string>) => {
      const omitted: MemoryOmission[] = memory
        .filter((item) => !kept.has(item.id) && !upstreamIds.has(item.id))
        .map((item) => ({
          evidenceId: item.id,
          reason: missing.has(item.id)
            ? stateEvidenceTools.includes(item.toolName ?? '')
              ? 'missing_state_selection'
              : 'missing_memory_selection'
            : 'budget_or_group_limit',
        }))
      omitted.push(...upstream)
      const plan = {
        ...brief.plan,
        facts: facts.filter((fact) => fact.evidenceIds.every((id) => !ids.has(id) || kept.has(id))),
      }
      if (omitted.length) {
        delete plan.replySuggestion
        plan.strategy =
          'Use only retained evidence; acknowledge missing information without reconstructing omitted memory.'
        plan.expressionGoals = []
      }
      const packed: ReplyBrief = {
        ...brief,
        plan,
        evidence: brief.evidence.filter((item) => !ids.has(item.id) || kept.has(item.id)),
      }
      return {
        packed,
        omitted,
        payload: {
          ...envelope,
          ...packed,
          memoryEvidenceCoverage: { status: omitted.length ? 'omitted' : 'complete', omitted },
        },
      }
    }
    const candidates = [...groups.entries()].map(([id, receipts], index) => ({
      id,
      rank: 40 - index,
      mandatory: receipts.some((ref) => mandatory.has(ref)),
      complete: true,
      units: receipts.reduce((sum, ref) => sum + sizes.get(ref)!, 0),
      value: receipts,
    }))
    const serializer = (values: readonly JsonValue[]) => {
      const payload = project(new Set(values.flatMap((value) => value as string[]))).payload
      freeze(payload)
      return input.serialize(payload)
    }
    let selected = selectEvidenceGroups<string[]>({
      candidates,
      budget: input.budget,
      serialize: serializer,
    })
    const omissions = selected.omitted
    // No optional memory fitting is a valid fixed-envelope handoff with explicit omissions.
    if (selected.status === 'insufficient' && selected.reason === 'no_complete_evidence')
      selected = selectEvidenceGroups<string[]>({
        candidates: [],
        budget: input.budget,
        serialize: serializer,
      })
    if (selected.status !== 'selected')
      return {
        status: selected.status,
        reason: selected.reason,
        omitted: project(new Set()).omitted,
      }
    const kept = new Set(selected.groups.flatMap((group) => group.value))
    const projected = project(kept)
    const diagnostics = projected.omitted.map((item) => {
      if (
        item.reason === 'missing_memory_selection' ||
        item.reason === 'missing_state_selection' ||
        item.reason === 'planner_evidence_budget'
      )
        return item
      const reason = omissions.find((omission) =>
        groups.get(omission.id)?.includes(item.evidenceId),
      )?.reason
      return {
        ...item,
        reason:
          reason === 'record_limit' || reason === 'group_limit' || reason === 'budget'
            ? reason
            : item.reason,
      }
    })
    freeze(diagnostics)
    freeze(projected.packed)
    freeze(projected.omitted)
    return {
      status: 'selected',
      brief: projected.packed,
      serialized: selected.serialized,
      usage: selected.usage,
      omitted: diagnostics,
    }
  } catch {
    return fail('invalid_reply_evidence')
  }
}
