// Executes actual TypeScript installAdapter; no bridge implementation is the oracle.
// node --import tsx backend/internal/plugins/generate-v1-adapter-fixture.mjs [--check]
import { readFileSync, writeFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { createHash } from 'node:crypto'
import fixture from './testdata/adapter-fixture.mjs'
const sourceRoot = process.env.V1_SOURCE_ROOT
  ? pathToFileURL(process.env.V1_SOURCE_ROOT + '/')
  : new URL('../../../', import.meta.url)
const sourceURL = new URL('src/plugins/adapters.ts', sourceRoot)
if (
  createHash('sha256').update(readFileSync(sourceURL)).digest('hex') !==
  '02c38bed5d0065397f815b999666fdde504806377561bb61f9d115b47bf022c9'
)
  throw Error('Review adapter source oracle')
const { installAdapter } = await import(sourceURL.href)
const actions = new Map(),
  definitions = [],
  cleanup = [],
  received = [],
  recalled = new Set(),
  adapters = new Map()
const controller = new AbortController()
await fixture.setup({
  config: {},
  signal: controller.signal,
  registerAdminAction: (name, action) => actions.set(name, action),
  registerAdapter: (name, factory) => definitions.push({ name, factory }),
  onDispose: (fn) => cleanup.push(fn),
})
for (const { name, factory } of definitions) {
  const installed = installAdapter(
    fixture.manifest.id + ':' + name,
    factory,
    {
      addAdapter(id, adapter) {
        adapters.set(id, adapter)
      },
      removeAdapter(id) {
        const adapter = adapters.get(id)
        adapters.delete(id)
        adapter?.dispose()
      },
      ingest(message, scene) {
        received.push(message)
      },
      edit() {
        return {
          status: 'edited',
          bodyCoverage: 'unavailable',
          ref: { eventId: 'synthetic-edit', revision: 2, deletionEpoch: 0 },
          previousRef: {
            eventId: 'synthetic-event',
            revision: 1,
            deletionEpoch: 0,
          },
        }
      },
      recall(scene, message) {
        recalled.add(scene + '/' + message)
      },
    },
    {
      recalled(scene, message) {
        return recalled.has(scene + '/' + message)
      },
    },
    controller.signal,
    {
      media(assetId) {
        return { file: 'synthetic:' + assetId, type: 'image/png' }
      },
    },
  )
  cleanup.push(installed.dispose)
  await installed.start()
}
const adapter = adapters.values().next().value
const { scene, message } = await actions.get('context')('scene')
const result = {
  scene,
  message,
  received: [...received],
  status: adapter.status,
  invalid: await actions.get('context')('invalid'),
  media: await actions.get('context')('media'),
  edit: await actions.get('context')('edit'),
  readImage: typeof adapter.readImage,
}
result.sends = {}
for (const mode of ['ok', 'throw', 'invalid', 'private-detail'])
  result.sends[mode] = await adapter.send(scene, {
    parts: [{ type: 'text', text: mode }],
  })
result.history = await adapter.history(scene, { limit: 10 })
result.getMessage = await adapter.getMessage(scene, message.id)
result.recalled = await actions.get('context')('recall')
result.afterRecall = await adapter.getMessage(scene, message.id)
controller.abort('plugin_disabled')
for (const fn of cleanup.reverse()) await fn()
result.trace = await actions.get('inspect')()
const json = JSON.stringify(result, null, 2) + '\n'
const output = new URL('testdata/adapter-source-fixture.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(output, 'utf8') !== json) throw Error('Regenerate adapter source fixture')
} else writeFileSync(output, json)
