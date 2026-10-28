/** Only server-projected, scene/message-bound resources can be fetched by the image UI. */
export function chatMediaUrl(value: string | undefined): string | undefined {
  if (!value || !/^\/api\/scenes\/[^/?#]+\/messages\/[^/?#]+\/media\/(?:0|[1-9]\d*)$/.test(value))
    return undefined
  return value
}
export const chatImageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
export const chatImageByteLimit = 10 * 1024 * 1024

/** Transport diagnostics are enums, not user text or a safe UI caption. */
export function historyReasonKey(reason: string): string {
  if (reason === 'platform_history_window') return 'cwHistoryWindow'
  if (reason === 'history_exhausted_or_unavailable') return 'cwHistoryNoMore'
  if (reason === 'unsupported') return 'cwHistoryUnsupported'
  return 'cwHistoryLimited'
}
