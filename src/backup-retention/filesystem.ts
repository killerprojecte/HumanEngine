import {
  constants,
  lstatSync,
  readSync,
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
/** Stream member hashes; only explicitly bounded manifests retain their bytes. */
function regular(path: string, contentLimit?: number, expectedBytes?: number) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const s = fstatSync(fd, { bigint: true })
    if (!s.isFile() || s.nlink !== 1n) throw new Error('retention_unsafe_entry')
    if (s.size < 0n || s.size > BigInt(Number.MAX_SAFE_INTEGER))
      throw new Error('retention_unsafe_entry')
    if (contentLimit !== undefined && s.size > BigInt(contentLimit))
      throw new Error('retention_manifest_invalid')
    if (
      expectedBytes !== undefined &&
      (!Number.isSafeInteger(expectedBytes) ||
        expectedBytes < 0 ||
        s.size !== BigInt(expectedBytes))
    )
      throw new Error('retention_identity_changed')
    const expected = Number(s.size)
    const content = contentLimit === undefined ? undefined : Buffer.allocUnsafe(expected)
    const buffer = content ?? Buffer.allocUnsafe(256 * 1024)
    const digest = createHash('sha256')
    let bytes = 0
    while (bytes < expected) {
      const offset = content === undefined ? 0 : bytes
      const count = readSync(fd, buffer, offset, Math.min(256 * 1024, expected - bytes), null)
      if (!count) throw new Error('retention_identity_changed')
      digest.update(buffer.subarray(offset, offset + count))
      bytes += count
    }
    // Bound work to the initial size, but reject growth rather than hashing a prefix.
    if (readSync(fd, Buffer.allocUnsafe(1), 0, 1, null)) {
      if (contentLimit !== undefined && bytes >= contentLimit)
        throw new Error('retention_manifest_invalid')
      throw new Error('retention_identity_changed')
    }
    const unchanged = (current: typeof s) =>
      current.isFile() &&
      current.nlink === 1n &&
      current.dev === s.dev &&
      current.ino === s.ino &&
      current.size === s.size &&
      current.mtimeNs === s.mtimeNs &&
      current.ctimeNs === s.ctimeNs
    if (!unchanged(fstatSync(fd, { bigint: true }))) throw new Error('retention_identity_changed')
    try {
      if (!unchanged(lstatSync(path, { bigint: true })))
        throw new Error('retention_identity_changed')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT')
        throw new Error('retention_identity_changed')
      throw error
    }
    return {
      bytes,
      sha256: digest.digest('hex'),
      device: String(s.dev),
      inode: String(s.ino),
      content,
    }
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
  const manifest = regular(join(directory, 'manifest.json'), 32 * 1024 * 1024)
  const value = BackupManifestSchema.parse(JSON.parse(manifest.content!.toString('utf8')))
  const files = [
    ...value.files,
    { path: 'manifest.json', sha256: manifest.sha256, bytes: manifest.bytes },
  ].map((f) => {
    if (f.path.startsWith('media/')) safeDirectory(join(directory, 'media'))
    const actual = regular(join(directory, f.path), undefined, f.bytes)
    if (actual.bytes !== f.bytes || actual.sha256 !== f.sha256)
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
    manifestHash: manifest.sha256,
    directory: identity(directory),
    files,
  }
}
function checkEntries(directory: string, allowed: readonly string[]) {
  const members = new Set(allowed)
  for (const entry of readdirSync(directory)) {
    if (entry === 'media') {
      safeDirectory(join(directory, 'media'))
      for (const media of readdirSync(join(directory, 'media')))
        if (!members.has('media/' + media)) throw new Error('retention_unsafe_entry')
    } else if (!members.has(entry)) throw new Error('retention_unsafe_entry')
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
  const actual = regular(join(directory, f.path), undefined, f.bytes)
  if (
    actual.device !== f.device ||
    actual.inode !== f.inode ||
    actual.bytes !== f.bytes ||
    actual.sha256 !== f.sha256
  )
    throw new Error('retention_identity_changed')
}
