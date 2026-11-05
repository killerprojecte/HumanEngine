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
  reported: number
  calls: number
  readKnown: number
  writeKnown: number
}
export const usageKeys = ['input', 'read', 'write', 'output'] as const
export type UsageKey = (typeof usageKeys)[number]
export function usageCoverage(point: UsagePoint, key: UsageKey): number {
  return key === 'read' ? point.readKnown : key === 'write' ? point.writeKnown : point.reported
}
export function usageValue(point: UsagePoint, key: UsageKey): number | null {
  return usageCoverage(point, key) > 0 && point.calls > 0 ? point[key] : null
}
export function usageBars(point: UsagePoint) {
  return usageKeys
    .flatMap((key) => {
      const value = usageValue(point, key)
      return value === null
        ? []
        : [{ key, value, partial: usageCoverage(point, key) < point.calls }]
    })
    .sort((a, b) => b.value - a.value)
}
/** Split paths at unknown values rather than drawing them as zero or bridging them. */
export function usageLines(points: UsagePoint[], key: UsageKey, max: number) {
  const segments: { x: number; y: number; partial: boolean }[][] = []
  let current: { x: number; y: number; partial: boolean }[] = []
  points.forEach((point, index) => {
    const value = usageValue(point, key)
    if (value === null) {
      if (current.length) segments.push(current)
      current = []
      return
    }
    current.push({
      x: ((index + 0.5) / points.length) * 1000,
      y: 200 - (value / max) * 200,
      partial: usageCoverage(point, key) < point.calls,
    })
  })
  if (current.length) segments.push(current)
  return segments
}
