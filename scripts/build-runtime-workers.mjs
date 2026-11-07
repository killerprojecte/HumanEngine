import { build } from 'esbuild'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
await build({
  absWorkingDir: root,
  entryPoints: {
    'engine-owner': 'src/server/owner-worker.ts',
    'config-reader': 'src/server/config-worker.ts',
  },
  outdir: resolve(root, '.output/server'),
  outExtension: { '.js': '.mjs' },
  bundle: true,
  platform: 'node',
  target: 'node24',
  format: 'esm',
  banner: {
    js: "import { createRequire as __createRequire } from 'node:module'; const require = __createRequire(import.meta.url);",
  },
  external: ['better-sqlite3', 'sqlite-vec', 'sharp'],
  sourcemap: false,
  logLevel: 'warning',
})
