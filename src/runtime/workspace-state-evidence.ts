import { SemanticStateEvidenceSchema } from '../memory/read/semantic-exposure.ts'
/** DTO validation only: never grants memory access or replaces exposure validation. */
export const workspaceStateTools: readonly string[] = [
  'memory.workingSet',
  'memory.episodes',
  'memory.identities',
  'memory.knowledge',
  'memory.summaries',
  'memory.commitmentTimeline',
] as const
export const stateEvidenceTools: readonly string[] = [
  'memory.commitments',
  'memory.history',
  'memory.commitmentHistory',
  ...workspaceStateTools,
] as const
const obj = (v: unknown): Record<string, any> | undefined =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, any>) : undefined
const count = (v: unknown) => Number.isSafeInteger(v) && Number(v) >= 0
const ref = (v: unknown) => {
  const r = obj(v)
  return (
    !!r &&
    typeof r.id === 'string' &&
    r.id.length > 0 &&
    count(r.version) &&
    r.version > 0 &&
    count(r.deletionEpoch)
  )
}
const record = (v: unknown) => {
  const r = obj(v)
  return (
    !!r &&
    typeof r.id === 'string' &&
    typeof r.content === 'string' &&
    typeof r.kind === 'string' &&
    ['active', 'disputed'].includes(r.status)
  )
}
const plan = (v: unknown) => {
  const r = obj(v)
  const terms = obj(r?.terms)
  if (
    r?.terms !== undefined &&
    (!terms ||
      terms.kind !== 'plan' ||
      typeof terms.title !== 'string' ||
      !Array.isArray(terms.participantIds) ||
      !terms.participantIds.every((x: unknown) => typeof x === 'string') ||
      !Array.isArray(terms.conditions) ||
      !terms.conditions.every((x: unknown) => typeof x === 'string') ||
      !['positive', 'negative'].includes(terms.polarity) ||
      !['asserted', 'possible'].includes(terms.modality) ||
      !['current', 'past', 'future', 'unspecified'].includes(terms.temporalScope))
  )
    return false
  return (
    !!r &&
    ref(r.ref) &&
    r.qualification === 'declared_not_verified' &&
    r.sourceBodyCoverage === 'unavailable' &&
    ['pending', 'done', 'cancelled', 'needs_review'].includes(r.lifecycle) &&
    ['proposed', 'confirmed', 'incomplete'].includes(r.confirmation)
  )
}
export function validWorkspaceState(tool: string, value: unknown): boolean {
  if (tool === 'memory.summaries') return SemanticStateEvidenceSchema.safeParse(value).success
  const v = obj(value),
    p = obj(v?.page)
  if (tool === 'memory.commitmentTimeline') {
    const w = obj(v?.window)
    return (
      !!v &&
      v.stateKind === 'commitment_timeline' &&
      ref(v.anchor) &&
      count(v.anchor.mutationVersion) &&
      v.anchor.mutationVersion > 0 &&
      v.coverage === 'limited' &&
      v.interpretation === 'previously_recorded_declaration_state_not_current_truth' &&
      v.sourceBodyCoverage === 'unavailable' &&
      v.stateTime === 'server_recorded_not_occurred' &&
      Array.isArray(v.reasons) &&
      v.reasons.every((r: unknown) => typeof r === 'string') &&
      !!w &&
      w.maxTransitions === 64 &&
      w.throughMutationVersion === v.anchor.mutationVersion &&
      Array.isArray(v.recordedStates) &&
      v.recordedStates.length <= 20 &&
      v.recordedStates.every(
        (r: any) =>
          r &&
          typeof r.recordedAt === 'string' &&
          Number.isFinite(Date.parse(r.recordedAt)) &&
          count(r.mutationVersion) &&
          r.mutationVersion > 0 &&
          r.mutationVersion <= v.anchor.mutationVersion &&
          ['available', 'redacted'].includes(r.status) &&
          Array.isArray(r.states) &&
          r.states.length <= 2 &&
          (r.status !== 'redacted' || r.states.length === 0) &&
          r.states.every(plan),
      )
    )
  }
  if (tool === 'memory.knowledge') {
    return (
      !!v &&
      v.stateKind === 'knowledge_graph' &&
      !!p &&
      ['complete', 'limited'].includes(p.coverage) &&
      p.interpretation === 'attributed_claims_not_transitive_truth' &&
      Array.isArray(p.edges) &&
      p.edges.length <= 40 &&
      Array.isArray(p.paths) &&
      p.paths.length <= 64 &&
      p.edges.every(
        (e: any) =>
          !!e &&
          typeof e.id === 'string' &&
          ref(e.backing) &&
          ['positive', 'negative'].includes(e.polarity) &&
          ['asserted', 'possible'].includes(e.modality) &&
          ['current', 'past', 'future', 'unspecified'].includes(e.temporalScope) &&
          Array.isArray(e.conditions) &&
          Array.isArray(e.proofs) &&
          Array.isArray(e.dependencies) &&
          Array.isArray(e.readDependencies) &&
          Array.isArray(e.spans),
      ) &&
      !!obj(p.supportClosure) &&
      Array.isArray(p.supportClosure.edges) &&
      Array.isArray(p.supportClosure.records) &&
      p.edges.length + p.supportClosure.edges.length <= 40 &&
      p.supportClosure.records.length <= 128 &&
      p.supportClosure.records.every(
        (value: any) => ref(value?.ref) && !!obj(value.record) && value.ref.id === value.record.id,
      ) &&
      p.paths.every(
        (path: any) =>
          Array.isArray(path) &&
          path.length <= 2 &&
          path.every((id: any) => p.edges.some((e: any) => e.id === id)),
      )
    )
  }
  if (tool === 'memory.identities') {
    return (
      !!v &&
      v.stateKind === 'identity_view' &&
      !!p &&
      Object.keys(v).every((k) => ['stateKind', 'page'].includes(k)) &&
      typeof p.sceneId === 'string' &&
      typeof p.personId === 'string' &&
      count(p.revision) &&
      p.coverage === 'complete' &&
      p.interpretation === 'administrator_verified_links_not_read_authority_or_personal_facts' &&
      Array.isArray(p.items) &&
      p.items.length > 0 &&
      p.items.length <= 32 &&
      p.items.every((entry: unknown) => {
        const item = obj(entry)
        return (
          !!item &&
          typeof item.personId === 'string' &&
          Array.isArray(item.aliases) &&
          item.aliases.every((value: unknown) => {
            const alias = obj(value)
            return (
              !!alias &&
              typeof alias.label === 'string' &&
              ['manual', 'observation'].includes(alias.sourceKind) &&
              (alias.validFrom === null || typeof alias.validFrom === 'string') &&
              (alias.validUntil === null || typeof alias.validUntil === 'string')
            )
          })
        )
      }) &&
      p.items.reduce((n: number, item: any) => n + item.aliases.length, 0) <= 64
    )
  }
  if (
    !v ||
    Object.keys(v).some((k) => !['stateKind', 'page'].includes(k)) ||
    !p ||
    !Array.isArray(p.items) ||
    p.items.length > 12
  )
    return false
  if (tool === 'memory.workingSet') {
    if (
      v.stateKind !== 'working_set' ||
      typeof p.enabled !== 'boolean' ||
      !['complete', 'limited', 'disabled'].includes(p.coverage) ||
      !count(p.omittedCold)
    )
      return false
    if (!p.enabled) return p.coverage === 'disabled' && p.items.length === 0
    if (
      p.coverage === 'disabled' ||
      p.interpretation !== 'accessibility_not_truth_or_absence' ||
      !count(p.omittedEntries)
    )
      return false
    return p.items.every((e: unknown) => {
      const x = obj(e),
        a = obj(x?.accessibility)
      if (
        !x ||
        !a ||
        !['hot', 'cold', 'unknown'].includes(a.state) ||
        typeof a.reviewDue !== 'boolean' ||
        typeof a.pinIntent !== 'boolean'
      )
        return false
      return x.kind === 'assertion'
        ? ref(obj(x.snapshot)?.ref) && record(obj(x.snapshot)?.record)
        : x.kind === 'commitment' && plan(x.view)
    })
  }
  if (tool === 'memory.episodes') {
    const c = obj(p.coverage)
    if (
      v.stateKind !== 'experience_view' ||
      p.interpretation !== 'independent_qualified_records_not_synthesized_episodes' ||
      p.sourceBodyCoverage !== 'unavailable' ||
      !c ||
      !['complete', 'limited'].includes(c.status) ||
      !Array.isArray(c.reasons) ||
      !c.reasons.every((r: unknown) => ['upstream_limited', 'entry_limit'].includes(String(r))) ||
      c.returned !== p.items.length ||
      !count(c.omittedEntries)
    )
      return false
    return p.items.every((e: unknown) => {
      const x = obj(e)
      if (!x) return false
      if (x.kind === 'reported_experience') {
        const t = obj(x.time)
        return (
          ref(x.assertionRef) &&
          record(x.record) &&
          x.record.kind === 'experience' &&
          !!t &&
          (t.occurredAt === null || typeof t.occurredAt === 'string') &&
          typeof t.recordedAt === 'string'
        )
      }
      return (
        x.kind === 'shared_plan_declaration' &&
        ref(x.commitmentRef) &&
        plan(x.view) &&
        x.completionBasis === 'participant_self_report_not_observed_event'
      )
    })
  }
  return false
}
