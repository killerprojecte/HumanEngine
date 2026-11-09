// Concrete source MemoryService/CommitmentService authority and StateExposure.
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {Store} from '../../src/store.ts'
import {MemoryService} from '../../src/memory.ts'
import {CommitmentExposure} from '../../src/memory/read/commitment-exposure.ts'
import {commitmentBackingFingerprint} from '../../src/memory/domain/commitments.ts'
assert.equal(process.version,'v24.22.1')
const fixed='2026-10-28T12:00:00.000Z',outcomes=[]
const ref=(id,version=1)=>({id,version,deletionEpoch:0})
function run(name,fn,count=2){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',participationEnabled:true,learningEnabled:false,proactiveEnabled:false});store.set('settings',{...store.settings,memoryCommitmentsEnabled:true,memoryCommitmentsAnswerEnabled:true});const memory=new MemoryService(store)
 const records=[],proofs=[],heads=[],revisions=[]
 for(let i=0;i<count;i++){
  const id='proposal-'+i,source={sceneId:'s',messageId:id,senderId:'p'}
  const record={id,scope:{kind:'scene',sceneId:'s',personId:'p'},content:'Synthetic '+id,kind:'fact',epistemicKind:'explicit',sources:[source],importance:'low',confidence:0.5,learnedAt:fixed,status:'active',locked:false,origin:'learned',lineageStatus:'verified'}
  const supports=[{sources:[source],parents:[],parentReferences:[],subjectWitness:{kind:'self',personId:'p',sourceMessageId:id}}]
  store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)').run(id,'scene:s','p',record.content,JSON.stringify(record),id)
  store.db.prepare('INSERT INTO memory_supports(memory_id,value) VALUES(?,?)').run(id,JSON.stringify(supports));records.push(record);proofs.push(supports)
  memory.historyFor({sceneId:'s',simulation:false},id)
  const current=memory.authority.relationAuthority({sceneId:'s',simulation:false,provenance:'contextual'},ref(id));assert(current)
  const head={id:'plan-'+i,sceneId:'s',latestVersion:1,activeVersion:null,everActivatedVersion:null,everTerminal:null,deletionEpoch:0,mutationVersion:1,deleted:false}
  const revision={ref:ref(head.id),sceneId:'s',terms:{kind:'plan',title:'Synthetic plan '+i,participantIds:['p'],temporalScope:'future',polarity:'positive',modality:'asserted',conditions:[]},recordedAt:fixed,evidence:[{backing:ref(id),backingFingerprint:commitmentBackingFingerprint(current.record),proofs:current.proofs,kind:'proposal'}],requiredProposalGroups:[]}
  store.db.transaction(()=>memory.commitments.storage.put(head,revision))();heads.push(head);revisions.push(revision)
 }
 const exposure=new CommitmentExposure({commitments:memory.commitments,enabled:()=>store.settings.memoryCommitmentsEnabled&&store.settings.memoryCommitmentsAnswerEnabled,ready:()=>{store.assertRecoveryReady();return true}})
 const context=(mode='real')=>({scene:{id:'s'},mode,turnId:'synthetic',signal:new AbortController().signal,dependencies:new Set(),validations:[]})
 try{outcomes.push({name,records,proofs,heads,revisions,value:fn({store,memory,exposure,context})})}finally{memory.dispose();store.close()}
}
const caught=fn=>{try{return{result:fn()}}catch(e){return{error:e.message}}}
run('whole_receipts_and_current_refs',h=>{const c=h.context(),values=h.exposure.query(c);return{values,preparedRefs:h.exposure.readExposedRefs(c),forged:h.exposure.deliver(c,structuredClone(values)).length,partial:h.exposure.deliver(c,values.slice(0,1)).length,reversed:h.exposure.deliver(c,[...values].reverse()).length,foreign:h.exposure.deliver(h.context(),values).length,delivered:h.exposure.deliver(c,values).length,repeat:h.exposure.deliver(c,values).length,refs:h.exposure.readExposedRefs(c),dependencies:[...c.dependencies],valid:h.exposure.captureGuard(c)(),simulation:h.exposure.query(h.context('simulation')).length}})
run('recall_invalidates_original_projection',h=>{const c=h.context(),values=h.exposure.query(c);h.exposure.deliver(c,values);const guard=h.exposure.captureGuard(c);h.store.db.prepare('INSERT INTO recall_tombstones VALUES(?,?,?)').run('s','proposal-0',fixed);return{guard:guard(),present:!!h.exposure.captureGuard(c),refs:h.exposure.readExposedRefs(c),stale:h.exposure.deliver(c,values).length,next:h.exposure.query(h.context()),turnValid:c.validations.every(f=>f())}})
run('answer_flag_and_head_mutation_revoke',h=>{const c=h.context(),values=h.exposure.query(c);h.exposure.deliver(c,values);const guard=h.exposure.captureGuard(c);h.store.db.transaction(()=>{const head=h.memory.commitments.storage.head('plan-0');h.memory.commitments.storage.put({...head,mutationVersion:head.mutationVersion+1})})();const mutationValid=guard();h.store.set('settings',{...h.store.settings,memoryCommitmentsAnswerEnabled:false});return{mutationValid,guard:guard(),present:!!h.exposure.captureGuard(c),query:h.exposure.query(c),refs:h.exposure.readExposedRefs(c),turnValid:c.validations.every(f=>f())}})
run('source_default_filters_and_empty',h=>({other:h.exposure.query(h.context(),{personId:'other'}),terminal:h.exposure.query(h.context(),{lifecycle:'terminal'}),all:h.exposure.query(h.context(),{lifecycle:'all'}),ownUndefined:h.exposure.query(h.context(),{lifecycle:undefined}),invalid:caught(()=>h.exposure.query(h.context(),{lifecycle:'invalid'}))}))
run('complete_page_required_at_source_limit',h=>caught(()=>h.exposure.query(h.context())),13)
const files=['src/engine.ts','src/memory.ts','src/memory/read/state-exposure.ts','src/memory/read/commitment-exposure.ts','src/memory/commitments/service.ts','src/memory/commitments/transitions.ts','src/memory/read/assertion-authority.ts','backend/tests/generate-commitment-exposure.mjs'];const hashes=Object.fromEntries(files.map(f=>[f,createHash('sha256').update(readFileSync(new URL('../../'+f,import.meta.url))).digest('hex')]));const output=JSON.stringify({fixed,hashes,outcomes},null,2)+'\n';const target=new URL('../internal/memoryservice/testdata/commitment-exposure-source.json',import.meta.url);if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),output);else writeFileSync(target,output)
