/** Synthetic JSON.parse/stringify oracle; Node 24.22.1, no application state.
 * node backend/tests/generate-jsjson-fixtures.mjs [--check]
 * Strings cross the fixture boundary as UTF-16 units, never lossy UTF-8.
 */
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'

if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
if (process.argv.slice(2).some(arg => arg !== '--check')) throw Error('Only --check is supported')
const units = s => Array.from({ length: s.length }, (_, i) => s.charCodeAt(i))
const fromUnits = a => a.map(u => String.fromCharCode(u)).join('')
const numberBuffer = new ArrayBuffer(8)
const numberView = new DataView(numberBuffer)
const bitsOf = n => {
  numberView.setFloat64(0, n)
  return numberView.getBigUint64(0).toString(16).padStart(16, '0')
}
const fromBits = bits => {
  numberView.setBigUint64(0, BigInt(`0x${bits}`))
  return numberView.getFloat64(0)
}
const describe = v => {
  if (v === null) return { kind: 'null' }
  if (typeof v === 'number') return { kind: 'number', bits: bitsOf(v) }
  if (typeof v === 'boolean') return { kind: 'boolean', boolean: v }
  if (typeof v === 'string') return { kind: 'string', units: units(v), utf16Length: v.length, codePoints: [...v].length }
  if (Array.isArray(v)) return { kind: 'array', values: v.map(describe) }
  return { kind: 'object', members: Object.keys(v).map(k => ({ units: units(k), value: describe(v[k]) })) }
}

const parseCases = []
const parseCase = (name, source) => {
  let value
  try { value = JSON.parse(source) } catch { parseCases.push({ name, sourceUnits: units(source), valid: false }); return }
  parseCases.push({ name, sourceUnits: units(source), valid: true, encoded: JSON.stringify(value), value: describe(value) })
}
for (const [name, source] of [
  ['null', 'null'], ['true', 'true'], ['false', 'false'], ['empty_string', '""'],
  ['whitespace', ' \r\n\t { "x" : [true, null, false] } \t\r\n'],
  ['empty_array', '[]'], ['empty_object', '{}'],
  ['string_escaping', '"\\u0000\\u0001\\b\\f\\n\\r\\t\\\"\\\\\\/"'],
  ['html_and_separators', '"</script>&\u2028\u2029"'],
  ['lone_high', '"\\uD800"'], ['lone_low', '"\\udfff"'],
  ['paired_surrogates', '"\\uD83D\\uDE00"'], ['literal_pair', '"😀"'],
  ['reversed_surrogates', '"\\udc00\\ud800"'],
  ['adjacent_high_then_pair', '"\\ud800\\ud801\\udc02"'],
  ['replacement_and_surrogates', '["\\ud800","\\ud801","�","\\udc00"]'],
  ['raw_surrogates', `"${fromUnits([0xd800, 0x61, 0xdfff])}"`],
  ['raw_high_escaped_low', `"${String.fromCharCode(0xd83d)}\\ude00"`],
  ['escaped_high_raw_low', `"\\ud83d${String.fromCharCode(0xde00)}"`],
  ['duplicate_preserves_position', '{"b":0,"a":1,"b":2,"c":3,"a":4}'],
  ['escaped_duplicate', '{"a":1,"\\u0061":2,"z":3}'],
  ['surrogate_key_identity', '{"\\ud800":1,"\\ud801":2,"�":3,"\\ud800":4}'],
  ['paired_key_identity', '{"😀":1,"x":2,"\\ud83d\\ude00":3}'],
  ['prototype_keys', '{"__proto__":{"polluted":true},"constructor":1,"toString":2}'],
  ['integer_key_order', '{"z":0,"10":10,"2":2,"01":1,"0":0,"-0":0,"4294967295":1,"4294967294":2,"a":3,"1.0":4,"1e0":5}'],
  ['nested', '{"z":[{"2":"two","1":"one","a":"first"},null,[1,-0]],"a":{"b":true}}'],
  ['zero', '0'], ['negative_zero', '-0'], ['negative_zero_fraction', '-0.000e123'],
  ['positive_overflow', '1e400'], ['negative_overflow', '-1e400'],
  ['negative_underflow', '-1e-4000'], ['positive_underflow', '1e-4000'],
  ['large_integer_rounding', '9007199254740993'],
  ['large_digits', '1234567890123456789012345678901234567890'],
  ['rounding_midpoint', '1.00000000000000011102230246251565404236316680908203125'],
  ['huge_exponent', '1e99999999999999999999999999999999999999999'],
  ['zero_huge_exponent', '-0e99999999999999999999999999999999999999999'],
]) parseCase(name, source)

for (const source of [
  '', ' ', 'undefined', 'NaN', 'Infinity', '-Infinity', '+1', '.1', '1.', '1e', '1e+', '1e-',
  '01', '-01', '00', '--1', '0x1', '1_0', 'true false', 'nullx', 'TRUE', 'nul',
  '[1,]', '[,]', '[1 2]', '[', '{', '{"x":}', '{"x":1,}', '{x:1}', '{"x" 1}',
  '"unterminated', '"\\"', '"\\u000"', '"\\uqqqq"', '"\\x20"', '"\\v"', '"\\0"',
  '"\n"', '"\t"', '"\u0000"', '\ufeffnull', '\u00a0null', 'null\u2028', '/*x*/null', 'null//x',
]) parseCase(`invalid_${parseCases.length}`, source)

