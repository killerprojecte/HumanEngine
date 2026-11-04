import { constants, type BigIntStats } from 'node:fs'
import { lstat, open, type FileHandle } from 'node:fs/promises'
import { createHash } from 'node:crypto'

const CHUNK_BYTES = 1024 * 1024
export const MAX_MANIFEST_BYTES = 32 * 1024 * 1024
export interface FileDigest {
  sha256: string
  bytes: number
}
const identical = (a: BigIntStats, b: BigIntStats) => a.dev === b.dev && a.ino === b.ino
const unchanged = (a: BigIntStats, b: BigIntStats) =>
  identical(a, b) && a.size === b.size && a.mtimeNs === b.mtimeNs && a.ctimeNs === b.ctimeNs

async function assertStable(path: string, handle: FileHandle, initial: BigIntStats) {
  const current = await handle.stat({ bigint: true })
  const named = await lstat(path, { bigint: true })
  if (!named.isFile() || !unchanged(initial, current) || !unchanged(initial, named))
    throw new Error('file_changed_during_read')
}
async function withRegularFile<T>(
  path: string,
  work: (handle: FileHandle, stat: BigIntStats) => Promise<T>,
): Promise<T> {
  const named = await lstat(path, { bigint: true })
  if (!named.isFile()) throw new Error('regular_file_required')
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const initial = await handle.stat({ bigint: true })
    if (!initial.isFile() || !unchanged(named, initial)) throw new Error('regular_file_required')
    if (initial.size > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('file_too_large')
    const result = await work(handle, initial)
    await assertStable(path, handle, initial)
    return result
  } finally {
    await handle.close()
  }
}

// Read exactly the captured extent, then probe EOF. A growing file cannot make this loop unbounded.
async function scan(
  handle: FileHandle,
  bytes: number,
  consume: (chunk: Buffer, offset: number) => Promise<void>,
) {
  const buffer = Buffer.allocUnsafe(CHUNK_BYTES)
  let offset = 0
  while (offset < bytes) {
    const { bytesRead } = await handle.read(
      buffer,
      0,
      Math.min(buffer.length, bytes - offset),
      offset,
    )
    if (!bytesRead) throw new Error('file_changed_during_read')
    await consume(buffer.subarray(0, bytesRead), offset)
    offset += bytesRead
  }
  if ((await handle.read(buffer, 0, 1, offset)).bytesRead)
    throw new Error('file_changed_during_read')
}
async function hashHandle(handle: FileHandle, bytes: number): Promise<FileDigest> {
  const hash = createHash('sha256')
  await scan(handle, bytes, async (chunk) => {
    hash.update(chunk)
  })
  return { sha256: hash.digest('hex'), bytes }
}

export async function hashRegularFile(path: string): Promise<FileDigest> {
  return withRegularFile(path, (handle, stat) => hashHandle(handle, Number(stat.size)))
}

export async function readSmallRegularFile(
  path: string,
  maxBytes: number,
  privateKey = false,
): Promise<Buffer> {
  return withRegularFile(path, async (handle, stat) => {
    if (privateKey && (stat.mode & 0o077n) !== 0n) throw new Error('secret_key_permissions')
    if (stat.size > BigInt(maxBytes))
      throw new Error(privateKey ? 'secret_key_invalid' : 'file_too_large')
    if (privateKey && stat.size !== 32n) throw new Error('secret_key_invalid')
    const result = Buffer.alloc(Number(stat.size))
    await scan(handle, result.length, async (chunk, offset) => {
      chunk.copy(result, offset)
    })
    return result
  })
}

// The source stays open throughout copy and verification. Metadata checks detect ordinary
// concurrent mutation; neither streaming nor these checks freeze a hostile mutable source.
export async function copyRegularFile(
  source: string,
  destination: string,
  expected: { sha256: string; bytes?: number },
  mismatch = 'backup_hash_mismatch',
): Promise<FileDigest> {
  let output: FileHandle | undefined
  try {
    return await withRegularFile(source, async (input, stat) => {
      const bytes = Number(stat.size)
      if (expected.bytes !== undefined && bytes !== expected.bytes) throw new Error(mismatch)
      output = await open(
        destination,
        constants.O_RDWR | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
        0o600,
      )
      const hash = createHash('sha256')
      await scan(input, bytes, async (chunk, offset) => {
        hash.update(chunk)
        let written = 0
        while (written < chunk.length) {
          const { bytesWritten } = await output!.write(
            chunk,
            written,
            chunk.length - written,
            offset + written,
          )
          if (!bytesWritten) throw new Error('file_copy_failed')
          written += bytesWritten
        }
      })
      const sha256 = hash.digest('hex')
      if (sha256 !== expected.sha256) throw new Error(mismatch)
      const copiedStat = await output.stat({ bigint: true })
      const copied = await hashHandle(output, bytes)
      if (copied.sha256 !== sha256) throw new Error('file_copy_hash_mismatch')
      await assertStable(destination, output, copiedStat)
      return copied
    })
  } finally {
    await output?.close()
  }
}
