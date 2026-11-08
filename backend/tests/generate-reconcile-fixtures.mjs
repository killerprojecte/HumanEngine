// Actual TypeScript reconciliation, synthetic staging databases only.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import {syncBuiltinESMExports,createRequire} from 'node:module'
import {mkdtempSync,mkdirSync,rmSync,readFileSync,writeFileSync,readdirSync,copyFileSync} from 'node:fs'
import {resolve,join,dirname} from 'node:path'
import {pathToFileURL} from 'node:url'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT||'.'), output=resolve(process.env.HUMANENGINE_FIXTURE_OUTPUT||'reconcile.json'),work=resolve(process.env.HUMANENGINE_ORACLE_WORK||dirname(output))
assert.equal(process.version,'v24.22.1')
const nativeCompare=String.prototype.localeCompare,comparisons=new Map();String.prototype.localeCompare=function(other,...args){const a=String(this),b=String(other),n=nativeCompare.call(this,other,...args);comparisons.set(JSON.stringify([a,b]),[a,b,n]);return n}
let sequence=0;crypto.randomUUID=()=>`00000000-0000-4000-8000-${String(++sequence).padStart(12,'0')}`;syncBuiltinESMExports()
const NativeDate=Date;globalThis.Date=class extends NativeDate{constructor(...args){super(...(args.length?args:['2026-10-28T12:00:00.000Z']))}static now(){return 1793188800000}}
const load=p=>import(pathToFileURL(resolve(root,p)))
const require=createRequire(resolve(root,'package.json')),Database=require('better-sqlite3')
const {Store}=await load('src/store.ts'),{MemoryService}=await load('src/memory.ts')
const {RecoveryAuthority,saveGate,metadata}=await load('src/memory/recovery/ledger.ts')
const {reconcileRecovery}=await load('src/memory/recovery/reconcile.ts')
const {eligibilityIncarnation,eligibilityMaterial}=await load('src/memory/recovery/eligibility-contract.ts')
const {IdentityRegistry}=await load('src/memory/identities/service.ts')
const tables=['config','scenes','recall_tombstones','memories','memory_sources','memory_supports','memory_deleted','memory_assertion_state','memory_assertion_revisions','memory_assertion_revision_sources','memory_assertion_revision_parents','memory_relation_entities','memory_relation_candidates','memory_relation_tombstones','memory_knowledge_edges','memory_semantic_candidates','memory_semantic_attempts','memory_commitment_heads','memory_commitment_revisions','memory_commitment_commands','memory_commitment_transitions','memory_commitment_temporal_windows','memory_working_set_pins','memory_working_set_items','memory_working_set_jobs','memory_working_set_receipts','memory_projection_outbox','memory_person_projection_state','memory_fts','source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones','vector_spaces','memory_vectors','recovery_security_state','recovery_model_hold','memory_identity_registry','budget_external_cost_revisions','habits','habit_deleted','meme_assets','meme_sources','meme_reference_denials','meme_media_references','meme_feedback','meme_search_concepts','memory_experience_outcomes','memory_experience_policies','memory_experience_validations','memory_experience_uses','memory_experience_tombstones','memory_experience_trials']
const schemas={},cases=[]
function snapshot(db){return Object.fromEntries(tables.filter(name=>db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(name)).map(name=>{schemas[name]=db.prepare('SELECT sql FROM sqlite_master WHERE name=?').get(name).sql;const rows=db.prepare(`SELECT * FROM "${name}"`).all();if(name==='recovery_model_hold')for(const row of rows)row.restore_id='$RESTORE_ID';return[name,rows]}))}
const scenarios=['empty','terminal-backup','source-backup','source-already-applied','fingerprint-gap','redaction-duplicates','withdrawal-backup','grant-material-missing','security-missing','unknown-semantic-schema','final-gate-failure-restart','prefix-invalid','identity-unlink-backup','configuration-backup','scene-flags-backup','external-cost-backup','semantic-reject-backup','semantic-admit-backup','semantic-erase-backup','mixed-all-domains-restart']
for(const name of scenarios){
 const temp=mkdtempSync(join(work,'.reconcile-oracle-')),old=process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY;process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=join(temp,'authority');let live,staging,authority
 try{
  const database=join(temp,'live','main.sqlite'),stage=join(temp,'stage','main.sqlite');mkdirSync(dirname(database));mkdirSync(dirname(stage));mkdirSync(join(temp,'media'));live=new Store(database);const memory=new MemoryService(live)
  live.saveScene({id:'s',adapterId:'synthetic',accountId:'bot',platformSceneId:'group',kind:'group',name:'Synthetic',participationEnabled:false,learningEnabled:true,proactiveEnabled:false})
  let record
  if(!['empty','security-missing','unknown-semantic-schema','prefix-invalid','identity-unlink-backup'].includes(name)){
   record=memory.create({scope:{kind:'scene',sceneId:'s'},content:'Synthetic retained declaration',kind:'fact',sources:[{sceneId:'s',messageId:'m',senderId:'p'}]});memory.update(record.id,{content:'Synthetic revised declaration'})
  }
  let registry,left={adapterId:'synthetic',accountId:'bot',personId:'a'},right={adapterId:'synthetic',accountId:'bot',personId:'b'}
  if(name==='identity-unlink-backup'){
   const {recordIdentityMutation}=await load('src/memory/recovery/identities.ts');registry=new IdentityRegistry(live.db,{assertReady:()=>live.assertRecoveryReady(),beforeWrite:m=>recordIdentityMutation(live.db,m)})
   live.db.transaction(()=>registry.link({operationId:'link',expectedRevision:0,adminConfirmed:true},left,right)).immediate()
  }
  const mixed=name==='mixed-all-domains-restart';let mixedDomain,mixedTrial
  if(mixed){
   const fixtures=resolve(process.env.HUMANENGINE_DOMAIN_FIXTURES||join(root,'backend/internal/recoverydomains/testdata'))
   mixedDomain=JSON.parse(readFileSync(join(fixtures,'domains-node.json'),'utf8'));mixedTrial=JSON.parse(readFileSync(join(fixtures,'trials-node.json'),'utf8'))
   const {HabitService}=await load('src/habits.ts'),{MemeService}=await load('src/memes.ts'),{ExperienceStore}=await load('src/memory/experience/store.ts')
   new HabitService(live);new MemeService(live,join(temp,'media'));new ExperienceStore(live.db)
   live.saveScene({id:'scene',adapterId:'synthetic',accountId:'bot',platformSceneId:'other',kind:'group',name:'Synthetic domain',participationEnabled:false,learningEnabled:true,proactiveEnabled:false})
   for(const [table,rows]of Object.entries(mixedDomain.initial))for(const row0 of rows){const row={...row0};if(table==='memory_experience_policies'){row.id='domain-policy';row.value=JSON.stringify({...JSON.parse(row.value),id:'domain-policy'})};const keys=Object.keys(row);live.db.prepare(`INSERT INTO "${table}" (${keys.join(',')}) VALUES(${keys.map(()=>'?').join(',')})`).run(...keys.map(k=>row[k]))}
   live.db.exec('CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)')
   for(const [table,rows]of Object.entries(mixedTrial.initial))for(const row of rows){const keys=Object.keys(row);live.db.prepare(`INSERT INTO "${table}" (${keys.join(',')}) VALUES(${keys.map(()=>'?').join(',')})`).run(...keys.map(k=>row[k]))}
   writeFileSync(join(temp,'media',mixedDomain.media.id+'.png'),Buffer.from(mixedDomain.media.bytes,'base64'))
   live.set('settings',{...live.settings,memoryWorkingSetEnabled:true})
   live.sourceEvents.observe({kind:'ingress',identity:{sceneId:'s',messageId:'m',senderId:'p',adapterId:'synthetic',accountId:'bot'},bodyHash:'a'.repeat(64),occurredAt:null})
   const ctx={sceneId:'s',simulation:false,provenance:'source-only'},item=memory.workspace.workingSet(ctx,{includeCold:true}).items.find(x=>x.kind==='assertion'&&x.snapshot.record.id===record.id);assert.ok(item,'mixed pin material')
   memory.workspace.access.setPinned(ctx,{ref:item.accessibility.ref,dependencyKey:item.accessibility.dependencyKey},{expectedRevision:0,pinned:true})
  }
  let candidate
  if(name.startsWith('semantic-')){
   live.set('settings',{...live.settings,memorySemanticEnabled:true,memorySemanticAutoAdmissionEnabled:false})
   const ctx={sceneId:'s',simulation:false,provenance:'source-only'},parents=[]
   for(const messageId of ['one','two']){
    const content=`I use Synthetic ${messageId}`
    const result=memory.propose({...ctx,sourceEvidence:[{sceneId:'s',messageId,senderId:'p',sourceText:content,parts:[{type:'text',text:content}],available:true,isBot:false}]},{kind:'fact',content,sources:[{sceneId:'s',messageId,senderId:'p'}],subject:{kind:'self',sourceMessageId:messageId},epistemicKind:'explicit'})
    assert.equal(result.status,'saved',JSON.stringify(result));const state=memory.revisionState(result.memory.id),ref={id:result.memory.id,version:state.version,deletionEpoch:state.deletionEpoch},current=memory.semanticCurrent(ctx,ref);assert.ok(current);parents.push({ref,dependencyKey:current.dependencyKey})
   }
   live.db.transaction(()=>{const result=memory.semantic.admit(ctx,parents,[{kind:'summary_candidate',selectedParentIds:parents.map(p=>p.ref.id),text:'Synthetic summary',hypothesis:false}]);assert.equal(result.created.length,1,JSON.stringify(result));candidate=result.created[0];if(name==='semantic-admit-backup')candidate=memory.semantic.review(ctx,candidate.id,candidate.version,'rejected')}).immediate()
  }
  live.db.pragma('wal_checkpoint(TRUNCATE)');copyFileSync(database,stage)
  authority=new RecoveryAuthority(join(temp,'authority'))
  const append=(v,force=false)=>authority.append(v,authority.head(),force)
  if(name==='terminal-backup'||name==='final-gate-failure-restart')memory.delete(record.id)
  if(name==='source-backup'||name==='source-already-applied')memory.recall('s','m')
  if(name==='source-already-applied'){live.db.pragma('wal_checkpoint(TRUNCATE)');copyFileSync(database,stage)}
  if(name==='fingerprint-gap'){const row=live.db.prepare('SELECT fingerprint FROM memories WHERE id=?').get(record.id);append({kind:'fingerprint',value:row.fingerprint})}
  if(name==='redaction-duplicates'){const state=memory.revisionState(record.id);append({kind:'assertion',id:record.id,throughVersion:state.version,terminal:false},true);append({kind:'assertion',id:record.id,throughVersion:state.version,terminal:false},true)}
  if(name==='withdrawal-backup'||name==='grant-material-missing'){
   const current=JSON.parse(live.db.prepare('SELECT value FROM memories WHERE id=?').get(record.id).value),proofs=JSON.parse(live.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(record.id)?.value||'[]')
   append({kind:'assertion_eligibility',schemaVersion:1,operationId:crypto.randomUUID(),mode:'transition',changes:[{id:record.id,incarnationHash:eligibilityIncarnation(current),state:{status:name==='withdrawal-backup'?'invalidated':'active',lineageStatus:null},materialHash:name==='withdrawal-backup'?null:eligibilityMaterial({...current,content:'different material'},proofs)}]})
  }
  if(name==='external-cost-backup'){
   const {ExternalCostService}=await load('src/budget/external-cost/service.ts'),{recordExternalCostReceipt}=await load('src/memory/recovery/external-cost.ts')
   const costs=new ExternalCostService(live.db,{assertReady:()=>live.assertRecoveryReady(),beforeWrite:r=>recordExternalCostReceipt(live.db,r),now:()=>Date.now()}),base={id:'cost-one',sourceId:'synthetic-admin',category:'storage',currency:'USD',billingDate:'2026-10-28',periodStart:'2026-10-01',periodEnd:'2026-10-31'}
   costs.import({...base,status:'known',amountNanoUsd:12345});costs.correct({id:base.id,expectedRevision:1,correctionId:'synthetic-correction',replacement:{...base,status:'known',amountNanoUsd:67890}});costs.import({...base,id:'unknown-cost',status:'unknown'})
  }
  if(name==='semantic-reject-backup'||name==='semantic-admit-backup')live.db.transaction(()=>memory.semantic.review({sceneId:'s',simulation:false},candidate.id,candidate.version,name==='semantic-admit-backup'?'admitted':'rejected')).immediate()
  if(name==='semantic-erase-backup')live.db.transaction(()=>memory.semantic.lifecycle.erase({sceneId:'s',simulation:false},candidate.id)).immediate()
  if(mixed){
   append(mixedTrial.event);append(mixedTrial.erase)
   append({kind:'source',sceneId:'scene',messageId:'message'})
   append({kind:'source',sceneId:'s',messageId:'m'})
   const deletion=mixedDomain.steps.find(x=>x.event.kind==='meme_delete').event
   append(deletion,true);append(deletion,true)
  }
  if(name==='configuration-backup')live.set('settings',{...live.settings,memoryGraphEnabled:true,memoryHistoricalReadEnabled:true})
  if(name==='scene-flags-backup')live.saveScene({...live.scene('s'),memoryReadEnabled:false,personalizationEnabled:false})
  if(name==='security-missing')append({kind:'security_state',revision:crypto.randomUUID()})
  if(name==='identity-unlink-backup')live.db.transaction(()=>registry.unlink({operationId:'unlink',expectedRevision:1,adminConfirmed:true},left,right)).immediate()
  live.close();live=undefined
  const raw=new Database(stage);saveGate(raw,{...metadata(raw),state:'quarantine'});if(name==='prefix-invalid')saveGate(raw,{...metadata(raw),head:{...metadata(raw).head,hash:'f'.repeat(64)}});raw.close()
  staging=new Store(stage,{authority})
  if(name==='unknown-semantic-schema')staging.db.exec('CREATE TABLE memory_semantic_unknown(id TEXT)')
  if(name==='final-gate-failure-restart'||mixed)staging.db.exec("CREATE TRIGGER final_fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.state')='ready' BEGIN SELECT RAISE(ABORT,'synthetic_final_failure'); END")
  const ledger=new Database(join(temp,'authority','authority.sqlite'),{readonly:true}),history=ledger.prepare('SELECT * FROM revocations ORDER BY sequence').all();ledger.close()
  const mediaSnapshot=()=>Object.fromEntries(readdirSync(join(temp,'media')).sort().map(file=>[file,readFileSync(join(temp,'media',file)).toString('base64')]))
  const item={name,mediaBefore:mediaSnapshot(),before:snapshot(staging.db),gateBefore:metadata(staging.db),history,head:authority.head(),steps:[]}
  for(let attempt=0;attempt<(name==='final-gate-failure-restart'||mixed?2:1);attempt++){
   const step={uuidSequenceBefore:sequence,before:snapshot(staging.db)}
   try{step.result=await reconcileRecovery(staging,authority,join(temp,'media'))}catch(error){step.error=error.message}
   step.mediaAfter=mediaSnapshot();step.after=snapshot(staging.db);step.gate=metadata(staging.db);step.head=authority.head();item.steps.push(step)
   if((name==='final-gate-failure-restart'||mixed)&&attempt===0){assert.equal(step.error,'synthetic_final_failure');staging.db.exec('DROP TRIGGER final_fault');staging.close();staging=new Store(stage,{authority})}
  }
  if(['grant-material-missing','security-missing','unknown-semantic-schema','prefix-invalid'].includes(name))assert.ok(item.steps[0].error,name)
  else assert.equal(item.steps.at(-1).error,undefined,`${name}: ${item.steps.at(-1).error}`)
  cases.push(JSON.parse(JSON.stringify(item).replaceAll(temp,'$FIXTURE')))
 }finally{live?.close();staging?.close();authority?.close();rmSync(temp,{recursive:true,force:true});if(old===undefined)delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY;else process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=old}
}
const paths=['src/memory.ts','src/store.ts',...readdirSync(join(root,'src/memory'),{recursive:true,withFileTypes:true}).filter(e=>e.isFile()&&e.name.endsWith('.ts')).map(e=>resolve(e.parentPath,e.name).slice(root.length+1))].sort()
const compareKeys=new Set();for(const[a,b]of comparisons.values()){compareKeys.add(a);compareKeys.add(b)};for(const a of compareKeys)for(const b of compareKeys)a.localeCompare(b)
const result={comparisons:[...comparisons.values()],formatVersion:1,node:process.version,clock:{iso:'2026-10-28T12:00:00.000Z',millis:1793188800000},sources:Object.fromEntries(paths.map(p=>[p,crypto.createHash('sha256').update(readFileSync(join(root,p))).digest('hex')])),schemas,cases};mkdirSync(dirname(output),{recursive:true});writeFileSync(output,JSON.stringify(result,null,2)+'\n');console.log(`Actual reconcile oracle: ${cases.length} cases`)
