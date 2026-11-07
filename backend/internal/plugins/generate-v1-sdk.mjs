/** Source-derived V1 SDK and retry branch. Run with Node 24.22.1:
 * node --import tsx backend/internal/plugins/generate-v1-sdk.mjs [--check]
 */
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import ts from 'typescript'
import { format } from 'prettier'
const root = new URL('../../../', import.meta.url)
const hashes = {
  'src/model-protocols.ts': '95e45d005e786fcd2ef957354ffe7608cb5d827bc7e16cca5344a7905b193222',
  'src/models.ts': '61293616cd4cd978f44bc80ccfafe8ec95a1254d6e575ddb184bd10fcf91c8ca',
  'src/models/request-policy.ts':
    '55f0080b2cd64ba68fd207bfe94cab162f10cd3d9f34d819094a39a85890a63d',
}
if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
const source = (path) => {
  const value = readFileSync(new URL(path, root), 'utf8')
  if (createHash('sha256').update(value).digest('hex') !== hashes[path])
    throw Error('Review changed V1 oracle: ' + path)
  return value
}
const emit = async (name, contents) => {
  if (name.endsWith('.mjs') || name.endsWith('.json'))
    contents = await format(contents, {
      parser: name.endsWith('.json') ? 'json' : 'babel',
      semi: false,
      singleQuote: true,
      printWidth: 100,
    })
  const url = new URL(name, import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(url, 'utf8') !== contents) throw Error('Regenerate ' + name)
  } else writeFileSync(url, contents)
}
const transpile = (value) =>
  ts.transpileModule(value, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
  }).outputText
const sdk = source('src/model-protocols.ts')
const models = source('src/models.ts')
source('src/models/request-policy.ts')
const ast = ts.createSourceFile('models.ts', models, ts.ScriptTarget.Latest, true)
let branch, admission
const visit = (node) => {
  if (
    ts.isIfStatement(node) &&
    node.expression.getText(ast) === 'error instanceof ModelProtocolError'
  ) {
    if (branch) throw Error('Ambiguous protocol error oracle')
    branch = node.thenStatement.getText(ast)
  }
  if (ts.isFunctionDeclaration(node) && node.name?.text === 'isReadAdmissionCode')
    admission = node.getText(ast)
  ts.forEachChild(node, visit)
}
visit(ast)
if (!branch || !admission || !branch.includes('throw new ModelError('))
  throw Error('Missing protocol error oracle')
await emit(
  'v1-model-protocol-sdk.mjs',
  '// Generated from src/model-protocols.ts; SHA-256 ' +
    hashes['src/model-protocols.ts'] +
    '\n' +
    transpile(sdk),
)
// The copied branch preserves JS evaluation order, repeated kind getter reads,
// strict equality/coercion, subclasses and runtime field mutation. It returns
// only a local snapshot; the worker sanitizes it before transport.
await emit(
  'v1-model-error-classify.mjs',
  '// Generated from src/models.ts; SHA-256 ' +
    hashes['src/models.ts'] +
    '\n' +
    transpile(`
${admission}
function captureProtocolFailure(code, status, retryable, options) {
  return { code, status, retryable, retryAfterMs: options.retryAfterMs }
}
function classifyProtocolFailure(error) {
  try {
    // invokeProtocol reads Error.message before checking the SDK constructor.
    const readCode = error instanceof Error ? error.message : ''
    if (!(error instanceof ModelProtocolError)) return undefined
    if (isReadAdmissionCode(readCode)) return { code: readCode, retryable: false }
    ${branch.replace('throw new ModelError(', 'return captureProtocolFailure(')}
  } catch {
    // Accessor/coercion failures escape the source catch and never grant retry.
    return undefined
  }
}
`),
)

// Invoke the actual Router and SDK, not the generated classifier, as the oracle.
const { Router, ModelError } = await import(new URL('src/models.ts', root))
const { ModelProtocolError, ModelProtocolRegistry } = await import(
  new URL('src/model-protocols.ts', root)
)
const { retryDelay } = await import(new URL('src/models/request-policy.ts', root))
const { modes, makeFailure } = await import('./testdata/v1-error-cases.mjs')
const provider = {
  id: 'fixture',
  protocol: 'fixture:custom',
  baseUrl: 'http://unused.invalid',
  credentialEnv: '',
}
const model = {
  id: 'fixture',
  providerId: provider.id,
  apiModelName: 'fixture',
  capabilities: ['text'],
}
const rows = []
for (const mode of modes) {
  const reads = []
  const registry = new ModelProtocolRegistry()
  const lifecycle = new AbortController()
  registry.register(
    provider.protocol,
    'Synthetic fixture',
    {
      complete() {
        throw makeFailure(ModelProtocolError, mode, reads)
      },
    },
    lifecycle.signal,
  )
  const router = new Router(
    { providers: [provider], models: [model], routes: [] },
    undefined,
    undefined,
    registry,
  )
  try {
    await router.invokeProtocol(
      provider,
      model,
      'complete',
      30000,
      0,
      undefined,
      (context, registration) => registration.adapter.complete(context, [], []),
    )
    throw Error('Expected failure')
  } catch (error) {
    rows.push({
      mode,
      reads,
      classified: error instanceof ModelError,
      retryable: error instanceof ModelError && error.retryable,
      ...(error instanceof ModelError
        ? {
            code: error.code,
            ...(error.status !== undefined ? { status: error.status } : {}),
            delay: retryDelay(1, error.retryAfterMs),
          }
        : {}),
    })
  } finally {
    lifecycle.abort()
  }
}
await emit(
  'testdata/v1-model-errors.json',
  JSON.stringify({ sources: hashes, cases: rows }, null, 2) + '\n',
)
