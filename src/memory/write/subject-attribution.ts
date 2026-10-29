import type {
  MemoryCandidate,
  MemoryContext,
  MemoryKind,
  EpistemicKind,
  SubjectWitness,
  SourceObservation,
} from '../domain/contracts.ts'
export type SubjectAttribution =
  | {
      ok: true
      personId?: string
      kind: MemoryKind
      epistemicKind?: EpistemicKind
      witness?: SubjectWitness
      sourceObservations: SourceObservation[]
    }
  | { ok: false; reason: string }
/** Only explicit offset instants from trusted transport metadata. No date inference. */
const trustedTime = (value: unknown): string | undefined => {
  if (typeof value !== 'string') return undefined
  const match =
    /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(?:Z|[+-](\d\d):(\d\d))$/.exec(value)
  if (!match || !Number.isFinite(Date.parse(value))) return undefined
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number)
  const leap = year! % 4 === 0 && (year! % 100 !== 0 || year! % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (
    month! < 1 ||
    month! > 12 ||
    day! < 1 ||
    day! > days[month! - 1]! ||
    hour! > 23 ||
    minute! > 59 ||
    second! > 59 ||
    Number(match[7] ?? 0) > 23 ||
    Number(match[8] ?? 0) > 59
  )
    return undefined
  return value
}
/** Identity binding, not semantic proof that a quote/joke describes a real person. */
export function resolveSubjectAttribution(
  ctx: MemoryContext,
  candidate: Pick<
    MemoryCandidate,
    | 'sources'
    | 'subject'
    | 'personId'
    | 'kind'
    | 'epistemicKind'
    | 'occurredAt'
    | 'validFrom'
    | 'validUntil'
  >,
): SubjectAttribution {
  const reject = (reason: string): SubjectAttribution => ({ ok: false, reason })
  if (ctx.simulation) return reject('simulation_does_not_learn')
  if (
    candidate.subject !== undefined &&
    [candidate.occurredAt, candidate.validFrom, candidate.validUntil].some(
      (value) => value !== undefined && trustedTime(value) === undefined,
    )
  )
    return reject('invalid_subject_time')
  const sources = candidate.sources.map((source) =>
    ctx.sourceEvidence?.find(
      (e) =>
        e.sceneId === ctx.sceneId &&
        e.sceneId === source.sceneId &&
        e.messageId === source.messageId &&
        e.senderId === source.senderId &&
        e.available &&
        !e.isBot &&
        e.senderId !== (ctx.botUserId ?? 'bot'),
    ),
  )
  if (!sources.length || sources.some((s) => !s)) return reject('invalid_source_evidence')
  const sourceObservations = sources.map((source) => ({
    sceneId: source!.sceneId,
    messageId: source!.messageId,
    senderId: source!.senderId,
    ...(trustedTime(source!.sentAt) ? { sentAt: trustedTime(source!.sentAt) } : {}),
    ...(trustedTime(source!.receivedAt) ? { receivedAt: trustedTime(source!.receivedAt) } : {}),
  }))
  let personId: string | undefined, witness: SubjectWitness | undefined
  const subject = candidate.subject
  if (subject?.kind === 'unresolved') {
    if (candidate.personId) return reject('subject_person_mismatch')
    witness = { kind: 'unresolved' }
  } else if (subject?.kind === 'self' || subject?.kind === 'mentioned') {
    const source = sources.find((s) => s!.messageId === subject.sourceMessageId)!
    if (!source) return reject('subject_source_not_cited')
    if (subject.kind === 'self') {
      personId = source.senderId
      witness = { kind: 'self', personId, sourceMessageId: source.messageId }
    } else {
      if (
        !Number.isSafeInteger(subject.mentionPartIndex) ||
        subject.mentionPartIndex < 0 ||
        !subject.personId ||
        subject.personId === 'all' ||
        subject.personId === (ctx.botUserId ?? 'bot')
      )
        return reject('invalid_subject_mention')
      const mention = source.mentions?.find(
        (m) =>
          m.partIndex === subject.mentionPartIndex && m.userId === subject.personId && !m.isBot,
      )
      if (!mention) return reject('subject_mention_not_witnessed')
      personId = subject.personId
      witness = {
        kind: 'mentioned',
        personId,
        sourceMessageId: source.messageId,
        mentionPartIndex: subject.mentionPartIndex,
      }
    }
    if (candidate.personId !== undefined && candidate.personId !== personId)
      return reject('subject_person_mismatch')
  } else if (subject !== undefined) return reject('invalid_subject')
  else if (candidate.personId) {
    const source = sources.find((s) => s!.senderId === candidate.personId)
    if (!source) return reject('legacy_subject_not_cited_sender')
    personId = candidate.personId
    witness = { kind: 'legacy_sender', personId, sourceMessageId: source.messageId }
  }
  const epistemicKind =
    subject?.kind === 'mentioned'
      ? candidate.epistemicKind === 'inferred' || candidate.epistemicKind === 'hypothesis'
        ? candidate.epistemicKind
        : 'reported'
      : candidate.epistemicKind
  const kind =
    subject?.kind === 'unresolved' ||
    (subject !== undefined &&
      epistemicKind &&
      ['reported', 'inferred', 'hypothesis'].includes(epistemicKind))
      ? 'claim'
      : candidate.kind
  return { ok: true, personId, kind, epistemicKind, witness, sourceObservations }
}
export function earliestObservation(
  observations: readonly SourceObservation[],
): string | undefined {
  return observations
    .map((item) => trustedTime(item.receivedAt))
    .filter((time): time is string => time !== undefined)
    .sort((a, b) => Date.parse(a) - Date.parse(b) || a.localeCompare(b))[0]
}
