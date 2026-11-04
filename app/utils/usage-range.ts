export type UsagePreset = '24h' | 'today' | 'yesterday' | '7d' | '14d' | 'month' | 'custom'
export function usageRange(preset: UsagePreset, now = new Date(), start = '', end = '') {
  let from = new Date(now),
    to = new Date(now)
  if (preset === 'custom') {
    from = new Date(start)
    to = new Date(end)
    // datetime-local has no offset. Reject nonexistent local times (for example a DST gap)
    // instead of silently normalizing them into a different wall-clock time.
    const matchesLocal = (value: string, date: Date) => {
      if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?$/.test(value)) return false
      const pad = (n: number) => String(n).padStart(2, '0')
      const local = `${String(date.getFullYear()).padStart(4, '0')}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}${value.length === 19 ? `:${pad(date.getSeconds())}` : ''}`
      return local === value
    }
    if (!matchesLocal(start, from) || !matchesLocal(end, to)) return undefined
  } else if (preset === '24h') from = new Date(now.getTime() - 86400000)
  else if (preset === 'today' || preset === 'yesterday') {
    from.setHours(0, 0, 0, 0)
    if (preset === 'yesterday') {
      to = new Date(from)
      from.setDate(from.getDate() - 1)
    }
  } else if (preset === 'month') {
    const day = from.getDate()
    from.setDate(1)
    from.setMonth(from.getMonth() - 1)
    const last = new Date(from.getFullYear(), from.getMonth() + 1, 0).getDate()
    from.setDate(Math.min(day, last))
  } else from.setDate(from.getDate() - (preset === '7d' ? 7 : 14))
  if (!Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to)
    return undefined
  return { start: from.toISOString(), end: to.toISOString() }
}
