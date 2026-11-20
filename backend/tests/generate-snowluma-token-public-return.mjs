import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { readOneBotImage } from '../../src/plugins/builtin/snowluma/media.ts'
import { encode } from '../../src/plugins/builtin/snowluma/protocol.ts'

assert.equal(process.version, 'v24.22.1')
const scene = { adapterId: 'snowluma', accountId: '42', kind: 'group', platformSceneId: '99',
  id: 'ob:s:' + encode(['snowluma', '42', 'group', '99']) }
const asset = 'ob:media:' + encode({ sceneId: scene.id, type: 'image', file: 'ignored', fileId: 'opaque' })
const specs = [
  { name: 'url-over-inline-file', reply: { url: 'http://fixture.invalid/token/path?fixture=1', file: 'base64://YQ==' } },
  { name: 'file-only-public', reply: { file: 'https://fixture.invalid/file' } },
  { name: 'empty-url-file', reply: { url: '', file: 'http://fixture.invalid/fallback' } },
  { name: 'non-string-url-file', reply: { url: {}, file: 'http://fixture.invalid/typed-file' } },
  { name: 'rejected-url-no-inline-fallback', reply: { url: 'http://fixture.invalid/failed', file: 'base64://YQ==' }, fail: true },
  { name: 'invalid-url-no-file-fallback', reply: { url: 'file:///private', file: 'https://fixture.invalid/ignored' } },
  { name: 'returned-inline-unchanged', reply: { file: 'base64://YQ==' } },
  { name: 'cancel-between-action-and-fetch', reply: { url: 'http://fixture.invalid/cancelled' }, cancel: true },
]
const cases = []
for (const fixture of specs) {
  const abort = new AbortController()
  const actions = [], urls = []
  let bytes = '', error = ''
  try {
    const value = await readOneBotImage(scene, asset, async (action, params) => {
      actions.push({ action, ...params })
      if (fixture.cancel) abort.abort()
      return fixture.reply
    }, abort.signal, async locator => {
      urls.push(locator)
      if (fixture.fail) throw new Error('synthetic_fetch_failed')
      return Buffer.from('remote bytes')
    })
    bytes = value.toString()
  } catch (err) {
    error = err.message
  }
  assert.deepEqual(actions, [{ action: 'get_image', file: 'opaque' }])
  cases.push({ ...fixture, actions, urls, bytes, error })
}
const target = new URL('../plugins/snowluma/host/runtime/testdata/token-public-return-source.json', import.meta.url)
const serialized = JSON.stringify({ scene, asset, cases }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), serialized)
else writeFileSync(target, serialized)
console.log('PASS', cases.length, 'actual SnowLuma token public continuation cases')
