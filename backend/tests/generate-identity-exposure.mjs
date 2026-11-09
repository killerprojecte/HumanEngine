// Concrete IdentityFacade + IdentityExposure + shared StateExposure oracle.
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {isDeepStrictEqual} from 'node:util'
import {Store} from '../../src/store.ts'
import {MemoryService} from '../../src/memory.ts'
import {IdentityExposure} from '../../src/memory/read/identity-exposure.ts'
import {StateExposure} from '../../src/memory/read/state-exposure.ts'
import {ModelReadPolicy} from '../../src/memory/read/model-read-policy.ts'
assert.equal(process.version,'v24.22.1')
const now=1793188800000,priorNow=Date.now;Date.now=()=>now
const outcomes=[]
const endpoint=p=>({adapterId:'adapter',accountId:'account',personId:p})
const scene={id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',participationEnabled:true,learningEnabled:false,proactiveEnabled:false}
const sourceIds=values=>values.flatMap(v=>v.page.items.flatMap(item=>item.aliases.flatMap(a=>a.sourceMessageId?[a.sourceMessageId]:[])))
const records=['p','q'].map(personId=>({id:'m-'+personId,scope:{kind:'scene',sceneId:'s',personId},kind:'experience',content:'Synthetic',sources:[],importance:'low',confidence:0.5,locked:false,origin:'manual',lineageStatus:'unverified',status:'active',learnedAt:'2000-01-01T00:00:00.000Z'}))
function run(name,fn){const store=new Store(':memory:');store.saveScene(scene);const memory=new MemoryService(store);const policy=new ModelReadPolicy(store)
 try{
  for(const r of records)store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)').run(r.id,'scene:s',r.scope.personId,r.content,JSON.stringify(r),r.id)
  let revision=0
  const mutate=fn=>store.db.transaction(()=>{const result=fn({operationId:'op-'+revision,expectedRevision:revision,adminConfirmed:true});revision=result.revision;return result})()
  mutate(c=>memory.identities.adminLink(c,endpoint('p'),endpoint('q')))
  mutate(c=>memory.identities.adminLink(c,endpoint('q'),endpoint('invisible')))
  mutate(c=>memory.identities.putManualAlias(c,{id:'alias-p',endpoint:endpoint('p'),label:'Synthetic P',scopeSceneId:'s',validFrom:null,validUntil:null},'admin'))
  mutate(c=>memory.identities.putManualAlias(c,{id:'alias-q',endpoint:endpoint('q'),label:'Synthetic Q',scopeSceneId:'s',validFrom:null,validUntil:null},'admin'))
  mutate(c=>memory.identities.observeAlias({sceneId:'s',simulation:false,sourceEvidence:[{sceneId:'s',messageId:'observed-name',senderId:'p',available:true,isBot:false}]},c,{id:'observed',endpoint:endpoint('p'),label:'Observed P',scopeSceneId:'s',validFrom:null,validUntil:null,source:{kind:'observation',sceneId:'s',messageId:'observed-name',senderId:'p'}}))
  const registry=JSON.parse(store.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get().value)
  const identity=new IdentityExposure({query:(c,p)=>memory.identities.query(c,p),allowed:c=>c.mode==='real'&&policy.personalizationAllowed(c.scene.id)})
  const context=(mode='real')=>({scene:{id:'s'},mode,turnId:'synthetic',signal:new AbortController().signal,dependencies:new Set(),validations:[]})
  const result=fn({store,memory,identity,context,mutate,personal:v=>store.saveScene({...store.scene('s'),personalizationEnabled:v})})
  outcomes.push({name,registry,value:result})
 }finally{memory.dispose();store.close()}}
