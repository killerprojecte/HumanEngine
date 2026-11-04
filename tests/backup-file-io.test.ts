import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import {
  mkdtemp,
  open,
  readFile,
  writeFile,
  rename,
  truncate,
  symlink,
  chmod,
  lstat,
  rm,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  copyRegularFile,
  hashRegularFile,
  readSmallRegularFile,
  MAX_MANIFEST_BYTES,
} from '../scripts/backup-file-io.ts'

const digest = (bytes: Buffer) => createHash('sha256').update(bytes).digest('hex')

test('bounded descriptor file operations retain full hashes, identity checks, and exclusive private copies', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'humanengine-stream-test-'))
  const source = join(root, 'source'),
    target = join(root, 'target')
  const bytes = Buffer.alloc(2 * 1024 * 1024 + 17, 37)
  const expected = { sha256: digest(bytes), bytes: bytes.length }
  const sample = await open(join(root, 'sample'), 'wx')
  const proto = Object.getPrototypeOf(sample)
  await sample.close()
  try {
    await t.test('multi-chunk and empty hashes; short writes are completed', async (t) => {
      await writeFile(source, bytes)
      const originalRead = proto.read,
        originalWrite = proto.write
      t.mock.method(
        proto,
        'read',
        async function (
          this: any,
          buffer: Buffer,
          offset: number,
          length: number,
          position: number,
        ) {
          assert.ok(length <= 1024 * 1024)
          return originalRead.call(this, buffer, offset, Math.min(length, 65537), position)
        },
      )
      t.mock.method(
        proto,
        'write',
        async function (
          this: any,
          buffer: Buffer,
          offset: number,
          length: number,
          position: number,
        ) {
          return originalWrite.call(this, buffer, offset, Math.min(length, 4099), position)
        },
      )
      assert.deepEqual(await hashRegularFile(source), expected)
      assert.deepEqual(await copyRegularFile(source, target, expected), expected)
      assert.deepEqual(await readFile(target), bytes)
      assert.equal((await lstat(target)).mode & 0o777, 0o600)
      await assert.rejects(copyRegularFile(source, target, expected), /EEXIST/)
      await writeFile(source, '')
      assert.deepEqual(await hashRegularFile(source), { sha256: digest(Buffer.alloc(0)), bytes: 0 })
    })
    await t.test('same-length tampering fails full hash comparison', async () => {
      await writeFile(source, Buffer.alloc(bytes.length, 38))
      await assert.rejects(
        copyRegularFile(source, join(root, 'tamper-copy'), expected),
        /backup_hash_mismatch/,
      )
    })
    for (const mutation of ['replace', 'truncate', 'append', 'rewrite'] as const) {
      await t.test(`rejects source ${mutation} during read`, async (t) => {
        await writeFile(source, bytes)
        const originalRead = proto.read
        let changed = false
        t.mock.method(proto, 'read', async function (this: any, ...args: any[]) {
          const result = await originalRead.apply(this, args)
          if (!changed) {
            changed = true
            if (mutation === 'replace') {
              await rename(source, join(root, 'old-source'))
              await writeFile(source, bytes)
            }
            if (mutation === 'truncate') await truncate(source, 1)
            if (mutation === 'append') await writeFile(source, 'extra', { flag: 'a' })
            if (mutation === 'rewrite') await writeFile(source, Buffer.alloc(bytes.length, 39))
          }
          return result
        })
        await assert.rejects(hashRegularFile(source), /file_changed_during_read/)
      })
    }
    await t.test(
      'destination replacement during reread cannot verify an unlinked fd',
      async (t) => {
        await writeFile(source, bytes)
        const destination = join(root, 'replaced-target')
        const originalRead = proto.read
        let reads = 0
        t.mock.method(proto, 'read', async function (this: any, ...args: any[]) {
          const result = await originalRead.apply(this, args)
          // Three data reads and one EOF read belong to the source.
          if (++reads === 5) {
            await rename(destination, join(root, 'old-target'))
            await writeFile(destination, bytes)
          }
          return result
        })
        await assert.rejects(
          copyRegularFile(source, destination, expected),
          /file_changed_during_read/,
        )
        assert.deepEqual(
          await readFile(destination),
          bytes,
          'helper never deletes a replaced target',
        )
      },
    )
    await t.test(
      'symlinks and directories fail closed; existing destinations stay intact',
      async () => {
        const link = join(root, 'link')
        await symlink(source, link)
        await assert.rejects(hashRegularFile(link), /regular_file_required/)
        await assert.rejects(hashRegularFile(root), /regular_file_required/)
        await assert.rejects(copyRegularFile(source, link, expected), /EEXIST/)
        assert.deepEqual(await readFile(source), bytes)
      },
    )
    await t.test(
      'small reads reject oversized sparse manifests and enforce key length/permissions',
      async () => {
        const manifest = join(root, 'manifest.json'),
          key = join(root, 'key')
        const file = await open(manifest, 'wx')
        await file.truncate(MAX_MANIFEST_BYTES + 1)
        await file.close()
        await assert.rejects(readSmallRegularFile(manifest, MAX_MANIFEST_BYTES), /file_too_large/)
        await writeFile(key, Buffer.alloc(33), { mode: 0o600 })
        await assert.rejects(readSmallRegularFile(key, 32, true), /secret_key_invalid/)
        await writeFile(key, Buffer.alloc(32))
        await chmod(key, 0o644)
        await assert.rejects(readSmallRegularFile(key, 32, true), /secret_key_permissions/)
        await chmod(key, 0o600)
        assert.equal((await readSmallRegularFile(key, 32, true)).length, 32)
      },
    )
    await t.test(
      'write failure closes both handles without taking cleanup ownership',
      async (t) => {
        const handles = new Set<{ fd: number }>()
        const originalRead = proto.read
        t.mock.method(proto, 'read', async function (this: any, ...args: any[]) {
          handles.add(this)
          return originalRead.apply(this, args)
        })
        t.mock.method(proto, 'write', async function (this: any) {
          handles.add(this)
          throw new Error('synthetic_write_failure')
        })
        await assert.rejects(
          copyRegularFile(source, join(root, 'write-failed'), expected),
          /synthetic_write_failure/,
        )
        assert.equal(handles.size, 2)
        for (const handle of handles) assert.equal(handle.fd, -1)
      },
    )
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

test('backup and restore failure cleanup preserve replacement staging directories', async (t) => {
  const { Store } = await import('../src/store.ts')
  const { metadata } = await import('../src/memory/recovery/ledger.ts')
  const { backup } = await import('../scripts/backup.ts')
  const { restore } = await import('../scripts/restore.ts')
  const { mkdir } = await import('node:fs/promises')
  const root = await mkdtemp(join(tmpdir(), 'humanengine-stream-cleanup-'))
  const database = join(root, 'source.sqlite')
  const store = new Store(database)
  const mediaRoot = join(root, 'source-media')
  const sample = await open(join(root, 'sample'), 'wx')
  const proto = Object.getPrototypeOf(sample)
  await sample.close()
  try {
    const goodBackup = join(root, 'good-backup')
    await backup({ database, mediaRoot, destination: goodBackup })
    for (const operation of ['backup', 'restore'] as const) {
      await t.test(operation, async (t) => {
        const destination = join(root, operation)
        const staging = operation === 'backup' ? destination : destination + '.quarantine'
        let replaced = false
        const original = proto[operation === 'backup' ? 'read' : 'write']
        t.mock.method(
          proto,
          operation === 'backup' ? 'read' : 'write',
          async function (this: any, ...args: any[]) {
            const result = await original.apply(this, args)
            if (!replaced) {
              replaced = true
              await rename(staging, staging + '.moved')
              await mkdir(staging)
              await writeFile(join(staging, 'sentinel'), 'unrelated-directory')
            }
            return result
          },
        )
        await assert.rejects(
          operation === 'backup'
            ? backup({ database, mediaRoot, destination })
            : restore({
                backupRoot: goodBackup,
                destination,
                authorityDirectory: metadata(store.db)!.authority,
              }),
        )
        assert.equal(replaced, true)
        assert.equal(await readFile(join(staging, 'sentinel'), 'utf8'), 'unrelated-directory')
      })
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('cleanup compares exact bigint directory identities beyond the safe integer range', async () => {
  const original = 2n ** 53n,
    replacement = original + 1n
  assert.equal(Number(original), Number(replacement), 'numeric inode checks would collide')
  for (const script of ['backup', 'restore']) {
    const source = await readFile(new URL(`../scripts/${script}.ts`, import.meta.url), 'utf8')
    const variable = script === 'backup' ? 'current' : 'staging'
    const initial = script === 'backup' ? 'destinationIdentity' : 'stagingIdentity'
    assert.ok(source.includes(`const ${initial} = await lstat(destination, { bigint: true })`))
    const start = source.indexOf(`    const ${variable} = await lstat(destination`)
    const end = source.indexOf(
      script === 'backup' ? '    throw error' : '    if (published)',
      start,
    )
    assert.ok(start >= 0 && end > start)
    const cleanup = new Function(
      'lstat',
      'rm',
      'destination',
      initial,
      `return (async () => { ${source.slice(start, end)} })()`,
    )
    let removed = false
    await cleanup(
      async (_path: string, options: unknown) => {
        assert.deepEqual(options, { bigint: true })
        return { dev: 7n, ino: replacement, isDirectory: () => true }
      },
      async () => {
        removed = true
      },
      'synthetic',
      { dev: 7n, ino: original },
    )
    assert.equal(removed, false, `${script} must preserve a different exact inode`)
  }
})
