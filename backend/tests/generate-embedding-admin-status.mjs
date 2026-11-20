import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const paths = ['src/api.ts', 'src/admin.ts', 'src/engine.ts', 'src/embedding-index.ts', 'src/memory.ts', 'src/config-read-values.ts', 'src/schema.ts', 'src/memory/lifecycle/proof-invalidation.ts']
const sources = Object.fromEntries(paths.map(path => [path, createHash('sha256').update(readFileSync(path)).digest('hex')]))
const root = mkdtempSync(join(tmpdir(), 'he-embedding-status-'))
const cwd = process.cwd()
const realNow = Date.now
const cases = []
let store, engine
// Independently governed fixture clock; it advances only after construction.
const lease = 1_800_000_030_000
const job = { id:'fixture-job', state:'running', modelId:'m', encoderRevision:'old', total:3, processed:1,
  requiresRetryConfirmation:false, expectedActive:'old', dimensions:2, spaceId:'space-active',
  attemptId:'attempt', ownerId:'other-process', leaseUntil:lease }
const scene = { id:'s', name:'Synthetic', kind:'group', adapterId:'fixture', accountId:'fixture', platformSceneId:'s',
  learningEnabled:true, participationEnabled:false, proactiveEnabled:false }
const provider = { id:'p', name:'Synthetic', protocol:'openai-chat', baseUrl:'https://例え.テスト:443/a/../路径' }
const model = { id:'m', providerId:'p', apiModelName:'向量', displayName:'Synthetic', capabilities:['embedding'] }
const route = { role:'embedding', orderedModelIds:['m'], timeoutMs:1000 }
const models = (p = provider, m = model, r = route) => ({ providers:[p], models:[m], routes:[r] })
const memory = (id, more = {}) => ({id,scope:{kind:'scene',sceneId:'s'},content:'Synthetic '+id,kind:'fact',
  status:'active',origin:'manual',confidence:.8,importance:'medium',sources:[],learnedAt:'2026-01-01T00:00:00.000Z',...more})
const records = [memory('indexed'), memory('pending',{status:'disputed',validUntil:'2026-01-02T00:00:00.000Z'}),
  memory('future',{validFrom:'2099-01-01T00:00:00.000Z'}), memory('private',{scope:{kind:'scene',sceneId:'s',personId:'person'}}),
  memory('internal',{internalUse:'synthetic'}), memory('quarantined',{origin:'learned',lineageStatus:'quarantined'}),
  memory('unverified',{origin:'learned'}), memory('old',{status:'superseded'})]
const spaces = [['space-active',{generation:'active'}],['space-prior',{generation:'prior'}],
  ['space-staging',{generation:'staging'}],['space-legacy',{}],['space-null',{generation:null}]]
const vectors = [['indexed','space-active'],['unverified','space-active'],['pending','space-prior'],
  ['pending','space-staging'],['private','space-legacy'],['private','space-null']]
const source = { sceneId:'s', messageId:'fixture-message', senderId:'fixture-person' }
const proposal = { context:{sceneId:'s', simulation:false, provenance:'source-only',
  sourceEvidence:[{...source,available:true,isBot:false,origin:'live'}]},
  candidate:{content:'Synthetic supported fact', kind:'fact', sources:[source]} }
