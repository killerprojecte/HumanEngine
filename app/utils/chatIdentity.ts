/** Names are presentation only. Never decode a canonical ID into identity metadata. */
export function humanName(value: string | undefined): string {
  const name = value?.replace(/[\u0000-\u001f\u007f]/g, '').trim() ?? ''
  return !name || /^(?:ob:|plugin:|\d{5,}$)/i.test(name) ? '' : name
}
export function avatarSource(value: string | undefined): string | undefined {
  if (!value) return undefined
  if (/^\/(?!\/)/.test(value)) return value
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined
  } catch {
    return undefined
  }
}
export function identityColor(id: string): string {
  let hash = 0
  for (const character of id) hash = (hash * 31 + character.charCodeAt(0)) | 0
  return ['#477b77', '#8066a4', '#a77446', '#5279a8', '#a35f7d'][Math.abs(hash) % 5]!
}
