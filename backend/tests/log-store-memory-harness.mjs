/** Actual Store logging methods, extracted by AST without rewriting their bodies.
 * Only DB construction/authority/diagnostic cleanup, clock, UUID, flow context,
 * and the acquireLogChannel boundary are replaced. No real worker/fd is created.
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { dirname, join } from 'node:path'
import vm from 'node:vm'
import ts from 'typescript'
import { readableLog, stdoutLog } from '../../src/diagnostics/format.ts'
import { canonicalModelRole } from '../../src/model-roles.ts'

const root = new URL('../../', import.meta.url)
const text = readFileSync(new URL('src/store.ts', root), 'utf8')
const file = ts.createSourceFile('store.ts', text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
const store = file.statements.find(n => ts.isClassDeclaration(n) && n.name?.text === 'Store')
assert(store, 'Store declaration changed')
const fields = ['logs', 'logDirectory', 'fileLogs', 'stdoutLogs', 'loggingClosed',
  'loggingShutdownComplete', 'closedLogRejections', 'logBytes', 'retainedLogBytes',
  'evictedLogs', 'oversizedLogs', 'unserializableLogs', 'loggingNoticeAt', 'loggingInitializationFailure']
const methods = ['log', 'retainLog', 'loggingNotice', 'loggingStatus', 'closeLogs', 'close']
const selected = [...fields, ...methods].map(name => {
  const nodes = store.members.filter(n => n.name?.getText(file) === name)
  assert.equal(nodes.length, 1, `source member ${name} changed`)
  return nodes[0].getText(file)
})
const ctor = store.members.find(ts.isConstructorDeclaration)
const constructorStatements = ctor.body.statements.filter(n =>
  n.getText(file).startsWith('this.stdoutLogs =') || n.getText(file).includes('this.logDirectory ='))
assert.equal(constructorStatements.length, 2, 'constructor logging seams changed')
const sourceSlice = `class Store {\n${selected.join('\n')}\nconstructor(${ctor.parameters.map(n => n.getText(file)).join(',')}) {\n${constructorStatements.map(n => n.getText(file)).join('\n')}\n}\n}`
const compiled = ts.transpileModule(sourceSlice, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  reportDiagnostics: true,
})
assert.equal(compiled.diagnostics?.length ?? 0, 0)
export const sourceHashes = Object.fromEntries(['src/store.ts', 'src/diagnostics/format.ts',
  'src/model-roles.ts', 'src/diagnostics/log-writer.ts',
  ...['zh-CN', 'presentation-zh-CN', 'errors-zh-CN'].map(n => `src/diagnostics/catalogs/${n}.json`),
].map(p => [p, createHash('sha256').update(readFileSync(new URL(p, root))).digest('hex')]))
export const sliceHash = createHash('sha256').update(sourceSlice).digest('hex')
export const fixedID = '11111111-1111-4111-8111-111111111111'
export const fixedAt = '2026-11-09T00:00:00.123Z'
export function sourceStore({ acquire, path, env = {}, stdout, flow = {}, operations = [] }) {
  let now = Date.parse(fixedAt)
  class ControlledDate extends Date {
    constructor(...args) { super(...(args.length ? args : [now])) }
    static now() { return now }
  }
  const context = { Buffer, Date: ControlledDate, randomUUID: () => fixedID,
    currentFlowContext: () => flow, canonicalModelRole, readableLog, stdoutLog,
    acquireLogChannel: acquire, process: { env }, dirname, join }
  const ActualStore = vm.runInNewContext(`${compiled.outputText}\nStore`, context)
  const instance = new ActualStore(path, undefined, stdout ? { stdout } : undefined)
  instance.clearDiagnostics = () => operations.push('clear-diagnostics')
  instance.recoveryAuthority = { close() { operations.push('close-authority') } }
  instance.db = { close() { operations.push('close-db') } }
  return { store: instance, advance: millis => { now += millis }, directory: () => instance.logDirectory ?? null }
}
