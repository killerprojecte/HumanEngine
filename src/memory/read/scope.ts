import type { MemoryContext } from '../domain/contracts.ts'

/** Scope SQL only; callers retain their own fresh recovery/proof gates. */
export function assertionScopeFilter(
  ctx: Pick<MemoryContext, 'sceneId' | 'simulation'>,
  personId?: string,
): { sql: string; params: string[] } | undefined {
  if (ctx.simulation) return personId ? undefined : { sql: "scope_key='shared:public'", params: [] }
  if (personId)
    return { sql: 'scope_key=? AND person_id=?', params: [`scene:${ctx.sceneId}`, personId] }
  return { sql: "(scope_key=? OR scope_key='shared:public')", params: [`scene:${ctx.sceneId}`] }
}
