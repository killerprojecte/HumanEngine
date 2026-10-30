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
const offline = (...args) =>
  compose(
    'run',
    '--rm',
    '--no-deps',
    '--pull',
    'never',
    '--entrypoint',
    'node',
    'humanengine',
    ...args,
  )
async function blocked(code) {
  const address = compose('port', 'humanengine', '3000')
  assert.match(address, /^127\.0\.0\.1:\d+$/)
  base = `http://${address}`
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    try {
      const response = await api('/api/status')
      if (response.status === 503 && (await response.json()).error.startsWith(`${code}:`)) {
        const unauthorized = await api('/api/status', {
          headers: { Authorization: 'Bearer wrong' },
        })
        assert.equal(unauthorized.status, 401)
        assert.doesNotMatch(await unauthorized.text(), /recovery_|authority|enroll/)
        return
      }
    } catch {
      // The server may not yet be listening. No raw process error/response is logged.
    }
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
  throw new Error(`Container did not expose authenticated recovery block ${code}`)
}
const maintenance = '.output/server/recovery-authority.mjs'
const enroll = () =>
  offline(
    maintenance,
    'enroll',
    '/app/data/humanengine.sqlite',
    '/app/authority',
    '--current-live-database-not-a-backup',
  )
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
  // Replace only this randomly named test project's disposable volumes. The next
  // phase starts with genuine pre-gate fixture bytes, not by deleting a live gate.
  compose('down', '--volumes', '--remove-orphans')
  offline(
    '--input-type=module',
    '-e',
    `
    import {createRequire} from 'node:module';
    const require = createRequire('/app/.output/server/index.mjs');
    const db = new (require('better-sqlite3'))('/app/data/humanengine.sqlite');
    db.exec('CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE legacy_rows(value TEXT NOT NULL)');
    db.prepare('INSERT INTO config VALUES(?,?)').run('settings', JSON.stringify({name:'Legacy container fixture', timezone:'UTC', persona:'Synthetic fixture', cacheEnabled:true, cacheHours:24, debounceMs:1200, maxWaitMs:6000}));
    db.prepare('INSERT INTO legacy_rows VALUES(?)').run('SYNTHETIC_RETAINED_UPGRADE_ROW');
    db.close();
  `,
  )
  const legacyHash = () =>
    offline(
      '--input-type=module',
      '-e',
      `
    import {readFileSync} from 'node:fs'; import {createHash} from 'node:crypto';
    console.log(createHash('sha256').update(readFileSync('/app/data/humanengine.sqlite')).digest('hex'));
  `,
    )
  const beforeEnrollment = legacyHash()
  compose('up', '-d', '--no-build', '--pull', 'never')
  await blocked('recovery_legacy_enrollment_required')
  compose('stop', 'humanengine')
  assert.equal(legacyHash(), beforeEnrollment)
  // Exercise the exact bundled plain-Node executable shipped in the final image.
  assert.throws(() =>
    offline(maintenance, 'enroll', '/app/data/humanengine.sqlite', '/app/authority'),
  )
  assert.equal(legacyHash(), beforeEnrollment)
  assert.match(enroll(), /enrollment completed/)
  compose('up', '-d', '--no-build', '--pull', 'never')
  await ready()
  assert.equal((await (await api('/api/status')).json()).name, 'Legacy container fixture')
  const verifyLegacy = () =>
    compose(
      'exec',
      '-T',
      'humanengine',
      'node',
      '--input-type=module',
      '-e',
      `
    import {createRequire} from 'node:module';
    const require = createRequire('/app/.output/server/index.mjs');
    const db = new (require('better-sqlite3'))('/app/data/humanengine.sqlite', {readonly:true});
    if (db.prepare('SELECT value FROM legacy_rows').get().value !== 'SYNTHETIC_RETAINED_UPGRADE_ROW') throw Error('legacy_data_changed');
    console.log(JSON.parse(db.prepare('SELECT value FROM recovery_gate WHERE id=1').get().value).head.instanceId);
    db.close();
  `,
    )
  const enrolledIdentity = verifyLegacy()
  compose('restart', 'humanengine')
  await ready()
  assert.equal(verifyLegacy(), enrolledIdentity)
  compose('stop', 'humanengine')
  // A lost mount/file must not silently become a new enrollment on this same DB.
  offline(
    '--input-type=module',
    '-e',
    "import{renameSync}from'node:fs';renameSync('/app/authority/authority.sqlite','/app/authority/retained-authority.sqlite')",
  )
  compose('up', '-d', '--no-build', '--pull', 'never')
  await blocked('recovery_authority_missing')
  compose('stop', 'humanengine')
  assert.throws(enroll)
  offline(
    '--input-type=module',
    '-e',
    "import{existsSync,renameSync}from'node:fs';if(existsSync('/app/authority/authority.sqlite'))throw Error('authority_recreated');renameSync('/app/authority/retained-authority.sqlite','/app/authority/authority.sqlite')",
  )
  compose('up', '-d', '--no-build', '--pull', 'never')
  await ready()
  assert.equal(verifyLegacy(), enrolledIdentity)
  compose('stop', 'humanengine')
  assert.equal(docker(['inspect', '--format', '{{.State.ExitCode}}', container()]), '0')
  console.log(
    'Container smoke passed: fresh startup, authenticated recovery 503, offline legacy enrollment, retained data/authority through restart, missing-authority refusal, nonroot native modules, persisted settings/media, graceful shutdown.',
  )
} finally {
  // Only this randomly named smoke project's disposable data is removed.
  compose('down', '--volumes', '--remove-orphans')
}
