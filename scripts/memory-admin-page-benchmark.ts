/** Synthetic only. Measures unchanged full-read cost separately from response size/stringification. */
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import { selectAdminMemoryPage } from '../src/memory/interfaces/admin-page.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

for (const count of [1000, 10000]) {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-page-benchmark', engine })
  const headers = { Authorization: 'Bearer synthetic-page-benchmark' }
  try {
    const insert = store.db.prepare('INSERT INTO memories VALUES (?,?,?,?,?,?)')
    store.db.transaction(() => {
      for (let i = 0; i < count; i++) {
        const record: MemoryRecord = {
          id: `synthetic-${i}`,
          scope: { kind: 'scene', sceneId: 'synthetic', personId: `person-${i % 100}` },
          content: `Synthetic benchmark ${i} ${'x'.repeat(512)}`,
          kind: 'fact',
          importance: 'medium',
          confidence: 1,
          locked: false,
          sources: [],
          learnedAt: `2026-01-${String((i % 28) + 1).padStart(2, '0')}T00:00:00.000Z`,
          status: 'active',
          origin: 'manual',
        }
        insert.run(
          record.id,
          'scene:synthetic',
          `person-${i % 100}`,
          record.content,
          `synthetic-fingerprint-${i}`,
          JSON.stringify(record),
        )
      }
    })()
    const times = { legacy: [] as number[], page: [] as number[] }
    const bytes = { legacy: 0, page: 0 }
    let original: MemoryRecord[] = []
    for (let pair = 0; pair < 5; pair++)
      for (const mode of (pair % 2 ? ['page', 'legacy'] : ['legacy', 'page']) as (
        'legacy' | 'page'
      )[]) {
        const before = process.cpuUsage()
        const response = await app.request(
          mode === 'legacy'
            ? '/api/memories'
            : '/api/memories/page?scene=scene:synthetic&limit=20&locale=en',
          { headers },
        )
        if (response.status !== 200) throw new Error(`benchmark_status_${response.status}`)
        const body = await response.text()
        const cpu = process.cpuUsage(before)
        times[mode].push((cpu.user + cpu.system) / 1000)
        bytes[mode] = Buffer.byteLength(body)
        if (mode === 'legacy') original = JSON.parse(body)
      }
    const page = selectAdminMemoryPage(original, {
      page: 1,
      limit: 20,
      scene: 'scene:synthetic',
      person: '',
      kind: 'all',
      status: 'all',
      q: '',
      locale: 'en',
    })
    const stringify = { legacy: 0, page: 0 }
    for (const mode of ['legacy', 'page'] as const) {
      const before = process.cpuUsage()
      for (let n = 0; n < 10; n++) JSON.stringify(mode === 'legacy' ? original : page)
      const cpu = process.cpuUsage(before)
      stringify[mode] = (cpu.user + cpu.system) / 1000 / 10
    }
    times.legacy.sort((a, b) => a - b)
    times.page.sort((a, b) => a - b)
    console.log(
      JSON.stringify({
        records: count,
        responseBytes: bytes,
        responseReduction: bytes.legacy / bytes.page,
        endpointCpuMedianMs: { legacy: times.legacy[2], page: times.page[2] },
        stringifyCpuMeanMs: stringify,
        pageRecords: page.items.length,
        personFacets: page.personIds.length,
        note: 'Both endpoints still eagerly read/parse/present every row; paged endpoint also filters/sorts every row.',
      }),
    )
  } finally {
    await engine.dispose()
    store.close()
  }
}
