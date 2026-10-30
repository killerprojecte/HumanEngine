import Database from 'better-sqlite3'
import { writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { enroll, metadata, RecoveryAuthority, sameHead } from '../src/memory/recovery/ledger.ts'
export function enrollLegacy(database: string, authorityDirectory: string) {
  const db = new Database(database, { fileMustExist: true })
  try {
    const authority = enroll(db, database, authorityDirectory)
    try {
      return authority.head()
    } finally {
      authority.close()
    }
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
const refusalHints: Record<string, string> = {
  usage:
    'Use enroll DATABASE AUTHORITY_DIRECTORY --current-live-database-not-a-backup, or export DATABASE NEW_EXPORT_FILE. Stop all writers before enrollment; never enroll a backup.',
  recovery_already_enrolled:
    'This database is already enrolled. Retain its originally bound current authority; do not re-enroll it or replace missing authority with a new one.',
  recovery_enrollment_transaction_active:
    'Run enrollment as a standalone offline operation after stopping all writers, not inside another database transaction.',
  recovery_legacy_enrollment_required:
    'Stop all writers and deliberately enroll only the current live database, never an old snapshot.',
  recovery_existing_authority_requires_reconciliation:
    'Preserve the existing authority and database. Resolve custody or an interrupted enrollment offline; do not delete authority to retry.',
  recovery_authority_missing:
    'Restore access to the originally bound current authority at the same absolute path. Do not create a replacement authority.',
  recovery_authority_head_changed:
    'Preserve the current database and independent authority. Reconcile offline; do not reset their heads or enroll an older copy.',
  recovery_authority_not_independent:
    'Use a separate persistent authority directory outside the data directory.',
  recovery_authority_path_required: 'Supply the independently retained authority directory.',
  recovery_symlink_denied: 'Use direct, independently retained paths without symbolic links.',
  recovery_gate_invalid:
    'Keep the database stopped and preserve it for recovery review. Do not delete or recreate its recovery gate.',
  recovery_authority_invalid:
    'Keep the database stopped and preserve the original authority for recovery review.',
  recovery_chain_invalid:
    'Keep the database stopped and preserve the original authority for recovery review.',
  recovery_format_unsupported:
    'Use a compatible participating recovery reader. Do not downgrade or reset the authority.',
  recovery_quarantined_or_unsupported:
    'Keep the database stopped and use the documented recovery procedure; do not enroll it again.',
}
function refusal(error: unknown): string {
  const message = error instanceof Error ? error.message : ''
  if (Object.hasOwn(refusalHints, message)) return `${message}: ${refusalHints[message]}`
  const code = (error as NodeJS.ErrnoException | undefined)?.code
  if (code === 'SQLITE_BUSY' || code === 'SQLITE_LOCKED')
    return 'recovery_database_busy: Keep the service stopped and stop all other database writers before retrying. Do not disable locking.'
  if (code === 'SQLITE_READONLY' || code === 'SQLITE_CANTOPEN')
    return 'recovery_database_unavailable: Keep the service stopped. Verify the current live volume, mount paths and write permissions without exposing its contents.'
  // SQLite and filesystem messages can embed private paths or database bytes.
  // Unknown errors are deliberately not echoed, including their stack/cause.
  return 'recovery_operation_failed: Keep the database stopped. Verify the current live database, independent authority custody, permissions and exclusive offline access; see docs/recovery-authority.md. Do not delete either store to retry.'
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [action, database, target, acknowledgment, ...extra] = process.argv.slice(2)
  try {
    if (!database || !target || extra.length) throw new Error('usage')
    if (action === 'enroll' && acknowledgment === '--current-live-database-not-a-backup') {
      enrollLegacy(database, target)
      console.log(
        'Recovery authority enrollment completed. Retain the independent authority before restarting the service.',
      )
    } else if (action === 'export' && !acknowledgment) {
      await exportRevocations(database, target)
      console.log('Recovery metadata exported. This transport record is not proof of freshness.')
    } else throw new Error('usage')
  } catch (error) {
    console.error(`Recovery authority operation refused. ${refusal(error)}`)
    process.exitCode = 1
  }
}
