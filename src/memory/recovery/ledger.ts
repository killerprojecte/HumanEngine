import {
  parseAssertionEligibilityEvent,
  type AssertionEligibilityEvent,
} from './eligibility-contract.ts'
import { readMemoryList, type MemoryListRead } from '../read/list.ts'
import { readCommitmentPrivacy } from '../read/commitment-privacy.ts'
import type { CommitmentRef } from '../domain/commitments.ts'
import type { Message } from '../../onebot.ts'
import type { Store } from '../../store.ts'
import type { RuntimeMessage } from '../../runtime.ts'
import type { MemoryRecord, MemorySource } from '../domain/contracts.ts'
import { readMemorySearch, type MemorySearchRead } from '../read/search.ts'
import { domainSourceCurrent } from '../read/domain-source-events.ts'
import { currentSourceSnapshot } from '../../runtime/source-events.ts'
import { normalizeBotMessage } from '../../runtime/chat-history.ts'
import {
  parseExternalCostRecoveryEvent,
  type ExternalCostRecoveryEvent,
} from './external-cost-contract.ts'
import {
  parseExperienceTrialRecoveryEvent,
  type ExperienceTrialRecoveryEvent,
} from './experience-trial-contract.ts'
import { parseSemanticRecoveryEvent, type SemanticRecoveryEvent } from './semantic-contract.ts'
import { parseIdentityRecoveryEvent, type IdentityRecoveryEvent } from './identity-contract.ts'
import { readRecoveryStatement } from './read-statements.ts'
import {
  parseWorkingSetPinRecoveryEvent,
  type WorkingSetPinRecoveryEvent,
} from './working-set-contract.ts'
import {
  parseCommitmentRecoveryEvent,
  type CommitmentRecoveryEvent,
} from './commitment-contract.ts'
import { parseSourceEditHeadEvent, type SourceEditHeadEvent } from './source-event-contract.ts'
import {
  parseConfigurationRecoveryEvent,
  type ConfigurationRecoveryEvent,
} from './configuration-contract.ts'
import { parseDomainRecoveryEvent, type DomainRecoveryEvent } from './domain-contracts.ts'
import Database from 'better-sqlite3'
import { createHash, randomUUID } from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  lstatSync,
  realpathSync,
  chmodSync,
  openSync,
  closeSync,
} from 'node:fs'
import { dirname, resolve, relative, join, parse } from 'node:path'

export const RECOVERY_FORMAT = 1
// Reader capability is separate from the unchanged external chain format.
export const RECOVERY_READER_CAPABILITY = 11
export type Revocation =
  | AssertionEligibilityEvent
  | ExternalCostRecoveryEvent
  | ExperienceTrialRecoveryEvent
  | SemanticRecoveryEvent
  | IdentityRecoveryEvent
  | WorkingSetPinRecoveryEvent
  | CommitmentRecoveryEvent
  | SourceEditHeadEvent
  | DomainRecoveryEvent
  | ConfigurationRecoveryEvent
  | { kind: 'security_state'; revision: string }
  | { kind: 'assertion'; id: string; throughVersion: number; terminal: boolean }
  | { kind: 'fingerprint'; value: string }
  | { kind: 'source'; sceneId: string; messageId: string }
export interface Head {
  instanceId: string
  sequence: number
  hash: string
}
const hash = (s: string) => createHash('sha256').update(s).digest('hex')
const zero = '0'.repeat(64)
const maintenanceConnections = new WeakSet<object>()
const replayEffectCollectors = new WeakMap<Database.Database, Revocation[]>()
const bindings = new WeakMap<Database.Database, RecoveryAuthority>()
interface SourceReadScope {
  main: Database.Database
  head: Readonly<Head>
  authorityChanges: number
  mainChanges: number
  authoritySchema: unknown
  mainSchema: unknown
}
const sourceReadScopes = new WeakMap<RecoveryAuthority, SourceReadScope>()
const changes = (db: Database.Database) =>
  (readRecoveryStatement(db, 'totalChanges') as { n: number }).n
const queryOnly = (db: Database.Database) =>
  (readRecoveryStatement(db, 'queryOnly') as { query_only: number }).query_only
const schemaVersion = (db: Database.Database) =>
  (readRecoveryStatement(db, 'schemaVersion') as { schema_version: number }).schema_version

interface AuthorityStamp {
  dataVersion: number
  totalChanges: number
  schemaVersion: number
}
const authorityStamp = (db: Database.Database): AuthorityStamp => ({
  // Literal PRAGMAs cannot be impersonated by TEMP or virtual tables named pragma_*.
  dataVersion: (readRecoveryStatement(db, 'dataVersion') as { data_version: number }).data_version,
  totalChanges: changes(db),
  schemaVersion: schemaVersion(db),
})

const sameAuthorityStamp = (a: AuthorityStamp, b: AuthorityStamp) =>
  a.dataVersion === b.dataVersion &&
  a.totalChanges === b.totalChanges &&
  a.schemaVersion === b.schemaVersion

