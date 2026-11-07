/** Synthetic benchmark instrumentation only. Never loaded by production workers. */
import { parentPort } from 'node:worker_threads'
import { register } from 'tsx/esm/api'
register()
const Database = (await import('better-sqlite3')).default
const { ConfigReadConnection } = await import('../src/server/config-read.ts')
const emit = (event, detail) => parentPort.postMessage({ publicationProbe: { event, detail } })
const prepare = Database.prototype.prepare
Database.prototype.prepare = function (sql) {
  const statement = prepare.call(this, sql)
  if (this.readonly && sql === 'SELECT * FROM main.revocations ORDER BY sequence') {
    const iterate = statement.iterate
    statement.iterate = function (...args) {
      emit('fullScan')
      return iterate.apply(this, args)
    }
  }
  return statement
}
for (const name of ['beginPublication', 'verifyPublication', 'abandonPublication']) {
  const method = ConfigReadConnection.prototype[name]
  ConfigReadConnection.prototype[name] = function (...args) {
    emit(name)
    try {
      return method.apply(this, args)
    } catch (error) {
      emit('failure', { operation: name, reason: error.code ?? error.message })
      throw error
    }
  }
}
