import Database from 'better-sqlite3'
import { writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { enroll, metadata, RecoveryAuthority, sameHead } from '../src/memory/recovery/ledger.ts'
export function enrollLegacy(database: string, authorityDirectory: string) {
  const db = new Database(database, { fileMustExist: true })
  try {
    const authority = enroll(db, database, authorityDirectory)
    const head = authority.head()
    authority.close()
    return head
  } finally {
    db.close()
  }
}
export async function exportRevocations(database: string, output: string) {
  const db = new Database(database, { readonly: true, fileMustExist: true })
  try {
    const gate = metadata(db)
    if (!gate) throw new Error('recovery_legacy_enrollment_required')
    const authority = new RecoveryAuthority(gate.authority)
    try {
      const head = authority.head()
      if (!sameHead(gate.head, head)) throw new Error('recovery_authority_head_changed')
      const entries = authority.entries()
      if (!sameHead(head, authority.head())) throw new Error('recovery_authority_head_changed')
      await writeFile(
        output,
        JSON.stringify({
          format: 1,
          head,
          entries,
          purpose: 'transport-only-not-freshness-authority',
        }),
        { flag: 'wx', mode: 0o600 },
      )
      return head
    } finally {
      authority.close()
    }
  } finally {
    db.close()
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [action, database, target, acknowledgment] = process.argv.slice(2)
  try {
    if (!database || !target) throw new Error('usage')
    if (action === 'enroll' && acknowledgment === '--current-live-database-not-a-backup')
      enrollLegacy(database, target)
    else if (action === 'export') await exportRevocations(database, target)
    else throw new Error('usage')
  } catch {
    console.error(
      'Recovery authority operation refused. Enroll only the current live database offline, never an old snapshot. Export is transport only; restore requires the independently retained authority directory.',
    )
    process.exitCode = 1
  }
}
