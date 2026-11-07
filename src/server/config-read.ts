import Database from 'better-sqlite3'
import { join } from 'node:path'
import { defaultSettingsInput, settingsReadValue, modelsReadValue } from '../config-read-values.ts'
import {
  assertRecoveryReady,
  metadata,
  openReadOnlyGate,
  type Head,
  type RecoveryAuthority,
} from '../memory/recovery/ledger.ts'

export const configReadPaths = ['/api/settings', '/api/scenes', '/api/models'] as const
export type ConfigReadPath = (typeof configReadPaths)[number]
export interface PublicationInfo {
  authorityPath: string
  mainTimeout: number
  authorityTimeout: number
}
export interface PublicationTicket {
  ticket: number
}
export interface PublicationVerifyRequest extends PublicationTicket {
  path?: ConfigReadPath
}
export interface PublicationVerifiedResult {
  observedHead: Head
  body?: Uint8Array<ArrayBuffer>
}

/** Reserve the existing authority without migrations or application data changes.
 * SQLite may silently open a requested writable handle as native readonly. Its JS
 * readonly flag and BEGIN IMMEDIATE alone cannot prove that RESERVED was acquired.
 * This zero-row write on the verified ordinary table proves native capability once
 * for this handle's lifetime. Native hot-journal recovery may still write on open. */
function openPublicationReservation(path: string): Database.Database {
  const reservation = new Database(join(path, 'authority.sqlite'), { fileMustExist: true })
  try {
    reservation.exec('BEGIN IMMEDIATE')
    if (reservation.pragma('main.journal_mode', { simple: true }) !== 'delete')
      throw new Error('recovery_read_journal_unsupported')
    const identity = reservation
      .prepare("SELECT type, sql FROM main.sqlite_master WHERE name='identity'")
      .get() as { type: string; sql: string } | undefined
    if (
      identity?.type !== 'table' ||
      identity.sql.replace(/\s/g, '').toLowerCase() !==
        'createtableidentity(idtextprimarykey,formatintegernotnull)'
    )
      throw new Error('recovery_schema_unsupported')
    if (reservation.prepare('UPDATE main.identity SET id=id WHERE 0').run().changes !== 0)
      throw new Error('configuration_reservation_invalid')
    reservation.exec('ROLLBACK')
    return reservation
  } catch (error) {
    // Closing also releases an uncertain transaction if rollback itself failed.
    reservation.close()
    throw error
  }
}

