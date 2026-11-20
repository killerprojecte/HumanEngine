import fs from 'node:fs'
import { readOneBotImage } from '../../src/plugins/builtin/snowluma/media.ts'
import { encode } from '../../src/plugins/builtin/snowluma/protocol.ts'

const scene = {
  adapterId: 'snowluma',
  accountId: '42',
  kind: 'group',
  platformSceneId: '99',
  id: 'ob:s:' + encode(['snowluma', '42', 'group', '99']),
}
const cases = []
for (const [name, file] of [
  ['original', 'base64://AAH+/w=='],
  ['pad-bits', 'base64://YR=='],
  ['data-case', 'DATA:IMAGE/WebP;BASE64,YQ=='],
  ['data-jpeg', 'data:image/jpeg;base64,YQ=='],
  ['data-png', 'data:image/png;base64,YQ=='],
  ['data-gif', 'data:image/gif;base64,YQ=='],
  ['newline', 'base64://YQ==\n'],
  ['space', 'base64:// YQ='],
  ['misplaced-padding', 'base64://YQ=A'],
  ['length', 'base64://YQ'],
  ['empty', 'base64://'],
  ['url-alphabet', 'base64://AAH-_w=='],
]) {
  const asset = 'ob:media:' + encode({ sceneId: scene.id, type: 'image', file })
  const unexpected = () => {
    throw new Error('fixture_unexpected_io')
  }
  try {
    const bytes = await readOneBotImage(scene, asset, unexpected, undefined, unexpected)
    cases.push({ name, asset, bytes: bytes.toString('base64') })
  } catch (err) {
    if (err.message === 'fixture_unexpected_io') throw err
    cases.push({ name, asset, error: err.message })
  }
}
// The inline/Vision path never opens a selected public locator. Browser
// continuation has a separate source oracle with the same reply precedence.
const tokenCases = []
for (const [name, file, fileId, reply, goLimited] of [
  ['token-file-inline', 'opaque', undefined, { file: 'base64://YQ==' }],
  ['explicit-token', 'secondary', 'opaque', { url: '', file: 'data:image/png;base64,YQ==' }],
  ['inline-url-wins', 'opaque', undefined, { url: 'DATA:image/JPEG;base64,Yg==', file: 'base64://YQ==' }],
  ['public-url-wins', 'opaque', undefined, { url: 'https://fixture.invalid/fresh', file: 'base64://YQ==' }, 'no-public-fetch'],
  ['local-return-denied', 'opaque', undefined, { file: 'C:\fixture\image.png' }],
  ['non-object-return', 'opaque', undefined, null],
  ['malformed-inline-return', 'opaque', undefined, { file: 'base64://YQ' }],
  ['direct-first-refresh', 'https://fixture.invalid/expired', 'opaque', { file: 'base64://Yw==' }, 'no-refresh'],
  ['file-path-input', '/fixture/nonlocal.png', undefined, { file: 'base64://YQ==' }, 'no-path-token'],
]) {
  const asset = 'ob:media:' + encode({ sceneId: scene.id, type: 'image', file, fileId })
  const actions = [], urls = []
  const entry = { name, asset, reply, actions, urls }
  if (goLimited) entry.goLimited = goLimited
  try {
    const bytes = await readOneBotImage(scene, asset, async (action, params) => {
      actions.push({ action, file: params.file })
      return reply
    }, undefined, async (url) => {
      urls.push(url)
      if (url.endsWith('/expired')) throw new Error('media_fetch_failed')
      return Buffer.from('c')
    })
    entry.bytes = bytes.toString('base64')
  } catch (err) {
    entry.error = err.message
  }
  tokenCases.push(entry)
}
const serialized = JSON.stringify({ scene, cases, tokenCases }, null, 2) + '\n'
const path = new URL('../plugins/snowluma/host/runtime/testdata/inline-image-source.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (fs.readFileSync(path, 'utf8') !== serialized) process.exitCode = 1
} else fs.writeFileSync(path, serialized)
