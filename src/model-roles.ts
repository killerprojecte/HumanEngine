/** Persisted legacy role IDs remain accepted; new routing and observations use Replyer. */
export const MODEL_ROLE_INPUTS = [
  'planner',
  'replyer',
  'thinker',
  'learning',
  'vision',
  'embedding',
] as const
export type ModelRole = (typeof MODEL_ROLE_INPUTS)[number]
export type CanonicalModelRole = Exclude<ModelRole, 'thinker'>
export function canonicalModelRole<T extends string>(role: T): T extends 'thinker' ? 'replyer' : T {
  return (role === 'thinker' ? 'replyer' : role) as T extends 'thinker' ? 'replyer' : T
}
export function normalizeRoleRoutes<T extends { role: ModelRole }>(
  routes: T[],
): Array<Omit<T, 'role'> & { role: CanonicalModelRole }> {
  const normalized = routes.map((route) => ({ ...route, role: canonicalModelRole(route.role) }))
  if (new Set(normalized.map((route) => route.role)).size !== normalized.length)
    throw new Error('duplicate_role_routes')
  return normalized
}