/** Pure projections of initialized data. No Store, plugins, secrets key or migrations. */
export class ConfigReadConnection {
  private readonly db: Database.Database
  private readonly authority: RecoveryAuthority
  private readonly reservation: Database.Database
  private publicationTicket?: number
  private closed = false
  constructor(database: string) {
    // Preserve better-sqlite3's existing default lock wait, as used by Store.
    this.db = new Database(database, { readonly: true, fileMustExist: true })
    try {
      this.db.pragma('query_only = ON')
      this.authority = openReadOnlyGate(this.db)
      try {
        this.reservation = openPublicationReservation(this.authority.path)
      } catch (error) {
        this.authority.close()
        throw error
      }
    } catch (error) {
      this.db.close()
      throw error
    }
  }
  private config(key: string, fallback: unknown) {
    const row = this.db.prepare('SELECT value FROM config WHERE key=?').get(key) as
      { value: string } | undefined
    return row ? JSON.parse(row.value) : fallback
  }
  private projection(path: ConfigReadPath) {
    return path === '/api/settings'
      ? settingsReadValue(this.config('settings', defaultSettingsInput()))
      : path === '/api/models'
        ? modelsReadValue(
            this.config('models', { providers: [], models: [], routes: [] }),
            (id) =>
              !!this.db
                .prepare('SELECT 1 FROM secrets WHERE namespace=? AND name=?')
                .get('model', id),
          )
        : (
            this.db.prepare('SELECT value FROM scenes ORDER BY id').all() as { value: string }[]
          ).map((row) => JSON.parse(row.value))
  }
  publicationInfo(): PublicationInfo {
    if (this.closed) throw new Error('configuration_reader_closed')
    return {
      authorityPath: this.authority.path,
      mainTimeout: this.db.pragma('busy_timeout', { simple: true }) as number,
      authorityTimeout: this.authority.db.pragma('busy_timeout', { simple: true }) as number,
    }
  }
  private releasePublication() {
    this.publicationTicket = undefined
    try {
      try {
        if (this.authority.db.open && this.authority.db.inTransaction)
          this.authority.db.exec('ROLLBACK')
      } finally {
        try {
          if (this.db.open && this.db.inTransaction) this.db.exec('ROLLBACK')
        } finally {
          if (this.reservation.open && this.reservation.inTransaction)
            this.reservation.exec('ROLLBACK')
        }
      }
    } catch (error) {
      // An uncertain rollback cannot leave a ticketless lock or reusable connection.
      // Try all three rollbacks, then close every handle before reporting it.
      try {
        this.closeConnections()
      } finally {
        throw error
      }
    }
  }
  private closeConnections() {
    this.closed = true
    try {
      if (this.authority.db.open) this.authority.close()
    } finally {
      try {
        if (this.db.open) this.db.close()
      } finally {
        if (this.reservation.open) this.reservation.close()
      }
    }
  }
  /** Reserve authority before pinning readonly authority and main WAL snapshots.
   * RESERVED excludes a writer's PENDING phase while the host obtains its SHARED
   * lock. Keep it until verification releases: the host then protects enqueueing.
   * Never upgrade either reader, call the owner, or wait for its main ACK here. */
  beginPublication(ticket: number): PublicationTicket {
    if (this.closed) throw new Error('configuration_reader_closed')
    if (!Number.isSafeInteger(ticket) || ticket < 1)
      throw new Error('configuration_publication_ticket_invalid')
    if (
      this.publicationTicket !== undefined ||
      this.db.inTransaction ||
      this.authority.db.inTransaction ||
      this.reservation.inTransaction
    )
      throw new Error('configuration_publication_active')
    try {
      if (this.db.pragma('journal_mode', { simple: true }) !== 'wal')
        throw new Error('recovery_read_journal_unsupported')
      this.reservation.exec('BEGIN IMMEDIATE')
      this.authority.db.exec('BEGIN')
      // head() pins and validates this exact snapshot, retaining the read lock.
      // Actual readonly integrity can be cached even when this lease rolls back.
      // Warming then releasing it before pinning main permits old-main/new-authority.
      this.authority.head()
      if (this.authority.db.pragma('journal_mode', { simple: true }) !== 'delete')
        throw new Error('recovery_read_journal_unsupported')
      this.db.exec('BEGIN')
      if (!metadata(this.db)) throw new Error('recovery_gate_invalid')
      if (this.db.pragma('journal_mode', { simple: true }) !== 'wal')
        throw new Error('recovery_read_journal_unsupported')
      this.publicationTicket = ticket
      return { ticket }
    } catch (error) {
      this.releasePublication()
      throw error
    }
  }
  /** Verification takes no new locks: both snapshots were pinned by beginPublication.
   * Fixed reads and encoding run here, off the host, then release before the IPC reply. */
  verifyPublication({ ticket, path }: PublicationVerifyRequest): PublicationVerifiedResult {
    if (this.publicationTicket === undefined || ticket !== this.publicationTicket)
      throw new Error('configuration_publication_ticket_invalid')
    try {
      if (
        !this.db.inTransaction ||
        !this.authority.db.inTransaction ||
        !this.reservation.inTransaction
      )
        throw new Error('configuration_publication_snapshot_missing')
      if (path !== undefined && !configReadPaths.includes(path))
        throw new Error('configuration_read_path_invalid')
      const gate = metadata(this.db)
      if (!gate || gate.authority !== this.authority.path)
        throw new Error('recovery_authority_not_bound')
      assertRecoveryReady(this.db)
      const body =
        path === undefined
          ? undefined
          : new TextEncoder().encode(JSON.stringify(this.projection(path)))
      return { observedHead: gate.head, ...(body === undefined ? {} : { body }) }
    } finally {
      this.releasePublication()
    }
  }
  abandonPublication(ticket: number): void {
    if (this.publicationTicket === undefined) return
    if (ticket !== this.publicationTicket)
      throw new Error('configuration_publication_ticket_invalid')
    this.releasePublication()
  }
  /** RPC release/cancel are idempotent and cannot release a later publication. */
  releasePublicationTicket(ticket: number): void {
    if (ticket === this.publicationTicket) this.releasePublication()
  }

  close() {
    try {
      this.releasePublication()
    } finally {
      this.closeConnections()
    }
  }
}