function assertSourceReadScope(authority: RecoveryAuthority, scope: SourceReadScope) {
  if (
    !authority.db.inTransaction ||
    !scope.main.inTransaction ||
    queryOnly(authority.db) !== 1 ||
    queryOnly(scope.main) !== 1 ||
    changes(authority.db) !== scope.authorityChanges ||
    changes(scope.main) !== scope.mainChanges ||
    schemaVersion(authority.db) !== scope.authoritySchema ||
    schemaVersion(scope.main) !== scope.mainSchema ||
    bindings.get(scope.main) !== authority
  )
    throw new Error('recovery_read_scope_changed')
}

function inside(child: string, parent: string) {
  const r = relative(parent, child)
  return r === '' || (!r.startsWith('..') && !r.startsWith('/'))
}
export function authorityPath(database: string, explicit?: string): string {
  const db = resolve(database),
    data = dirname(db)
  if (data === parse(data).root && !explicit) throw new Error('recovery_authority_path_required')
  const path = resolve(explicit ?? join(dirname(data), '.humanengine-authority', hash(db)))
  if (inside(path, data)) throw new Error('recovery_authority_not_independent')
  for (let p = path; ; p = dirname(p)) {
    if (existsSync(p) && lstatSync(p).isSymbolicLink()) throw new Error('recovery_symlink_denied')
    if (dirname(p) === p) break
  }
  return path
}
function event(value: unknown): Revocation {
  const e = value as Record<string, unknown>
  const valid = (v: unknown): v is string =>
    typeof v === 'string' && v.length > 0 && v.length <= 2000
  if (e?.kind === 'assertion_eligibility') return parseAssertionEligibilityEvent(e)
  if (e?.kind === 'external_cost_receipt') return parseExternalCostRecoveryEvent(e)
  if (e?.kind === 'experience_trial') return parseExperienceTrialRecoveryEvent(e)
  if (e?.kind === 'semantic_mutation') return parseSemanticRecoveryEvent(value)
  if (e?.kind === 'identity_mutation') return parseIdentityRecoveryEvent(value)
  if (e?.kind === 'working_set_pin') return parseWorkingSetPinRecoveryEvent(value)
  if (e?.kind === 'commitment_head' || e?.kind === 'commitment_delete')
    return parseCommitmentRecoveryEvent(value)
  if (e?.kind === 'source_edit_head') return parseSourceEditHeadEvent(value)
  if (e?.kind === 'configuration_flags' || e?.kind === 'scene_flags')
    return parseConfigurationRecoveryEvent(value)
  if (
    e?.kind === 'security_state' &&
    typeof e.revision === 'string' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(e.revision) &&
    Object.keys(e).length === 2
  )
    return e as Revocation
  if (
    e?.kind === 'assertion' &&
    valid(e.id) &&
    Number.isSafeInteger(e.throughVersion) &&
    Number(e.throughVersion) >= 0 &&
    typeof e.terminal === 'boolean' &&
    Object.keys(e).length === 4
  )
    return e as Revocation
  if (
    e?.kind === 'fingerprint' &&
    typeof e.value === 'string' &&
    /^[a-f0-9]{64}$/.test(e.value) &&
    Object.keys(e).length === 2
  )
    return e as Revocation
  if (e?.kind === 'source' && valid(e.sceneId) && valid(e.messageId) && Object.keys(e).length === 3)
    return e as Revocation
  if (
    typeof e?.kind === 'string' &&
    ['habit_delete', 'meme_delete', 'meme_state', 'experience_delete', 'experience_state'].includes(
      e.kind,
    )
  )
    return parseDomainRecoveryEvent(value)
  throw new Error('recovery_unknown_revocation')
}
export class RecoveryAuthority {
  readonly db: Database.Database
  readonly path: string
  // Integrity of the last committed SQLite snapshot, not permission to use main data.
  #validated?: { head: Head; stamp: AuthorityStamp }
  constructor(path: string, createInstance?: string, readOnly = false) {
    if (readOnly && createInstance !== undefined)
      throw new Error('recovery_read_connection_invalid')
    this.path = resolve(path)
    if (createInstance) mkdirSync(this.path, { recursive: true, mode: 0o700 })
    if (!existsSync(this.path) || realpathSync(this.path) !== this.path)
      throw new Error('recovery_authority_missing')
    const file = join(this.path, 'authority.sqlite')
    if (!createInstance && !existsSync(file)) throw new Error('recovery_authority_missing')
    if (existsSync(file) && !lstatSync(file).isFile()) throw new Error('recovery_authority_invalid')
    this.db = new Database(file, {
      fileMustExist: !createInstance,
      ...(readOnly ? { readonly: true } : {}),
    })
    try {
      if (readOnly) {
        if (this.db.pragma('journal_mode', { simple: true }) !== 'delete')
          throw new Error('recovery_read_journal_unsupported')
        this.db.pragma('query_only = ON')
      } else {
        chmodSync(file, 0o600)
        this.db.pragma('journal_mode = DELETE')
        this.db.pragma('synchronous = FULL')
      }
      if (createInstance) {
        this.db.exec(
          'CREATE TABLE IF NOT EXISTS main.identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS main.revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL)',
        )
        if (!this.db.prepare('SELECT 1 FROM main.identity').get())
          this.db
            .prepare('INSERT INTO main.identity VALUES(?,?)')
            .run(createInstance, RECOVERY_FORMAT)
      }
      // Additive local migration: preserve the event format and exact deduplication.
      // The index is built once for existing installations; no history lives in a JS cache.
      if (!readOnly)
        this.db.exec('CREATE INDEX IF NOT EXISTS main.revocations_payload ON revocations(payload)')
      this.entries(false)
    } catch (error) {
      this.db.close()
      throw error
    }
  }
  /** Explicit full audit, including startup/recovery. Always rehash and parse every row. */
  entries(collect = true): Array<{ sequence: number; revocation: Revocation }> {
    this.#validated = undefined
    const ownTransaction = !this.db.inTransaction
    try {
      const audit = () => this.#scan(collect)
      const result = ownTransaction ? this.db.transaction(audit)() : audit()
      // An actual readonly handle cannot verify uncommitted local main writes.
      // Its caller may end the read with ROLLBACK without undoing the audited data.
      // query_only alone is not enough: writable callers can later roll back writes.
      if (ownTransaction || this.db.readonly) this.#validated = result.validated
      return result.entries
    } catch (error) {
      this.#validated = undefined
      throw error
    }
  }
  #scan(collect: boolean) {
    // Integrity reuse requires the format's ordinary tables, not views/virtual
    // tables or conflict policies that can hide changes from SQLite counters.
    const definitions = this.db
      .prepare(
        "SELECT name, type, sql FROM main.sqlite_master WHERE name IN ('identity','revocations')",
      )
      .all() as Array<{ name: string; type: string; sql: string }>
    const expected: Record<string, string> = {
      identity: 'CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL)',
      revocations:
        'CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL)',
    }
    const normalized = (sql: string) => sql.replace(/\s/g, '').toLowerCase()
    if (
      definitions.length !== 2 ||
      definitions.some(
        (row) =>
          row.type !== 'table' ||
          !expected[row.name] ||
          normalized(row.sql) !== normalized(expected[row.name]!),
      )
    )
      throw new Error('recovery_schema_unsupported')

