import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'

const support = (tag) => {
  try {
    return Intl.getCanonicalLocales(tag).length === 1 && Intl.Collator.supportedLocalesOf(tag).length === 1
  } catch {
    return false
  }
}
const twoLetter = []
for (const a of 'abcdefghijklmnopqrstuvwxyz')
  for (const b of 'abcdefghijklmnopqrstuvwxyz')
    if (support(a + b)) twoLetter.push(a + b)

const tags = [
  '', 'zz', 'und', 'x-private', 'i-klingon', 'zh-yue', 'not_a_locale',
  'en-', 'en-u', 'en-u-kn-u-co-emoji', 'en\0US', 'en-US-en-US', 'en_US',
  'fil', 'iw', 'in', 'ji', 'jw', 'mo', 'tl', 'sh', 'no', 'nb-NO', 'nn',
  'EN-us', 'en-001', 'en-US-x-private-u-kn', 'en-US-u-kn-x-private',
  'en-u-kn', 'en-u-kn-false', 'en-u-kn-invalid', 'en-u-kf-upper',
  'en-u-kf-lower', 'en-u-kf-invalid', 'en-u-co-emoji', 'en-u-co-search',
  'de-DE-u-co-phonebk', 'es-u-co-trad', 'sv-SE-u-co-standard',
  'th-TH', 'zh-Hans-CN', 'zh-Hant-TW', 'zh-TW-u-co-stroke', 'yue-Hant-HK',
  'ku-Arab-IQ', 'pa-PK', 'sr-RS', 'sr-Latn-RS', 'az-AZ', 'az-Latn-AZ',
  'az-Cyrl-AZ', 'lt-LT', 'tr-Latn-TR', 'el-GR', 'kk-KZ', 'haw-US',
]
const input = ['I', 'İ', 'I\u0307', 'I\u0301', 'ΟΔΥΣΣΕΎΣ', 'A\0B', '', 'Straße']
const sorted = ['2', '10', 'ä', 'ae', 'a', 'A', 'å', 'Å', 'z', '', 'U\0x', 'é', 'e\u0301', 'ch', 'cz', '😀']
const cases = tags.map((tag) => support(tag) ? {
  tag, lower: input.map((item) => item.toLocaleLowerCase(tag)),
  sorted: [...sorted].sort((a, b) => a.localeCompare(b, tag)),
} : { tag })
const dest = new URL('../internal/topiccollation/testdata/requested-locale.json', import.meta.url)
const out = JSON.stringify({ node: process.version, input, sorted, twoLetter, cases }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), out)
else writeFileSync(dest, out)
console.log(`PASS ${cases.length} requested locale cases plus 676 two-letter tags`)
