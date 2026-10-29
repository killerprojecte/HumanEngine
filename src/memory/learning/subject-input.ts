import { KNOWLEDGE_PREDICATES } from '../knowledge/registry.ts'
import type { z } from '@hono/zod-openapi'
import type { CandidateSchema } from './contracts.ts'
import type { MemoryContext } from '../domain/contracts.ts'
import { resolveSubjectAttribution, type SubjectAttribution } from '../write/subject-attribution.ts'
import type { RuntimeMessage } from '../../runtime.ts'

/** Extraction guidance is not an authority check; writes must use the shared subject validator. */
export const subjectExtractionInstructions =
  "Attribute a personal assertion explicitly with subject: self plus sourceMessageId only for the cited speaker's actual self-statement; mentioned plus personId, sourceMessageId and mentionPartIndex only for the actual subject identified by that exact normalized mention witness; or unresolved when identity is unknown. A mention proves identity binding, not the truth or grammatical subject of a statement. Do not infer identity from a nickname, similar name, roster, raw platform ID or another message. Never use the speaker as an unresolved-subject fallback. Third-party testimony must be kind claim with epistemicKind reported, not an autobiographical fact. Quoted speech, jokes, roleplay and hypotheses are not self-facts; omit them or retain explicitly qualified scene claims. Never infer personality, gender or age. For occurredAt and validity times, emit a precise offset ISO instant only when the source supports that precision; do not replace unknown dates with now, source time or midnight. Resolve explicit relative dates only against the source sentAt and supplied timezone; otherwise omit exact time. receivedAt, observedAt, learnedAt and trusted sourceEvidence are server authority and must never be proposed."

/** Explicit original part indices survive compact wire projection; nicknames are not witnesses. */
export function subjectLearningMessages(
  messages: readonly RuntimeMessage[],
  includeTextParts = false,
) {
  return messages.map((message) => {
    const subjectMentions = message.parts.flatMap((part, partIndex) =>
      part.type === 'mention' && part.userId !== 'all' && part.userId !== 'bot'
        ? [{ personId: part.userId, mentionPartIndex: partIndex }]
        : [],
    )
    return {
      ...message,
      ...(includeTextParts
        ? {
            textParts: message.parts.flatMap((part, partIndex) =>
              part.type === 'text' ? [{ partIndex, text: part.text }] : [],
            ),
          }
        : {}),
      ...(subjectMentions.length ? { subjectMentions } : {}),
    }
  })
}

/** Repair admission uses the same authoritative attribution rules as persistence/reconciliation. */
export function resolveLearningSubject(
  ctx: MemoryContext,
  candidate: Pick<
    z.infer<typeof CandidateSchema>,
    'sourceMessageIds' | 'subject' | 'personId' | 'kind' | 'epistemicKind'
  >,
): SubjectAttribution {
  const evidence = candidate.sourceMessageIds.map((id) =>
    ctx.sourceEvidence?.find((source) => source.sceneId === ctx.sceneId && source.messageId === id),
  )
  if (evidence.some((source) => !source)) return { ok: false, reason: 'invalid_source_evidence' }
  return resolveSubjectAttribution(ctx, {
    ...candidate,
    sources: evidence.map((source) => ({
      sceneId: source!.sceneId,
      messageId: source!.messageId,
      senderId: source!.senderId,
    })),
  })
}

export function relationExtractionInstructions(enabled: boolean): string {
  return enabled
    ? ' Relation extraction is enabled as an optional part of the same memory proposal; never make an extra model call just to populate it. Propose at most three participates_in relations only when the cited source explicitly supports the complete assertion. personId must be the current validated self/mentioned subject. New targets must be concrete plans or events from these sources; titles are labels, never identity keys or merge authority. Existing targets require an exact entity reference actually exposed in this contextual turn; source-only extraction must not use existing targets. Preserve negative polarity, all conditions, uncertainty and explicit validity/event times; unknown time stays omitted. Always provide modality asserted or possible and temporalScope current, past, future or unspecified. Possible participation is possible, never a positive current assertion. Preserve past participation with temporalScope past even when its exact date is unknown; omit exact timestamps rather than inventing them. Unknown temporal applicability is unspecified, not current. Do not erase these qualifiers to seek automatic admission: only asserted/current can be considered for the separate low-risk admission policy, and even accepted does not mean verified truth. Reported statements remain candidate claims, not verified truth. Do not assume automatic acceptance or invent graph endpoints.'
    : ' Relation extraction is disabled. Do not propose relations or infer graph endpoints; ordinary sourced memory extraction remains available.'
}

