import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

test('read-only container configuration retains an independent writable authority volume', () => {
  const compose = readFileSync(new URL('../compose.yaml', import.meta.url), 'utf8')
  const dockerfile = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8')
  assert.match(compose, /read_only: true/)
  assert.match(compose, /HUMANENGINE_DB: \/app\/data\/humanengine\.sqlite/)
  assert.match(compose, /HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY: \/app\/authority/)
  assert.match(compose, /- humanengine-data:\/app\/data/)
  assert.match(compose, /- humanengine-authority:\/app\/authority/)
  assert.match(compose, /^  humanengine-authority:\s*$/m)
  assert.match(dockerfile, /HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=\/app\/authority/)
  assert.match(dockerfile, /chown -R node:node[^\n]*\/app\/authority/)
  assert.match(dockerfile, /USER node/)
})
