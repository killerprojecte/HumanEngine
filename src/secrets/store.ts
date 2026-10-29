import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto'
import {
  constants,
  openSync,
  closeSync,
  readFileSync,
  writeFileSync,
  fstatSync,
  fsyncSync,
} from 'node:fs'
import type Database from 'better-sqlite3'
import { assertDomainReady } from '../memory/recovery/ledger.ts'
import { advanceSecurityState } from '../memory/recovery/security-state.ts'

export type SecretMutation =
  { action: 'preserve' } | { action: 'clear' } | { action: 'set'; value: string }

/** Ciphertext only in SQLite; the installation key lives outside the database. */
export class SecretStore {
  private key?: Buffer
  constructor(
    private readonly db: Database.Database,
    private readonly keyPath?: string,
  ) {
    db.exec(
      'CREATE TABLE IF NOT EXISTS secrets (namespace TEXT NOT NULL, name TEXT NOT NULL, ciphertext TEXT NOT NULL, PRIMARY KEY(namespace,name))',
    )
  }
  private encryptionKey(): Buffer {
    if (this.key) return this.key
    if (!this.keyPath) return (this.key = randomBytes(32))
    let fd: number
    try {
      fd = openSync(this.keyPath, constants.O_RDONLY | constants.O_NOFOLLOW)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT')
        throw new Error('secret_key_unavailable')
      // Never silently replace a lost key, even when adding a new credential.
      if (this.db.prepare('SELECT 1 FROM secrets LIMIT 1').get())
        throw new Error('secret_key_missing')
      try {
        fd = openSync(
          this.keyPath,
          constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
          0o600,
        )
        try {
          writeFileSync(fd, randomBytes(32))
          fsyncSync(fd)
        } finally {
          closeSync(fd)
        }
      } catch (cause) {
        if ((cause as NodeJS.ErrnoException).code !== 'EEXIST')
          throw new Error('secret_key_unavailable')
      }
      fd = openSync(this.keyPath, constants.O_RDONLY | constants.O_NOFOLLOW)
    }
    try {
      const stat = fstatSync(fd)
      if (!stat.isFile() || (stat.mode & 0o077) !== 0) throw new Error('secret_key_permissions')
      const key = readFileSync(fd)
      if (key.length !== 32) throw new Error('secret_key_invalid')
      return (this.key = key)
    } finally {
      closeSync(fd)
    }
  }
  /** Server-only: scrub known credentials from optional diagnostics. Never serialize this list. */
  redactionValues(): string[] {
    const rows = this.db.prepare('SELECT namespace,name FROM secrets').all() as Array<{
      namespace: string
      name: string
    }>
    return rows.map((row) => this.get(row.namespace, row.name)!).filter(Boolean)
  }
  has(namespace: string, name: string): boolean {
    assertDomainReady(this.db)
    return !!this.db
      .prepare('SELECT 1 FROM secrets WHERE namespace=? AND name=?')
      .get(namespace, name)
  }
  get(namespace: string, name: string): string | undefined {
    assertDomainReady(this.db)
    const row = this.db
      .prepare('SELECT ciphertext FROM secrets WHERE namespace=? AND name=?')
      .get(namespace, name) as { ciphertext: string } | undefined
    if (!row) return undefined
    const key = this.encryptionKey()
    try {
      const [version, iv, tag, ciphertext] = row.ciphertext.split('.')
      if (version !== 'v1' || !iv || !tag || !ciphertext) throw new Error()
      const decipher = createDecipheriv('aes-256-gcm', key, Buffer.from(iv, 'base64'))
      decipher.setAAD(Buffer.from(JSON.stringify([namespace, name])))
      decipher.setAuthTag(Buffer.from(tag, 'base64'))
      return Buffer.concat([
        decipher.update(Buffer.from(ciphertext, 'base64')),
        decipher.final(),
      ]).toString('utf8')
    } catch {
      throw new Error('secret_decryption_failed')
    }
  }
  apply(namespace: string, name: string, mutation?: SecretMutation): void {
    assertDomainReady(this.db)
    if (!mutation || mutation.action === 'preserve') return
    if (mutation.action === 'clear') {
      if (!this.has(namespace, name)) return
      this.db.transaction(() => {
        advanceSecurityState(this.db)
        this.db.prepare('DELETE FROM secrets WHERE namespace=? AND name=?').run(namespace, name)
      })()
      return
    }
    if (
      mutation.action !== 'set' ||
      typeof mutation.value !== 'string' ||
      !mutation.value.trim() ||
      mutation.value.length > 16384
    )
      throw new Error('invalid_secret')
    const iv = randomBytes(12)
    const cipher = createCipheriv('aes-256-gcm', this.encryptionKey(), iv)
    cipher.setAAD(Buffer.from(JSON.stringify([namespace, name])))
    const encrypted = Buffer.concat([cipher.update(mutation.value, 'utf8'), cipher.final()])
    const ciphertext = [
      'v1',
      iv.toString('base64'),
      cipher.getAuthTag().toString('base64'),
      encrypted.toString('base64'),
    ].join('.')
    this.db.transaction(() => {
      advanceSecurityState(this.db)
      this.db
        .prepare(
          'INSERT INTO secrets(namespace,name,ciphertext) VALUES(?,?,?) ON CONFLICT(namespace,name) DO UPDATE SET ciphertext=excluded.ciphertext',
        )
        .run(namespace, name, ciphertext)
    })()
  }
}