/** A content repair cannot establish that its old relation semantics remain unchanged. */
export function repairLearningCandidate(
  candidate: z.infer<typeof CandidateSchema>,
  content: string,
) {
  const changed = content !== candidate.content
  const { relations, commitment, knowledge, ...rest } = candidate
  return {
    candidate: {
      ...rest,
      content,
      ...(!changed && relations !== undefined ? { relations } : {}),
      ...(!changed && commitment !== undefined ? { commitment } : {}),
      ...(!changed && knowledge !== undefined ? { knowledge } : {}),
    },
    relationsDiscarded: changed && Boolean(relations?.length),
  }
}

/** Structured plan proposals share the existing learning call, never add autonomous sends. */
export function commitmentExtractionInstructions(enabled: boolean): string {
  return enabled
    ? ' Plan tracking is enabled as an optional part of this same sourced memory proposal. Use exactly one explicit self-declaration source with ephemeral text, subject.kind self and epistemicKind explicit; reported/hypothetical/unclear declarations must not become confirmations. The server stores a typed internal declaration marker, not your expanded content or raw chat. Preserve complete participants, polarity, modality, conditions, and temporalScope; unknown time stays omitted, never guess a date. A source-only proposal may propose a new plan, never identify an old plan by title. Confirm, amend, complete or cancel requires the exact plan reference actually delivered in this contextual turn and an explicit statement by the corresponding participant; one person cannot confirm for others. An amended proposal does not replace the old confirmed arrangement until all required people independently confirm that same terms version. Completion/cancellation is self-reported, not externally verified. Do not treat jokes, reported speech, silence, message delivery or a WAIT as confirmation, and never schedule reminders or send messages from this field.'
    : ' Plan tracking is disabled; do not include commitment commands.'
}

/** Finite structured claims share the same source-only call; spans locate evidence, not semantic truth. */
export function knowledgeExtractionInstructions(enabled: boolean): string {
  return enabled
    ? ' Structured knowledge is enabled as optional knowledge in the same scene-scoped candidate, at most three proposals; never call a model solely to fill it. Use registryVersion 1 and only registered predicates. Preserve polarity, modality, temporalScope, conditions and supported validity; unknown times/units remain unknown, not invented. subjects of kind self identify the actual cited speaker, never a third-party quoted person. Reported or quoted spans must retain their stance and require a claim with epistemicKind reported, inferred or hypothesis; they cannot become explicit self-facts. Use asserted stance for a direct non-person declaration, not self. For concepts/tasks/tools/topics/organizations, new entity titles are labels, not identity: never merge same-title entities or guess an existing entity ID. Source-only extraction may use self or new subjects and new or typed literal objects, never existing endpoints or old memory. Each sourceSpans entry locates the exact original textParts item by messageId and partIndex; start/end are JavaScript UTF-16 code-unit offsets, end exclusive. Do not use offsets in joined text, Unicode code-point counts, normalized whitespace or paraphrases. Non-text parts still occupy original part indices. A valid span only proves where text occurs, not that its meaning supports your predicate or that the claim is true. Use has_property with exactly one registered property version/count/duration/enabled for supported typed literal values; do not invent property names. Use literal text, number with explicit unit or null for unknown unit, or boolean only when the registered predicate permits it. depends_on connects concept/task entities only, not arbitrary literals. Never combine knowledge with publicTechnicalDomain: structured claims remain scene-private and are not public-review material. Knowledge does not upgrade old relations or commitment confirmations. Do not treat a queued or proposed structure as saved or accepted. Registered endpoint kinds: ' +
        JSON.stringify(KNOWLEDGE_PREDICATES)
    : ' Structured knowledge extraction is disabled; do not include knowledge proposals, source spans or guessed entities.'
}
