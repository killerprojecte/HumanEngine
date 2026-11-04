import { metadata, RecoveryAuthority, sameHead } from '../src/memory/recovery/ledger.ts'
import Database from 'better-sqlite3'
import { mkdir, writeFile, lstat, rm, chmod } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { copyRegularFile, hashRegularFile, readSmallRegularFile } from './backup-file-io.ts'

export interface BackupManifest {
  version: 1
  createdAt: string
  node: string
  files: { path: string; sha256: string; bytes: number }[]
}
export const digest = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
export function mediaRecords(db: Database.Database): { path: string; sha256: string }[] {
  if (!db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='meme_assets'").get())
    return []
  return (db.prepare('SELECT id,value FROM meme_assets').all() as { id: string; value: string }[])
    .map((row) => {
      const asset = JSON.parse(row.value)
      if (
        !/^[a-f0-9]{64}$/.test(row.id) ||
        asset.id !== row.id ||
        !['jpeg', 'png', 'gif', 'webp'].includes(asset.format) ||
        !/^[a-f0-9]{64}$/.test(asset.contentHash ?? row.id)
      )
        throw new Error('invalid_media_record')
      return { path: `media/${row.id}.${asset.format}`, sha256: asset.contentHash ?? row.id }
    })
    .sort((a, b) => a.path.localeCompare(b.path))
}
export function mediaFiles(db: Database.Database): string[] {
  return mediaRecords(db).map((record) => record.path)
}
export function checkIntegrity(db: Database.Database) {
  const results = db.pragma('integrity_check') as { integrity_check: string }[]
  if (
    results.length !== 1 ||
    results[0]?.integrity_check !== 'ok' ||
    (db.pragma('foreign_key_check') as unknown[]).length
  )
    throw new Error('database_integrity_failed')
}
export async function backup(options: {
  database: string
  mediaRoot: string
  destination: string
}): Promise<BackupManifest> {
  const destination = resolve(options.destination)
  await mkdir(destination, { mode: 0o700 })
  const destinationIdentity = await lstat(destination, { bigint: true })
  let source: Database.Database | undefined
  let snapshot: Database.Database | undefined
  try {
    if (!(await lstat(options.database)).isFile()) throw new Error('regular_database_required')
    source = new Database(options.database, { readonly: true, fileMustExist: true })
    const gate = metadata(source)
    if (!gate || gate.state !== 'ready') throw new Error('recovery_legacy_enrollment_required')
    const authority = new RecoveryAuthority(gate.authority)
    try {
      if (!sameHead(gate.head, authority.head())) throw new Error('recovery_authority_head_changed')
    } finally {
      authority.close()
    }
    const databasePath = join(destination, 'humanengine.sqlite')
    await source.backup(databasePath)
    source.close()
    source = undefined
    await chmod(databasePath, 0o600)
    // Normalize only the copied snapshot; the live database keeps its existing WAL mode.
    snapshot = new Database(databasePath)
    snapshot.pragma('journal_mode = DELETE')
    checkIntegrity(snapshot)
    const media = mediaRecords(snapshot)
    const hasSecrets =
      !!snapshot
        .prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='secrets'")
        .get() && !!snapshot.prepare('SELECT 1 FROM secrets LIMIT 1').get()
    snapshot.close()
    snapshot = undefined
    await mkdir(join(destination, 'media'), { mode: 0o700 })
    const manifest: BackupManifest = {
      version: 1,
      createdAt: new Date().toISOString(),
      node: process.version,
      files: [],
    }
    const dbDigest = await hashRegularFile(databasePath)
    manifest.files.push({
      path: 'humanengine.sqlite',
      ...dbDigest,
    })
    if (hasSecrets) {
      const keyPath = join(dirname(options.database), '.secrets.key')
      const keyStat = await lstat(keyPath)
      if (!keyStat.isFile() || (keyStat.mode & 0o077) !== 0)
        throw new Error('secret_key_permissions')
      const key = await readSmallRegularFile(keyPath, 32, true)
      if (key.length !== 32) throw new Error('secret_key_invalid')
      await writeFile(join(destination, '.secrets.key'), key, { flag: 'wx', mode: 0o600 })
      manifest.files.push({ path: '.secrets.key', sha256: digest(key), bytes: key.length })
    }
    for (const { path, sha256 } of media) {
      const original = join(options.mediaRoot, path.slice('media/'.length))
      const target = join(destination, path)
      const copied = await copyRegularFile(
        original,
        target,
        { sha256 },
        'media_content_hash_mismatch',
      )
      manifest.files.push({ path, ...copied })
    }
    await writeFile(join(destination, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', {
      flag: 'wx',
      mode: 0o600,
    })
    return manifest
  } catch (error) {
    source?.close()
    snapshot?.close()
    const current = await lstat(destination, { bigint: true }).catch(() => undefined)
    if (
      current?.isDirectory() &&
      current.dev === destinationIdentity.dev &&
      current.ino === destinationIdentity.ino
    )
      await rm(destination, { recursive: true, force: true })
    throw error
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [database, mediaRoot, destination] = process.argv.slice(2)
  if (!database || !mediaRoot || !destination || process.argv.length !== 5) {
    console.error(
      'Usage: node --import tsx scripts/backup.ts DATABASE MEDIA_ROOT NEW_BACKUP_DIRECTORY',
    )
    process.exitCode = 1
  } else
    backup({ database, mediaRoot, destination })
      .then((m) => console.log(`Backup verified: ${m.files.length} files`))
      .catch(() => {
        console.error(
          'Backup failed; source unchanged. Check paths, media consistency, and permissions.',
        )
        process.exitCode = 1
      })
}
