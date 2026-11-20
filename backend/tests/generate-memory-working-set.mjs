import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const root = mkdtempSync(join(tmpdir(), 'he-working-set-'))
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
  value('invalid','a',{status:'invalidated'}), value('claim','a',{kind:'claim'}), value('unknown','a',{learnedAt:'unknown'}), value('shared',null,{scope:{kind:'sharedPublicKnowledge'}}),
]
const packed = Array.from({length:30},(_,i) => value('packed-'+String(i).padStart(2,'0'),'crowd',{learnedAt:'2025-02-'+String(i%20+1).padStart(2,'0')+'T00:00:00.000Z'}))
const inherited = JSON.parse(readFileSync(new URL('../internal/app/testdata/commitment-admin-read-source.json',import.meta.url),'utf8'))
const commitment = inherited.inputs[0]
const variantProposal = structuredClone(commitment)
const variantTerms = {...variantProposal.candidate.commitment.terms,title:'Synthetic versioned plan',conditions:[]}
variantProposal.context.sourceEvidence[0].messageId = 'variant-proposal'
variantProposal.context.sourceEvidence[0].sourceText = 'I propose Synthetic versioned plan with the named participant. Time: 18:00; exact date unknown.'
variantProposal.candidate.sources[0].messageId = 'variant-proposal'
variantProposal.candidate.subject.sourceMessageId = 'variant-proposal'
variantProposal.candidate.commitment.terms = variantTerms
function variantCommand(messageId,senderId,target,mutationVersion,command) {
  const source = {sceneId:'scene',messageId,senderId}
  const context = {sceneId:'scene',simulation:false,provenance:'contextual',dependencyMemoryIds:[],dependencyParentReferences:[],sourceEvidence:[{...source,available:true,isBot:false,
    sourceText:command.terms ? 'I amend Synthetic versioned plan. Time: 22:00; exact date unknown.' : 'I confirm Synthetic versioned plan at 18:00; exact date unknown.',
    mentions:[{partIndex:1,userId:senderId === 'a' ? 'b' : 'a'}]}],exposedCommitments:[{...target,mutationVersion}]}
  return {context,candidate:{kind:'fact',epistemicKind:'explicit',content:'Synthetic declaration '+messageId,subject:{kind:'self',sourceMessageId:messageId},sources:[source],commitment:{...command,target}}}
}
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
  // Compare the synchronous HTTP operation, not which variant a timer happened
  // to materialize before it. The real request's enqueue and drain remain on.
  engine.workspaceMaintenance.dispose()
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
    const response = await app.request('/api/memory-working-set'+query,{method,headers})
    const raw = await response.text()
    const body = raw ? JSON.parse(raw) : null
    if (body?.requestId) body.requestId = '<request-id>'
    for (const p of body?.policies?.items ?? []) {
      assert.match(p.selection.dependencyKey,/^[a-f0-9]{64}$/)
      p.selection.dependencyKey = '<dependency>'
    }
    for (const item of body?.items ?? []) {
      if (item.record?.id === learnedId) {
        item.record.id = '@learned'
        item.record.learnedAt = '<learned>'
      }
      if (item.view?.recordedAt) item.view.recordedAt = '<recorded>'
      if (item.accessibility) {
        assert.match(item.accessibility.dependencyKey,/^[a-f0-9]{64}$/)
        item.accessibility.dependencyKey = '<dependency>'
        if (item.accessibility.ref.kind === 'commitment') item.accessibility.ref.id = '@commitment'
        if (item.accessibility.ref.id === learnedId) item.accessibility.ref.id = '@learned'
      }
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
  await capture('strict','?sceneId=scene&bogus=true')
  await capture('invalid-include-cold','?sceneId=scene&includeCold=True')
  await capture('duplicate-include-cold','?sceneId=scene&includeCold=true&includeCold=false')
  await capture('missing-scene','?sceneId=missing')
  await capture('simulation','?sceneId=simulation')
  await capture('disabled')
  await capture('disabled-head','?sceneId=scene',{method:'HEAD'})
  flag({memoryWorkingSetEnabled:true,memoryCommitmentsEnabled:true,memoryCommitmentsAnswerEnabled:false})
  const learned = engine.memory.propose(sourceProposal.context,sourceProposal.candidate)
  assert.equal(learned.status,'saved')
  learnedId = learned.memory.id
  learnedAt = learned.memory.learnedAt
  const plan = engine.memory.propose(commitment.context,commitment.candidate)
  assert.equal(plan.commitment.status,'saved')
  await capture('enabled-person','?sceneId=scene&personId=a')
  await capture('include-cold','?sceneId=scene&personId=a&includeCold=true')
  const selected = await (await app.request('/api/memory-working-set?sceneId=scene&personId=a&includeCold=true',{headers:{Authorization:'Bearer synthetic-token'}})).json()
  const {ref,dependencyKey} = selected.items.find(i=>i.record?.id==='a-A').accessibility
  const response = await app.request('/api/memory-working-set/pin',{method:'POST',headers:{Authorization:'Bearer synthetic-token','Content-Type':'application/json'},body:JSON.stringify({sceneId:'scene',selection:{ref,dependencyKey},pinned:true,expectedRevision:0})})
  assert.equal(response.status,200)
  await capture('pinned-priority','?sceneId=scene&personId=a')
  flag({memoryWorkingSetEnabled:false,memoryEpisodesEnabled:true})
  await capture('disabled-with-pin','?sceneId=scene&personId=b')
  flag({memoryWorkingSetEnabled:true,memoryEpisodesEnabled:false})
  await capture('person-isolated','?sceneId=scene&personId=b')
  await capture('private','?sceneId=foreign')
  await capture('enabled-head','?sceneId=scene&personId=a',{method:'HEAD'})
  flag({memoryCommitmentsAnswerEnabled:true})
  await capture('commitments-answer-on','?sceneId=scene&personId=a')
  await capture('commitment-participant-b','?sceneId=scene&personId=b')
  function variantSave({context,candidate}) {
    const result = engine.memory.propose(context,candidate)
    assert.equal(result.commitment?.status,'saved',JSON.stringify(result))
    return result.commitment.item
  }
  const prerevision = variantSave(variantProposal)
  const confirm = variantCommand('variant-confirm','b',prerevision.ref,prerevision.mutationVersion,{kind:'confirm'})
  const confirmed = variantSave(confirm)
  const amend = variantCommand('variant-amend','a',confirmed.ref,confirmed.mutationVersion,{kind:'amend',terms:{...variantTerms,timeQualification:'22:00; exact date unknown'}})
  variantSave(amend)
  function normalizeVariant(input) {
    const out = structuredClone(input)
    out.context.exposedCommitments[0].id = '@variant'
    out.candidate.commitment.target.id = '@variant'
    return out
  }
  await capture('commitment-variants','?sceneId=scene&personId=b&includeCold=true')
  await capture('commitment-variants-repeat','?sceneId=scene&personId=b&includeCold=true')
  engine.memory.recall(source.sceneId,source.messageId)
  await capture('after-recall','?sceneId=scene&personId=a')
  packed.forEach(add)
  await capture('bounded-prefix','?sceneId=scene&personId=crowd&includeCold=true')
  const pick = name=>cases.find(c=>c.name===name)
  assert.deepEqual(pick('disabled').body.items,[])
  assert.equal(pick('disabled').workspace.legacy,0)
  assert.equal(pick('enabled-person').workspace.jobs,0)
  assert.ok(pick('enabled-person').body.items.some(i=>i.record?.id==='@learned'))
  assert.ok(pick('include-cold').body.items.some(i=>i.accessibility.state==='cold'))
  assert.ok(pick('include-cold').body.items.some(i=>i.accessibility.state==='unknown'))
  assert.ok(pick('include-cold').body.items.some(i=>i.record?.kind==='claim'))
  assert.equal(pick('pinned-priority').body.items[0].record.id,'a-A')
  assert.equal(pick('disabled-with-pin').body.policies.items.length,1)
  assert.equal(pick('disabled-with-pin').body.enabled,false)
  assert.ok(!('omittedEntries' in pick('disabled-with-pin').body))
  assert.ok(pick('commitments-answer-on').body.items.some(i=>i.kind==='commitment'))
  const versions = pick('commitment-variants').body.items.filter(i=>i.view?.terms?.title==='Synthetic versioned plan')
  assert.equal(versions.length,2)
  assert.deepEqual(versions[0].accessibility.ref,versions[1].accessibility.ref)
  assert.deepEqual(versions.map(i=>i.accessibility.metadataRevision),[1,1])
  const repeat = pick('commitment-variants-repeat').body.items.filter(i=>i.view?.terms?.title==='Synthetic versioned plan')
  assert.deepEqual(repeat.map(i=>i.accessibility.metadataRevision),[0,0])
  assert.ok(!pick('after-recall').body.items.some(i=>i.record?.id==='@learned'))
  assert.equal(pick('bounded-prefix').body.items.length,12)
  assert.equal(pick('bounded-prefix').body.omittedEntries,16)
  assert.equal(pick('bounded-prefix').body.coverage,'limited')
  const dest = new URL('../internal/app/testdata/memory-working-set-source.json',import.meta.url)
  const data = JSON.stringify({node:process.version,scenes,seeds,packed,sourceProposal,commitment,variantProposal,variantConfirm:normalizeVariant(confirm),variantAmend:normalizeVariant(amend),cases},null,2)+'\n'
  if(process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),data)
  else writeFileSync(dest,data)
  console.log('PASS '+cases.length+' actual memory working-set oracle cases')
} finally {
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(root,{recursive:true,force:true})
}