try {
  process.chdir(root)
  Date.now = () => lease - 1
  store = new Store(join(root,'live/main.sqlite'))
  engine = new Engine(store)
  const app = createApp(store,{ engine, token:'synthetic-token' })
  store.saveScene(scene)
  assert.equal(store.get('embedding.rebuild',null),null)
  const intents = []
  let learnedId
  const fetchTrap = globalThis.fetch
  globalThis.fetch = () => { throw new Error('embedding status must not call fetch') }
  const set = (key,value) => {
    store.db.prepare('INSERT OR REPLACE INTO config(key,value) VALUES(?,?)').run(key,JSON.stringify(value))
    intents.push({key,value})
  }
  const clear = key => { store.db.prepare('DELETE FROM config WHERE key=?').run(key); intents.push({key}) }
  async function capture(name, {method='GET',auth=true,at=lease-1}={}) {
    Date.now = () => at
    const headers = {'X-Request-ID':'12345678-1234-4123-8123-123456789012'}
    if(auth) headers.Authorization = 'Bearer synthetic-token'
    const response = await app.request('/api/embedding-index',{method,headers})
    const raw = await response.text()
    const responseID = response.headers.get('x-request-id')
    assert.match(responseID, /^[0-9a-f-]{36}$/)
    assert.notEqual(responseID, headers['X-Request-ID'])
    const stored = store.get('embedding.rebuild',null)
    const learned = learnedId ? JSON.parse(store.db.prepare('SELECT value FROM memories WHERE id=?').get(learnedId).value).status : null
    const learnedVector = learnedId ? store.db.prepare('SELECT count(*) n FROM memory_vectors WHERE memory_id=?').get(learnedId).n : 0
    const keys = ['content-type','cache-control','x-content-type-options']
    const result = {name,method,auth,at,set:intents.splice(0),status:response.status,
      headers:Object.fromEntries(keys.map(k=>[k,response.headers.get(k)??''])),
      body:raw ? JSON.parse(raw) : null, stored, learned, learnedVector}
    cases.push(result)
    return result.body
  }
  try {
    await capture('empty')
    set('models',models())
    const selected = (await capture('native-width')).selected
    set('models',models({...provider,credentialEnv:'SYNTHETIC',timeoutMs:1500},{...model,embeddingBatchSize:7}))
    assert.equal((await capture('operational-metadata-not-fingerprint')).selected.encoderRevision,selected.encoderRevision)
    set('models',models(provider,{...model,embeddingDimensions:2}))
    const explicit = (await capture('explicit-width')).selected
    assert.notEqual(explicit.encoderRevision,selected.encoderRevision)
    for(const record of records) store.db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)')
      .run(record.id,'scene:s',record.scope.personId??null,record.content,record.id,JSON.stringify(record))
    for(const [id,value] of spaces) store.db.prepare('INSERT INTO vector_spaces VALUES(?,?)').run(id,JSON.stringify(value))
    for(const [id,space] of vectors) store.db.prepare('INSERT INTO memory_vectors VALUES(?,?,?)').run(id,space,Buffer.alloc(8))
    const active = {generationId:'active',encoderRevision:explicit.encoderRevision,modelId:'m',dimensions:2,spaceId:'space-active'}
    set('embedding.active',active)
    const saved = engine.memory.propose(proposal.context,proposal.candidate)
    assert.equal(saved.status,'saved')
    learnedId = saved.memory.id
    store.db.prepare('INSERT INTO memory_vectors VALUES(?,?,?)').run(learnedId,'space-active',Buffer.alloc(8))
    await capture('ready')
    set('embedding.incompatible','active')
    await capture('incompatible')
    clear('embedding.incompatible')
    set('models',models({...provider,baseUrl:'https://synthetic.invalid/private?key=never-expose'}))
    await capture('invalid-endpoint')
    set('models',models(provider,{...model,embeddingDimensions:4}))
    await capture('different-encoder')
    set('models',{...models(),models:[{...model,id:'plain',capabilities:['text']},model],
      routes:[{...route,orderedModelIds:['plain','m']}]})
    await capture('first-model-only')
    set('models',models(provider,{...model,embeddingDimensions:2}))
    store.db.prepare("INSERT OR IGNORE INTO recall_tombstones(scene_id,message_id,recalled_at) VALUES(?,?,'2026-01-03T00:00:00.000Z')").run('s',source.messageId)
    set('embedding.rebuild',job)
    await capture('unauthorized-expired',{auth:false,at:lease})
    await capture('peer-running',{at:lease-1})
    await capture('normal-expiry',{at:lease})
    set('embedding.rebuild',{...job,requiresRetryConfirmation:true})
    await capture('uncertain-head-expiry',{method:'HEAD',at:lease})
    await capture('uncertain-paused',{at:lease})
    set('embedding.rebuild',Object.fromEntries(Object.entries(job).filter(([key])=>key!=='leaseUntil')))
    await capture('missing-lease-expiry',{at:lease})
  } finally { globalThis.fetch = fetchTrap }
} finally {
  Date.now = realNow
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(root,{recursive:true,force:true})
}
const output = JSON.stringify({node:process.version,sources,scene,records,spaces,vectors,proposal,cases},null,2)+'\n'
const dest = 'backend/internal/app/testdata/embedding-admin-status-source.json'
if(process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),output)
else writeFileSync(dest,output)
