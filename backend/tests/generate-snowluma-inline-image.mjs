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
const serialized = JSON.stringify({ scene, cases }, null, 2) + '\n'
const path = new URL('../plugins/snowluma/host/runtime/testdata/inline-image-source.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (fs.readFileSync(path, 'utf8') !== serialized) process.exitCode = 1
} else fs.writeFileSync(path, serialized)
