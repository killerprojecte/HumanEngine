import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import { canonicalProofKey } from '../domain/proofs.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { RelationProof } from '../domain/relations.ts'
import type { KnowledgeRef, KnowledgeSpan } from './contracts.ts'
export const digest = (value: unknown) =>
  createHash('sha256').update(JSON.stringify(value)).digest('hex')
export const knowledgeBackingFingerprint = (record: MemoryRecord) =>
  digest([
    record.id,
    record.scope,
    record.content,
    record.kind,
    record.epistemicKind ?? null,
    record.occurredAt ?? null,
    record.validFrom ?? null,
    record.validUntil ?? null,
    record.internalUse ?? null,
  ])
export const sameRef = (a: KnowledgeRef, b: KnowledgeRef) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
export const sameSource = (a: KnowledgeSpan['source'], b: KnowledgeSpan['source']) =>
  isDeepStrictEqual(a, b)
export const intersect = (stored: RelationProof[], live: RelationProof[]) =>
  stored.filter((p) =>
    live.some(
      (q) =>
        p.proofId === q.proofId &&
        canonicalProofKey(p) === canonicalProofKey(q) &&
        isDeepStrictEqual(p, q),
    ),
  )
