/** Missing mandatory SQLite profile must fail before opening a DB/listening.
 * Positive virtual-table semantics are exercised by the actual projection tests. */
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const directory = mkdtempSync(join(tmpdir(), 'humanengine-untagged-'))
try {
  const binary = join(directory, 'humanengine')
  const built = spawnSync('go', ['build', '-tags=', '-o', binary, './cmd/humanengine'], {
    cwd: new URL('../', import.meta.url),
    encoding: 'utf8',
    stdio: 'pipe',
  })
  assert.equal(built.status, 0, `${built.error ?? ''}\n${built.stderr}`)
  const result = spawnSync(
    binary,
    [
      '--read-only-preview',
      '--database',
      join(directory, 'absent.sqlite'),
      '--listen',
      '127.0.0.1:0',
    ],
    { encoding: 'utf8', timeout: 10000 },
  )
  assert.equal(result.status, 1, `${result.error ?? ''}\n${result.stderr}`)
  assert.match(result.stderr, /sqlite_fts5_required/)
  assert.doesNotMatch(result.stdout, /listening|ready/i)
  console.log('PASS: untagged backend refuses startup before opening a database or listening')
} finally {
  rmSync(directory, { recursive: true, force: true })
}
