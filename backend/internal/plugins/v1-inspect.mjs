// Explicit management only: no setup and no shared runtime host.
import { pathToFileURL } from 'node:url'
import { registerHooks } from 'node:module'
// Resolve only deployment-supplied exact module URLs, never a basename or suffix.
// All aliases and the bridge itself load this one source-derived SDK instance.
const sdkURL = 'humanengine:v1-model-protocols'
const sdkAliases = new Set(
  (inspectionSDK.modelProtocolModuleURLs ?? []).map((url) => new URL(url).href),
)
registerHooks({
  resolve(specifier, context, nextResolve) {
    let candidate
    try {
      candidate = /^(?:\.{1,2}\/|\/)/.test(specifier)
        ? new URL(specifier, context.parentURL).href
        : new URL(specifier).href
    } catch {}
    if (specifier === sdkURL || sdkAliases.has(candidate))
      return { url: sdkURL, shortCircuit: true }
    const resolved = nextResolve(specifier, context)
    return sdkAliases.has(resolved.url) ? { url: sdkURL, shortCircuit: true } : resolved
  },
  load(url, context, nextLoad) {
    return url === sdkURL
      ? {
          format: 'module',
          source: inspectionSDK.modelProtocolSDK,
          shortCircuit: true,
        }
      : nextLoad(url, context)
  },
})

console.log = (...args) => console.error(...args)
let text = ''
for await (const chunk of process.stdin) text += chunk
let result
try {
 const input = JSON.parse(text)
 const module = await import(pathToFileURL(input.path).href)
 result = inspectPluginConfiguration(module.default ?? module.plugin,input)
} catch { result={failed:true,code:'plugin_setup_failed'} }
process.stdout.write(JSON.stringify(result)+'\n',()=>process.exit(0))
