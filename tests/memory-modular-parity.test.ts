import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { RecoveryAuthority, metadata } from '../src/memory/recovery/ledger.ts'

// Captured on the immutable 9b implementation before extraction. Only generated
// UUIDs and wall-clock timestamps are normalized; versions/epochs/proofs are not.
test('memory command and lifecycle normalized database/authority trace matches 9b', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-memory-parity-'))
  const store = new Store(join(root, 'live', 'db.sqlite'))
  try {
    store.saveScene({
      id: 's',
      adapterId: 'synthetic',
      accountId: 'a',
      platformSceneId: 's',
      kind: 'group',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const ids = new Map<string, string>()
    const normalize = (value: unknown): unknown =>
      JSON.parse(
        JSON.stringify(value)
          .replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g, (id) => {
            if (!ids.has(id)) ids.set(id, `id-${ids.size + 1}`)
            return ids.get(id)!
          })
          .replace(/\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z/g, '<time>'),
      )
    const trace: unknown[] = []
    const capture = (stage: string, result?: unknown) => {
      const authority = new RecoveryAuthority(metadata(store.db)!.authority)
      try {
        trace.push(
          normalize({
            stage,
            result,
            records: store.db
              .prepare('SELECT value FROM memories ORDER BY rowid')
              .all()
              .map((r: any) => JSON.parse(r.value)),
            proofs: store.db
              .prepare('SELECT memory_id,value FROM memory_supports ORDER BY rowid')
              .all()
              .map((r: any) => ({ id: r.memory_id, proofs: JSON.parse(r.value) })),
            state: store.db.prepare('SELECT * FROM memory_assertion_state ORDER BY rowid').all(),
            history: store.db
              .prepare(
                'SELECT assertion_id,version,value FROM memory_assertion_revisions ORDER BY rowid',
              )
              .all()
              .map((r: any) => ({ ...r, value: r.value ? JSON.parse(r.value) : null })),
            sources: store.db.prepare('SELECT * FROM memory_sources ORDER BY rowid').all(),
            authority: authority.entries(),
          }),
        )
      } finally {
        authority.close()
      }
    }
    const propose = (messageId: string, content: string) => {
      const source = { sceneId: 's', messageId, senderId: 'p' }
      return memory.propose(
        {
          sceneId: 's',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        { content, kind: 'fact', sources: [source] },
      )
    }
    const first = propose('m1', 'Synthetic tea preference')
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') throw Error('fixture')
    capture('first', first)
    capture('alternative', propose('m2', 'Synthetic tea preference'))
    memory.recall('s', 'm1')
    capture('recall-one', memory.list({ sceneId: 's', simulation: false }))
    const manual = memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'fact',
      content: 'Synthetic manual fact',
    })
    capture('manual', manual)
    capture('update', memory.update(manual.id, { content: 'Synthetic reviewed correction' }))
    capture('delete', memory.delete(first.memory.id))
    capture('tombstone-rejection', propose('m3', 'Synthetic tea preference'))
    assert.deepEqual(
      trace,
      JSON.parse(
        readFileSync(new URL('./fixtures/memory-modular-parity.json', import.meta.url), 'utf8'),
      ),
    )
  } finally {
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})
