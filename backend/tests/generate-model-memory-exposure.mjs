// Actual source owner + MemoryService/Store/WorkspaceSelectionTracker, synthetic
// SQLite only. No model, network, fake authorization or memory-result stub.
import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { Store } from '../../src/store.ts'
import { MemoryService } from '../../src/memory.ts'
import { ModelMemoryExposure } from '../../src/memory/read/model-exposure.ts'
import { WorkspaceSelectionTracker } from '../../src/memory/working-set/selection-tracker.ts'
assert.equal(process.version, 'v24.22.1')
const outcomes=[]
function run(name,fn){
 const store=new Store(':memory:')
 store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',participationEnabled:true,learningEnabled:true,proactiveEnabled:false})
 const memory=new MemoryService(store)
 const tracker=new WorkspaceSelectionTracker(memory.workspace,()=>true)
 let callback, delivered=0
 const exposure=new ModelMemoryExposure(memory,(ctx,refs)=>{delivered++; tracker.capture(ctx,refs);callback?.()})
 const context=(mode='real')=>({scene:{id:'s'},mode,turnId:'synthetic-turn',signal:new AbortController().signal,dependencies:new Set(),validations:[]})
 const put=(id,shared=false)=>{const r={id,scope:shared?{kind:'sharedPublicKnowledge'}:{kind:'scene',sceneId:'s',personId:'p'},kind:'experience',content:'Synthetic',sources:[],importance:'low',confidence:0.5,locked:false,origin:'manual',lineageStatus:'unverified',status:'active',learnedAt:'2000-01-01T00:00:00.000Z'};store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)').run(id,shared?'shared:public':'scene:s','p','Synthetic',JSON.stringify(r),id);return r}
 try{outcomes.push({name,value:fn({store,memory,tracker,exposure,context,put,callback:fn=>{callback=fn},delivered:()=>delivered})})}finally{memory.dispose();store.close()}
}
run('identity_batch_and_exact_revision',h=>{const a=h.put('a'),b=h.put('b'),c=h.context(),p=h.exposure.prepare(c,[a,b]);const before=h.exposure.readExposed(c);const forged=h.exposure.commit(c,structuredClone(p)).length;const mixed=h.exposure.commit(c,[p[0],structuredClone(p[1])]).length;const foreign=h.exposure.commit(h.context(),p).length;const selected=h.exposure.commit(c,[p[0]]).length;const guard=h.exposure.captureExactGuard(c,'a');const valid=guard();h.store.db.prepare('UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id=?').run('a');return{prepared:p.length,before,forged,mixed,foreign,selected,exposed:h.exposure.readExposed(c),valid,revoked:guard(),delivered:h.delivered()}})
run('stale_sibling_is_atomic',h=>{const c=h.context(),p=h.exposure.prepare(c,[h.put('a'),h.put('b')]);h.store.db.prepare('UPDATE memory_assertion_state SET deletion_epoch=deletion_epoch+1 WHERE assertion_id=?').run('b');return{selected:h.exposure.commit(c,p).length,exposed:h.exposure.readExposed(c),delivered:h.delivered()}})
run('callback_mutation_reauthorizes',h=>{const c=h.context(),p=h.exposure.prepare(c,[h.put('a')]);h.callback(()=>h.memory.lock('a',true));return{selected:h.exposure.commit(c,p).length,exposed:h.exposure.readExposed(c),valid:c.validations.every(fn=>fn()),delivered:h.delivered()}})
run('callback_cannot_replace_selected_identity',h=>{const c=h.context(),p=h.exposure.prepare(c,[h.put('a')]);const original=p[0];h.callback(()=>{p[0]={...p[0],content:'forged during callback'}});const out=h.exposure.commit(c,p);return{selected:out.length,original:out[0]===original,content:out[0]?.content,exposed:h.exposure.readExposed(c),delivered:h.delivered()}})
run('callback_failure_invalidates',h=>{const c=h.context(),p=h.exposure.prepare(c,[h.put('a')]);h.callback(()=>{throw Error('synthetic_callback')});let error;try{h.exposure.commit(c,p)}catch(e){error=e.message};return{error,exposed:h.exposure.readExposed(c),valid:c.validations.every(fn=>fn()),delivered:h.delivered()}})
run('first_exposure_is_immutable',h=>{const c=h.context(),p=h.exposure.prepare(c,[h.put('a')]);h.exposure.commit(c,p);h.memory.lock('a',true);const next=h.exposure.prepare(c,h.memory.list({sceneId:'s',simulation:false}));return{selected:h.exposure.commit(c,next).length,exposed:h.exposure.readExposed(c),valid:c.validations.every(fn=>fn()),delivered:h.delivered()}})
run('duplicate_scores_and_conflicting_bodies',h=>{const c=h.context(),a=h.put('a');const conflict=h.exposure.prepare(c,[a,{...a,content:'different'}]).length;const p=h.exposure.prepare(c,[{...a,score:1},{...a,score:2}]);return{conflict,prepared:p.length,selected:h.exposure.commit(c,p).length,exposed:h.exposure.readExposed(c),delivered:h.delivered()}})
run('simulation_does_not_capture_real_refs',h=>{const c=h.context('simulation'),a=h.put('a',true);const before=h.store.db.prepare('SELECT total_changes() n').get().n;const p=h.exposure.prepare(c,[a]);const n=h.exposure.commit(c,p).length;return{prepared:p.length,selected:n,exposed:h.exposure.readExposed(c),writes:h.store.db.prepare('SELECT total_changes() n').get().n-before,delivered:h.delivered()}})
run('workspace_receipts_use_retained_bodies',h=>{h.store.set("settings",{...h.store.settings,memoryWorkingSetEnabled:true});const c=h.context();const records=Array.from({length:81},(_,i)=>h.put('a'+i));const p=h.exposure.prepare(c,records);h.exposure.commit(c,p);const brief={evidence:[{memories:[{id:'a0'},{id:'never-delivered'}],states:[{page:{items:[{kind:'assertion',snapshot:{ref:{id:'a40'}}},{kind:'reported_experience',assertionRef:{id:'a80'}},{kind:'commitment',assertionRef:{id:'a20'}}]}}]}]};h.tracker.selected(c,brief);h.tracker.selected(c,brief);const rows=h.store.db.prepare("SELECT json_extract(value,'$.snapshot.ref.id') id, json_extract(value,'$.contextSelectedCount') count FROM memory_working_set_items WHERE json_extract(value,'$.contextSelectedCount')>0 ORDER BY id").all();const allBrief={evidence:[{memories:records.map(r=>({id:r.id}))}]};h.tracker.selected(c,allBrief);h.tracker.selected(c,allBrief);const allRows=h.store.db.prepare("SELECT count(*) n, sum(json_extract(value,'$.contextSelectedCount')) total FROM memory_working_set_items WHERE json_extract(value,'$.contextSelectedCount')>0").get();return{prepared:p.length,exposed:h.exposure.readExposed(c).length,rows,allRows}})
const sources=['src/memory/read/model-exposure.ts','src/memory/working-set/selection-tracker.ts','src/memory.ts','src/memory/read/memory-workspace.ts','backend/tests/generate-model-memory-exposure.mjs','package-lock.json']
const hashes=Object.fromEntries(sources.map(p=>[p,createHash('sha256').update(readFileSync(new URL('../../'+p,import.meta.url))).digest('hex')]))
const output=JSON.stringify({hashes,outcomes},null,2)+'\n'
const target=new URL('../internal/memoryservice/testdata/model-exposure-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),output);else{mkdirSync(new URL('.',target),{recursive:true});writeFileSync(target,output)}
