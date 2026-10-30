import { build } from 'esbuild'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Ship the same offline enrollment/export implementation, not a second gate.
// Only the native SQLite binding remains external and resolves from the runtime
// server's node_modules. No TypeScript loader or development tree is shipped.
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const output = process.argv[2] ?? resolve(root, '.output/server/recovery-authority.mjs')
if (process.argv.length > 3) throw new Error('recovery_cli_build_usage')
await build({
  absWorkingDir: root,
  entryPoints: ['scripts/recovery-authority.ts'],
  outfile: output,
  bundle: true,
  platform: 'node',
  target: 'node24',
  format: 'esm',
  external: ['better-sqlite3'],
  sourcemap: false,
  logLevel: 'warning',
})
