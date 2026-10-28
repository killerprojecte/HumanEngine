import { mkdir, writeFile, rm } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { Store } from '../src/store.ts'
import {
  checkIntegrity,
  digest,
  mediaFiles,
  mediaRecords,
  regularFile,
  type BackupManifest,
} from './backup.ts'

export async function restore(options: {
  backupRoot: string
  destination: string
}): Promise<{ database: string; mediaRoot: string; vectorVersion: string }> {
  const root = resolve(options.backupRoot),
    destination = resolve(options.destination)
  const manifest: BackupManifest = JSON.parse(
    (await regularFile(join(root, 'manifest.json'))).toString('utf8'),
  )
  if (
    manifest.version !== 1 ||
    !Array.isArray(manifest.files) ||
    !manifest.files.length ||
    manifest.files.length > 100000
  )
    throw new Error('invalid_backup_manifest')
  const paths = new Set<string>()
  for (const file of manifest.files) {
    if (
      !file ||
      typeof file.path !== 'string' ||
      !/^(humanengine\.sqlite|\.secrets\.key|media\/[a-f0-9]{64}\.(jpeg|png|gif|webp))$/.test(
        file.path,
      ) ||
      paths.has(file.path) ||
      !/^[a-f0-9]{64}$/.test(file.sha256) ||
      !Number.isSafeInteger(file.bytes) ||
      file.bytes < 0
    )
      throw new Error('invalid_backup_manifest')
    paths.add(file.path)
  }
  if (!paths.has('humanengine.sqlite')) throw new Error('backup_database_missing')
  await mkdir(destination, { mode: 0o700 })
  let store: Store | undefined
  try {
    await mkdir(join(destination, 'media'), { mode: 0o700 })
    for (const file of manifest.files) {
      const bytes = await regularFile(join(root, file.path))
      if (bytes.length !== file.bytes || digest(bytes) !== file.sha256)
        throw new Error('backup_hash_mismatch')
      await writeFile(join(destination, file.path), bytes, { flag: 'wx', mode: 0o600 })
    }
    const database = join(destination, 'humanengine.sqlite')
    store = new Store(database)
    checkIntegrity(store.db)
    for (const record of mediaRecords(store.db)) {
      if (digest(await regularFile(join(destination, record.path))) !== record.sha256)
        throw new Error('media_content_hash_mismatch')
    }
    const hasSecrets = !!store.db.prepare('SELECT 1 FROM secrets LIMIT 1').get()
    if (hasSecrets && !paths.has('.secrets.key')) throw new Error('secret_key_missing')
    if (hasSecrets) {
      const rows = store.db.prepare('SELECT namespace,name FROM secrets').all() as Array<{
        namespace: string
        name: string
      }>
      for (const row of rows) store.secrets.get(row.namespace, row.name)
    }
    const expected = [
      'humanengine.sqlite',
      ...(hasSecrets ? ['.secrets.key'] : []),
      ...mediaFiles(store.db),
    ].sort()
    if (JSON.stringify(expected) !== JSON.stringify([...paths].sort()))
      throw new Error('backup_media_manifest_mismatch')
    const vectorVersion = store.vectorVersion
    store.close()
    store = undefined
    return { database, mediaRoot: join(destination, 'media'), vectorVersion }
  } catch (error) {
    store?.close()
    await rm(destination, { recursive: true, force: true })
    throw error
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [backupRoot, destination] = process.argv.slice(2)
  if (!backupRoot || !destination || process.argv.length !== 4) {
    console.error('Usage: node --import tsx scripts/restore.ts BACKUP_DIRECTORY NEW_DATA_DIRECTORY')
    process.exitCode = 1
  } else
    restore({ backupRoot, destination })
      .then((result) =>
        console.log(
          `Restore verified; sqlite-vec ${result.vectorVersion}. Start manually after shutdown of the old instance.`,
        ),
      )
      .catch(() => {
        console.error(
          'Restore failed; existing destinations are never overwritten. Check manifest, hashes, and permissions.',
        )
        process.exitCode = 1
      })
}
