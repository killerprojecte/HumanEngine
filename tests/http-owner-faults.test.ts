import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer, IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import Database from 'better-sqlite3'
import { RecoveryAuthority, metadata, saveGate } from '../src/memory/recovery/ledger.ts'
import {
  getEngine,
  engineUnavailable,
  handleEngineRequest,
  publishEngineResponse,
  closeEngine,
} from '../server/utils/engine.ts'

test('HTTP config fails closed on authority lock and stale proof, then the same verifier recovers', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-owner-faults-'))
  const saved = { ...process.env }
  process.env.HUMANENGINE_DB = join(root, 'data', 'live.sqlite')
  process.env.HUMANENGINE_ADMIN_TOKEN = 'synthetic-fault-token'
  process.env.HUMANENGINE_PLUGINS = ''
  process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
  delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  const server = createServer(async (req, res) => {
    const controller = new AbortController()
    res.once('close', () => {
      if (!res.writableFinished) controller.abort()
    })
    const response = await handleEngineRequest(
      new Request(`http://localhost${req.url}`, {
        headers: req.headers as Record<string, string>,
        signal: controller.signal,
      }),
    )
    try {
      if (await publishEngineResponse(response, res)) return
    } catch {
      if (res.headersSent) res.destroy()
      else {
        res.writeHead(503)
        res.end('safe')
      }
      return
    }
    res.writeHead(response.status, Object.fromEntries(response.headers))
    res.end(Buffer.from(await response.arrayBuffer()))
  })
  let db: Database.Database | undefined, authority: RecoveryAuthority | undefined
  try {
    const runtime = await getEngine()
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const base = `http://127.0.0.1:${(server.address() as { port: number }).port}`
    const headers = { Authorization: 'Bearer synthetic-fault-token' }
    const request = (path: string) => fetch(base + path, { headers })
    assert.equal((await request('/api/settings')).status, 200)
    db = new Database(process.env.HUMANENGINE_DB)
    authority = new RecoveryAuthority(metadata(db)!.authority)
    // A real authority lock must delay data, not the HTTP event loop. A timer and
    // unauthenticated request finish while the accepted config read is still waiting.
    authority.db.exec('BEGIN EXCLUSIVE')
    let settled = false
    const waitingRead = request('/api/settings').then((value) => {
      settled = true
      return value
    })
    try {
      await new Promise((resolve) => setTimeout(resolve, 50))
      assert.equal((await fetch(base + '/api/settings')).status, 401)
      assert.equal(settled, false)
    } finally {
      authority.db.exec('ROLLBACK')
    }
    assert.equal((await waitingRead).status, 200)
    authority.db.exec('BEGIN EXCLUSIVE')
    try {
      assert.equal((await request('/api/settings')).status, 503)
      assert.equal((await fetch(base + '/api/settings')).status, 401)
    } finally {
      authority.db.exec('ROLLBACK')
    }
    assert.equal((await request('/api/settings')).status, 200)
    const settings = await (await request('/api/settings')).json()
    const mutation = new Request(base + '/api/settings', {
      method: 'PUT',
      headers: {
        ...headers,
        'Content-Type': 'application/json',
        'X-Request-ID': 'synthetic-write',
      },
      body: JSON.stringify({ ...settings, name: 'Committed before publication failure' }),
    })
    const committed = await handleEngineRequest(mutation)
    assert.equal(committed.status, 200)
    const ownerRequestId = committed.headers.get('X-Request-ID')
    assert.match(ownerRequestId!, /^[a-f0-9-]{36}$/)
    assert.notEqual(ownerRequestId, 'synthetic-write')
    assert.equal(
      JSON.parse(
        (db.prepare("SELECT value FROM config WHERE key='settings'").get() as { value: string })
          .value,
      ).name,
      'Committed before publication failure',
    )
    const head = authority.append(
      { kind: 'source', sceneId: 'synthetic', messageId: 'after-write' },
      authority.head(),
    )
    await assert.rejects(
      () => publishEngineResponse(committed, new ServerResponse(new IncomingMessage(new Socket()))),
      /worker_request_failed/,
    )
    const uncertain = engineUnavailable(mutation)
    assert.equal(uncertain.status, 503)
    assert.equal(uncertain.headers.get('X-Request-ID'), ownerRequestId)
    assert.match(uncertain.headers.get('X-Request-ID')!, /^[a-f0-9-]{36}$/)
    assert.notEqual(uncertain.headers.get('X-Request-ID'), 'synthetic-write')
    assert.match(
      (await uncertain.json()).error,
      /outcome may be unknown.*Do not automatically retry writes/,
    )
    saveGate(db, { ...metadata(db)!, head })
    assert.equal(
      (await (await request('/api/settings')).json()).name,
      'Committed before publication failure',
    )
    // Ordinary verification errors leave the same owner and verifier alive. True
    // worker exits are tested in subprocesses because they now terminate the host.
    assert.equal(await getEngine(), runtime)
    assert.equal((await request('/api/status')).status, 200)
    assert.equal((await fetch(base + '/api/settings')).status, 401)
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
    authority?.close()
    db?.close()
    await closeEngine()
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
    rmSync(root, { recursive: true, force: true })
  }
})
