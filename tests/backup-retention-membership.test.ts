import assert from 'node:assert/strict'
import test from 'node:test'
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  unlinkSync,
  writeFileSync,
  symlinkSync,
} from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import ts from 'typescript'
import { hash, identity, verifyRegistered } from '../src/backup-retention/filesystem.ts'
import type { Registration } from '../src/backup-retention/contracts.ts'

test('registered membership retains partial deletion, fresh unknown members and identity checks', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-retention-membership-'))
  const name = 'synthetic',
    directory = join(root, name)
  mkdirSync(directory)
  mkdirSync(join(directory, 'media'))
  const paths = ['manifest.json', 'humanengine.sqlite', `media/${'a'.repeat(64)}.png`]
  for (const path of paths) writeFileSync(join(directory, path), path)
  const registration: Registration = {
    id: '00000000-0000-4000-8000-000000000000',
    name,
    createdAt: '2026-01-01T00:00:00Z',
    expiresAt: '2026-01-02T00:00:00Z',
    manifestHash: hash(Buffer.from('synthetic')),
    directory: identity(directory),
    files: paths.map((path) => {
      const bytes = readFileSync(join(directory, path))
      return { path, bytes: bytes.length, sha256: hash(bytes), ...identity(join(directory, path)) }
    }),
    state: 'retained',
    error: null,
    verifiedAt: null,
  }
  try {
    assert.equal(verifyRegistered(root, registration), true)
    // Duplicates do not change membership; the later identity checks still run.
    assert.equal(
      verifyRegistered(root, {
        ...registration,
        files: [...registration.files, registration.files[0]!],
      }),
      true,
    )
    unlinkSync(join(directory, paths[2]!))
    assert.throws(() => verifyRegistered(root, registration), /retention_identity_changed/)
    assert.equal(verifyRegistered(root, registration, true), true)
    // An empty media directory remains safe even though the directory itself is not a member.
    writeFileSync(join(directory, 'media', 'unexpected.png'), 'synthetic')
    assert.throws(() => verifyRegistered(root, registration, true), /retention_unsafe_entry/)
    unlinkSync(join(directory, 'media', 'unexpected.png'))
    writeFileSync(join(directory, 'unexpected.txt'), 'synthetic')
    assert.throws(() => verifyRegistered(root, registration, true), /retention_unsafe_entry/)
    unlinkSync(join(directory, 'unexpected.txt'))
    rmSync(join(directory, 'media'), { recursive: true })
    mkdirSync(join(root, 'outside'))
    symlinkSync(join(root, 'outside'), join(directory, 'media'))
    assert.throws(() => verifyRegistered(root, registration, true), /retention_unsafe_path/)
    unlinkSync(join(directory, 'media'))
    mkdirSync(join(directory, 'media'))
    writeFileSync(join(directory, 'humanengine.sqlite'), 'changed')
    assert.throws(() => verifyRegistered(root, registration, true), /retention_identity_changed/)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('actual directory membership loop builds one bounded set and performs one lookup per file', () => {
  const source = readFileSync(
    new URL('../src/backup-retention/filesystem.ts', import.meta.url),
    'utf8',
  )
  const start = source.indexOf('function checkEntries('),
    end = source.indexOf('/** Pending permits', start)
  assert.ok(start >= 0 && end > start)
  const code = ts.transpileModule(source.slice(start, end), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  const run = (count: number) => {
    let constructions = 0,
      inputs = 0,
      lookups = 0
    class CountedSet<T> extends Set<T> {
      constructor(values: readonly T[]) {
        constructions++
        inputs += values.length
        super(values)
      }
      override has(value: T) {
        lookups++
        return super.has(value)
      }
    }
    const media = Array.from({ length: count }, (_, index) => `${index}.png`)
    const allowed = ['manifest.json', ...media.map((name) => `media/${name}`)]
    const visited: string[] = []
    const check = new Function(
      'readdirSync',
      'safeDirectory',
      'join',
      'Set',
      `${code};return checkEntries`,
    )(
      (path: string) => (path === 'root' ? ['manifest.json', 'media'] : media),
      (path: string) => visited.push(path),
      join,
      CountedSet,
    ) as (directory: string, allowed: string[]) => void
    check('root', allowed)
    assert.equal(constructions, 1)
    assert.equal(inputs, count + 1)
    assert.equal(lookups, count + 1)
    assert.deepEqual(visited, ['root/media'])
    allowed.pop()
    assert.throws(() => check('root', allowed), /retention_unsafe_entry/)
    assert.equal(constructions, 2, 'a new call must rebuild membership from current input')
  }
  run(1_000)
  run(100_000)
})
