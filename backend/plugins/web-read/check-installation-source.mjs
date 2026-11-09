import { WebReadConfigSchema, webReadPlugin } from '../../../src/plugins/builtin/web-read.ts'
import { validateConfig } from '../../../src/plugins/config.ts'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { deepStrictEqual } from 'node:assert/strict'

const inputs = [null, [], {}, { timeoutMs: 499 }, { timeoutMs: 500 }, { timeoutMs: 2147483647 }, { timeoutMs: 2147483648 }, { timeoutMs: 1.5 }, { timeoutMs: '60000' }, { timeoutMs: null }, { maxChars: 255 }, { maxChars: 256 }, { maxChars: 30000 }, { maxChars: 30001 }, { maxConcurrency: 0 }, { maxConcurrency: 1 }, { maxConcurrency: 64 }, { maxConcurrency: 65 }, { maxResponseBytes: 0 }, { maxResponseBytes: 9007199254740991 }, { maxResponseBytes: 9007199254740992 }, { extra: true }, { maxChars: 512, maxConcurrency: 1, timeoutMs: 500, maxResponseBytes: 0 }]
const cases = inputs.map(config => {
  const result = WebReadConfigSchema.safeParse(config)
  return { operation: 'configurationValidate', config, expected: result.success ? { valid: true, config: result.data } : { valid: false } }
})
const definitions = []
let cleanup
webReadPlugin().setup({ config: {}, signal: new AbortController().signal, onDispose(f) { cleanup = f }, registerTool({ execute, ...definition }) { definitions.push(definition) } })
await cleanup()
deepStrictEqual(definitions.map(({name, effect}) => ({name, effect})), [{name:'web-read.read',effect:'read'},{name:'web-read.request',effect:'read'},{name:'web-read.send',effect:'learningCandidate'}])
const toolCases = []
for (const tool of definitions) {
  const inputs = [null, {}, {url:'x'}, {url:'x',method:'POST'}, {url:'x',method:'GET'}, {url:'x',headers:{}}, {url:'x',headers:{accept:'text/plain'}}, {url:'x',extra:1}, {url:'x',bodyEncoding:'invalid'}, {url:'x',maxRedirects:1.5}]
  for (const input of inputs) {
    let expected
    try { validateConfig(input, tool.inputSchema); expected = {valid:true} }
    catch(error) { expected = {error:error.message} }
    toolCases.push({operation:'toolValidate',tool:tool.name,input,expected})
  }
}
const output = { cases, toolCases, definitions }
const destination = process.argv[2]
if (!destination) throw new Error('Pass an explicit output fixture path')
mkdirSync(dirname(destination), { recursive: true })
writeFileSync(destination, JSON.stringify(output, null, 2) + '\n')
console.log(`PASS actual source installation contract: ${cases.length} configurations and ${definitions.length} tools, ${toolCases.length} manager input cases`)
