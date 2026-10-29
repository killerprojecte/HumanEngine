import {
  constants,
  lstatSync,
  readFileSync,
  readdirSync,
  realpathSync,
  openSync,
  closeSync,
  fstatSync,
} from 'node:fs'
import { createHash } from 'node:crypto'
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import Database from 'better-sqlite3'
import { metadata } from '../memory/recovery/ledger.ts'
import { BackupManifestSchema, type Registration } from './contracts.ts'
export const hash = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
export const identity = (path: string) => {
  const s = lstatSync(path, { bigint: true })
  return { device: String(s.dev), inode: String(s.ino) }
}
export function safeDirectory(path: string): string {
  const absolute = resolve(path)
  let cursor = absolute
  for (;;) {
    const stat = lstatSync(cursor)
    if (stat.isSymbolicLink() || !stat.isDirectory()) throw new Error('retention_unsafe_path')
    const parent = dirname(cursor)
    if (parent === cursor) break
    cursor = parent
  }
  return realpathSync(absolute)
}
export function contained(parent: string, path: string) {
  const rel = relative(parent, path)
  return rel === '' || (!rel.startsWith('..' + sep) && rel !== '..' && !isAbsolute(rel))
}
function regular(path: string) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW)
  try {
    const s = fstatSync(fd, { bigint: true })
    if (!s.isFile() || s.nlink !== 1n) throw new Error('retention_unsafe_entry')
    const bytes = readFileSync(fd)
    return { bytes, device: String(s.dev), inode: String(s.ino) }
  } finally {
    closeSync(fd)
  }
}
export function absent(path: string) {
  try {
    lstatSync(path)
    return false
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === 'ENOENT') return true
    throw e
  }
}
export function inspectBackup(root: string, name: string) {
  const directory = join(root, name)
  safeDirectory(directory)
  const manifest = regular(join(directory, 'manifest.json'))
  if (manifest.bytes.length > 32 * 1024 * 1024) throw new Error('retention_manifest_invalid')
  const value = BackupManifestSchema.parse(JSON.parse(manifest.bytes.toString('utf8')))
  const files = [
    ...value.files,
    { path: 'manifest.json', sha256: hash(manifest.bytes), bytes: manifest.bytes.length },
  ].map((f) => {
    if (f.path.startsWith('media/')) safeDirectory(join(directory, 'media'))
    const actual = regular(join(directory, f.path))
    if (actual.bytes.length !== f.bytes || hash(actual.bytes) !== f.sha256)
      throw new Error('retention_identity_changed')
    return { ...f, device: actual.device, inode: actual.inode }
  })
  checkEntries(
    directory,
    files.map((f) => f.path),
  )
  const db = new Database(join(directory, 'humanengine.sqlite'), {
    readonly: true,
    fileMustExist: true,
  })
  try {
    const gate = metadata(db)
    if (!gate) throw new Error('retention_backup_identity_missing')
    const authority = safeDirectory(gate.authority)
    if (contained(root, authority) || contained(authority, root))
      throw new Error('retention_authority_overlap')
  } finally {
    db.close()
  }
  return {
    createdAt: value.createdAt,
    manifestHash: hash(manifest.bytes),
    directory: identity(directory),
    files,
  }
}
function checkEntries(directory: string, allowed: readonly string[]) {
  for (const entry of readdirSync(directory)) {
    if (entry === 'media') {
      safeDirectory(join(directory, 'media'))
      for (const media of readdirSync(join(directory, 'media')))
        if (!allowed.includes('media/' + media)) throw new Error('retention_unsafe_entry')
    } else if (!allowed.includes(entry)) throw new Error('retention_unsafe_entry')
  }
}
/** Pending permits already removed members, but never replacement files or new members. */
export function verifyRegistered(root: string, r: Registration, partial = false) {
  const directory = join(root, r.name)
  if (absent(directory)) return false
  safeDirectory(directory)
  const id = identity(directory)
  if (id.device !== r.directory.device || id.inode !== r.directory.inode)
    throw new Error('retention_identity_changed')
  checkEntries(
    directory,
    r.files.map((f) => f.path),
  )
  for (const f of r.files) {
    const path = join(directory, f.path)
    if (absent(path)) {
      if (partial) continue
      throw new Error('retention_identity_changed')
    }
    verifyMember(directory, f)
  }
  return true
}

export function verifyMember(directory: string, f: Registration['files'][number]) {
  if (f.path.startsWith('media/')) safeDirectory(join(directory, 'media'))
  const actual = regular(join(directory, f.path))
  if (
    actual.device !== f.device ||
    actual.inode !== f.inode ||
    actual.bytes.length !== f.bytes ||
    hash(actual.bytes) !== f.sha256
  )
    throw new Error('retention_identity_changed')
}
