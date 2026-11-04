import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import { computed, ref } from 'vue'

type Person = { personId: string; displayName?: string | null; recordCount: number }
type TranslationCall = [key: string, number: number | undefined]
const page = readFileSync(new URL('../app/pages/memories.vue', import.meta.url), 'utf8')
// Execute the page's actual computed definitions; keep the old expressions only
// as an equivalence oracle. This is logic coverage, not rendered browser QA.
const compiled = ts.transpileModule(
  page.slice(page.indexOf('const scopedPeople ='), page.indexOf('function personName(')),
  { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } },
).outputText
const setup = new Function(
  'computed',
  'administrationPersonIds',
  'people',
  't',
  'Map',
  `${compiled}\nreturn { scopedPeople, personOptions };`,
)
function fixture(ids: string[], rows: Person[], MapType: typeof Map = Map) {
  const administrationPersonIds = ref(ids),
    people = ref(rows),
    locale = ref('en')
  const calls: TranslationCall[] = []
  const t = (key: string, params?: { number: number }) => {
    calls.push([key, params?.number])
    return `${locale.value}:${key}:${params?.number ?? ''}`
  }
  const state = setup(computed, administrationPersonIds, people, t, MapType) as {
    scopedPeople: { value: { personId: string; displayName?: string | null }[] }
    personOptions: { value: { label: string; value: string }[] }
  }
  return { ...state, administrationPersonIds, people, locale, calls }
}
function original(ids: string[], people: Person[], locale = 'en') {
  const calls: TranslationCall[] = []
  const t = (key: string, params?: { number: number }) => {
    calls.push([key, params?.number])
    return `${locale}:${key}:${params?.number ?? ''}`
  }
  const scopedPeople = ids.map((personId) => ({
    personId,
    displayName: people.find((person) => person.personId === personId)?.displayName,
  }))
  const personOptions = [
    { label: t('mxAllPeople'), value: '' },
    ...scopedPeople.map((person, index) => ({
      label: person.displayName
        ? scopedPeople.filter((other) => other.displayName === person.displayName).length > 1
          ? `${person.displayName} · ${index + 1}`
          : person.displayName
        : t('mpUnnamed', { number: index + 1 }),
      value: person.personId,
    })),
  ]
  return { scopedPeople, personOptions, calls }
}
function parity(ids: string[], rows: Person[]) {
  const actual = fixture(ids, rows)
  const expected = original(ids, rows)
  assert.deepEqual(actual.scopedPeople.value, expected.scopedPeople)
  assert.deepEqual(actual.personOptions.value, expected.personOptions)
  assert.deepEqual(actual.calls, expected.calls)
}

test('person facets preserve first matches, facet order, exact names and translation calls', () => {
  const rows: Person[] = [
    { personId: 'empty-first', displayName: '', recordCount: 1 },
    { personId: 'empty-first', displayName: 'Ignored', recordCount: 2 },
    { personId: 'undefined-first', recordCount: 1 },
    { personId: 'undefined-first', displayName: 'Ignored', recordCount: 2 },
    { personId: 'null-first', displayName: null, recordCount: 1 },
    { personId: 'null-first', displayName: 'Ignored', recordCount: 2 },
    { personId: 'one', displayName: 'Same', recordCount: 3 },
    { personId: 'two', displayName: 'Same', recordCount: 4 },
    { personId: 'outside-facets', displayName: 'Only', recordCount: 10 },
    { personId: 'unique', displayName: 'Only', recordCount: 1 },
    { personId: 'composed', displayName: '\u00e9', recordCount: 1 },
    { personId: 'decomposed', displayName: 'e\u0301', recordCount: 1 },
    { personId: '__proto__', displayName: '__proto__', recordCount: 1 },
    { personId: 'constructor', displayName: ' ', recordCount: 1 },
    { personId: '', displayName: '空', recordCount: 1 },
  ]
  parity([], rows)
  parity(['missing', 'missing'], [])
  const ids = [
    'missing',
    'one',
    'empty-first',
    'two',
    'undefined-first',
    'one',
    'null-first',
    'unique',
    'composed',
    'decomposed',
    '__proto__',
    'constructor',
    '',
  ]
  parity(ids, rows)
  assert.deepEqual(
    fixture(ids, rows)
      .personOptions.value.slice(1, 7)
      .map((option) => option.label),
    ['en:mpUnnamed:1', 'Same · 2', 'en:mpUnnamed:3', 'Same · 4', 'en:mpUnnamed:5', 'Same · 6'],
  )
})

