/** Revalidate the checked-in alias census against the pinned legacy Node/ICU oracle. */
import { readFileSync, writeFileSync } from 'node:fs'
if (
  process.version !== 'v24.22.1' ||
  process.versions.icu !== '78.3' ||
  process.versions.tz !== '2026e'
)
  throw Error(
    'Use the pinned Node 24.22.1 / ICU 78.3 / tzdata 2026e oracle; updating the contract requires explicit review',
  )
const check = process.argv.includes('--check')
const write = (file, content) => {
  if (check) {
    if (readFileSync(file, 'utf8') !== content) throw Error(`Timezone oracle drift: ${file}`)
  } else writeFileSync(file, content)
}
const target = new URL('../internal/timezones/', import.meta.url)
const prior = JSON.parse(readFileSync(new URL('timezone-names-node.json', target), 'utf8'))
// The initial alias census combines ICU enumeration, Go's zoneinfo names and
// Intl.supportedValuesOf. Additional alias lists may be supplied as JSON arrays.
const candidates = new Set([...Object.keys(prior), ...Intl.supportedValuesOf('timeZone'), 'UTC'])
for (const file of process.argv.slice(2).filter((arg) => arg !== '--check'))
  for (const name of JSON.parse(readFileSync(file, 'utf8'))) candidates.add(name)
const accepted = {}
for (const input of candidates) {
  try {
    accepted[input.toLowerCase()] = new Intl.DateTimeFormat('en', {
      timeZone: input,
    }).resolvedOptions().timeZone
  } catch {}
}
write(
  new URL('timezone-names-node.json', target),
  JSON.stringify(Object.fromEntries(Object.entries(accepted).sort()), null, 2) + '\n',
)
const cases = [
  ...Object.keys(accepted),
  ...Object.keys(accepted).map((x) => x.toUpperCase()),
  '',
  'Local',
  'local',
  'not/a/timezone',
  ' utc ',
  '\ufeffUTC',
  '+05:30',
  '+0530',
  '+05',
  '-00',
  '-2359',
  '+24:00',
  '+23:60',
  '+5:30',
  '−05:30',
  '+05:30:00',
  'Asia/Kolkata',
]
write(
  new URL('../internal/contract/testdata/timezones-node.json', import.meta.url),
  JSON.stringify(
    cases.map((input) => {
      try {
        new Intl.DateTimeFormat('en', { timeZone: input })
        return { input, valid: true }
      } catch {
        return { input, valid: false }
      }
    }),
    null,
    2,
  ) + '\n',
)