    const identities = readRecoveryStatement(this.db, 'identities') as {
      id: string
      format: number
    }[]
    if (identities.length !== 1 || identities[0]!.format !== RECOVERY_FORMAT)
      throw new Error('recovery_format_unsupported')
    const instanceId = identities[0]!.id
    let previous = zero,
      sequence = 0
    const entries: Array<{ sequence: number; revocation: Revocation }> = []
    for (const row of readRecoveryStatement(this.db, 'chain') as Iterable<{
      sequence: number
      previous_hash: string
      payload: string
      hash: string
    }>) {
      if (
        row.sequence !== ++sequence ||
        row.previous_hash !== previous ||
        row.hash !== hash(`${instanceId}|${sequence}|${previous}|${row.payload}`)
      )
        throw new Error('recovery_chain_invalid')
      const revocation = event(JSON.parse(row.payload))
      if (collect) entries.push({ sequence, revocation })
      previous = row.hash
    }
    // Captured under the same read lock as the verified rows, never after COMMIT.
    return {
      entries,
      validated: {
        head: { instanceId, sequence, hash: previous },
        stamp: authorityStamp(this.db),
      },
    }
  }
  #current() {
    // Reading identity first pins SQLite's snapshot before reading its change stamps.
    // Do not stamp a just-committed external write as if it were already verified.
    readRecoveryStatement(this.db, 'identity')
    const stamp = authorityStamp(this.db)
    if (this.#validated && sameAuthorityStamp(stamp, this.#validated.stamp)) return this.#validated
    return this.#scan(false).validated
  }
  head(): Head {
    const scope = sourceReadScopes.get(this)
    if (scope) return { ...scope.head }
    const ownTransaction = !this.db.inTransaction
    try {
      const current = ownTransaction
        ? this.db.transaction(() => this.#current())()
        : this.#current()
      if (ownTransaction || this.db.readonly) this.#validated = current
      return { ...current.head }
    } catch (error) {
      this.#validated = undefined
      throw error
    }
  }
  prefix(sequence: number): Head {
    const head = this.head()
    if (!Number.isSafeInteger(sequence) || sequence < 0 || sequence > head.sequence)
      throw new Error('recovery_prefix_invalid')
    if (!sequence) return { instanceId: head.instanceId, sequence: 0, hash: zero }
    const row = this.db
      .prepare('SELECT hash FROM main.revocations WHERE sequence=?')
      .get(sequence) as {
      hash: string
    }
    return { instanceId: head.instanceId, sequence, hash: row.hash }
  }
  append(input: Revocation, expectedHead: Head, forceAppend = false): Head {
    if (sourceReadScopes.has(this)) throw new Error('recovery_read_scope_write_denied')
    const parsed = event(input)
    const payload = JSON.stringify(parsed)
    const ownTransaction = !this.db.inTransaction
    try {
      const committed = this.db
        .transaction(() => {
          const previous = this.#current()
          if (!sameHead(previous.head, expectedHead))
            throw new Error('recovery_authority_head_changed')
          // Intent itself is durable authority even when the later main write rolls back.
          if (
            forceAppend ||
            parsed.kind === 'assertion_eligibility' ||
            parsed.kind === 'configuration_flags' ||
            parsed.kind === 'scene_flags' ||
            parsed.kind === 'meme_state' ||
            parsed.kind === 'experience_state' ||
            !this.db.prepare('SELECT 1 FROM main.revocations WHERE payload=?').get(payload)
          ) {
            const sequence = previous.head.sequence + 1
            const digest = hash(
              `${previous.head.instanceId}|${sequence}|${previous.head.hash}|${payload}`,
            )
            const inserted = this.db
              .prepare('INSERT INTO main.revocations VALUES(?,?,?,?)')
              .run(sequence, previous.head.hash, payload, digest)
            const head = { ...previous.head, sequence, hash: digest }
            const stamp = authorityStamp(this.db)
            // Supported SQL can install triggers. Only our exact single-row write
            // may extend verified history without another full audit.
            if (
              inserted.changes !== 1 ||
              stamp.totalChanges !== previous.stamp.totalChanges + 1 ||
              stamp.schemaVersion !== previous.stamp.schemaVersion ||
              stamp.dataVersion !== previous.stamp.dataVersion
            ) {
              const audited = this.#scan(false).validated
              if (!sameHead(audited.head, head)) throw new Error('recovery_authority_head_changed')
              return audited
            }
            return { head, stamp }
          }
          return previous
        })
        .immediate()
      if (ownTransaction) this.#validated = committed
      return { ...committed.head }
    } catch (error) {
      this.#validated = undefined
      throw error
    }
  }
  close() {
    if (sourceReadScopes.has(this)) throw new Error('recovery_read_scope_write_denied')
    this.db.close()
    this.#validated = undefined
  }
}
export function sameHead(a: Head, b: Head) {
  return a.instanceId === b.instanceId && a.sequence === b.sequence && a.hash === b.hash
}
export function metadata(
  db: Database.Database,
): { head: Head; authority: string; minimumReader: number; state: string } | undefined {
  if (!readRecoveryStatement(db, 'gateExists')) return undefined
  const row = readRecoveryStatement(db, 'gateValue') as { value: string } | undefined
  if (!row) throw new Error('recovery_gate_invalid')
  let value
  try {
    value = JSON.parse(row.value)
  } catch {
    throw new Error('recovery_gate_invalid')
  }
  if (
    !value ||
    !value.head ||
    typeof value.head.instanceId !== 'string' ||
    !/^[a-f0-9-]{36}$/.test(value.head.instanceId) ||
    !Number.isSafeInteger(value.head.sequence) ||
    value.head.sequence < 0 ||
    typeof value.head.hash !== 'string' ||
    !/^[a-f0-9]{64}$/.test(value.head.hash) ||
    typeof value.authority !== 'string' ||
    resolve(value.authority) !== value.authority ||
    !Number.isSafeInteger(value.minimumReader) ||
    value.minimumReader < 1 ||
    !['ready', 'quarantine'].includes(value.state)
  )
    throw new Error('recovery_gate_invalid')
  return value
}
export function saveGate(db: Database.Database, value: NonNullable<ReturnType<typeof metadata>>) {
  db.exec(
    'CREATE TABLE IF NOT EXISTS main.recovery_gate (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)',
  )
  db.prepare(
    'INSERT INTO main.recovery_gate VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
  ).run(JSON.stringify(value))
}
export function enroll(
  db: Database.Database,
  database: string,
  explicit?: string,
): RecoveryAuthority {
  // Enrollment is a one-time operator trust decision, not a repair for damaged gates.
  // Serialize the check and write on the main database, including competing CLIs.
  if (db.inTransaction) throw new Error('recovery_enrollment_transaction_active')
  let authority: RecoveryAuthority | undefined
  try {
    return db
      .transaction(() => {
        if (metadata(db)) throw new Error('recovery_already_enrolled')
        const path = authorityPath(database, explicit)
        const file = join(path, 'authority.sqlite')
        if (existsSync(file)) throw new Error('recovery_existing_authority_requires_reconciliation')
        // BEGIN IMMEDIATE alone does not reject a readonly SQLite connection. Perform
        // the first reversible main-DB write before creating any external authority.
        db.exec(
          'CREATE TABLE main.recovery_gate (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)',
        )
        mkdirSync(path, { recursive: true, mode: 0o700 })
        try {
          closeSync(openSync(file, 'wx', 0o600))
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code === 'EEXIST')
            throw new Error('recovery_existing_authority_requires_reconciliation')
          throw error
        }
        authority = new RecoveryAuthority(path, randomUUID())
        saveGate(db, {
          head: authority.head(),
          authority: path,
          minimumReader: RECOVERY_FORMAT,
          state: 'ready',
        })
        return authority
      })
      .immediate()
  } catch (error) {
    authority?.close()
    // Cross-database commits are not atomic. If a later failure/crash left a newly
    // reserved authority, retain it as a fail-closed reconciliation obligation.
    // Never delete/reuse a possibly durable authority to make a retry succeed.
    throw error
  }
}
export function openGate(db: Database.Database, database: string): RecoveryAuthority | undefined {
  if (database === ':memory:') return
  let state = metadata(db)
  let authority: RecoveryAuthority
  if (!state) {
    if (
      db
        .prepare(
          "SELECT 1 FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' LIMIT 1",
        )
        .get()
    )
      throw new Error('recovery_legacy_enrollment_required')
    authority = enroll(db, database, process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY)
    state = metadata(db)!
  } else {
    if (state.minimumReader > RECOVERY_READER_CAPABILITY || state.state !== 'ready')
      throw new Error('recovery_quarantined_or_unsupported')
    authorityPath(database, state.authority)
    authority = new RecoveryAuthority(state.authority)
    if (!sameHead(state.head, authority.head())) {
      authority.close()
      throw new Error('recovery_authority_head_changed')
    }
  }
  bindings.set(db, authority)
  return authority
}
export function bindRecovery(db: Database.Database, authority: RecoveryAuthority) {
  bindings.set(db, authority)
}

