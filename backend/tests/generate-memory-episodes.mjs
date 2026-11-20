import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const root = mkdtempSync(join(tmpdir(), 'he-episodes-'))
const cwd = process.cwd()
const base = { adapterId:'fixture', accountId:'fixture', name:'Synthetic', learningEnabled:true, participationEnabled:false, proactiveEnabled:false }
const scenes = [['scene','group'],['foreign','private'],['simulation','simulation']].map(([id,kind]) => ({...base,id,kind,platformSceneId:id}))
const value = (id, personId, more = {}) => ({
  id, scope:{kind:'scene',sceneId:'scene',...(personId ? {personId} : {})}, status:'active', kind:'experience',
  content:'Synthetic '+id, learnedAt:'2025-01-01T00:00:00.000Z', sources:[], ...more,
})
const seeds = [
  value('a_a','a'), value('a-A','a',{ occurredAt:'2024-12-31T00:00:00Z', epistemicKind:'reported' }), value('aa','a',{status:'disputed'}),
  value('legacy-other','b'), value('private','a',{scope:{kind:'scene',sceneId:'foreign',personId:'a'}}),
  value('internal','a',{internalUse:'synthetic-private'}), value('expired','a',{validUntil:'2025-06-01T00:00:00Z'}),
  value('invalid','a',{status:'invalidated'}), value('claim','a',{kind:'claim'}), value('shared',null,{scope:{kind:'sharedPublicKnowledge'}}),
]
const packed = Array.from({length:30},(_,i) => value('packed-'+String(i).padStart(2,'0'),'crowd',{learnedAt:'2025-02-'+String(i%20+1).padStart(2,'0')+'T00:00:00.000Z'}))
const inherited = JSON.parse(readFileSync(new URL('../internal/app/testdata/commitment-admin-read-source.json',import.meta.url),'utf8'))
const commitment = inherited.inputs[0]
const source = { sceneId:'scene', messageId:'episode-source', senderId:'a' }
const sourceProposal = {
  context:{sceneId:'scene',simulation:false,provenance:'source-only',sourceEvidence:[{...source,available:true,isBot:false}]},
  candidate:{kind:'experience',epistemicKind:'explicit',content:'Synthetic reported event',subject:{kind:'self',sourceMessageId:source.messageId},sources:[source],occurredAt:'2024-09-01T00:00:00Z'}
}
let store, engine
const cases = []
try {
  process.chdir(root)
  store = new Store(join(root, 'live/humanengine.sqlite'))
  engine = new Engine(store)
  const app = createApp(store, {engine,token:'synthetic-token'})
  for (const scene of scenes) store.saveScene(scene)
  const write = store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)')
  const add = record => write.run(record.id,record.scope.kind==='scene'?'scene:'+record.scope.sceneId:'shared:public',record.scope.personId??null,record.content,record.id,JSON.stringify(record))
  seeds.forEach(add)
  let learnedId, learnedAt
  function flag(settings) { store.set('settings',{...store.settings,...settings}) }
  const headersKeys = ['content-type','cache-control','x-content-type-options']
  async function capture(name, query = '?sceneId=scene', {method='GET',auth=true} = {}) {
    const headers = { 'X-Request-ID':'12345678-1234-4123-8123-123456789012' }
    if (auth) headers.Authorization = 'Bearer synthetic-token'
    const response = await app.request('/api/memory-episodes'+query,{method,headers})
    const raw = await response.text()
    const body = raw ? JSON.parse(raw) : null
    if (body?.requestId) body.requestId = '<request-id>'
    for (const item of body?.items ?? []) {
      if (item.record?.id === learnedId) {
        item.record.id = '@learned'
        item.record.learnedAt = item.time.recordedAt = '<learned>'
      }
      if (item.view?.recordedAt) item.view.recordedAt = '<recorded>'
    }
    const exists = name => Boolean(store.db.prepare('SELECT 1 FROM sqlite_master WHERE name=?').get(name))
    const rows = name => exists(name) ? store.db.prepare('SELECT count(*) n FROM '+name).get().n : 0
    cases.push({name,query,method,auth,status:response.status,body,headers:Object.fromEntries(headersKeys.map(k=>[k,response.headers.get(k)??''])),
      workspace:{ items:rows('memory_working_set_items'), jobs:rows('memory_working_set_jobs'),
        legacy:store.db.prepare("SELECT version FROM memory_assertion_state WHERE assertion_id='a-A'").get()?.version??0 }})
  }
  await capture('unauthorized','?sceneId=scene',{auth:false})
  await capture('missing-query','')
  await capture('empty-person','?sceneId=scene&personId=')
  await capture('astral-max','?sceneId='+encodeURIComponent('💡'.repeat(512)))
  await capture('astral-over','?sceneId='+encodeURIComponent('💡'.repeat(513)))
  await capture('duplicate-scene','?sceneId=scene&sceneId=foreign')
  await capture('strict','?sceneId=scene&includeCold=true')
  await capture('missing-scene','?sceneId=missing')
  await capture('simulation','?sceneId=simulation')
  await capture('disabled')
  await capture('disabled-head','?sceneId=scene',{method:'HEAD'})
  flag({memoryEpisodesEnabled:true,memoryCommitmentsEnabled:true,memoryCommitmentsAnswerEnabled:false})
  const learned = engine.memory.propose(sourceProposal.context,sourceProposal.candidate)
  assert.equal(learned.status,'saved')
  learnedId = learned.memory.id
  learnedAt = learned.memory.learnedAt
  const plan = engine.memory.propose(commitment.context,commitment.candidate)
  assert.equal(plan.commitment.status,'saved')
  await capture('enabled-person','?sceneId=scene&personId=a')
  await capture('person-isolated','?sceneId=scene&personId=b')
  await capture('private','?sceneId=foreign')
  await capture('enabled-head','?sceneId=scene&personId=a',{method:'HEAD'})
  flag({memoryCommitmentsAnswerEnabled:true})
  await capture('commitments-answer-on','?sceneId=scene&personId=a')
  await capture('commitment-participant-b','?sceneId=scene&personId=b')
  engine.memory.recall(source.sceneId,source.messageId)
  await capture('after-recall','?sceneId=scene&personId=a')
  packed.forEach(add)
  await capture('bounded-prefix','?sceneId=scene&personId=crowd')
  const pick = name=>cases.find(c=>c.name===name)
  assert.deepEqual(pick('disabled').body.items,[])
  assert.equal(pick('disabled').workspace.legacy,0)
  assert.equal(pick('enabled-person').workspace.legacy,1)
  assert.equal(pick('enabled-person').workspace.jobs,0)
  assert.ok(pick('enabled-person').body.items.some(i=>i.record?.id==='@learned'))
  assert.ok(pick('commitments-answer-on').body.items.some(i=>i.kind==='shared_plan_declaration'))
  assert.ok(!pick('after-recall').body.items.some(i=>i.record?.id==='@learned'))
  assert.equal(pick('bounded-prefix').body.coverage.returned,12)
  assert.equal(pick('bounded-prefix').body.coverage.omittedEntries,16)
  assert.equal(pick('bounded-prefix').body.coverage.status,'limited')
  const dest = new URL('../internal/app/testdata/memory-episodes-source.json',import.meta.url)
  const data = JSON.stringify({node:process.version,scenes,seeds,packed,sourceProposal,commitment,cases},null,2)+'\n'
  if(process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),data)
  else writeFileSync(dest,data)
  console.log('PASS '+cases.length+' actual memory episodes oracle cases')
} finally {
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(root,{recursive:true,force:true})
}
