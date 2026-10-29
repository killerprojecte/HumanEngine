import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { readRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import { recordExternalCostReceipt } from '../src/memory/recovery/external-cost.ts'
import { parseExternalCostRecoveryEvent } from '../src/memory/recovery/external-cost-contract.ts'
import { ExternalCostService } from '../src/budget/external-cost/service.ts'
import { ExternalCostStorage } from '../src/budget/external-cost/storage.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-external-recovery-')),
    database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database),
    opened: Store[] = []
  const service = new ExternalCostService(store.db, {
    assertReady: () => store.assertRecoveryReady(),
    beforeWrite: (r) => recordExternalCostReceipt(store.db, r),
    now: () => Date.parse('2026-10-29T13:00:00Z'),
  })
  return {
    root,
    store,
    service,
    snapshot: async (name: string) => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (backupRoot: string, name = 'restored') => {
      const r = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const s = new Store(r.database)
      opened.push(s)
      return s
    },
    close: async () => {
      for (const s of opened) s.close()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}
const base = {
  id: 'cost-one',
  sourceId: 'declared-admin-source',
  category: 'storage' as const,
  currency: 'USD' as const,
  billingDate: '2026-10-29',
  periodStart: '2026-10-01',
  periodEnd: '2026-10-31',
}
test('old backup replays exact declared external-cost revisions and retains unknown coverage plus model HOLD', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot('before')
    f.service.import({ ...base, status: 'known', amountNanoUsd: 12345 })
    f.service.correct({
      id: base.id,
      expectedRevision: 1,
      correctionId: 'correction-one',
      replacement: { ...base, status: 'known', amountNanoUsd: 67890 },
    })
    f.service.import({ ...base, id: 'unknown-cost', status: 'unknown' })
    assert.equal(metadata(f.store.db)!.minimumReader, 10)
    const restored = await f.recover(old),
      storage = new ExternalCostStorage(restored.db)
    assert.deepEqual(storage.current(), f.service.storage.current())
    assert.equal(readRestoredModelHold(restored.db)?.status, 'held')
    assert.equal(storage.latest('unknown-cost')?.receipt.status, 'unknown')
    const again = await f.recover(await f.snapshot('current'), 'again')
    assert.deepEqual(new ExternalCostStorage(again.db).current(), storage.current())
  } finally {
    await f.close()
  }
})
test('failed external-cost local commit retains authoritative declaration, blocks live state and can safely replay metadata', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot('old')
    assert.throws(
      () =>
        f.store.db
          .transaction(() => {
            f.service.import({ ...base, status: 'unknown' })
            throw Error('synthetic_abort')
          })
          .immediate(),
      /synthetic_abort/,
    )
    assert.throws(() => f.store.settings, /recovery_authority_head_changed/)
    const restored = await f.recover(old)
    assert.equal(new ExternalCostStorage(restored.db).latest(base.id)?.receipt.status, 'unknown')
    assert.equal(readRestoredModelHold(restored.db)?.status, 'held')
  } finally {
    await f.close()
  }
})
test('external authority refuses invoice prose, URLs and incoherent correction sequence metadata', () => {
  const receipt = {
    schemaVersion: 1,
    revision: 1,
    receipt: { ...base, status: 'known', amountNanoUsd: 10 },
    correctionId: null,
    recordedAt: '2026-10-29T13:00:00.000Z',
  }
  assert.deepEqual(
    parseExternalCostRecoveryEvent({ kind: 'external_cost_receipt', receipt }).receipt,
    receipt,
  )
  for (const patch of [
    { revision: 2 },
    { correctionId: 'unexpected' },
    { invoiceText: 'secret' },
    { receipt: { ...receipt.receipt, sourceId: 'https://example.invalid/invoice' } },
  ])
    assert.throws(
      () =>
        parseExternalCostRecoveryEvent({
          kind: 'external_cost_receipt',
          receipt: { ...receipt, ...patch },
        }),
      /recovery_external_cost_event_invalid/,
    )
})
