// Actual Engine.proposeHabit transformation. Capture at its real HabitService
// boundary; native integration separately executes real writes, not this capture.
import assert from 'node:assert/strict'
import {mkdtempSync,readFileSync,writeFileSync,rmSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join,resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
assert.equal(process.version,'v24.22.1');assert.equal(process.env.TZ,'UTC')
const root=resolve(process.env.HE_SOURCE_ROOT), {Store}=await import(pathToFileURL(join(root,'src/store.ts'))),{Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const dir=mkdtempSync(join(tmpdir(),'he-habit-call-')),old=process.cwd();process.chdir(dir)
const store=new Store(join(dir,'live','main.sqlite')),engine=new Engine(store)
const m={id:'m',sceneId:'s',senderId:'p',origin:'live',status:'available',sentAt:'2026-11-02T00:00:00.000Z',parts:[{type:'text',text:'assistant always use concise replies'},{type:'image',url:'synthetic'},{type:'text',text:'second'}]}
const base={kind:'botExpression',preference:'concise',direction:1,sourceMessageIds:['m']}
const specs=[['ordinary',false,base,[m]],['simulation-invalid',true,{},[]],['missing-author',false,{...base,sourceMessageIds:['missing']},[m]],['duplicate-first-author',false,base,[m,{...m,senderId:'second'}]],['simulation-filter',false,base,[{...m,origin:'simulation'},m]],['unavailable',false,base,[{...m,status:'recalled'}]],['limited',false,base,[{...m,sourceEventRegistrationLimited:true}]],['invalid-date',false,base,[{...m,sentAt:'not a date'}]],['legacy-date',false,base,[{...m,sentAt:'Nov 2 2026 00:00:00 GMT'}]],['scene-rule',false,{kind:'sceneRule',content:'Use short replies',sourceMessageIds:['m']},[m]],['extra-key',false,{...base,extra:true},[m]],['empty-ids',false,{...base,sourceMessageIds:[]},[m]]]
const cases=[],tools=[]
store.saveScene({id:"s",kind:"group",adapterId:"synthetic",accountId:"account",platformSceneId:"s",name:"Synthetic",participationEnabled:true,learningEnabled:true,proactiveEnabled:false})
const execute=engine.runtime.extensions.find(e=>e.execute?.toString().includes("habits.feedback")).execute
assert.equal(typeof execute,'function')
const toolSpecs=[
 ['propose-captured','habits.propose',false,base,[m]],
 ['propose-simulation-captured','habits.propose',true,base,[]],
 ['propose-simulation-invalid','habits.propose',true,{},[]],
 ['propose-simulation-fallback','habits.propose',true,base,null],
 ['feedback-captured','habits.feedback',false,{id:'h',direction:1,sourceMessageIds:['m']},[m]],
 ['feedback-simulation-invalid','habits.feedback',true,{},null],
 ['feedback-invalid-direction','habits.feedback',false,{id:'h',direction:0,sourceMessageIds:['m']},[m]],
 ['feedback-extra-ignored','habits.feedback',false,{id:'h',direction:-1,sourceMessageIds:[],extra:true},[]],
]

try {for(const[name,simulation,input,messages]of specs){let capture=null;engine.habits.propose=(context,candidate)=>{capture={context,candidate};return {status:'captured'}};let result,error=null;try{result=engine.proposeHabit('s',simulation,input,messages)}catch(e){error=e.name};cases.push({name,simulation,input,messages,result:result??null,error,capture})}
 for(const[name,tool,simulation,input,snapshots]of toolSpecs){let capture=null;engine.habits.propose=(context,candidate)=>{capture={context,candidate};return {status:'captured'}};engine.habits.feedback=(context,id,candidate)=>{capture={context,id,candidate};return {status:'captured'}};engine.runtime.preview=()=>[m];const context={scene:{id:'s',kind:'group'},mode:simulation?'simulation':'real',dependencies:new Set(),validations:[],signal:new AbortController().signal};if(snapshots!==null)context.sourceSnapshots=snapshots;let result,error=null;try{result=await execute(tool,input,context)}catch(e){error=e.name+':'+e.message};tools.push({name,tool,simulation,input,snapshots,preview:[m],result:result??null,error,capture})}
}finally{await engine.dispose();store.close();process.chdir(old);rmSync(dir,{recursive:true,force:true})}
const hashes=Object.fromEntries(['src/engine.ts','src/habits.ts','src/memory/learning/contracts.ts','package-lock.json'].map(p=>[p,createHash('sha256').update(readFileSync(join(root,p))).digest('hex')]))
const output=new URL('../internal/habitengine/testdata/source.json',import.meta.url),text=JSON.stringify({node:process.version,timezone:'UTC',boundary:'actual Engine.proposeHabit argument capture; not a persisted write oracle',hashes,cases,tools},null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(output,'utf8'),text);else writeFileSync(output,text)
console.log(`${cases.length} Engine proposals and ${tools.length} actual tool branch captures verified`)