/** Existing enrolled data only. No initialization, migration, chmod or journal-mode changes.
 * The private readonly pair may pin authority before main: its fixed WAL read never
 * upgrades either handle, calls the owner, or waits for a newer main acknowledgement. */
export function openReadOnlyGate(db: Database.Database): RecoveryAuthority {
  if (!db.readonly || db.name === ':memory:' || db.inTransaction)
    throw new Error('recovery_read_connection_invalid')
  if (db.pragma('journal_mode', { simple: true }) !== 'wal')
    throw new Error('recovery_read_journal_unsupported')
  const state = metadata(db)
  if (!state) throw new Error('recovery_legacy_enrollment_required')
  if (state.minimumReader > RECOVERY_READER_CAPABILITY || state.state !== 'ready')
    throw new Error('recovery_quarantined_or_unsupported')
  const authority = new RecoveryAuthority(authorityPath(db.name, state.authority), undefined, true)
  try {
    // The first gate only discovers the path. A normal writer may complete while
    // opening/auditing authority, so compare freshly pinned overlapping snapshots.
    authority.db.transaction(() => {
      const head = authority.head()
      if (authority.db.pragma('journal_mode', { simple: true }) !== 'delete')
        throw new Error('recovery_read_journal_unsupported')
      db.transaction(() => {
        const current = metadata(db)
        if (db.pragma('journal_mode', { simple: true }) !== 'wal')
          throw new Error('recovery_read_journal_unsupported')
        if (!current || current.authority !== authority.path)
          throw new Error('recovery_authority_not_bound')
        if (current.minimumReader > RECOVERY_READER_CAPABILITY || current.state !== 'ready')
          throw new Error('recovery_quarantined_or_unsupported')
        // Durable authority intent without its main ACK still fails closed. Do not
        // wait or retry this mismatch while retaining the authority read lock.
        if (!sameHead(current.head, head)) throw new Error('recovery_authority_head_changed')
      })()
    })()
    bindings.set(db, authority)
    return authority
  } catch (error) {
    authority.close()
    throw error
  }
}

