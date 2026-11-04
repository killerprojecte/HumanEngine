import test from 'node:test'
import assert from 'node:assert/strict'
import { PromptContextWindows } from '../src/runtime/prompt-context-window.ts'
const options = { enabled: true, maxMessages: 4, retainMessages: 2, maxBytes: 8192 }
const rows = (n: number) =>
  Array.from({ length: n }, (_, id) => ({ id: String(id), text: `message ${id}` }))
test('append windows grow and batch rebuild rather than sliding on every message', () => {
  const windows = new PromptContextWindows()
  assert.deepEqual(
    windows.select('a', rows(3), options, 1).map((x) => x.id),
    ['1', '2'],
  )
  assert.deepEqual(
    windows.select('a', rows(4), options, 1).map((x) => x.id),
    ['1', '2', '3'],
  )
  assert.deepEqual(
    windows.select('a', rows(5), options, 1).map((x) => x.id),
    ['1', '2', '3', '4'],
  )
  assert.deepEqual(
    windows.select('a', rows(6), options, 1).map((x) => x.id),
    ['4', '5'],
  )
})
test('selection always uses fresh authorized content; edits, recalls, authority and config rebuild', () => {
  for (const change of ['edit', 'recall', 'authority', 'config', 'insert']) {
    const windows = new PromptContextWindows()
    windows.select('a', rows(3), options, 1)
    windows.select('a', rows(5), options, 1)
    const current = rows(5)
    if (change === 'edit') current[1]!.text = 'corrected'
    if (change === 'recall') current.splice(1, 1)
    if (change === 'insert') current.splice(2, 0, { id: 'insert', text: 'new earlier source' })
    const selected = windows.select(
      'a',
      current,
      change === 'config' ? { ...options, maxMessages: 5 } : options,
      change === 'authority' ? 2 : 1,
    )
    assert.deepEqual(selected, current.slice(-2), change)
    assert.ok(selected.every((item) => current.includes(item)))
  }
})
test('keys are isolated, returned arrays independent, bytes bounded without truncating latest message', () => {
  const windows = new PromptContextWindows()
  windows.select('a', rows(2), options, 1)
  const earlier = windows.select('a', rows(3), options, 1)
  assert.equal(windows.select('b', rows(3), options, 1).length, 2)
  windows.select('a', rows(4), options, 1)
  assert.equal(earlier.length, 3)
  earlier.pop()
  assert.equal(windows.select('a', rows(4), options, 1).length, 4)
  assert.throws(
    () => windows.select('a', [{ id: 'huge', text: 'x'.repeat(9000) }], options, 1),
    /budget_exhausted/,
  )
  const bounded = windows.select('a', rows(4), { ...options, maxBytes: 40 }, 1)
  assert.equal(bounded.length, 1)
  assert.equal(bounded[0]!.id, '3')
  assert.equal(windows.select('a', rows(30), { ...options, enabled: false }, 1).length, 24)
  windows.clear()
})

test('prompt authority checks the live gate once and rereads settings, rules, models and scene atomically', async () => {
  const { default: Database } = await import('better-sqlite3')
  const { promptWindowAuthority } = await import('../src/runtime/prompt-context-window.ts')
  const db = new Database(':memory:')
  db.exec(
    'CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT)',
  )
  let checks = 0,
    revoked = false
  const store = {
    db,
    assertRecoveryReady() {
      checks++
      if (revoked) throw new Error('revoked')
    },
  }
  try {
    db.prepare('INSERT INTO scenes VALUES (?,?)').run('scene', '{"participationEnabled":true}')
    let previous = promptWindowAuthority(store, 'scene', [])
    assert.equal(checks, 1)
    for (const key of ['settings', 'rules', 'models']) {
      db.prepare('INSERT INTO config VALUES (?,?)').run(key, '{"changed":true}')
      const next = promptWindowAuthority(store, 'scene', [])
      assert.notEqual(next, previous)
      previous = next
    }
    db.prepare('DELETE FROM scenes WHERE id=?').run('scene')
    assert.notEqual(promptWindowAuthority(store, 'scene', []), previous)
    assert.equal(checks, 5)
    revoked = true
    assert.throws(() => promptWindowAuthority(store, 'scene', []), /revoked/)
  } finally {
    db.close()
  }
})
