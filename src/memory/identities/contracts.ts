import { z } from 'zod'
import { SourceEventRefSchema } from '../domain/events.ts'
export const EndpointSchema = z
  .object({
    adapterId: z.string().min(1).max(160),
    accountId: z.string().min(1).max(160),
    personId: z.string().min(1).max(160),
  })
  .strict()
export type IdentityEndpoint = z.infer<typeof EndpointSchema>
const id = z.string().min(1).max(160)
const revision = z.number().int().min(0).max(Number.MAX_SAFE_INTEGER)
const time = z.string().datetime({ offset: true }).nullable()
export const AliasSchema = z
  .object({
    id,
    endpoint: EndpointSchema,
    label: z.string().min(1).max(200),
    scopeSceneId: id,
    source: z.discriminatedUnion('kind', [
      z.object({ kind: z.literal('manual'), administratorId: id }).strict(),
      z
        .object({
          kind: z.literal('observation'),
          sceneId: id,
          messageId: id,
          senderId: id,
          eventRef: SourceEventRefSchema.optional(),
        })
        .strict(),
    ]),
    validFrom: time,
    validUntil: time,
  })
  .strict()
  .refine(
    (a) => !a.validFrom || !a.validUntil || Date.parse(a.validFrom) < Date.parse(a.validUntil),
    'invalid_alias_interval',
  )
  .refine(
    (a) => a.source.kind !== 'observation' || a.source.sceneId === a.scopeSceneId,
    'identity_alias_scope_mismatch',
  )
export type IdentityAlias = z.infer<typeof AliasSchema>
export const RegistryStateSchema = z
  .object({
    format: z.literal(1),
    revision,
    edges: z.array(z.tuple([EndpointSchema, EndpointSchema])).max(4096),
    aliases: z.array(AliasSchema).max(4096),
    operations: z
      .array(z.object({ id, hash: z.string().regex(/^[a-f0-9]{64}$/), revision }).strict())
      .max(16384),
  })
  .strict()
  .superRefine((state, ctx) => {
    const key = (e: IdentityEndpoint) => JSON.stringify([e.adapterId, e.accountId, e.personId])
    const issue = () => ctx.addIssue({ code: 'custom', message: 'identity_registry_inconsistent' })
    if (state.operations.length + state.edges.length + state.aliases.length > 16384) issue()
    if (
      state.revision !== state.operations.length ||
      state.operations.some((op, i) => op.revision !== i + 1) ||
      new Set(state.operations.map((op) => op.id)).size !== state.operations.length ||
      new Set(state.aliases.map((a) => a.id)).size !== state.aliases.length
    )
      issue()
    const edgeKeys = state.edges.map(([a, b]) => JSON.stringify([key(a), key(b)].sort()))
    if (
      new Set(edgeKeys).size !== edgeKeys.length ||
      state.edges.some(([a, b]) => key(a) === key(b))
    )
      issue()
    const graph = new Map<string, Set<string>>()
    for (const [a, b] of state.edges) {
      for (const [x, y] of [
        [key(a), key(b)],
        [key(b), key(a)],
      ] as [string, string][]) {
        if (!graph.has(x)) graph.set(x, new Set())
        graph.get(x)!.add(y)
      }
    }
    const visited = new Set<string>()
    for (const start of graph.keys()) {
      if (visited.has(start)) continue
      const pending = [start]
      let size = 0
      while (pending.length) {
        const current = pending.pop()!
        if (visited.has(current)) continue
        visited.add(current)
        size++
        for (const next of graph.get(current) ?? []) if (!visited.has(next)) pending.push(next)
      }
      if (size > 32) issue()
    }
  })
export type RegistryState = z.infer<typeof RegistryStateSchema>
export interface IdentityScope {
  sceneId: string
  adapterId: string
  accountId: string
  allowedPersonIds: readonly string[]
  simulation?: boolean
}
export interface IdentitySnapshot {
  revision: number
  sceneId: string
  anchor: IdentityEndpoint
  members: IdentityEndpoint[]
}
export interface IdentityMutation {
  kind: 'link' | 'unlink' | 'split' | 'alias_put' | 'alias_remove' | 'source_recall'
  operationId: string
  expectedRevision: number
  nextRevision: number
  beforeHash: string
  qualificationHash: string
  targets: {
    endpoints: IdentityEndpoint[]
    aliasIds: string[]
    source?: { sceneId: string; messageId: string }
  }
}
export interface IdentityPorts {
  assertReady(): void
  beforeWrite?(mutation: IdentityMutation): void
  authorizeObservation?(alias: IdentityAlias): boolean
  sourceAvailable?(
    source: Extract<IdentityAlias['source'], { kind: 'observation' }>,
    endpoint: IdentityEndpoint,
  ): boolean
  now?(): number
}
export interface IdentityCommand {
  operationId: string
  expectedRevision: number
  simulation?: boolean
  adminConfirmed: true
}