function assertDurableAuthorityTransaction(authority: RecoveryAuthority) {
  if (sourceReadScopes.has(authority)) throw new Error('recovery_read_scope_write_denied')
  // Main data must not acknowledge an intent still inside somebody else's transaction.
  if (authority.db.inTransaction) throw new Error('recovery_authority_transaction_active')
}
export function revoke(db: Database.Database, value: Revocation) {
  const authority = bindings.get(db)
  if (!authority) {
    if (db.name !== ':memory:') throw new Error('recovery_authority_not_bound')
    return
  }
  const gate = metadata(db)!
  if (maintenanceConnections.has(db)) {
    const effects = replayEffectCollectors.get(db)
    if (effects) {
      if (!db.inTransaction) throw new Error('recovery_transaction_required')
      effects.push(structuredClone(event(value)))
    }
    return
  }
  assertDurableAuthorityTransaction(authority)
  if (!db.inTransaction) throw new Error('recovery_transaction_required')
  if (!sameHead(gate.head, authority.head())) throw new Error('recovery_authority_head_changed')
  const head = authority.append(value, gate.head)
  if (head.instanceId !== gate.head.instanceId) throw new Error('recovery_instance_mismatch')
  saveGate(db, { ...gate, head })
}

export function maintenanceGate(
  db: Database.Database,
  database: string,
  authority: RecoveryAuthority,
) {
  const gate = metadata(db)
  if (
    !gate ||
    gate.state !== 'quarantine' ||
    gate.minimumReader > RECOVERY_READER_CAPABILITY ||
    gate.authority !== authority.path ||
    gate.head.instanceId !== authority.head().instanceId
  )
    throw new Error('recovery_maintenance_denied')
  authorityPath(database, authority.path)
  bindings.set(db, authority)
  maintenanceConnections.add(db)
}

export function assertRecoveryReady(db: Database.Database) {
  checkedRecoveryHead(db)
}

