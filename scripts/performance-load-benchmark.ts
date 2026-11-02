import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { performance, monitorEventLoopDelay } from 'node:perf_hooks'
import sharp from 'sharp'
import { compressImage } from '../src/image-upload.ts'
import { ImageByteCache } from '../src/runtime/image-byte-cache.ts'
import { PreviewQueue } from '../app/utils/preview-queue.ts'

// Synthetic, offline, bounded acceptance workload; not a production throughput claim.
const cap = 768 * 1024 * 1024
const started = performance.now()
const cpu = process.cpuUsage()
let peakRss = process.memoryUsage().rss
const watchdog = setInterval(() => {
  peakRss = Math.max(peakRss, process.memoryUsage().rss)
  if (peakRss > cap || performance.now() - started > 120_000) {
    console.error('benchmark_resource_budget_exceeded')
    process.exit(2)
  }
}, 20)
const delay = monitorEventLoopDelay({ resolution: 10 })
delay.enable()
const timings: Record<string, number[]> = {}
async function timed<T>(name: string, run: () => Promise<T>) {
  const start = performance.now()
  try {
    return await run()
  } finally {
    ;(timings[name] ??= []).push(performance.now() - start)
  }
}
const gc = async () => {
  global.gc?.()
  await new Promise((resolve) => setImmediate(resolve))
  global.gc?.()
  return process.memoryUsage()
}
try {
  // Retain native libvips defaults: the harness limits JS work, not library internals.
  const native = { concurrency: sharp.concurrency(), cache: sharp.cache() }
  for (const [width, height] of [
    [1000, 1000],
    [4000, 2500],
    [8000, 5000],
  ]) {
    const source = await sharp({
      create: { width: width!, height: height!, channels: 3, background: '#7192b4' },
    })
      .png()
      .toBuffer()
    for (let repeat = 0; repeat < 2; repeat++)
      await Promise.all(
        Array.from({ length: 2 }, () =>
          timed(`image_${width! * height!}_pixels_c2`, async () => {
            const url = await compressImage(source, {})
            const output = Buffer.from(url.split(',')[1]!, 'base64')
            const meta = await sharp(output).metadata()
            assert.ok(meta.width! <= 1536 && meta.height! <= 1536)
            assert.ok(output.length <= 10 * 1024 * 1024)
          }),
        ),
      )
    await gc()
  }
  if (process.argv.includes('--soak')) {
    const noisy = await sharp(randomBytes(4000 * 2500 * 3), {
      raw: { width: 4000, height: 2500, channels: 3 },
    })
      .png({ compressionLevel: 0 })
      .toBuffer()
    await Promise.all(
      Array.from({ length: 2 }, () =>
        timed('noisy_10mp_c2', async () => {
          const result = await compressImage(noisy, {})
          assert.ok(Buffer.from(result.split(',')[1]!, 'base64').length <= 10 * 1024 * 1024)
        }),
      ),
    )
    await gc()
  }
  const excessive = await sharp({
    create: { width: 8001, height: 5000, channels: 3, background: '#7192b4' },
  })
    .png()
    .toBuffer()
  await timed('over_40mp_rejection', () =>
    assert.rejects(compressImage(excessive, {}), /invalid_or_unsupported_image/),
  )
  const cache = new ImageByteCache()
  const adapter = {},
    signal = new AbortController().signal
  const retained: ReturnType<typeof process.memoryUsage>[] = []
  const batches = process.argv.includes('--soak') ? 120 : 20
  for (let batch = 0; batch < batches; batch++) {
    for (let scene = 0; scene < 100; scene++) {
      const sceneId = `synthetic-${batch}-${scene}`
      const load = async () => Buffer.alloc(256 * 1024, 7)
      await timed('cache_scene_read_pair', () =>
        Promise.all([
          cache.read(adapter, sceneId, 'image', signal, () => true, load),
          cache.read(adapter, sceneId, 'image', signal, () => true, load),
        ]),
      )
      await cache.read(adapter, sceneId, 'image', signal, () => true, load)
      cache.clear(sceneId)
    }
    retained.push(await gc())
    if (process.argv.includes('--soak')) await new Promise((resolve) => setTimeout(resolve, 250))
  }
  assert.equal(cache.stats.misses, batches * 100)
  assert.equal(cache.stats.coalesced, batches * 100)
  assert.equal(cache.stats.hits, batches * 100)
  const internal = cache as unknown as {
    entries: Map<unknown, unknown>
    flights: Map<unknown, unknown>
  }
  assert.equal(internal.entries.size, 0)
  assert.equal(internal.flights.size, 0)
  let activePreviews = 0,
    maxPreviews = 0,
    previewStarts = 0,
    abortedPreviews = 0
  const previewQueue = new PreviewQueue(async (_id, signal) => {
    maxPreviews = Math.max(maxPreviews, ++activePreviews)
    previewStarts++
    await new Promise((resolve) => setTimeout(resolve, 10))
    if (signal.aborted) abortedPreviews++
    activePreviews--
  })
  for (let page = 0; page < 200; page++) {
    await timed('meme_page_replacement', async () => {
      previewQueue.update(Array.from({ length: 12 }, (_, i) => `page-${page}-${i}`))
      await new Promise((resolve) => setTimeout(resolve, 2))
    })
  }
  previewQueue.dispose()
  await new Promise((resolve) => setTimeout(resolve, 30))
  assert.ok(maxPreviews <= 3)
  assert.equal(activePreviews, 0)
  const previewInternals = previewQueue as unknown as { pending: unknown[] }
  assert.equal(previewInternals.pending.length, 0)
  const summary = Object.fromEntries(
    Object.entries(timings).map(([name, values]) => {
      values.sort((a, b) => a - b)
      return [
        name,
        {
          n: values.length,
          p50Ms: values[Math.floor(values.length * 0.5)],
          p95Ms: values[Math.min(values.length - 1, Math.ceil(values.length * 0.95) - 1)],
          maxMs: values.at(-1),
        },
      ]
    }),
  )
  console.log(
    JSON.stringify(
      {
        kind: 'bounded_synthetic_offline',
        native,
        node: process.version,
        elapsedMs: performance.now() - started,
        cpu: process.cpuUsage(cpu),
        peakRssBytes: peakRss,
        eventLoopP95Ms: delay.percentile(95) / 1e6,
        eventLoopMaxMs: delay.max / 1e6,
        timings: summary,
        cache: cache.stats,
        previews: {
          pageReplacements: 200,
          maxPreviews,
          previewStarts,
          abortedPreviews,
          activePreviews,
        },
        gcAvailable: !!global.gc,
        postGcMemory: retained,
      },
      null,
      2,
    ),
  )
} finally {
  clearInterval(watchdog)
  delay.disable()
}
