// Actual Engine closure ordering, with instrumented owner boundaries only.
import {mkdtempSync,rmSync,readFileSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join,resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
import assert from 'node:assert/strict'
const root=resolve(process.env.HE_SOURCE_ROOT)
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const directory=mkdtempSync(join(tmpdir(),'he-experience-planner-'))
const original=process.cwd(); let engine,store
const cases=[]
try {
 process.chdir(directory); store=new Store(join(directory,'live/main.sqlite')); engine=new Engine(store)
 const extension=engine.runtime.extensions.find(x=>typeof x.plannerContextData==='function')
 assert(extension)
 for(const name of ['simulation','disabled','empty','capture-disabled','selected','assert-failure','begin-failure']) {
  const calls=[]; const ticket={syntheticTicket:true}; const result=name==='empty'?undefined:{experienceHints:[]}
  engine.modelRead.personalizationAllowed=()=>{calls.push('allowed');return name!=='disabled'}
  engine.modelRead.capture=()=>{calls.push('capture');return name==='capture-disabled'?undefined:ticket}
  engine.experienceRuntime.plannerContext=()=>{calls.push('planner');return result}
  engine.modelRead.assert=(value)=>{assert.equal(value,name==='capture-disabled'?undefined:ticket);calls.push('assert');if((name==='assert-failure'||name==='capture-disabled'))throw Error('memory_read_revoked')}
  engine.modelRead.begin=()=>{calls.push('begin');if(name==='begin-failure')throw Error('memory_read_revoked')}
  let output,error
  try {output=extension.plannerContextData({mode:name==='simulation'?'simulation':'real',scene:{id:'s'}},[])} catch(e) {error=e.message}
  cases.push({name,calls,result:output??null,error:error??null})
 }
} finally { await engine?.dispose();store?.close();process.chdir(original);rmSync(directory,{recursive:true,force:true}) }
const output=JSON.stringify({sourceHash:createHash('sha256').update(readFileSync(join(root,'src/engine.ts'))).digest('hex'),policySourceHash:createHash('sha256').update(readFileSync(join(root,'src/memory/read/model-read-policy.ts'))).digest('hex'),cases},null,2)+'\n'
const target=new URL('../internal/app/testdata/experience-planner-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),output);else writeFileSync(target,output)
console.log(`PASS ${cases.length} actual Engine planner-order cases`)