/** Observe the exact final HTTP-response snapshot, not an authorization token.
 * Cross-isolate publication must revalidate current authority and match this identity. */
export function captureResponseReadHead(db: Database.Database): Head | undefined {
  const authority = bindings.get(db)
  if (!authority) {
    assertRecoveryReady(db)
    return
  }
  return db.transaction(() => {
    metadata(db) // Pin main first, preserving the common main -> authority lock order.
    return authority.db.transaction(() => ({ ...checkedRecoveryHead(db)! }))()
  })()
}

// Internal current-use result only. Never export a reusable permission or retain
// this head after its fixed synchronous read snapshots have ended.
function checkedRecoveryHead(db: Database.Database): Head | undefined {
  const authority = bindings.get(db)
  if (!authority) {
    if (db.name !== ':memory:') throw new Error('recovery_authority_not_bound')
    return
  }
  const scope = sourceReadScopes.get(authority)
  if (scope?.main === db) return scope.head
  const gate = metadata(db)
  if (gate && gate.minimumReader > RECOVERY_READER_CAPABILITY)
    throw new Error('recovery_format_unsupported')
  if (!gate || gate.state !== 'ready') throw new Error('recovery_authority_head_changed')
  const head = authority.head()
  if (!sameHead(gate.head, head)) throw new Error('recovery_authority_head_changed')
  return head
}

/** Fixed synchronous source-read loop only; no caller callback or reusable validity token escapes. */
export function validateSourceSnapshots(
  store: Store,
  messages: readonly RuntimeMessage[],
): boolean {
  return validateFixedReads(store, { kind: 'sources', messages })
}

/** Fixed primitive dependency data only; never runs extension callbacks under a reused head. */
export function validateUnrecalledDependencies(
  store: Store,
  sceneId: string,
  messageIds: readonly string[],
): boolean {
  // Materialize before acquiring snapshots so caller iterators never execute inside the scope.
  const ids = Array.from(messageIds)
  if (typeof sceneId !== 'string' || ids.some((id) => typeof id !== 'string')) return false
  if (!ids.length) return true
  return validateFixedReads(store, { kind: 'dependencies', sceneId, ids })
}

/** Current-use results only, with no callback or reusable authority token. Materialize
 * caller-owned data before entering the fixed read scope; no getters/iterators run inside it.
 */
export function currentDomainSources(store: Store, sources: readonly MemorySource[]): boolean[] {
  const materialized = structuredClone(Array.from(sources))
  if (!materialized.length) return []
  return validateFixedReads(store, { kind: 'domain-sources', sources: materialized })
}

/** Browser display projection only. Inputs are copied before acquiring the fixed read
 * snapshots; no caller callback or authorization result escapes this synchronous call. */
export function projectDisplayMessages(
  store: Store,
  sceneId: string,
  messages: readonly Message[],
  locallyRecalled: Iterable<string>,
): Message[]
export function projectDisplayMessages(
  store: Store,
  sceneId: string,
  messages: readonly RuntimeMessage[],
  locallyRecalled: Iterable<string>,
): RuntimeMessage[]
export function projectDisplayMessages(
  store: Store,
  sceneId: string,
  messages: readonly RuntimeMessage[],
  locallyRecalled: Iterable<string>,
): RuntimeMessage[] {
  const materialized = structuredClone(Array.from(messages))
  const recalled = new Set(Array.from(locallyRecalled))
  if (!materialized.length) return []
  return validateFixedReads(store, { kind: 'display', sceneId, messages: materialized, recalled })
}

/** Remote browser history only. Keep recall filtering before the 30-row bound and
 * bot normalization before identity projection, within one fixed synchronous read.
 */
export function projectRemoteHistoryMessages(
  store: Store,
  sceneId: string,
  messages: readonly Message[],
  locallyRecalled: Iterable<string>,
  botUserId?: string,
): Message[] {
  const materialized = structuredClone(Array.from(messages))
  const recalled = new Set(Array.from(locallyRecalled))
  if (!materialized.length) return []
  return validateFixedReads(store, {
    kind: 'remote-history-display',
    sceneId,
    messages: materialized,
    recalled,
    botUserId,
  }) as Message[]
}

/** Fixed search proof/ranking read only. Copy inputs before acquiring either
 * snapshot, and never accept caller functions or reusable authority objects. */
export function searchMemorySnapshot(store: Store, input: MemorySearchRead): MemoryRecord[] {
  const search = structuredClone(input)
  // structuredClone retains SharedArrayBuffer backing; own the vector bytes too.
  if (search.options) search.options.vector = Uint8Array.from(search.options.vector)
  return validateFixedReads(store, { kind: 'memory-search', search })
}

/** Exact pre-mutation references only. The fixed implementation accepts no caller
 * callback, and restores write access before returning to the recall transaction. */
export function captureCommitmentPrivacySnapshot(store: Store): Map<string, CommitmentRef> {
  return validateFixedReads(store, { kind: 'commitment-privacy' })
}

