/** Bounded formatter cache shared by SQLite usage bucketing. */
const formatters = new Map<string, Intl.DateTimeFormat>()
// Usage is retained for recent dates, whose IANA transitions fall on minute boundaries.
// Cache local bucket results by UTC minute, not local wall time (repeated DST hours differ).
// A global cap also bounds memory when callers change zones or request long ranges.
const minuteBuckets = new Map<string, string>()
const MAX_MINUTE_BUCKETS = 8192
export function usageTimeZone(value: string): string | undefined {
  if (!value || value.length > 100) return undefined
  try {
    return new Intl.DateTimeFormat('en', { timeZone: value }).resolvedOptions().timeZone
  } catch {
    return undefined
  }
}
export function usageBucket(at: string, zone: string, bucket: 'hour' | 'day'): string {
  const date = new Date(at)
  const key = `${zone}:${bucket}:${Math.floor(date.getTime() / 60000)}`
  const cached = minuteBuckets.get(key)
  if (cached !== undefined) {
    minuteBuckets.delete(key)
    minuteBuckets.set(key, cached)
    return cached
  }
  const remember = (value: string) => {
    if (minuteBuckets.size >= MAX_MINUTE_BUCKETS)
      minuteBuckets.delete(minuteBuckets.keys().next().value!)
    minuteBuckets.set(key, value)
    return value
  }
  let formatter = formatters.get(zone)
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-CA', {
      timeZone: zone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23',
    })
    if (formatters.size >= 16) formatters.delete(formatters.keys().next().value!)
    formatters.set(zone, formatter)
  }
  const parts = Object.fromEntries(formatter.formatToParts(date).map((p) => [p.type, p.value]))
  const day = `${parts.year}-${parts.month}-${parts.day}`
  if (bucket === 'day') return remember(day)
  // Subtract local minutes/seconds, not UTC minutes: quarter-hour zones and repeated DST
  // hours retain their distinct UTC instants while sharing the same local wall-clock label.
  const offset = (at: Date, values: Record<string, string>) =>
    Date.UTC(
      Number(values.year),
      Number(values.month) - 1,
      Number(values.day),
      Number(values.hour),
      Number(values.minute),
      Number(values.second),
    ) -
    Math.floor(at.getTime() / 1000) * 1000
  const currentOffset = offset(date, parts)
  const start =
    date.getTime() -
    Number(parts.minute) * 60000 -
    Number(parts.second) * 1000 -
    date.getUTCMilliseconds()
  // A half-hour DST change may begin a local hour at :30 rather than :00.
  // Keep the bucket inside the current offset segment, rather than relabeling it as the previous hour.
  const isCurrentSegment = (minute: number) => {
    const candidate = new Date(start + minute * 60000)
    const values = Object.fromEntries(
      formatter.formatToParts(candidate).map((p) => [p.type, p.value]),
    )
    return (
      values.year === parts.year &&
      values.day === parts.day &&
      values.month === parts.month &&
      values.hour === parts.hour &&
      offset(candidate, values) === currentOffset
    )
  }
  // At most one offset transition can occur within this recent local-hour window.
  // The current minute is known to match. Binary search avoids scanning every minute
  // for each uncached timestamp during a half-hour DST transition.
  let first = 0
  let last = Number(parts.minute)
  if (!isCurrentSegment(0)) {
    while (first < last) {
      const middle = Math.floor((first + last) / 2)
      if (isCurrentSegment(middle)) last = middle
      else first = middle + 1
    }
  }
  return remember(new Date(start + first * 60000).toISOString())
}