// Reproducible random words. Counts are test coverage, not runtime limits.
let state = 0x6a09e667
const random = () => {
  state ^= state << 13
  state ^= state >>> 17
  state ^= state << 5
  return state >>> 0
}
const stringCases = []
const stringCase = (name, sourceUnits) => {
  const value = fromUnits(sourceUnits)
  stringCases.push({ name, units: sourceUnits, encoded: JSON.stringify(value), utf16Length: value.length, codePoints: [...value].length })
}
stringCase('empty', [])
stringCase('all_control_characters', Array.from({ length: 32 }, (_, i) => i))
stringCase('quotes_slash_html_separators', units('"\\/<> &\u2028\u2029'))
stringCase('bmp_and_supplementary', units('中文 é 😀 𝄞 𐀀 \ufffd \uffff'))
stringCase('minimum_maximum_pairs', [0xd800, 0xdc00, 0xdbff, 0xdfff])
stringCase('broken_pairs', [0xdc00, 0xd800, 0xd801, 0x61, 0xdc01, 0xd800])
for (let unit = 0xd800; unit <= 0xdfff; unit++) stringCase(`lone_${unit.toString(16)}`, [unit])
// This single case also checks every BMP code unit, including all boundaries.
stringCase('all_utf16_units', Array.from({ length: 0x10000 }, (_, i) => i))
for (let i = 0; i < 256; i++) {
  const sourceUnits = Array.from({ length: random() % 65 }, () => random() & 0xffff)
  stringCase(`random_${i}`, sourceUnits)
  if (i < 64) {
    const source = JSON.stringify(fromUnits(sourceUnits))
    parseCase(`random_string_${i}`, source)
    // Repeated and numeric keys exercise key ordering at every nested depth.
    parseCase(`random_object_${i}`, `{"z":${source},"${random() % 100}":${source},"x":[${source},-0],"z":null}`)
  }
}

const numberCases = []
const seenBits = new Set()
const numberCase = bits => {
  if (seenBits.has(bits)) return
  seenBits.add(bits)
  numberCases.push({ bits, encoded: JSON.stringify(fromBits(bits)) })
}
for (const bits of [
  '0000000000000000', '8000000000000000', '7ff0000000000000', 'fff0000000000000',
  '7ff8000000000000', '7ff0000000000001', 'fff8000000000001',
  '0000000000000001', '8000000000000001', '7fefffffffffffff', 'ffefffffffffffff',
  '0010000000000000', '000fffffffffffff', '4340000000000000', 'c340000000000000',
  '4430000000000000', '44b52d02c7e14af5', '44b52d02c7e14af6', '44b52d02c7e14af7',
  '444b1ae4d6e2ef4e', '444b1ae4d6e2ef4f', '444b1ae4d6e2ef50',
  '3eb0c6f7a0b5ed8c', '3eb0c6f7a0b5ed8d', '41b3de4355555553',
]) numberCase(bits)
for (let exponent = -323; exponent <= 308; exponent++) {
  const bits = BigInt(`0x${bitsOf(10 ** exponent)}`)
  for (const offset of [-1n, 0n, 1n]) {
    const nearby = bits + offset
    if (nearby >= 0n && nearby < 0x7ff0000000000000n) {
      numberCase(nearby.toString(16).padStart(16, '0'))
      numberCase((nearby | 0x8000000000000000n).toString(16).padStart(16, '0'))
    }
  }
}
for (let i = 0; i < 8192; i++) numberCase(random().toString(16).padStart(8, '0') + random().toString(16).padStart(8, '0'))
for (let i = 0; i < 512; i++) {
  const sign = random() & 1 ? '-' : ''
  const digits = `${random()}.${random().toString().padStart(10, '0')}${random().toString().padStart(10, '0')}`
  parseCase(`random_decimal_${i}`, `${sign}${digits}e${(random() % 801) - 400}`)
}

// JSON.parse cannot express deletion/re-insertion, so exercise those ordered
// object operations separately against actual ordinary JS objects.
const objectCases = []
const objectCase = (name, operations) => {
  const value = Object.create(null)
  for (const operation of operations) {
    const key = fromUnits(operation.units)
    if (operation.kind === 'delete') delete value[key]
    else value[key] = JSON.parse(operation.source)
  }
  objectCases.push({ name, operations, encoded: JSON.stringify(value), value: describe(value) })
}
const set = (key, source) => ({ kind: 'set', units: units(key), source })
const remove = key => ({ kind: 'delete', units: units(key) })
objectCase('replace_and_reinsert', [set('b', '1'), set('a', '2'), set('b', '3'), remove('b'), set('b', '4'), remove('missing')])
objectCase('numeric_reinsert', [set('a', '0'), set('10', '10'), set('2', '2'), remove('2'), set('2', '3'), set('01', '4'), set('4294967295', '5'), set('4294967294', '6')])
objectCase('surrogate_properties', [set('\ud800', '1'), set('\ud801', '2'), set('�', '3'), set('\ud800', '4'), set('😀', '5'), set('\ud83d\ude00', '6')])
objectCase('prototype_properties', [set('__proto__', '1'), set('constructor', '2'), remove('__proto__'), set('__proto__', '3')])

const document = { node: process.version, parseCases, stringCases, numberCases, objectCases }
const output = `${JSON.stringify(document)}\n`
const target = new URL('../internal/jsjson/testdata/node24.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== output) throw Error('jsjson fixtures changed')
} else {
  mkdirSync(new URL('.', target), { recursive: true })
  writeFileSync(target, output)
}
console.log(`jsjson Node ${process.version}: ${parseCases.length} parses, ${stringCases.length} strings, ${numberCases.length} binary64 values, ${objectCases.length} object traces`)
