import { readFileSync, writeFileSync } from 'node:fs'
if (
  process.version !== 'v24.22.1' ||
  process.versions.icu !== '78.3' ||
  process.versions.tz !== '2026e'
)
  throw Error('Pinned Node oracle required')
const names = JSON.parse(
  readFileSync(new URL('../timezones/timezone-names-node.json', import.meta.url)),
)
const zones = [
  ...new Set([
    ...Object.keys(names),
    ...Object.values(names),
    '+01',
    '-23:59',
    '−00:30',
    '+0530',
    '-00',
    'uTc',
    'aMeRiCa/NeW_yOrK',
  ]),
]
const timestamps = [
  -8640000000000000, -2208988800000, -1, 0, 1, 86399999, 86400000, 1710053999999, 1710054000000,
  1730613599999, 1730613600000, 1767225600000, 8640000000000000,
]
const cases = zones.map((zone) => {
  const formatter = new Intl.DateTimeFormat('en', {
    hour: 'numeric',
    hourCycle: 'h23',
    timeZone: zone,
  })
  return { zone, hours: timestamps.map((ms) => Number(formatter.format(new Date(ms)))) }
})
const output =
  JSON.stringify(
    {
      node: process.version,
      icu: process.versions.icu,
      tz: process.versions.tz,
      timestamps,
      cases,
    },
    null,
    2,
  ) + '\n'
const file = new URL('oracle.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(file, 'utf8') !== output) throw Error('Intl hour oracle drift')
} else writeFileSync(file, output)
console.log(`${cases.length * timestamps.length} exact Intl h23 comparisons`)
