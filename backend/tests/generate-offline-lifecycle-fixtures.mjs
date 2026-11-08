// Reconstructed after workspace loss; actual quarantined TS owners, synthetic files.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import { mkdtempSync,mkdirSync,rmSync,readFileSync,writeFileSync,readdirSync } from 'node:fs'
import { resolve,join,dirname } from 'node:path'
import { pathToFileURL } from 'node:url'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT||'.');assert.equal(process.version,'v24.22.1')
let sequence=0;crypto.randomUUID=()=>`00000000-0000-4000-8000-${String(++sequence).padStart(12,'0')}`;syncBuiltinESMExports()
const NativeDate=Date;class FixedDate extends NativeDate{constructor(...args){super(...(args.length?args:['2026-10-28T12:00:00.000Z']))}static now(){return 1793188800000}};globalThis.Date=FixedDate
const load=p=>import(pathToFileURL(resolve(root,p)))
const {Store}=await load('src/store.ts'),{MemoryService}=await load('src/memory.ts')
const {RecoveryAuthority,saveGate,metadata,captureRecoveryEffects}=await load('src/memory/recovery/ledger.ts')
const tables=['memories','memory_sources','memory_supports','memory_deleted','memory_assertion_state','memory_assertion_revisions','memory_assertion_revision_sources','memory_assertion_revision_parents','memory_relation_entities','memory_relation_candidates','memory_relation_tombstones','memory_knowledge_edges','memory_semantic_candidates','memory_semantic_attempts','memory_commitment_heads','memory_commitment_revisions','memory_commitment_commands','memory_commitment_transitions','memory_commitment_temporal_windows','memory_working_set_pins','memory_working_set_items','memory_working_set_jobs','memory_working_set_receipts','memory_projection_outbox','config','scenes','recall_tombstones','source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones','vector_spaces','memory_vectors','memory_person_projection_state','memory_fts']
const schemas={},cases=[]
const snapshot=db=>Object.fromEntries(tables.filter(name=>db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(name)).map(name=>{schemas[name]=db.prepare('SELECT sql FROM sqlite_master WHERE name=?').get(name).sql;return[name,db.prepare(`SELECT * FROM ${name}`).all()]}))
const ctx={sceneId:'s',simulation:false,provenance:'source-only'}
const source=(messageId,person='person')=>({sceneId:'s',messageId,senderId:person})
const evidence=(messageId,content,person='person')=>({...source(messageId,person),sourceText:content,parts:[{type:'text',text:content}],available:true,isBot:false})
function configure(store,projectionEnabled=false){
 store.sourceEvents.observe({kind:'ingress',identity:{sceneId:'s',messageId:'preserved-source-head',senderId:'person',adapterId:'offline',accountId:'bot'},bodyHash:'a'.repeat(64),occurredAt:null})
 store.saveScene({id:'s',adapterId:'offline',accountId:'bot',platformSceneId:'group',kind:'group',name:'Synthetic',participationEnabled:false,learningEnabled:true,proactiveEnabled:false})
 store.set('settings',{...store.settings,memoryGraphEnabled:true,memoryGraphAutoAdmissionEnabled:true,memoryKnowledgeEnabled:true,memoryKnowledgeAutoAdmissionEnabled:true,memoryKnowledgeAnswerEnabled:true,memoryProjectionEnabled:projectionEnabled,memorySemanticEnabled:true,memorySemanticAutoAdmissionEnabled:false,memorySemanticAnswerEnabled:true,memoryWorkingSetEnabled:true,memoryCommitmentsEnabled:true})
}
function propose(memory,messageId,content,parents=[]){
 const result=memory.propose({...ctx,provenance:parents.length?'contextual':'source-only',dependencyMemoryIds:parents.map(p=>p.id),dependencyParentReferences:parents.map(p=>memory.captureParentReference(ctx,p)),sourceEvidence:[evidence(messageId,content)]},{kind:'fact',content,sources:[source(messageId)],subject:{kind:'self',sourceMessageId:messageId},epistemicKind:'explicit'})
 assert.equal(result.status,'saved',JSON.stringify(result));return result.memory
}
const reference=(memory,id)=>{const state=memory.revisionState(id);return{id,version:state.version,deletionEpoch:state.deletionEpoch}}
function seedOwners(store,memory,projectionEnabled=false){
 configure(store,projectionEnabled)
 const content='I use Synthetic tool',record=propose(memory,'root',content),ref=reference(memory,record.id)
 const untouched=propose(memory,'unrelated','I use Independent tool')
 store.db.transaction(()=>{
  const relation=memory.relations.admit(ctx,ref,[{predicate:'participates_in',personId:'person',target:{kind:'new',entityKind:'plan',title:'Synthetic plan'},polarity:'positive',modality:'asserted',temporalScope:'current',conditions:[]}]);assert.equal(relation.created.length,1,JSON.stringify(relation))
  const knowledge=memory.knowledge.admit({...ctx,sourceEvidence:[evidence('root',content)]},ref,[{registryVersion:1,predicate:'uses_tool',subject:{kind:'self',personId:'person'},object:{kind:'new',entityKind:'tool',title:'Synthetic tool'},polarity:'positive',modality:'asserted',temporalScope:'current',conditions:[],sourceSpans:[{messageId:'root',partIndex:0,start:0,end:content.length,stance:'self'}]}]);assert.equal(knowledge.created.length,1,JSON.stringify(knowledge))
  const current=memory.semanticCurrent(ctx,ref);assert.ok(current)
  const otherRef=reference(memory,untouched.id),otherCurrent=memory.semanticCurrent(ctx,otherRef);assert.ok(otherCurrent)
  const semantic=memory.semantic.admit(ctx,[{ref,dependencyKey:current.dependencyKey},{ref:otherRef,dependencyKey:otherCurrent.dependencyKey}],[{kind:'summary_candidate',selectedParentIds:[record.id,untouched.id],text:'Synthetic summary',hypothesis:false}]);assert.equal(semantic.created.length,1,JSON.stringify(semantic))
 }).immediate()
 const item=memory.workspace.workingSet(ctx,{includeCold:true}).items.find(x=>x.kind==='assertion'&&x.snapshot.record.id===record.id);assert.ok(item)
 memory.workspace.access.setPinned(ctx,{ref:item.accessibility.ref,dependencyKey:item.accessibility.dependencyKey},{expectedRevision:0,pinned:true})
 assert.equal(store.db.prepare('SELECT COUNT(*) AS n FROM memory_working_set_pins').get().n,1)
 const space=memory.registerSpace({providerId:'offline',modelId:'synthetic',apiModelName:'synthetic',deployment:'offline',dimensions:2,preprocessing:'plain',normalized:true})
 memory.index(record.id,space,[1,0]);memory.index(untouched.id,space,[0,1])
 if(projectionEnabled){memory.flushProjections();assert.ok(store.db.prepare('SELECT 1 FROM memory_fts WHERE assertion_id=?').get(record.id))}
 return {record,seed:{kind:'real-domain-owners',unrelatedID:untouched.id,vectorSpace:space}}
}
function seedGraph(store,memory,cycle){
 configure(store)
 const record=propose(memory,'root','Synthetic graph root'),nodes=[record]
 for(let i=1;i<=12;i++)nodes.push(propose(memory,`child-${i}`,`Synthetic dependent ${i}`,[nodes.at(-1)]))
 const historical=propose(memory,'history-only','Synthetic former dependent',[record])
 memory.update(historical.id,{content:'Synthetic reviewed independent'})
 // A persisted legacy cycle is the same damaged-but-parseable input exercised
 // by tests/memory-lineage.test.ts. No authority or lifecycle callback is faked.
 if(cycle){
  const proofs=[{sources:record.sources,parents:[nodes.at(-1).id]}]
  store.db.prepare('UPDATE memory_supports SET value=? WHERE memory_id=?').run(JSON.stringify(proofs),record.id)
  const version=memory.revisionState(record.id).version
  const row=store.db.prepare('SELECT value FROM memory_assertion_revisions WHERE assertion_id=? AND version=?').get(record.id,version)
  const revision=JSON.parse(row.value);revision.supports=proofs
  store.db.prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=? AND version=?').run(JSON.stringify(revision),record.id,version)
  store.db.prepare('INSERT OR IGNORE INTO memory_assertion_revision_parents VALUES(?,?,?)').run(record.id,version,nodes.at(-1).id)
 }
 return {record,seed:{kind:cycle?'persisted-legacy-cycle':'current-and-historical-chain',nodes:nodes.map(n=>n.id),historicalOnlyID:historical.id}}
}
function seedCommitment(store,memory){
 configure(store)
 const content='Synthetic explicit declaration',messageId='commitment-evidence',person='a'
 const result=memory.propose({...ctx,sourceEvidence:[{...evidence(messageId,content,person),mentions:[{partIndex:0,userId:'b'}]}]},{kind:'fact',content,sources:[source(messageId,person)],subject:{kind:'self',sourceMessageId:messageId},epistemicKind:'explicit',commitment:{kind:'propose',terms:{kind:'plan',title:'Synthetic meeting',participantIds:['a','b'],temporalScope:'future',polarity:'positive',modality:'asserted',conditions:[],timeQualification:'18:00'}}})
 assert.equal(result.status,'saved',JSON.stringify(result));assert.ok(result.commitment?.item,JSON.stringify(result))
 return {record:result.memory,seed:{kind:'real-commitment-evidence',commitmentID:result.commitment.item.ref.id}}
}
const scenarios=[...['redactRecoveredHistory','eraseRecoveredAssertion'].flatMap(operation=>[false,true].map(absent=>({operation,absent,kind:'original'}))),...['redactRecoveredHistory','eraseRecoveredAssertion'].flatMap(operation=>['chain','cycle','owners','commitment','projection'].map(kind=>({operation,absent:false,kind})))]
for(const {operation,absent,kind} of scenarios){
 const temp=mkdtempSync(resolve(root,'backend/.offline-lifecycle-oracle-')),old=process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY;process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=join(temp,'authority');let live,staging,authority
 try{
  const database=join(temp,'data','main.sqlite');mkdirSync(dirname(database));live=new Store(database);const memory=new MemoryService(live)
  let record,seed
  if(kind==='original'){record=memory.create({scope:{kind:'scene',sceneId:'s'},content:'Synthetic retained history',kind:'fact',sources:[{sceneId:'s',messageId:'m',senderId:'p'}]});memory.update(record.id,{content:'Synthetic revised history'})}
  else if(kind==='owners'||kind==='projection')({record,seed}=seedOwners(live,memory,kind==='projection'))
  else if(kind==='commitment')({record,seed}=seedCommitment(live,memory))
  else ({record,seed}=seedGraph(live,memory,kind==='cycle'))
  if(absent)live.db.prepare('DELETE FROM memories WHERE id=?').run(record.id)
  saveGate(live.db,{...metadata(live.db),state:'quarantine'});live.close();live=undefined;authority=new RecoveryAuthority(join(temp,'authority'));staging=new Store(database,{authority});const restored=new MemoryService(staging)
  const entry={name:kind==='original'?`${operation}-${absent?'absent':'current'}`:`${operation}-${kind}`,seed,settings:{projectionEnabled:staging.settings.memoryProjectionEnabled,workingSetEnabled:staging.settings.memoryWorkingSetEnabled,commitmentsEnabled:staging.settings.memoryCommitmentsEnabled,knowledgeEnabled:staging.settings.memoryKnowledgeEnabled,semanticEnabled:staging.settings.memorySemanticEnabled},operation,id:record.id,before:snapshot(staging.db),gateBefore:metadata(staging.db),headBefore:authority.head(),steps:[]}
  for(let repeat=0;repeat<2;repeat++){const step={uuidSequenceBefore:sequence,before:snapshot(staging.db)};try{step.effects=captureRecoveryEffects(staging.db,()=>restored[operation](record.id))}catch(error){step.error=error.message}step.uuidSequenceAfter=sequence;step.after=snapshot(staging.db);step.gate=metadata(staging.db);step.head=authority.head();entry.steps.push(step)}
  assert.ok(entry.steps.every(step=>!step.error),`${entry.name}: ${JSON.stringify(entry.steps.map(step=>step.error))}`)
  if(kind==='owners'||kind==='projection'){
   const after=entry.steps[0].after,effects=entry.steps[0].effects
   assert.equal(after.memory_relation_candidates.length,0);assert.equal(after.memory_relation_entities.length,0);assert.equal(after.memory_relation_tombstones.length,1)
   if(operation==='eraseRecoveredAssertion'){
    assert.equal(after.memory_knowledge_edges.length,0);assert.equal(after.memory_semantic_candidates[0].value,null)
    assert.ok(effects.some(e=>e.kind==='semantic_mutation'));assert.ok(effects.some(e=>e.kind==='working_set_pin'&&e.policy.terminal))
    assert.deepEqual(after.memory_vectors.map(row=>row.memory_id),[seed.unrelatedID])
   }else{
    assert.equal(after.memory_knowledge_edges.length,1);assert.ok(after.memory_semantic_candidates[0].value)
    assert.equal(after.memory_vectors.length,2)
   }
  }
  if(kind==='commitment'&&operation==='eraseRecoveredAssertion')assert.ok(entry.steps[0].effects.some(e=>e.kind==='commitment_head'))
  cases.push(JSON.parse(JSON.stringify(entry).replaceAll(temp,'$FIXTURE')))
 }finally{live?.close();staging?.close();authority?.close();rmSync(temp,{recursive:true,force:true});if(old===undefined)delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY;else process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=old}
}
// Hash the entire real memory-domain source tree, including every lifecycle,
// storage, authority, projection and recovery owner loaded through MemoryService.
const memoryPaths=readdirSync(resolve(root,'src/memory'),{recursive:true,withFileTypes:true}).filter(entry=>entry.isFile()&&entry.name.endsWith('.ts')).map(entry=>resolve(entry.parentPath,entry.name).slice(root.length+1))
const paths=[...new Set(['src/memory.ts','src/store.ts','src/storage/fixed-read-statements.ts','tests/memory-lineage.test.ts',...memoryPaths])].sort()

const result={formatVersion:2,clock:{iso:'2026-10-28T12:00:00.000Z',millis:1793188800000},binaryEncoding:'Node Buffer JSON: {type:Buffer,data:number[]}',sources:Object.fromEntries(paths.map(p=>[p,crypto.createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')])),schemas,cases},output=resolve(process.env.HUMANENGINE_FIXTURE_OUTPUT||'backend/internal/offlinelifecycle/testdata/source.json'),encoded=JSON.stringify(result,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(output,'utf8'),encoded);else{mkdirSync(dirname(output),{recursive:true});writeFileSync(output,encoded)}
console.log(`Actual quarantined lifecycle oracle: ${cases.length} cases`)
