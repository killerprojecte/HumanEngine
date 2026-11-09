import { typingDelayMs } from '../../../src/plugins/builtin/typing-delay/policy.ts'
import { deepStrictEqual } from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// Fixture inputs are synthetic. Expected values are never used to compute results.
const mode = process.argv.slice(2)
if (mode.length !== 1 || !['--check', '--print'].includes(mode[0])) {
  throw new Error('Usage: node --import tsx backend/plugins/typing-delay/check-source.mjs --check|--print')
}
function fixture(name) {
  return JSON.parse(readFileSync(new URL(`./core/testdata/${name}`, import.meta.url), 'utf8'))
}
function verify(name, actual, expected) {
  if (mode[0] === '--check') {
    deepStrictEqual(actual, expected, `${name}: actual TypeScript source differs from committed fixture`)
    console.log(`PASS ${name}`)
  } else {
    // Print only: explicit review is required before replacing any committed fixture.
    console.log(JSON.stringify({ file: name, data: actual }, null, 2))
  }
}

const cases = fixture('node-delay.json')
verify('node-delay.json', cases.map(({ expected, ...input }) => ({
  ...input, expected: typingDelayMs(input.segment, input.config),
})), cases)
