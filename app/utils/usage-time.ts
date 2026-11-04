export function formatUsageTime(
  value: string,
  locale: string,
  timeZone: string,
  compact = false,
): string {
  const date = new Date(value.length === 10 ? `${value}T00:00:00Z` : value)
  if (!Number.isFinite(date.getTime())) return value
  return new Intl.DateTimeFormat(locale, {
    timeZone: timeZone || undefined,
    ...(compact
      ? {}
      : {
          year: 'numeric' as const,
          month: '2-digit' as const,
          day: '2-digit' as const,
          timeZoneName: 'shortOffset' as const,
        }),
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).format(date)
}
export function formatUsageBucket(
  value: string,
  locale: string,
  timeZone: string,
  compact = false,
): string {
  // A local calendar day from the API is already a date label, not a UTC instant.
  if (value.length === 10) return compact ? value.slice(5) : value
  return formatUsageTime(value, locale, timeZone, compact)
}