/** Listing reads run after lifecycle maintenance; no write callback is accepted. */
export function listMemorySnapshot(store: Store, input: MemoryListRead): MemoryRecord[] {
  const list = structuredClone(input)
  return validateFixedReads(store, { kind: 'memory-list', list })
}

type ListReads = { kind: 'memory-list'; list: MemoryListRead }
type CommitmentPrivacyReads = { kind: 'commitment-privacy' }
type SearchReads = { kind: 'memory-search'; search: MemorySearchRead }
type DisplayReads = {
  sceneId: string
  messages: RuntimeMessage[]
  recalled: Set<string>
} & ({ kind: 'display' } | { kind: 'remote-history-display'; botUserId?: string })
type FixedSourceReads =
  | { kind: 'sources'; messages: readonly RuntimeMessage[] }
  | { kind: 'dependencies'; sceneId: string; ids: readonly string[] }
type DomainSourceReads = { kind: 'domain-sources'; sources: readonly MemorySource[] }
function validateFixedReads(store: Store, input: DisplayReads): RuntimeMessage[]
function validateFixedReads(store: Store, input: FixedSourceReads): boolean
function validateFixedReads(store: Store, input: DomainSourceReads): boolean[]
function validateFixedReads(store: Store, input: SearchReads | ListReads): MemoryRecord[]
function validateFixedReads(store: Store, input: CommitmentPrivacyReads): Map<string, CommitmentRef>
function validateFixedReads(
  store: Store,
  input:
    | FixedSourceReads
    | DomainSourceReads
    | DisplayReads
    | SearchReads
    | ListReads
    | CommitmentPrivacyReads,
): boolean | boolean[] | RuntimeMessage[] | MemoryRecord[] | Map<string, CommitmentRef> {
  const db = store.db
  const authority = bindings.get(db)
  const read = () =>
    input.kind === 'memory-list'
      ? readMemoryList(store, input.list)
      : input.kind === 'commitment-privacy'
        ? readCommitmentPrivacy(store)
        : input.kind === 'memory-search'
          ? readMemorySearch(store, input.search)
          : input.kind === 'sources'
            ? input.messages.every((message) => currentSourceSnapshot(store, message))
            : input.kind === 'dependencies'
              ? input.ids.every((id) => !store.recalled(input.sceneId, id))
              : input.kind === 'domain-sources'
                ? input.sources.map((source) => domainSourceCurrent(store, source))
                : input.kind === 'remote-history-display'
                  ? input.messages
                      .filter(
                        (message) =>
                          message.sceneId === input.sceneId &&
                          !input.recalled.has(message.id) &&
                          !store.recalled(input.sceneId, message.id),
                      )
                      .slice(-30)
                      .map((message) =>
                        store.presentMessage(
                          normalizeBotMessage(message as Message, input.botUserId),
                        ),
                      )
                  : input.messages.map((message) =>
                      input.recalled.has(message.id) || store.recalled(input.sceneId, message.id)
                        ? { ...message, parts: [], status: 'recalled' as const }
                        : message.origin === 'simulation'
                          ? message
                          : store.presentMessage(message as Message),
                    )
  // All implementations here are fixed synchronous reads. A caller-owned main
  // write transaction may nest a read savepoint; its writes resume only afterward.
  if (authority && sourceReadScopes.get(authority)?.main === db) return read()
  if (
    !authority ||
    maintenanceConnections.has(db) ||
    authority.db.inTransaction ||
    db.pragma('journal_mode', { simple: true }) !== 'wal' ||
    authority.db.pragma('journal_mode', { simple: true }) !== 'delete' ||
    db.pragma('query_only', { simple: true }) !== 0 ||
    authority.db.pragma('query_only', { simple: true }) !== 0
  )
    return read()
  let result: boolean | boolean[] | RuntimeMessage[] | MemoryRecord[] | Map<string, CommitmentRef>
  try {
    db.pragma('query_only = ON')
    authority.db.pragma('query_only = ON')
    result = db.transaction(() => {
      // Pin the main snapshot first, matching normal main -> authority lock order.
      metadata(db)
      return authority.db.transaction(() => {
        // Validate the current committed authority snapshot once. No user callback
        // or asynchronous work runs under this synchronous read unit.
        const head = checkedRecoveryHead(db)!
        sourceReadScopes.set(authority, {
          main: db,
          head: Object.freeze({ ...head }),
          authorityChanges: changes(authority.db),
          mainChanges: changes(db),
          authoritySchema: schemaVersion(authority.db),
          mainSchema: schemaVersion(db),
        })
        try {
          const valid = read()
          assertSourceReadScope(authority, sourceReadScopes.get(authority)!)
          return valid
        } finally {
          sourceReadScopes.delete(authority)
        }
      })()
    })()
  } finally {
    sourceReadScopes.delete(authority)
    // The owned read transaction/savepoint has ended before restoring writes.
    // An enclosing recall transaction remains owned by its caller.
    try {
      authority.db.pragma('query_only = OFF')
    } finally {
      db.pragma('query_only = OFF')
    }
  }
  // A fresh gate/change check after releasing the authority lock; no read scope escapes.
  // An enclosing main transaction retains its own snapshot and will recheck before writes.
  assertRecoveryReady(db)
  return result
}

