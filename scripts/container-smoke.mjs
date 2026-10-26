import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Run only against a locally loaded candidate image. Never build, pull, or publish here.
const image = process.argv[2] || process.env.HUMANENGINE_IMAGE
if (!image) throw new Error('Pass the locally built candidate image as the first argument')
const cwd = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const project = `humanengine-smoke-${randomBytes(6).toString('hex')}`
const token = randomBytes(32).toString('hex')
const env = {
  ...process.env,
  HUMANENGINE_IMAGE: image,
  HUMANENGINE_ENV_FILE: `/tmp/${project}-no-env-file`,
  HUMANENGINE_ADMIN_TOKEN: token,
  HUMANENGINE_PORT: '0',
  HUMANENGINE_PLUGINS: '',
  OPENAI_API_KEY: '',
  SNOWLUMA_TOKEN: '',
}
function docker(args) {
  try {
    return execFileSync('docker', args, {
      cwd,
      env,
      encoding: 'utf8',
      timeout: 180_000,
      stdio: ['ignore', 'pipe', 'pipe'],
    }).trim()
  } catch {
    // Do not serialize child environments or inspect output containing credentials.
    throw new Error(`Container smoke command failed: docker ${args.slice(0, 3).join(' ')}`)
  }
}
const compose = (...args) => docker(['compose', '-f', 'compose.yaml', '-p', project, ...args])
let base = ''
async function api(path, options = {}) {
  return fetch(`${base}${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
      ...options.headers,
    },
    signal: AbortSignal.timeout(5000),
  })
}
function container() {
  return compose('ps', '-aq', 'humanengine')
}
async function ready() {
  const address = compose('port', 'humanengine', '3000')
  assert.match(address, /^127\.0\.0\.1:\d+$/)
  base = `http://${address}`
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    if (docker(['inspect', '--format', '{{.State.Health.Status}}', container()]) === 'healthy') {
      assert.equal((await api('/api/status')).status, 200)
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
  throw new Error('Container did not become healthy')
}
try {
  compose('up', '-d', '--no-build', '--pull', 'never')
  await ready()
  assert.equal(
    (await fetch(`${base}/api/status`, { signal: AbortSignal.timeout(5000) })).status,
    401,
  )
  assert.equal(docker(['inspect', '--format', '{{.Config.User}}', container()]), 'node')
  const settingsResponse = await api('/api/settings')
  assert.equal(settingsResponse.status, 200)
  const settings = await settingsResponse.json()
  const name = `Container smoke ${project}`
  assert.equal(
    (await api('/api/settings', { method: 'PUT', body: JSON.stringify({ ...settings, name }) }))
      .status,
    200,
  )
  // Exercise the exact runtime-resolved native modules, not the host's dependencies.
  compose(
    'exec',
    '-T',
    'humanengine',
    'node',
    '--input-type=module',
    '-e',
    `
    import { createRequire } from 'node:module';
    import { mkdirSync, writeFileSync } from 'node:fs';
    const require = createRequire('/app/.output/server/index.mjs');
    for (const name of ['node-forge', 'listhen']) {
      try { require.resolve(name); throw Error('unexpected_build_tool_dependency'); }
      catch (error) { if (error.code !== 'MODULE_NOT_FOUND') throw error; }
    }
    const db = new (require('better-sqlite3'))(':memory:');
    require('sqlite-vec').load(db);
    if (!db.prepare('select vec_version() as version').get().version) throw Error('vector_missing');
    db.close();
    const bytes = await require('sharp')({create:{width:2,height:2,channels:3,background:'#123456'}}).png().toBuffer();
    mkdirSync('/app/data/media', {recursive:true});
    writeFileSync('/app/data/media/container-smoke.png', bytes);
  `,
  )
  const mediaHash = () =>
    compose(
      'exec',
      '-T',
      'humanengine',
      'node',
      '-e',
      "console.log(require('node:crypto').createHash('sha256').update(require('node:fs').readFileSync('/app/data/media/container-smoke.png')).digest('hex'))",
    )
  const originalHash = mediaHash()
  assert.match(originalHash, /^[a-f0-9]{64}$/)
  compose('restart', 'humanengine')
  await ready()
  assert.equal((await (await api('/api/settings')).json()).name, name)
  assert.equal(mediaHash(), originalHash)
  // Recreate also proves persistence belongs to the named volume, not the writable layer.
  compose('up', '-d', '--force-recreate', '--no-build', '--pull', 'never')
  await ready()
  assert.equal((await (await api('/api/settings')).json()).name, name)
  assert.equal(mediaHash(), originalHash)
  compose('stop', 'humanengine')
  assert.equal(docker(['inspect', '--format', '{{.State.ExitCode}}', container()]), '0')
  console.log(
    'Container smoke passed: authenticated health, unauthorized rejection, nonroot native modules, persisted settings/media, graceful shutdown.',
  )
} finally {
  // Only this randomly named smoke project's disposable data is removed.
  compose('down', '--volumes', '--remove-orphans')
}
