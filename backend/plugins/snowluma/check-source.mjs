import { parseCqMessage } from '../../../src/plugins/builtin/snowluma/normalization.ts'
import { OneBotAdapter } from '../../../src/plugins/builtin/snowluma/transport.ts'
import { deepStrictEqual } from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// Fixture inputs are synthetic. Expected values are never used to compute results.
const mode = process.argv.slice(2)
if (mode.length !== 1 || !['--check', '--print'].includes(mode[0])) {
  throw new Error('Usage: node --import tsx backend/plugins/snowluma/check-source.mjs --check|--print')
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

const cases = fixture('node-cq.json')
verify('node-cq.json', cases.map(({ expected, ...input }) => ({
  ...input, expected: parseCqMessage(input.input),
})), cases)
const data = fixture('node-identifiers.json')
const { sceneId, messageId, userId, ...input } = data
// Construction is inert: never call connect/start or perform any network operation.
const adapter = new OneBotAdapter({
  id: input.adapter, accountId: input.account, url: 'ws://127.0.0.1:1',
})
try {
  const scene = adapter.scene(input.kind, input.peer)
  verify('node-identifiers.json', {
    ...input,
    sceneId: scene.id,
    messageId: adapter.messageId(scene, input.platform),
    userId: adapter.userId(input.platform),
  }, data)
} finally {
  adapter.dispose()
}
