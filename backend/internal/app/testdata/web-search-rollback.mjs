import assert from 'node:assert/strict'
import { join } from 'node:path'
import { Store } from '../../../../src/store.ts'
import { Runtime } from '../../../../src/runtime.ts'
import { PluginManager } from '../../../../src/plugins/manager.ts'
import { webSearchPlugin } from '../../../../src/plugins/builtin/web-search/index.ts'

const [operation, directory, scenario] = process.argv.slice(2)
const routing = {
  tavily: { provider: 'tavily', fallbackProvider: 'none', allowExperimentalHtml: false },
  bing: { provider: 'bing', fallbackProvider: 'tavily', allowExperimentalHtml: true },
  implicit: { allowExperimentalHtml: true },
  fallback: { provider: 'tavily', fallbackProvider: 'bing', allowExperimentalHtml: true },
}[scenario]
assert(routing && ['seed', 'rollback'].includes(operation))
const settings = {
  ...routing,
  maxResults: 3,
  timeoutMs: 8500,
  maxResponseBytes: 16384,
  maxConcurrency: 1,
}
const id = 'web-search'
const namespace = 'plugin:' + id
const key = 'synthetic-search-rollback-key'
process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = join(directory, 'authority')
const store = new Store(join(directory, 'live', 'main.sqlite'))
const runtime = new Runtime(store, () => {
  throw new Error('no model fixture expected')
})
const manager = new PluginManager(store, runtime)
const attach = runtime.addExtension.bind(runtime)
let search
runtime.addExtension = (extension) => {
  if (extension.tools?.some((tool) => tool.name === 'web-search.search')) search = extension
  return attach(extension)
}
const originHits = []
try {
  await manager.register(
    webSearchPlugin(store, async (url, init) => {
      originHits.push(url.hostname)
      if (url.hostname === 'api.tavily.com') {
        assert.equal(new Headers(init.headers).get('Authorization'), 'Bearer ' + key)
        return new Response('{"results":[]}', { headers: { 'content-type': 'application/json' } })
      }
      assert.equal(url.hostname, 'www.bing.com')
      assert.equal(new Headers(init.headers).get('Authorization'), null)
      return new Response('<html><body>No results found.</body></html>', {
        headers: { 'content-type': 'text/html' },
      })
    }),
    { builtin: true },
  )
  if (operation === 'seed') await manager.configure(id, { ...settings, tavilyKey: key })
  else
    assert.equal(store.get('plugin-enabled:' + id, false), true, 'Go rewrote saved enabled intent')
  assert.deepEqual(manager.config(id), settings)
  assert.equal(store.secrets.get(namespace, 'tavilyKey'), key)

  await manager.enable(id)
  assert.equal(manager.list().find((item) => item.id === id).enabled, true)
  assert.equal(manager.list().find((item) => item.id === id).configuredSecrets.tavilyKey, true)
  assert.equal(store.get('plugin-enabled:' + id, false), true)
  assert(search)
  const result = await search.execute(
    'web-search.search',
    { query: 'public fixture' },
    {
      scene: {
        id: 'fixture',
        kind: 'group',
        adapterId: 'fixture',
        accountId: 'bot',
        platformSceneId: 'fixture',
      },
      mode: 'real',
      turnId: 'fixture',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    },
  )
  const provider = routing.provider ?? 'bing'
  assert.deepEqual(originHits, [provider === 'tavily' ? 'api.tavily.com' : 'www.bing.com'])
  assert.equal(result.status, 'no_results')
  assert.deepEqual(result.attemptedProviders, [provider])
  console.log('search-rollback-source-' + operation + '-' + scenario)
} finally {
  await manager.dispose()
  runtime.dispose()
  store.close()
}
