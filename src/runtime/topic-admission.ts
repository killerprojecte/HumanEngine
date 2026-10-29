/** Pure routing hypotheses only. Caller supplies same-scene trusted anchor ownership.
 * No model, body interpretation, scheduling permission, UUID allocation or mutation.
 */
export interface TopicSnapshot {
  id: string
  sceneId: string
  touchedAt: number
  participants: readonly string[]
  cue?: { initiatorId: string; label: string }
  provisional: boolean
  active: boolean
  pending: boolean
  waiting: boolean
  sendState: 'none' | 'settled' | 'partial' | 'unknown'
}
export interface TopicInput {
  sceneId: string
  senderId: string
  now: number
  /** Already validated by caller against exact available source/ACK ownership. */
  anchors?: readonly { kind: 'reply' | 'thread' | 'task'; topicId: string }[]
  unavailableReply?: boolean
  /** User routing hint only, never a plan/commitment identity or consent. */
  cue?: string
}
export type TopicAssignment = {
  kind: 'existing' | 'create' | 'provisional' | 'deferred'
  topicId?: string
  evictTopicId?: string
  reason:
    | 'reply_anchor'
    | 'thread_anchor'
    | 'task_anchor'
    | 'conflicting_anchors'
    | 'reply_anchor_unavailable'
    | 'explicit_cue'
    | 'participant_continuity'
    | 'single_conversation'
    | 'ambiguous'
    | 'no_recent_topic'
    | 'capacity_deferred'
  candidateIds: string[]
  cue?: { initiatorId: string; label: string }
}
export function parseTopicCue(text: string): string | undefined {
  const match = /^\[(?:话题|topic):\s*([^\]\r\n]{1,32})\]/u.exec(text)
  const label = match?.[1]?.trim().normalize('NFC')
  return label || undefined
}
const safeToEvict = (t: TopicSnapshot) =>
  !t.active && !t.pending && !t.waiting && (t.sendState === 'none' || t.sendState === 'settled')
export function assignTopic(
  input: TopicInput,
  topics: readonly TopicSnapshot[],
  config: { maxSlots?: number; recentMs?: number } = {},
): TopicAssignment {
  const maxSlots = config.maxSlots ?? 4,
    recentMs = config.recentMs ?? 120_000
  if (
    !Number.isSafeInteger(maxSlots) ||
    maxSlots < 1 ||
    maxSlots > 16 ||
    !Number.isSafeInteger(recentMs) ||
    recentMs < 1 ||
    !Number.isSafeInteger(input.now) ||
    input.now < 0
  )
    throw new Error('topic_admission_config_invalid')
  const scoped = topics.filter((t) => t.sceneId === input.sceneId)
  if (new Set(scoped.map((t) => t.id)).size !== scoped.length || scoped.length > maxSlots)
    throw new Error('topic_admission_snapshot_invalid')
  const recent = scoped.filter(
    (t) => t.touchedAt <= input.now && input.now - t.touchedAt <= recentMs,
  )
  const ordinary = recent.filter((t) => !t.provisional)
  const candidates = (values: readonly TopicSnapshot[]) => values.map((t) => t.id).sort()
  const allocate = (
    provisional: boolean,
    reason: TopicAssignment['reason'],
    ids: string[],
    cue?: TopicAssignment['cue'],
  ): TopicAssignment => {
    const bucket = provisional ? scoped.find((t) => t.provisional) : undefined
    if (bucket)
      return {
        kind: 'provisional',
        topicId: bucket.id,
        reason,
        candidateIds: ids,
      }
    const disposable = [...scoped]
      .filter(safeToEvict)
      .sort((a, b) => a.touchedAt - b.touchedAt || a.id.localeCompare(b.id))[0]
    if (scoped.length >= maxSlots && !disposable)
      return {
        kind: 'deferred',
        reason: 'capacity_deferred',
        candidateIds: ids,
      }
    return {
      kind: provisional ? 'provisional' : 'create',
      reason,
      candidateIds: ids,
      ...(scoped.length >= maxSlots ? { evictTopicId: disposable!.id } : {}),
      ...(cue ? { cue } : {}),
    }
  }
  const existing = (topic: TopicSnapshot, reason: TopicAssignment['reason']): TopicAssignment => ({
    kind: 'existing',
    topicId: topic.id,
    reason,
    candidateIds: [topic.id],
  })
  // Stronger explicit anchors cannot be overridden by attention, recency or participant.
  for (const kind of ['reply', 'thread', 'task'] as const) {
    if (kind !== 'reply' && input.unavailableReply)
      return allocate(true, 'reply_anchor_unavailable', [])
    const anchors = input.anchors?.filter((a) => a.kind === kind) ?? []
    if (!anchors.length) continue
    const ids = [...new Set(anchors.map((a) => a.topicId))].sort()
    if (ids.length !== 1)
      return allocate(
        true,
        'conflicting_anchors',
        ids.filter((id) => scoped.some((t) => t.id === id)),
      )
    const target = scoped.find((t) => t.id === ids[0])
    if (target) return existing(target, `${kind}_anchor`)
    return allocate(true, 'reply_anchor_unavailable', [])
  }
  if (input.unavailableReply) return allocate(true, 'reply_anchor_unavailable', [])
  if (input.cue !== undefined) {
    const label = input.cue.trim().normalize('NFC')
    if (!label || [...label].length > 32 || /[\r\n\[\]]/u.test(label))
      throw new Error('topic_cue_invalid')
    const cue = { initiatorId: input.senderId, label }
    const matches = scoped.filter(
      (t) => t.cue?.initiatorId === cue.initiatorId && t.cue.label === label,
    )
    if (matches.length === 1) return existing(matches[0]!, 'explicit_cue')
    if (matches.length > 1) return allocate(true, 'ambiguous', candidates(matches))
    return allocate(false, 'explicit_cue', [], cue)
  }
  const participant = ordinary.filter((t) => t.participants.includes(input.senderId))
  if (participant.length === 1) return existing(participant[0]!, 'participant_continuity')
  if (participant.length > 1) return allocate(true, 'ambiguous', candidates(participant))
  if (ordinary.length === 1) return existing(ordinary[0]!, 'single_conversation')
  return allocate(true, ordinary.length ? 'ambiguous' : 'no_recent_topic', candidates(ordinary))
}
