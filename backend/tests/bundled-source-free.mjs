import assert from 'node:assert/strict'
import { mkdtempSync, copyFileSync, chmodSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
const root = mkdtempSync(join(tmpdir(), 'humanengine-go-bundle-'))
try {
  const binary = join(root, 'humanengine')
  copyFileSync(resolve(process.argv[2] ?? 'backend/bin/humanengine'), binary)
  chmodSync(binary, 0o700)
  const result = spawnSync(binary, ['--inspect-builtins'], {
    cwd: root,
    env: { PATH: '/no-node-or-go', HOME: root },
    encoding: 'utf8',
  })
  assert.equal(result.status, 0, result.stderr)
  const plugins = JSON.parse(result.stdout)
  assert.deepEqual(
    plugins.map((p) => p.id),
    ['typing-delay', 'web-read', 'web-search', 'snowluma'],
  )
  assert.ok(plugins.every((p) => p.apiVersion === 2))
  console.log(
    'PASS: standalone Go binary executes all four embedded WASM manifests with no source tree, Node or Go compiler on PATH',
  )
} finally {
  rmSync(root, { recursive: true, force: true })
}
