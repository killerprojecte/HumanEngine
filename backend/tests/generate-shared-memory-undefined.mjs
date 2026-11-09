// Actual MemoryService/sanitize/ModelMemoryExposure and Node JSON semantics.
// This output is independent of the native implementation under test.
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {Store} from '../../src/store.ts'
import {MemoryService} from '../../src/memory.ts'
import {ModelMemoryExposure} from '../../src/memory/read/model-exposure.ts'
import {WorkspaceSelectionTracker} from '../../src/memory/working-set/selection-tracker.ts'
import {eligibilityMaterial} from '../../src/memory/recovery/eligibility-contract.ts'
assert.equal(process.version,'v24.22.1')
const record={id:'shared',scope:{kind:'sharedPublicKnowledge'},kind:'experience',content:'Synthetic',sources:[],importance:'low',confidence:0.5,locked:false,origin:'manual',lineageStatus:'unverified',status:'active',learnedAt:'2000-01-01T00:00:00.000Z'}
const own={before:undefined,kept:null,after:undefined,nested:{missing:undefined,kept:true}}
const serialization={object:JSON.stringify(own),keys:Object.keys(own),array:JSON.stringify([undefined,null,own]),rootKind:typeof JSON.stringify(undefined),nullValue:JSON.stringify(null),emptyObject:JSON.stringify({}),parsedNullIsUndefined:JSON.parse('null')===undefined,parsedStringIsUndefined:JSON.parse('"undefined"')===undefined}
const material=eligibilityMaterial({...record,supersedesId:undefined},[undefined,null])
const outcomes=[]
for(const mode of ['real','simulation']){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',participationEnabled:true,learningEnabled:false,proactiveEnabled:false});const memory=new MemoryService(store);const tracker=new WorkspaceSelectionTracker(memory.workspace,()=>true);const exposure=new ModelMemoryExposure(memory,(c,r)=>tracker.capture(c,r));
 try{
  store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)').run(record.id,'shared:public',null,record.content,JSON.stringify(record),record.id)
  const ctx=()=>({scene:{id:'s'},mode,turnId:'synthetic-turn',signal:new AbortController().signal,dependencies:new Set(),validations:[]})
  const current=memory.list({sceneId:'s',simulation:mode==='simulation'})[0]
  assert(current);assert(Object.hasOwn(current,'supersedesId'));assert.equal(current.supersedesId,undefined)
  const before=store.db.prepare('SELECT total_changes() n').get().n
  const raw=exposure.prepare(ctx(),[record]).length
  const missing=structuredClone(current);delete missing.supersedesId
  const deleted=exposure.prepare(ctx(),[missing]).length
  const nulled=exposure.prepare(ctx(),[{...current,supersedesId:null}]).length
  const c=ctx(),prepared=exposure.prepare(c,[current]);const delivered=exposure.commit(c,prepared)
  const writes=store.db.prepare('SELECT total_changes() n').get().n-before
  outcomes.push({mode,raw,deleted,nulled,prepared:prepared.length,delivered:delivered.length,ownUndefined:Object.hasOwn(current,'supersedesId')&&current.supersedesId===undefined,jsonSame:JSON.stringify(current)===JSON.stringify(record),exposed:exposure.readExposed(c),simulationWrites:mode==='simulation'?writes:null})
 }finally{memory.dispose();store.close()}
}
const files=['src/memory/read/assertion-authority.ts','src/memory/read/model-exposure.ts','src/memory/recovery/eligibility-contract.ts','src/memory.ts','backend/tests/generate-shared-memory-undefined.mjs']
const hashes=Object.fromEntries(files.map(p=>[p,createHash('sha256').update(readFileSync(new URL('../../'+p,import.meta.url))).digest('hex')]))
const output=JSON.stringify({hashes,serialization,record,material,outcomes},null,2)+'\n';const target=new URL('../internal/jsjson/testdata/native-undefined.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),output);else writeFileSync(target,output)
