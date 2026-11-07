// Run with the exact source Node v24.22.1. Synthetic data only.
import { readFileSync, writeFileSync } from 'node:fs'
if (
  process.version !== 'v24.22.1' ||
  process.versions.icu !== '78.3' ||
  process.versions.unicode !== '17.0' ||
  process.versions.cldr !== '48.0'
)
  throw Error('requires pinned Node v24.22.1 ICU/Unicode/CLDR')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
let values = [
  '',
  '0',
  '1',
  '12',
  '13',
  '49',
  '50',
  '99',
  '100',
  '0000',
  '-000000',
  '+000000',
  '-000000-01-01',
  '2024-02-30',
  '2023-02-29',
  '2020-13-01',
  '2020-01-32',
  '2020-01-01T24:00:00.000',
  '2020-01-01T24:00:00.001',
  '2020-01-01T00:00:00.0000000001',
  '2020-01-01\0garbage',
  'Jan 1 2000',
  'Januarylong 1 2000',
  'garbage 2000/1/1',
  'garbage2000/1/1',
  '2000/1/1 garbage',
  '(unclosed 2000',
  '2000 (unclosed',
  '2000/1/1 GMT+1234',
  '2000/1/1 GMT+12:34',
  '2000/1/1 12::',
  '2000/1/1 12:34.56',
  '2000/1/1 12:34:56.000000000123',
  '2000/1/1 12:34:56 AM',
  '2000/1/1 24:00',
  '+275760-09-13',
  '+275760-09-13T00:00:00.000Z',
  '+275760-09-13T00:00:00.001Z',
  '-271821-04-20T00:00:00.000Z',
  '-271821-04-19T23:59:59.999Z',
]
for (const date of [
  '+275760-09-12',
  '+275760-09-13',
  '+275760-09-14',
  '-271821-04-19',
  '-271821-04-20',
  '-271821-04-21',
])
  for (const clock of [
    '00:00:00.000',
    '00:00:00.001',
    '01:00:00.000',
    '12:00:00.000',
    '23:59:59.999',
    '24:00:00.000',
  ])
    for (const suffix of ['', 'Z', '+14:00', '-12:00']) values.push(date + 'T' + clock + suffix)
const seeds = [...values]
for (const s of seeds)
  for (const edge of [' ', '\t', '\n', '\u2028', '\ufeff', '\ud800', '\0', '(', ')', 'Z', '+01:00'])
    values.push(edge + s, s + edge)
for (const year of ['0000', '0099', '1970', '2024', '275760', '-271821', '999999999999999'])
  for (const month of ['0', '1', '02', '12', '13'])
    for (const day of ['0', '1', '29', '31', '32'])
      for (const sep of ['-', '/', ' ', '.']) values.push([year, month, day].join(sep))
let state = 12345
function rand(n) {
  state = (Math.imul(state, 1664525) + 1013904223) >>> 0
  return state % n
}
const parts = [
  '2000',
  '01',
  '31',
  '12',
  '24',
  '59',
  '999',
  '0000000001',
  'Jan',
  'GMT',
  'UTC',
  'Z',
  'T',
  'PM',
  'am',
  'foo',
  '-',
  ':',
  '.',
  '/',
  ',',
  '+',
  ' ',
  '\n',
  '\u2029',
  '\ud800',
  '(',
  ')',
  '\0',
]
for (let i = 0; i < 12000; i++) {
  let s = ''
  for (let j = 0, n = 1 + rand(12); j < n; j++) s += parts[rand(parts.length)]
  values.push(s)
}
values = [...new Set(values)]
const zones = ['UTC', 'America/New_York', 'Asia/Shanghai', 'Pacific/Kiritimati', 'Europe/Paris']
let cases = values.map((value) => ({
  units: Array.from({ length: value.length }, (_, i) => value.charCodeAt(i)),
  finite: [],
  milliseconds: [],
}))
for (const zone of zones) {
  process.env.TZ = zone
  values.forEach((value, i) => {
    const milliseconds = Date.parse(value)
    const finite = Number.isFinite(milliseconds)
    cases[i].finite.push(finite)
    cases[i].milliseconds.push(finite ? milliseconds : null)
  })
}
const target = new URL('./testdata/oracle.json', import.meta.url)
const output =
  JSON.stringify({ node: process.version, icu: process.versions.icu, zones, cases }) + '\n'
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== output) throw Error('Date oracle differs')
} else writeFileSync(target, output)
console.log(`${cases.length * zones.length} Node date validity and exact millisecond comparisons`)
