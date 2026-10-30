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
export const RECOVERY_READER_CAPABILITY = 10
export type Revocation =
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
  constructor(path: string, createInstance?: string) {
    this.path = resolve(path)
    if (createInstance) mkdirSync(this.path, { recursive: true, mode: 0o700 })
    if (!existsSync(this.path) || realpathSync(this.path) !== this.path)
      throw new Error('recovery_authority_missing')
    const file = join(this.path, 'authority.sqlite')
    if (!createInstance && !existsSync(file)) throw new Error('recovery_authority_missing')
    if (existsSync(file) && !lstatSync(file).isFile()) throw new Error('recovery_authority_invalid')
    this.db = new Database(file, { fileMustExist: !createInstance })
    try {
      chmodSync(file, 0o600)
      this.db.pragma('journal_mode = DELETE')
      this.db.pragma('synchronous = FULL')
      if (createInstance) {
        this.db.exec(
          'CREATE TABLE IF NOT EXISTS identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL)',
        )
        if (!this.db.prepare('SELECT 1 FROM identity').get())
          this.db.prepare('INSERT INTO identity VALUES(?,?)').run(createInstance, RECOVERY_FORMAT)
      }
      this.entries()
    } catch (error) {
      this.db.close()
      throw error
    }
  }
  entries(): Array<{ sequence: number; revocation: Revocation }> {
    const identities = readRecoveryStatement(this.db, 'identities') as {
      id: string
      format: number
    }[]
    if (identities.length !== 1 || identities[0]!.format !== RECOVERY_FORMAT)
      throw new Error('recovery_format_unsupported')
    let previous = zero,
      sequence = 0
    const result = []
    for (const row of readRecoveryStatement(this.db, 'chain') as Array<{
      sequence: number
      previous_hash: string
      payload: string
      hash: string
    }>) {
      if (
        row.sequence !== ++sequence ||
        row.previous_hash !== previous ||
        row.hash !== hash(`${identities[0]!.id}|${sequence}|${previous}|${row.payload}`)
      )
        throw new Error('recovery_chain_invalid')
      result.push({ sequence, revocation: event(JSON.parse(row.payload)) })
      previous = row.hash
    }
    return result
  }
  head(): Head {
    this.entries()
    const id = readRecoveryStatement(this.db, 'identity') as { id: string }
    const last = readRecoveryStatement(this.db, 'latest') as
      { sequence: number; hash: string } | undefined
    return { instanceId: id.id, sequence: last?.sequence ?? 0, hash: last?.hash ?? zero }
  }
  prefix(sequence: number): Head {
    const head = this.head()
    if (!Number.isSafeInteger(sequence) || sequence < 0 || sequence > head.sequence)
      throw new Error('recovery_prefix_invalid')
    if (!sequence) return { instanceId: head.instanceId, sequence: 0, hash: zero }
    const row = this.db.prepare('SELECT hash FROM revocations WHERE sequence=?').get(sequence) as {
      hash: string
    }
    return { instanceId: head.instanceId, sequence, hash: row.hash }
  }
  append(input: Revocation, expectedHead: Head, forceAppend = false): Head {
    const payload = JSON.stringify(event(input))
    return this.db
      .transaction(() => {
        const previous = this.head()
        if (!sameHead(previous, expectedHead)) throw new Error('recovery_authority_head_changed')
        // Intent itself is the durable revocation authority, even if the local write rolls back.
        if (
          forceAppend ||
          input.kind === 'configuration_flags' ||
          input.kind === 'scene_flags' ||
          input.kind === 'meme_state' ||
          input.kind === 'experience_state' ||
          !this.db.prepare('SELECT 1 FROM revocations WHERE payload=?').get(payload)
        ) {
          const n = previous.sequence + 1
          this.db
            .prepare('INSERT INTO revocations VALUES(?,?,?,?)')
            .run(
              n,
              previous.hash,
              payload,
              hash(`${previous.instanceId}|${n}|${previous.hash}|${payload}`),
            )
        }
        return this.head()
      })
      .immediate()
  }
  close() {
    this.db.close()
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
    'CREATE TABLE IF NOT EXISTS recovery_gate (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)',
  )
  db.prepare(
    'INSERT INTO recovery_gate VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
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
          'CREATE TABLE recovery_gate (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)',
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
          "SELECT 1 FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' LIMIT 1",
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
  const authority = bindings.get(db)
  if (!authority) {
    if (db.name !== ':memory:') throw new Error('recovery_authority_not_bound')
    return
  }
  const gate = metadata(db)
  if (gate && gate.minimumReader > RECOVERY_READER_CAPABILITY)
    throw new Error('recovery_format_unsupported')
  if (!gate || gate.state !== 'ready' || !sameHead(gate.head, authority.head()))
    throw new Error('recovery_authority_head_changed')
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
