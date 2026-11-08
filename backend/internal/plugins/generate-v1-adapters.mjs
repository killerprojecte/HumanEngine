// Source-owned validation, synchronous context semantics and send settlement.
// Run using pinned Node 24.22.1; --check verifies the embedded artifact.
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import ts from 'typescript'
import { format } from 'prettier'
if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
const root = new URL('../../../', import.meta.url)
const hashes = {
  'src/plugins/adapters.ts': '02c38bed5d0065397f815b999666fdde504806377561bb61f9d115b47bf022c9',
  'src/runtime/dispatch.ts': 'f3f40c29491a6cc637327045537943d95784020a6fac4be13f1327ff6b92d9a2',
  'src/diagnostics/error-context.ts':
    '4319a3cedac5fe015e4cbdea37db23aad915a035edf4304a9abb3e4320540a25',
  'src/diagnostics/error-codes.ts':
    '6672d79271e8b65cc7e9ebf98baa8950eec1c7631d6913f79f7dc43098427529',
  'src/budget/errors.ts': '327292e444f7b4347f27da14ea30980e090d68c6f2905cda3df3225bf14fecbd',
}
const sources = Object.fromEntries(
  Object.entries(hashes).map(([path, hash]) => {
    const text = readFileSync(new URL(path, root), 'utf8')
    if (createHash('sha256').update(text).digest('hex') !== hash)
      throw Error('Review changed adapter oracle: ' + path)
    return [path, text]
  }),
)
const transpile = (text) =>
  ts
    .transpileModule(text, {
      compilerOptions: {
        target: ts.ScriptTarget.ES2022,
        module: ts.ModuleKind.ES2022,
      },
    })
    .outputText.replace(/^import .*?;\n/gm, '')
    .replace(/export /g, '')
const dispatch = ts.createSourceFile(
  'dispatch.ts',
  sources['src/runtime/dispatch.ts'],
  ts.ScriptTarget.Latest,
  true,
)
const stage = dispatch.statements.find(
  (node) => ts.isFunctionDeclaration(node) && node.name?.text === 'awaitSendStage',
)
if (!stage) throw Error('awaitSendStage oracle missing')
let code =
  '// Generated from pinned TypeScript sources by generate-v1-adapters.mjs.\n' +
  "import { createHash } from 'node:crypto'\n" +
  ['src/budget/errors.ts', 'src/diagnostics/error-context.ts', 'src/diagnostics/error-codes.ts']
    .map((path) => transpile(sources[path]))
    .join('\n') +
  '\n' +
  transpile(stage.getText(dispatch)) +
  '\nconst abortContainedAdapter = (adapter) => adapter\n' +
  transpile(sources['src/plugins/adapters.ts'])
code = await format(code, {
  parser: 'babel',
  semi: false,
  singleQuote: true,
  printWidth: 100,
})
const target = new URL('v1-adapter-sdk.mjs', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== code) throw Error('Regenerate v1-adapter-sdk.mjs')
} else writeFileSync(target, code)
