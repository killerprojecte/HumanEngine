import { register } from 'tsx/esm/api'
import { workerData } from 'node:worker_threads'
import Database from 'better-sqlite3'

register()
const { RecoveryAuthority, metadata, saveGate } =
  await import('../../src/memory/recovery/ledger.ts')
const main = new Database(workerData.database)
const authority = new RecoveryAuthority(metadata(main).authority)
authority.db.pragma('busy_timeout=0')
const flags = new Int32Array(workerData.flags)
try {
  Atomics.store(flags, 0, 1)
  Atomics.notify(flags, 0)
  for (const [request, result] of [
    [1, 2],
    [3, 4],
  ]) {
    if (Atomics.wait(flags, request, 0, 10000) === 'timed-out') break
    let outcome = 1
    try {
      main
        .transaction(() => {
          const gate = metadata(main)
          // BEGIN IMMEDIATE inside append must be denied before any authority
          // write or PENDING commit while the reader actor holds its reservation.
          const head = authority.append(
            { kind: 'source', sceneId: 'synthetic', messageId: 'concurrent-writer' },
            gate.head,
          )
          const row = main.prepare("SELECT value FROM config WHERE key='settings'").get()
          main
            .prepare("UPDATE config SET value=? WHERE key='settings'")
            .run(JSON.stringify({ ...JSON.parse(row.value), name: 'After concurrent writer' }))
          saveGate(main, { ...gate, head })
        })
        .immediate()
    } catch (error) {
      outcome = error.code === 'SQLITE_BUSY' ? 2 : -1
    }
    Atomics.store(flags, result, outcome)
    Atomics.notify(flags, result)
  }
} finally {
  authority.close()
  main.close()
}