test('person facets match the prior expressions for deterministic duplicate-heavy inputs', () => {
  let seed = 0x25fa17
  const random = (limit: number) => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    return seed % limit
  }
  const ids = ['', 'a', 'b', 'c', 'missing', '__proto__', 'constructor']
  const names = [undefined, null, '', ' ', 'Same', 'Other', '\u00e9', 'e\u0301', '空', '__proto__']
  for (let run = 0; run < 500; run++) {
    const rows = Array.from({ length: random(15) }, () => ({
      personId: ids[random(ids.length)]!,
      displayName: names[random(names.length)],
      recordCount: random(20),
    }))
    const facets = Array.from({ length: random(18) }, () => ids[random(ids.length)]!)
    parity(facets, rows)
  }
})

test('person facets recompute from current directory, facet order and locale without retaining stale maps', () => {
  const f = fixture(
    ['a', 'b', 'missing'],
    [
      { personId: 'a', displayName: 'Same', recordCount: 1 },
      { personId: 'b', displayName: 'Same', recordCount: 1 },
    ],
  )
  const check = () => {
    const expected = original(f.administrationPersonIds.value, f.people.value, f.locale.value)
    assert.deepEqual(f.personOptions.value, expected.personOptions)
  }
  check()
  f.people.value[0]!.displayName = 'Changed'
  check()
  f.people.value.unshift({ personId: 'b', recordCount: 2 })
  check()
  f.administrationPersonIds.value = ['missing', 'b', 'a', 'a']
  check()
  f.locale.value = 'zh-TW'
  check()
  f.people.value = [{ personId: 'a', displayName: 'Replacement', recordCount: 1 }]
  check()
})

test('person facet ID reads and map operations grow linearly for a large all-same-name directory', () => {
  const measure = (size: number) => {
    let idReads = 0,
      mapOperations = 0
    class CountingMap<K, V> extends Map<K, V> {
      override get(key: K) {
        mapOperations++
        return super.get(key)
      }
      override has(key: K) {
        mapOperations++
        return super.has(key)
      }
      override set(key: K, value: V) {
        mapOperations++
        return super.set(key, value)
      }
    }
    const rows = Array.from({ length: size }, (_, index) => ({
      get personId() {
        idReads++
        return `person-${index}`
      },
      displayName: 'Same',
      recordCount: 1,
    }))
    const ids = Array.from({ length: size }, (_, index) => `person-${size - 1 - index}`)
    const f = fixture(ids, rows, CountingMap)
    const result = f.personOptions.value
    assert.equal(result.length, size + 1, 'all facets remain present without a limit')
    assert.deepEqual(result[1], { label: 'Same · 1', value: `person-${size - 1}` })
    assert.deepEqual(result.at(-1), { label: `Same · ${size}`, value: 'person-0' })
    assert.equal(idReads, size)
    assert.equal(mapOperations, size * 6)
    // Reading an unchanged computed value performs no extra directory traversal.
    assert.equal(f.personOptions.value, result)
    assert.equal(idReads, size)
    return { idReads, mapOperations }
  }
  const small = measure(2_000),
    large = measure(10_000)
  assert.equal(large.idReads, small.idReads * 5)
  assert.equal(large.mapOperations, small.mapOperations * 5)
})
