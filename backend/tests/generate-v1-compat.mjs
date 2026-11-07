/** Package the legacy V1 config vocabulary without changing its JavaScript semantics. */
import { stripTypeScriptTypes } from 'node:module'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
const source = readFileSync(new URL('../../src/plugins/config.ts', import.meta.url), 'utf8')
const hash = createHash('sha256').update(source).digest('hex')
if (
  process.version !== 'v24.22.1' ||
  hash !== 'd08f0a1f16f35d168acfac2014908bb71d857def88aebd5225cb5387a42391d6'
)
  throw Error('Review oracle changes before regenerating V1 compatibility code')
const target = new URL('../internal/plugins/v1-config.mjs', import.meta.url)
const generated =
  '// Generated from src/plugins/config.ts; SHA256 ' +
  hash +
  '\n' +
  stripTypeScriptTypes(source, { mode: 'transform' })
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== generated)
    throw Error('V1 compatibility source differs from legacy config.ts')
} else writeFileSync(target, generated)
