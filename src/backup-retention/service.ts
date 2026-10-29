import Database from 'better-sqlite3'
import { randomUUID } from 'node:crypto'
import {
  closeSync,
  constants,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  rmdirSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { z } from 'zod'
import { RegistrationSchema, RetentionInventorySchema, type Registration } from './contracts.ts'
import {
  absent,
  contained,
  identity,
  inspectBackup,
  safeDirectory,
  verifyRegistered,
  verifyMember,
} from './filesystem.ts'
const RootSchema = z
  .object({
    version: z.literal(1),
    id: z.string().uuid(),
    root: z.string(),
    device: z.string(),
    inode: z.string(),
  })
  .strict()
const nameSchema = RegistrationSchema.shape.name
/** Explicitly enrolled, locally exclusive management only. Never recursive deletion or arbitrary paths. */
export class BackupRetention {
  private readonly db: Database.Database
  private readonly registry: string
  private readonly control: z.infer<typeof RootSchema>
  constructor(registry: string) {
    this.registry = resolve(registry)
    safeDirectory(dirname(this.registry))
    if (lstatSync(this.registry).isSymbolicLink() || !lstatSync(this.registry).isFile())
      throw new Error('retention_registry_invalid')
    this.db = new Database(this.registry, { fileMustExist: true })
    try {
      const row = this.db.prepare('SELECT value FROM retention_root WHERE id=1').get() as
        { value: unknown } | undefined
      if (typeof row?.value !== 'string') throw new Error('retention_registry_invalid')
      this.control = RootSchema.parse(JSON.parse(row.value))
      this.rootReady()
    } catch (e) {
      this.db.close()
      throw e
    }
  }
  static registerRoot(root: string, registry: string) {
    root = resolve(root)
    registry = resolve(registry)
    if (contained(root, registry)) throw new Error('retention_registry_overlap')
    safeDirectory(dirname(root))
    safeDirectory(dirname(registry))
    if (absent(root)) mkdirSync(root, { mode: 0o700 })
    safeDirectory(root)
    const stat = lstatSync(root)
    if ((stat.mode & 0o022) !== 0 || (process.getuid && stat.uid !== process.getuid()))
      throw new Error('retention_root_permissions')
    const fd = openSync(
      registry,
      constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY | constants.O_NOFOLLOW,
      0o600,
    )
    closeSync(fd)
    const db = new Database(registry)
    try {
      db.pragma('journal_mode = DELETE')
      db.pragma('synchronous = FULL')
      db.exec(
        'CREATE TABLE retention_root(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE retention_backups(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL,value TEXT NOT NULL)',
      )
      db.prepare('INSERT INTO retention_root VALUES(1,?)').run(
        JSON.stringify({ version: 1, id: randomUUID(), root, ...identity(root) }),
      )
    } finally {
      db.close()
    }
    return new BackupRetention(registry)
  }
  close() {
    this.db.close()
  }
  private rootReady() {
    safeDirectory(this.control.root)
    const id = identity(this.control.root)
    const stat = lstatSync(this.control.root)
    if ((stat.mode & 0o022) !== 0 || (process.getuid && stat.uid !== process.getuid()))
      throw new Error('retention_root_permissions')
    if (
      id.device !== this.control.device ||
      id.inode !== this.control.inode ||
      contained(this.control.root, this.registry)
    )
      throw new Error('retention_root_identity_changed')
  }
  private locked<T>(work: () => T): T {
    const path = this.registry + '.lock'
    if (!absent(path)) {
      const s = lstatSync(path)
      if (!s.isFile() || s.isSymbolicLink()) throw new Error('retention_lock_invalid')
      const raw = readFileSync(path, 'utf8')
      let pid: number
      try {
        const value = JSON.parse(raw)
        pid = value.pid
        if (!Number.isSafeInteger(pid) || pid < 1) throw Error()
      } catch {
        throw new Error('retention_lock_invalid')
      }
      try {
        process.kill(pid, 0)
        throw new Error('retention_busy')
      } catch (e) {
        if ((e as NodeJS.ErrnoException).code !== 'ESRCH') throw e
      }
      if (lstatSync(path).ino !== s.ino) throw new Error('retention_busy')
      unlinkSync(path)
    }
    const fd = openSync(
      path,
      constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY | constants.O_NOFOLLOW,
      0o600,
    )
    const token = randomUUID()
    writeFileSync(fd, JSON.stringify({ pid: process.pid, token }))
    fsyncSync(fd)
    closeSync(fd)
    try {
      this.rootReady()
      return work()
    } finally {
      if (readFileSync(path, 'utf8') === JSON.stringify({ pid: process.pid, token }))
        unlinkSync(path)
    }
  }
  private registrations() {
    return (
      this.db.prepare('SELECT id,name,value FROM retention_backups ORDER BY name').all() as {
        id: string
        name: string
        value: unknown
      }[]
    ).map((row) => {
      if (typeof row.value !== 'string') throw new Error('retention_registry_invalid')
      const value = RegistrationSchema.parse(JSON.parse(row.value))
      if (row.id !== value.id || row.name !== value.name)
        throw new Error('retention_registry_invalid')
      return value
    })
  }
  private save(row: Registration) {
    this.db
      .prepare(
        'INSERT INTO retention_backups VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
      )
      .run(row.id, row.name, JSON.stringify(RegistrationSchema.parse(row)))
  }
  registerBackup(name: string, expiresAt: string) {
    return this.locked(() => {
      name = nameSchema.parse(name)
      if (this.registrations().length >= 10000) throw new Error('retention_capacity')
      if (this.registrations().some((r) => r.name === name))
        throw new Error('retention_already_registered')
      const inspected = inspectBackup(this.control.root, name)
      if (
        !Number.isFinite(Date.parse(expiresAt)) ||
        Date.parse(expiresAt) <= Date.parse(inspected.createdAt)
      )
        throw new Error('retention_expiry_invalid')
      const row = RegistrationSchema.parse({
        id: randomUUID(),
        name,
        ...inspected,
        expiresAt: new Date(expiresAt).toISOString(),
        state: 'retained',
        error: null,
        verifiedAt: null,
      })
      this.save(row)
      return row.id
    })
  }
  inventory(now = Date.now()) {
    return this.locked(() => this.report(now, true))
  }
  private report(now: number, dryRun: boolean) {
    return RetentionInventorySchema.parse({
      version: 1,
      rootId: this.control.id,
      coverage: 'registered_only',
      dryRun,
      items: this.registrations().map((row) => {
        let { id, name, createdAt, expiresAt, state, error, verifiedAt } = row
        try {
          const present = verifyRegistered(this.control.root, row, row.state === 'deletion_pending')
          if (!present) {
            state = 'verified_absent'
            verifiedAt = new Date(now).toISOString()
            error = null
          } else if (state === 'verified_absent') {
            state = 'unverifiable'
            error = 'identity_changed'
            verifiedAt = null
          }
        } catch (e) {
          state = 'unverifiable'
          error =
            (e as Error).message === 'retention_identity_changed'
              ? 'identity_changed'
              : 'unsafe_entry'
          verifiedAt = null
        }
        return {
          id,
          name,
          createdAt,
          expiresAt,
          state,
          error,
          verifiedAt,
          expired: Date.parse(expiresAt) <= now,
        }
      }),
    })
  }
  expire(options: { execute?: boolean; now?: number } = {}) {
    return this.locked(() => {
      const now = options.now ?? Date.now()
      if (!Number.isFinite(now)) throw new Error('retention_time_invalid')
      if (!options.execute) return this.report(now, true)
      for (const row of this.registrations()) {
        if (
          row.state === 'verified_absent' ||
          row.state === 'unverifiable' ||
          Date.parse(row.expiresAt) > now
        )
          continue
        try {
          const directory = join(this.control.root, row.name),
            partial = row.state === 'deletion_pending'
          if (!verifyRegistered(this.control.root, row, partial)) {
            row.state = 'verified_absent'
            row.verifiedAt = new Date(now).toISOString()
            row.error = null
            this.save(row)
            continue
          }
          row.state = 'deletion_pending'
          row.error = null
          this.save(row) // durable before the first filesystem effect
          for (const file of row.files
            .filter((f) => f.path !== 'manifest.json')
            .concat(row.files.filter((f) => f.path === 'manifest.json'))) {
            this.rootReady()
            const current = identity(directory)
            if (current.device !== row.directory.device || current.inode !== row.directory.inode)
              throw new Error('retention_identity_changed')
            const path = join(directory, file.path)
            if (!absent(path)) {
              verifyMember(directory, file)
              unlinkSync(path)
            }
          }
          if (!absent(join(directory, 'media'))) rmdirSync(join(directory, 'media'))
          rmdirSync(directory)
          const rootFd = openSync(
            this.control.root,
            constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW,
          )
          try {
            fsyncSync(rootFd)
          } finally {
            closeSync(rootFd)
          }
          if (!absent(directory)) throw new Error('retention_removal_incomplete')
          row.state = 'verified_absent'
          row.verifiedAt = new Date(now).toISOString()
          this.save(row)
        } catch (e) {
          row.state = 'unverifiable'
          row.error =
            (e as Error).message === 'retention_identity_changed'
              ? 'identity_changed'
              : (e as Error).message === 'retention_removal_incomplete'
                ? 'removal_incomplete'
                : 'unsafe_entry'
          this.save(row)
        }
      }
      return this.report(now, false)
    })
  }
}