try{
run('identity_exact_scope_and_receipts',h=>{const c=h.context(),values=h.identity.query(c,'p'),noneBefore=h.identity.captureGuard(c)===undefined,forged=h.identity.deliver(c,structuredClone(values)).length,foreign=h.identity.deliver(h.context(),values).length,delivered=h.identity.deliver(c,values).length;const guard=h.identity.captureGuard(c);return{values,noneBefore,forged,foreign,delivered,repeat:h.identity.deliver(c,values).length,valid:guard(),dependencies:[...c.dependencies],turnValid:c.validations.every(f=>f()),simulation:h.identity.query(h.context('simulation'),'p').length,invisible:h.identity.query(c,'invisible').length}})
run('identity_recall_and_detached_guard',h=>{const c=h.context(),values=h.identity.query(c,'p');h.identity.deliver(c,values);const guard=h.identity.captureGuard(c);h.store.db.prepare('INSERT INTO recall_tombstones VALUES(?,?,?)').run('s','observed-name','2026-10-28T12:00:00.000Z');return{guard:guard(),stillPresent:h.identity.captureGuard(c)!==undefined,turnValid:c.validations.every(f=>f())}})
run('identity_policy_revocation',h=>{const c=h.context(),values=h.identity.query(c,'p');h.identity.deliver(c,values);const guard=h.identity.captureGuard(c);h.personal(false);return{guard:guard(),stillPresent:h.identity.captureGuard(c)!==undefined,query:h.identity.query(c,'p').length}})
run('state_whole_order_callback_once',h=>{let delivered=0;const query=c=>[...h.identity.query(c,'p'),...h.identity.query(c,'q')];const states=new StateExposure({query:c=>query(c),valid:(c,values)=>isDeepStrictEqual(query(c),values),sourceIds,onDelivered:()=>delivered++});const c=h.context(),values=states.query(c,null);const partial=states.deliver(c,values.slice(0,1)).length,reordered=states.deliver(c,[...values].reverse()).length,selected=states.deliver(c,values).length,repeat=states.deliver(c,values).length;return{partial,reordered,selected,repeat,delivered,read:states.readDelivered(c).length,dependencies:[...c.dependencies],turnValid:c.validations.every(f=>f())}})
run('state_callback_failure_sticky',h=>{const query=c=>h.identity.query(c,'p');const states=new StateExposure({query:c=>query(c),valid:(c,v)=>isDeepStrictEqual(query(c),v),sourceIds,onDelivered:()=>{throw Error('synthetic_callback')}});const c=h.context(),values=states.query(c,null);let error;try{states.deliver(c,values)}catch(e){error=e.message};return{error,read:states.readDelivered(c).length,hasGuard:states.captureGuard(c)!==undefined,turnValid:c.validations.every(f=>f())}})
run('state_callback_reauthorization',h=>{let delivered=0;const query=c=>h.identity.query(c,'p');const states=new StateExposure({query:c=>query(c),valid:(c,v)=>isDeepStrictEqual(query(c),v),sourceIds,onDelivered:()=>{delivered++;h.mutate(c=>h.memory.identities.removeAlias(c,'alias-p'))}});const c=h.context(),values=states.query(c,null);const selected=states.deliver(c,values).length;return{selected,delivered,read:states.readDelivered(c).length,hasGuard:states.captureGuard(c)!==undefined,turnValid:c.validations.every(f=>f()),registryAfter:JSON.parse(h.store.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get().value)}})
}finally{Date.now=priorNow}
const files=['src/memory/read/model-read-policy.ts','src/memory/read/state-exposure.ts','src/memory/read/identity-exposure.ts','src/memory/identities/service.ts','src/memory/identities/facade.ts','src/memory/identities/storage.ts','src/memory/identities/contracts.ts','backend/tests/generate-identity-exposure.mjs'];const hashes=Object.fromEntries(files.map(f=>[f,createHash('sha256').update(readFileSync(new URL('../../'+f,import.meta.url))).digest('hex')]));const out=JSON.stringify({hashes,records,outcomes},null,2)+'\n';const target=new URL('../internal/memoryservice/testdata/identity-exposure-source.json',import.meta.url);if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),out);else{mkdirSync(new URL('.',target),{recursive:true});writeFileSync(target,out)}
