import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Worker } from 'node:worker_threads'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { ensureAssertionEligibilityBaseline } from '../src/memory/recovery/eligibility.ts'
import { WorkerRpc } from '../src/server/worker-rpc.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'he-startup-baseline-'))
  const database = join(root, 'data/live.sqlite')
  const store = new Store(database)
  const memory = new MemoryService(store)
  const events = () => {
    const authority = new RecoveryAuthority(metadata(store.db)!.authority)
    try {
      return authority.entries()
    } finally {
      authority.close()
    }
  }
  return {
    root,
    database,
    store,
    memory,
    events,
    close() {
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('automatic baseline captures current legacy negatives once and keeps bodies unchanged', () => {
  const f = fixture()
  try {
    const denied = f.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      kind: 'fact',
      content: 'legacy negative',
    })
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...denied, status: 'invalidated' }), denied.id)
    const before = f.store.db.prepare('SELECT * FROM memories').all()
    ensureAssertionEligibilityBaseline(f.store.db)
    const entries = f.events()
    const baseline = entries.at(-1)!.revocation
    assert.equal(baseline.kind, 'assertion_eligibility')
    if (baseline.kind !== 'assertion_eligibility') return
    assert.equal(baseline.mode, 'baseline')
    assert.deepEqual(
      baseline.changes.map((item) => item.id),
      [denied.id],
    )
    assert.equal(baseline.changes[0]!.materialHash, null)
    assert.equal(metadata(f.store.db)!.minimumReader, 11)
    assert.deepEqual(f.store.db.prepare('SELECT * FROM memories').all(), before)
    ensureAssertionEligibilityBaseline(f.store.db)
    assert.deepEqual(f.events(), entries)
  } finally {
    f.close()
  }
})

test('automatic empty baseline is durable, including after earlier transition-only prototype writes', () => {
  const f = fixture()
  try {
    const record = f.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      kind: 'fact',
      content: 'transition',
    })
    f.memory.update(record.id, { status: 'invalidated' })
    f.memory.update(record.id, { status: 'active' })
    ensureAssertionEligibilityBaseline(f.store.db)
    const event = f.events().at(-1)!.revocation
    assert.equal(event.kind, 'assertion_eligibility')
    if (event.kind === 'assertion_eligibility') {
      assert.equal(event.mode, 'baseline')
      assert.deepEqual(event.changes, [])
    }
  } finally {
    f.close()
  }
})

test('baseline main commit failure preserves durable intent and blocks retry', () => {
  const f = fixture()
  try {
    f.store.db.exec(`CREATE TRIGGER synthetic_baseline_fail BEFORE UPDATE ON recovery_gate
      WHEN json_extract(NEW.value, '$.head.sequence') > json_extract(OLD.value, '$.head.sequence')
      BEGIN SELECT RAISE(ABORT, 'synthetic_commit_failure'); END`)
    assert.throws(() => ensureAssertionEligibilityBaseline(f.store.db), /synthetic_commit_failure/)
    const entries = f.events()
    assert.equal(entries.at(-1)!.revocation.kind, 'assertion_eligibility')
    assert.throws(
      () => ensureAssertionEligibilityBaseline(f.store.db),
      /recovery_authority_head_changed/,
    )
    assert.deepEqual(f.events(), entries)
  } finally {
    f.close()
  }
})

test('actual owner startup establishes baseline before readiness and restart does not repeat it', async () => {
  const f = fixture()
  try {
    const ownerUrl = new URL('../src/server/owner-worker.ts', import.meta.url).href
    for (let i = 0; i < 2; i++) {
      const rpc = new WorkerRpc(
        new Worker(
          `import { register } from 'tsx/esm/api'; register(); await import(${JSON.stringify(ownerUrl)})`,
          {
            eval: true,
            env: {
              ...process.env,
              HUMANENGINE_DB: f.database,
              HUMANENGINE_LOG_DIR: join(f.root, 'logs'),
              HUMANENGINE_PLUGINS: '',
              NODE_ENV: 'test',
            },
          },
        ),
      )
      try {
        await rpc.ready
        const baselines = f
          .events()
          .filter(
            ({ revocation }) =>
              revocation.kind === 'assertion_eligibility' && revocation.mode === 'baseline',
          )
        assert.equal(baselines.length, 1)
      } finally {
        await rpc.close()
      }
    }
  } finally {
    f.close()
  }
})
