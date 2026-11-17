import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { readFile, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
if (!process.env.HE_SOURCE_ROOT) throw new Error('HE_SOURCE_ROOT must identify the published P1 TypeScript source checkout')
const root = resolve(process.env.HE_SOURCE_ROOT)
const load = (path) => import(pathToFileURL(join(root, path)).href)
const { Engine } = await load('src/engine.ts')
const { Store } = await load('src/store.ts')
const { HabitService } = await load('src/habits.ts')
const { PlannerEvidenceBudget } = await load('src/runtime/planner-evidence-budget.ts')
const store = new Store(':memory:')
const scene = { id: 's', name: 's', kind: 'group', adapterId: 'offline', accountId: 'bot', platformSceneId: 's', participationEnabled: false, learningEnabled: false, proactiveEnabled: false }
store.saveScene(scene)
const engine = new Engine(store)
const now = 1794873600000
Object.defineProperty(engine, 'habits', { value: new HabitService(store, { now: () => now }) })
try {
  const rows = [
    { id: 'general', sceneId: 's', kind: 'sceneRule', content: 'HINT concise ΣΟΣ İstanbul' },
    { id: 'technical', sceneId: 's', kind: 'sceneRule', content: 'HINT technical detail', context: 'technical' },
    { id: 'expression', sceneId: 's', kind: 'botExpression', preference: 'emoji' },
    { id: 'foreign', sceneId: 'elsewhere', kind: 'sceneRule', content: 'HINT foreign' },
  ]
  for (const row of rows) store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(row.id, row.id, JSON.stringify({ ...row, evidenceVersion: 2, sources: [{ sceneId: row.sceneId, messageId: row.id, senderId: 'speaker', direction: 1, timestamp: now }], weight: 0, confidence: 0, status: 'active', locked: false, learnedAt: new Date(now).toISOString() }))
  const extension = engine.runtime.extensions.find((e) => e.tools.some((tool) => tool.name === 'rules.list'))
  const cases = []
  for (const input of [{}, { query: 'HINT' }, { query: 'HINT', context: 'general' }, { query: 'HINT', context: 'technical' }, { query: 'technical detail' }, { query: 'emoji' }, { query: 'ΣΟΣ' }, { query: 'İSTANBUL' }, { query: 'HINT\ufeffconcise' }, { query: 'HINT\u0085concise' }, { context: 'other' }, { extra: true }, { query: '😀'.repeat(251) }]) {
    const context = { scene, mode: 'real', turnId: 'fixture', signal: new AbortController().signal, dependencies: new Set(), validations: [] }
    try { const value = await extension.execute('rules.list', input, context); cases.push({ input, success: true, ids: value.learned.map((r) => r.id) }) }
    catch { cases.push({ input, success: false }) }
  }
  const budgets = []
  for (const row of [{ name: 'small', maxBytes: 131072, padding: 0, content: 4 }, { name: 'receipt-limit', maxBytes: 131072, padding: 0, content: 70000 }, { name: 'cumulative', maxBytes: 2000, padding: 1400, content: 1000 }, { name: 'no-room', maxBytes: 50, padding: 40, content: 100 }]) {
    const messages = [{ role: 'user', content: 'c'.repeat(row.padding) }]
    const receipt = { id: 'rules', toolName: 'rules.list', value: { manual: [], learned: [{ content: 'x'.repeat(row.content) }] } }
    try { const result = new PlannerEvidenceBudget(row.maxBytes).receipt(messages, receipt, 'call'); budgets.push({ ...row, expected: result.receipt.value }) }
    catch (error) { budgets.push({ ...row, error: error.message }) }
  }
  const sources = await Promise.all(['src/engine.ts', 'src/runtime/planner-evidence-budget.ts'].map(async (path) => ({ path, sha256: createHash('sha256').update(await readFile(join(root, path))).digest('hex') })))
  await writeFile('internal/app/testdata/rules-p1-source.json', JSON.stringify({ reference: '04b1817 production tree; b7f3f92 test-only successor', node: process.version, sources, description: extension.tools.find((t) => t.name === 'rules.list').description, records: rows, cases, budgets }, null, 2) + '\n')
} finally { await engine.dispose(); store.close() }
