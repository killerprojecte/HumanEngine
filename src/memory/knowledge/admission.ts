import { createHash } from 'node:crypto'
import { z } from 'zod'
import type { MemoryCandidate, MemoryContext } from '../domain/contracts.ts'
import { KnowledgeProposalSchema } from './contracts.ts'

/** A typed graph declaration must not smuggle old graph prose into an ordinary personal fact. */
export function prepareKnowledgeAssertion(
  ctx: MemoryContext,
  candidate: MemoryCandidate,
):
  { ok: true; context: MemoryContext; candidate: MemoryCandidate } | { ok: false; reason: string } {
  const parsed = z.array(KnowledgeProposalSchema).min(1).max(3).safeParse(candidate.knowledge)
  if (!parsed.success) return { ok: false, reason: 'knowledge_proposal_invalid' }
  if (
    candidate.commitment ||
    candidate.relations?.length ||
    candidate.mergeIntoId ||
    candidate.supersedesId
  )
    return { ok: false, reason: 'knowledge_mixed_write_denied' }
  const reported = parsed.data.some((item) =>
    item.sourceSpans.some((span) => ['reported', 'quoted'].includes(span.stance)),
  )
  if (
    reported &&
    (candidate.kind !== 'claim' ||
      !['reported', 'inferred', 'hypothesis'].includes(candidate.epistemicKind ?? ''))
  )
    return { ok: false, reason: 'knowledge_stance_mismatch' }
  if (ctx.provenance !== 'contextual')
    return { ok: true, context: ctx, candidate: { ...candidate, knowledge: parsed.data } }
  // The exact source stays in the proof. Its text and the model's expanded graph prose do not.
  const declaration = createHash('sha256')
    .update(
      JSON.stringify([
        ctx.sceneId,
        candidate.sources,
        parsed.data,
        ctx.exposedKnowledgeEntities ?? [],
        ctx.exposedKnowledgeEdges ?? [],
      ]),
    )
    .digest('hex')
  return {
    ok: true,
    context: {
      ...ctx,
      provenance: 'source-only',
      dependencyMemoryIds: [],
      dependencyParentReferences: [],
      internalDeclaration: 'knowledge_declaration',
    },
    candidate: {
      ...candidate,
      content: `Model-extracted structured knowledge declaration; reference ${declaration}. Not independently verified.`,
      knowledge: parsed.data,
      kind: 'claim',
    },
  }
}