export interface DurableRevocationTicket {
  readonly before: Head
  readonly after: Head
}
const durableTickets = new WeakMap<object, Database.Database>()
/** Start an async filesystem operation without acknowledging local completion. */
export function beginDurableRevocation(
  db: Database.Database,
  value: Revocation,
): DurableRevocationTicket | undefined {
  if (db.name === ':memory:' || maintenanceConnections.has(db)) return undefined
  if (db.inTransaction) throw new Error('recovery_async_transaction_not_allowed')
  const authority = bindings.get(db),
    gate = metadata(db)
  if (!authority || !gate) throw new Error('recovery_authority_not_bound')
  assertDurableAuthorityTransaction(authority)
  assertRecoveryReady(db)
  const after = authority.append(value, gate.head, true)
  const ticket = Object.freeze({
    before: Object.freeze({ ...gate.head }),
    after: Object.freeze({ ...after }),
  })
  durableTickets.set(ticket, db)
  return ticket
}
/** No transaction crosses an await. Only the completed synchronous local effects acknowledge intent. */
export function commitDurableRevocation<T>(
  db: Database.Database,
  ticket: DurableRevocationTicket | undefined,
  apply: () => T,
): T {
  return db
    .transaction(() => {
      if (ticket) {
        if (durableTickets.get(ticket) !== db) throw new Error('recovery_ticket_invalid')
        const authority = bindings.get(db),
          gate = metadata(db)
        if (
          !authority ||
          !gate ||
          !sameHead(gate.head, ticket.before) ||
          !sameHead(authority.head(), ticket.after)
        )
          throw new Error('recovery_authority_head_changed')
        assertDurableAuthorityTransaction(authority)
      } else if (db.name !== ':memory:' && !maintenanceConnections.has(db))
        throw new Error('recovery_ticket_required')
      if (ticket) saveGate(db, { ...metadata(db)!, head: ticket.after })
      const result = apply()
      if (result && typeof (result as { then?: unknown }).then === 'function')
        throw new Error('recovery_async_commit_denied')
      if (ticket) {
        if (!sameHead(bindings.get(db)!.head(), ticket.after))
          throw new Error('recovery_authority_head_changed')
        saveGate(db, { ...metadata(db)!, head: ticket.after })
        durableTickets.delete(ticket)
      }
      return result
    })
    .immediate()
}
export function isRecoveryMaintenance(db: object) {
  return maintenanceConnections.has(db)
}
/** Domain repositories keep the same live gate, while explicit offline maintenance remains isolated. */
export function assertDomainReady(db: object) {
  if (!maintenanceConnections.has(db)) assertRecoveryReady(db as Database.Database)
}

/** Raise compatibility only for an actually committed source-event mutation, not construction. */
function requireReaderCapability(db: Database.Database, minimumReader: number): void {
  if (!db.inTransaction) throw new Error('recovery_transaction_required')
  if (db.name === ':memory:') return
  assertDomainReady(db)
  const gate = metadata(db)
  if (!gate) throw new Error('recovery_authority_not_bound')
  if (gate.minimumReader < minimumReader) saveGate(db, { ...gate, minimumReader })
}

export function requireEligibilityReader(db: Database.Database): void {
  requireReaderCapability(db, 11)
}
export function requireOperationalReader(db: Database.Database): void {
  requireReaderCapability(db, 10)
}
export function requireExperienceTrialReader(db: Database.Database): void {
  requireReaderCapability(db, 9)
}
export function requireSemanticReader(db: Database.Database): void {
  requireReaderCapability(db, 8)
}
export function requireKnowledgeReader(db: Database.Database): void {
  requireReaderCapability(db, 7)
}
export function requireIdentityReader(db: Database.Database): void {
  requireReaderCapability(db, 6)
}
export function requireSourceEventReader(db: Database.Database): void {
  requireReaderCapability(db, 2)
}
export function requireModelReadPolicyReader(db: Database.Database): void {
  requireReaderCapability(db, 5)
}
export function requireWorkingSetReader(db: Database.Database): void {
  requireReaderCapability(db, 4)
}
export function requireCommitmentReader(db: Database.Database): void {
  requireReaderCapability(db, 3)
}

/** Receipts prove an exact lifecycle effect ran in this successful offline transaction.
 * They are never inferred from an untrusted snapshot version, and never escape a rollback. */
export function captureRecoveryEffects(db: Database.Database, apply: () => unknown): Revocation[] {
  if (!maintenanceConnections.has(db)) throw new Error('recovery_maintenance_required')
  if (db.inTransaction || replayEffectCollectors.has(db))
    throw new Error('recovery_capture_transaction_active')
  const effects: Revocation[] = []
  try {
    db.transaction(() => {
      replayEffectCollectors.set(db, effects)
      const result = apply()
      if (result && typeof (result as { then?: unknown }).then === 'function')
        throw new Error('recovery_async_capture_denied')
    }).immediate()
    return effects
  } finally {
    replayEffectCollectors.delete(db)
  }
}
