import Database from 'better-sqlite3'
import { constants } from 'node:fs'
import { copyFile, mkdir, readFile, writeFile, lstat, rm, chmod } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

export interface BackupManifest {
  version: 1
  createdAt: string
  node: string
  files: { path: string; sha256: string; bytes: number }[]
}
export const digest = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
export async function regularFile(path: string): Promise<Buffer> {
  if (!(await lstat(path)).isFile()) throw new Error('regular_file_required')
  return readFile(path)
}
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
  let source: Database.Database | undefined
  let snapshot: Database.Database | undefined
  try {
    if (!(await lstat(options.database)).isFile()) throw new Error('regular_database_required')
    source = new Database(options.database, { readonly: true, fileMustExist: true })
    const databasePath = join(destination, 'humanengine.sqlite')
    await source.backup(databasePath)
    source.close()
    source = undefined
    await chmod(databasePath, 0o600)
    snapshot = new Database(databasePath, { readonly: true })
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
    const dbBytes = await regularFile(databasePath)
    manifest.files.push({
      path: 'humanengine.sqlite',
      sha256: digest(dbBytes),
      bytes: dbBytes.length,
    })
    if (hasSecrets) {
      const keyPath = join(dirname(options.database), '.secrets.key')
      const keyStat = await lstat(keyPath)
      if (!keyStat.isFile() || (keyStat.mode & 0o077) !== 0)
        throw new Error('secret_key_permissions')
      const key = await regularFile(keyPath)
      if (key.length !== 32) throw new Error('secret_key_invalid')
      await writeFile(join(destination, '.secrets.key'), key, { flag: 'wx', mode: 0o600 })
      manifest.files.push({ path: '.secrets.key', sha256: digest(key), bytes: key.length })
    }
    for (const { path, sha256 } of media) {
      const original = join(options.mediaRoot, path.slice('media/'.length))
      const bytes = await regularFile(original)
      if (digest(bytes) !== sha256) throw new Error('media_content_hash_mismatch')
      const target = join(destination, path)
      await copyFile(original, target, constants.COPYFILE_EXCL)
      await chmod(target, 0o600)
      const copied = await regularFile(target)
      if (digest(copied) !== digest(bytes)) throw new Error('media_changed_during_backup')
      manifest.files.push({ path, sha256: digest(copied), bytes: copied.length })
    }
    await writeFile(join(destination, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', {
      flag: 'wx',
      mode: 0o600,
    })
    return manifest
  } catch (error) {
    source?.close()
    snapshot?.close()
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
