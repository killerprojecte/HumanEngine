import assert from 'node:assert/strict'
import test from 'node:test'
import * as fs from 'node:fs'
import { createHash } from 'node:crypto'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import ts from 'typescript'
import { hash, identity, inspectBackup, verifyMember } from '../src/backup-retention/filesystem.ts'

const source = fs.readFileSync(
  new URL('../src/backup-retention/filesystem.ts', import.meta.url),
  'utf8',
)
const start = source.indexOf('function regular('),
  end = source.indexOf('export function absent(', start)
assert.ok(start >= 0 && end > start)
// Exercise the actual private reader with deterministic fd failures/races. Large
// logical reads use a fake fd/hash sink, not large files or large retained buffers.
const code = ts.transpileModule(source.slice(start, end), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText
interface ReadResult {
  bytes: number
  sha256: string
  device: string
  inode: string
  content?: Buffer
}
const ports = {
  openSync: fs.openSync,
  closeSync: fs.closeSync,
  fstatSync: fs.fstatSync,
  lstatSync: fs.lstatSync,
  readSync: fs.readSync,
  constants: fs.constants,
  createHash,
  Buffer,
}
function reader(overrides: Partial<Record<keyof typeof ports, unknown>> = {}) {
  const values = { ...ports, ...overrides }
  return new Function(...Object.keys(values), `${code}; return regular`)(
    ...Object.values(values),
  ) as (path: string, contentLimit?: number, expectedBytes?: number) => ReadResult
}
const chunk = 256 * 1024
function synthetic(size: bigint, options: { shortRead?: number; extraBytes?: number } = {}) {
  const initial = {
    dev: 7n,
    ino: 9n,
    size,
    nlink: 1n,
    mtimeNs: 11n,
    ctimeNs: 13n,
    isFile: () => true,
  }
  let offset = 0,
    closes = 0,
    reads = 0,
    hashed = 0,
    maximumRequest = 0,
    maximumBuffer = 0
  const buffers = new Set<Buffer>()
  const substitutions = {
    openSync: (_path: string, flags: number) => {
      assert.equal(flags & fs.constants.O_NOFOLLOW, fs.constants.O_NOFOLLOW)
      assert.equal(flags & fs.constants.O_NONBLOCK, fs.constants.O_NONBLOCK)
      return 41
    },
    closeSync: (fd: number) => {
      assert.equal(fd, 41)
      closes++
    },
    fstatSync: () => ({ ...initial }),
    lstatSync: () => ({ ...initial }),
    readSync: (
      fd: number,
      buffer: Buffer,
      bufferOffset: number,
      length: number,
      position: null,
    ) => {
      assert.equal(fd, 41)
      assert.equal(position, null)
      assert.ok(bufferOffset + length <= buffer.length)
      reads++
      maximumRequest = Math.max(maximumRequest, length)
      maximumBuffer = Math.max(maximumBuffer, buffer.length)
      buffers.add(buffer)
      const available = Number(size) + (options.extraBytes ?? 0) - offset
      const count = Math.min(length, Math.max(0, available), options.shortRead ?? length)
      offset += count
      return count
    },
    createHash: () => ({
      update: (bytes: Buffer) => {
        hashed += bytes.length
      },
      digest: () => 'synthetic-hash',
    }),
  }
  return {
    initial,
    substitutions,
    run: (
      overrides: Partial<Record<keyof typeof ports, unknown>> = {},
      limit?: number,
      expectedBytes?: number,
    ) => reader({ ...substitutions, ...overrides })('synthetic', limit, expectedBytes),
    counts: () => ({
      offset,
      closes,
      reads,
      hashed,
      maximumRequest,
      maximumBuffer,
      buffers: buffers.size,
    }),
  }
}

test('retention streams a logical member larger than 2 GiB with one fixed data buffer', () => {
  const size = 2n ** 31n + 17n
  const f = synthetic(size)
  assert.deepEqual(f.run(), {
    bytes: Number(size),
    sha256: 'synthetic-hash',
    device: '7',
    inode: '9',
    content: undefined,
  })
  assert.deepEqual(f.counts(), {
    offset: Number(size),
    closes: 1,
    reads: Math.ceil(Number(size) / chunk) + 1,
    hashed: Number(size),
    maximumRequest: chunk,
    maximumBuffer: chunk,
    buffers: 2,
  })
})

test('retention tolerates short reads, accepts empty files and stops at the captured size', () => {
  for (const size of [0, 1, chunk + 19]) {
    const f = synthetic(BigInt(size), { shortRead: 37 })
    assert.equal(f.run().bytes, size)
    assert.equal(f.counts().hashed, size)
    assert.equal(f.counts().closes, 1)
  }
  const growing = synthetic(123n, { extraBytes: 100_000_000 })
  assert.throws(() => growing.run(), /retention_identity_changed/)
  assert.equal(growing.counts().offset, 124, 'one-byte probe never follows an appending writer')
  assert.equal(growing.counts().closes, 1)
})

test('known size mismatches fail before reading data; matching sizes still hash every byte', () => {
  for (const expected of [99, 101, -1, NaN, Number.MAX_SAFE_INTEGER + 1]) {
    const f = synthetic(100n)
    assert.throws(() => f.run({}, undefined, expected), /retention_identity_changed/)
    assert.equal(f.counts().reads, 0)
    assert.equal(f.counts().hashed, 0)
    assert.equal(f.counts().closes, 1)
  }
  const exact = synthetic(100n, { shortRead: 13 })
  assert.equal(exact.run({}, undefined, 100).bytes, 100)
  assert.equal(exact.counts().hashed, 100)
  assert.equal(exact.counts().closes, 1)
})

test('manifest allocation is bounded before reads and growth past the limit is rejected', () => {
  const limit = 32 * 1024 * 1024
  let allocations = 0
  const oversized = synthetic(BigInt(limit + 1))
  assert.throws(
    () =>
      oversized.run(
        {
          Buffer: {
            allocUnsafe: () => {
              allocations++
              throw new Error('unexpected_allocation')
            },
          },
        },
        limit,
      ),
    /retention_manifest_invalid/,
  )
  assert.equal(allocations, 0)
  assert.equal(oversized.counts().reads, 0)
  assert.equal(oversized.counts().closes, 1)
  const boundary = synthetic(BigInt(limit))
  assert.equal(boundary.run({}, limit).content!.length, limit)
  assert.equal(boundary.counts().maximumRequest, chunk)
  const grown = synthetic(BigInt(limit), { extraBytes: 1 })
  assert.throws(() => grown.run({}, limit), /retention_manifest_invalid/)
  assert.equal(grown.counts().offset, limit + 1)
  assert.equal(grown.counts().maximumBuffer, limit)
  assert.equal(grown.counts().closes, 1)
})

test('unsafe size or type, early EOF and all read/stat failures close the descriptor', () => {
  for (const size of [-1n, BigInt(Number.MAX_SAFE_INTEGER) + 1n]) {
    const f = synthetic(size)
    assert.throws(() => f.run(), /retention_unsafe_entry/)
    assert.equal(f.counts().reads, 0)
    assert.equal(f.counts().closes, 1)
  }
  for (const change of [{ isFile: () => false }, { nlink: 2n }]) {
    const f = synthetic(1n)
    assert.throws(
      () => f.run({ fstatSync: () => ({ ...f.initial, ...change }) }),
      /retention_unsafe_entry/,
    )
    assert.equal(f.counts().closes, 1)
  }
  const eof = synthetic(20n)
  assert.throws(() => eof.run({ readSync: () => 0 }), /retention_identity_changed/)
  assert.equal(eof.counts().closes, 1)
  for (const operation of ['fstatSync', 'readSync', 'lstatSync'] as const) {
    const f = synthetic(1n),
      failure = new Error(`synthetic-${operation}`)
    assert.throws(
      () =>
        f.run({
          [operation]: () => {
            throw failure
          },
        }),
      (error) => error === failure,
    )
    assert.equal(f.counts().closes, 1)
  }
  const missing = synthetic(1n)
  assert.throws(
    () =>
      missing.run({
        lstatSync: () => {
          throw Object.assign(new Error('missing'), { code: 'ENOENT' })
        },
      }),
    /retention_identity_changed/,
  )
  assert.equal(missing.counts().closes, 1)
})

test('same fd metadata drift and a replaced path fail even if the streamed bytes match', () => {
  for (const field of ['dev', 'ino', 'size', 'mtimeNs', 'ctimeNs', 'nlink'] as const) {
    for (const target of ['fd', 'path'] as const) {
      const f = synthetic(32n)
      let stats = 0
      const changed = { ...f.initial, [field]: f.initial[field] + 1n }
      assert.throws(
        () =>
          f.run(
            target === 'fd'
              ? { fstatSync: () => (++stats === 1 ? { ...f.initial } : changed) }
              : { lstatSync: () => changed },
          ),
        /retention_identity_changed/,
        `${target}/${field}`,
      )
      assert.equal(f.counts().closes, 1)
    }
  }
})

test('real retention hashing keeps exact bytes and rejects same-length changes, links and oversized manifests', () => {
  const directory = fs.mkdtempSync(join(tmpdir(), 'he-retention-stream-'))
  try {
    const name = 'humanengine.sqlite',
      path = join(directory, name)
    const bytes = Buffer.alloc(chunk * 2 + 137)
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 251
    fs.writeFileSync(path, bytes)
    const expected = { path: name, bytes: bytes.length, sha256: hash(bytes), ...identity(path) }
    assert.doesNotThrow(() => verifyMember(directory, expected))
    assert.equal(reader()(path).sha256, expected.sha256)
    assert.deepEqual(reader()(path, bytes.length).content, bytes)
    bytes[5] = bytes[5]! ^ 0xff
    fs.writeFileSync(path, bytes)
    let readBytes = 0
    const changed = reader({
      readSync: (...args: unknown[]) => {
        const count = Reflect.apply(fs.readSync, fs, args) as number
        readBytes += count
        return count
      },
    })(path, undefined, expected.bytes)
    assert.equal(readBytes, expected.bytes)
    assert.notEqual(changed.sha256, expected.sha256)
    assert.throws(() => verifyMember(directory, expected), /retention_identity_changed/)
    fs.linkSync(path, join(directory, 'hardlink'))
    assert.throws(() => reader()(path), /retention_unsafe_entry/)
    fs.unlinkSync(join(directory, 'hardlink'))
    fs.symlinkSync(path, join(directory, 'symlink'))
    assert.throws(() => reader()(join(directory, 'symlink')), /ELOOP/)
    const root = join(directory, 'managed'),
      backup = join(root, 'backup')
    fs.mkdirSync(backup, { recursive: true })
    const manifest = fs.openSync(join(backup, 'manifest.json'), 'w')
    try {
      fs.ftruncateSync(manifest, 32 * 1024 * 1024 + 1)
    } finally {
      fs.closeSync(manifest)
    }
    assert.throws(() => inspectBackup(root, 'backup'), /retention_manifest_invalid/)
  } finally {
    fs.rmSync(directory, { recursive: true, force: true })
  }
})
