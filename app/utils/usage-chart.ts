/** Compact display only; exact values remain available in chart/table tooltips. */
export function compactNumber(value: number): string {
  if (!Number.isFinite(value)) return '—'
  const magnitude = Math.abs(value)
  const unit =
    magnitude >= 999_500_000 ? 1e9 : magnitude >= 999_500 ? 1e6 : magnitude >= 999.5 ? 1e3 : 1
  const suffix = unit === 1e9 ? 'B' : unit === 1e6 ? 'M' : unit === 1e3 ? 'K' : ''
  return `${Number((value / unit).toFixed(unit === 1 ? 0 : 2))}${suffix}`
}
export type UsagePoint = {
  input: number
  output: number
  read: number
  write: number
  known: number
  calls: number
  readKnown: number
  writeKnown: number
}
export const usageKeys = ['input', 'read', 'write', 'output'] as const
export type UsageKey = (typeof usageKeys)[number]
export function usageValue(point: UsagePoint, key: UsageKey): number | null {
  const complete =
    key === 'read'
      ? point.readKnown === point.calls
      : key === 'write'
        ? point.writeKnown === point.calls
        : point.known === 0
  return complete && point.calls > 0 ? point[key] : null
}
export function usageBars(point: UsagePoint) {
  return usageKeys
    .flatMap((key) => {
      const value = usageValue(point, key)
      return value === null ? [] : [{ key, value }]
    })
    .sort((a, b) => b.value - a.value)
}
/** Split paths at unknown values rather than drawing them as zero or bridging them. */
export function usageLines(points: UsagePoint[], key: UsageKey, max: number) {
  const segments: { x: number; y: number }[][] = []
  let current: { x: number; y: number }[] = []
  points.forEach((point, index) => {
    const value = usageValue(point, key)
    if (value === null) {
      if (current.length) segments.push(current)
      current = []
      return
    }
    current.push({ x: ((index + 0.5) / points.length) * 1000, y: 200 - (value / max) * 200 })
  })
  if (current.length) segments.push(current)
  return segments
}
