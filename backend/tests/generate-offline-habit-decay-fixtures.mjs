/** Freeze actual HabitService recall with fractional default-policy decay. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {resolve} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../',import.meta.url))
const {Store}=await import(pathToFileURL(resolve(root,'src/store.ts')).href)
const {HabitService,DEFAULT_HABIT_POLICY}=await import(pathToFileURL(resolve(root,'src/habits.ts')).href)
assert.equal(process.version,'v24.22.1')
const sha=x=>createHash('sha256').update(x).digest('hex')
const now=1793701056789,day=86400000
const cases=[]
for(const [name,ages,directions] of [
 ['mixed-first-fraction',[0,123456789],[-1,1]],
 ['mixed-three-fractions',[1234,123456789,234567891],[1,-1,1]],
 ['negative-three-fractions',[1234,123456789,234567891],[-1,1,-1]],
 ['fraction-near-retention',[89*day+12345,90*day-1],[1,-1]],
 ['fraction-near-half-life',[30*day-1,30*day+1],[1,-1]],
 ['future-source',[-123456789,123456789],[1,-1]],
 ['fraction-positive',[345678912,456789123],[1,1]],
 ['fraction-negative',[345678912,456789123],[-1,-1]],
]) {
 const store=new Store(':memory:')
 try {
  const scene={id:'s',kind:'group',adapterId:'synthetic',accountId:'account',platformSceneId:'s',name:'Fixture',participationEnabled:true,learningEnabled:true,proactiveEnabled:false}
  store.saveScene(scene)
  const habits=new HabitService(store,{now:()=>now})
  const source=(id,timestamp,direction)=>({sceneId:'s',messageId:id,senderId:'p'+id,timestamp,direction})
  const record={id:'h',kind:'botExpression',preference:'concise',evidenceVersion:2,sources:[source('drop',now,1),...ages.map((age,i)=>source('keep'+i,now-age,directions[i]))],weight:0,confidence:0,status:'active',locked:false,learnedAt:'2026-11-02T00:00:00.000Z'}
  const initial={id:'h',fingerprint:'synthetic-fingerprint',value:JSON.stringify(record)}
  store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(initial.id,initial.fingerprint,initial.value)
  const sourceToRecall={sceneId:'s',messageId:'drop'}
  const changed=habits.recall(sourceToRecall)
  const final=store.db.prepare('SELECT value FROM habits WHERE id=?').get('h').value
  const identity=habits.validationIdentity({sceneId:'s',simulation:false},'h')??null
  assert.equal(changed,1)
  cases.push({name,now,initial,event:{kind:'source',...sourceToRecall},final,identity})
 } finally {store.close()}
}
const result={source:{node:process.version,v8:process.versions.v8,glibc:process.report.getReport().header.glibcVersionRuntime,files:Object.fromEntries(['src/habits.ts','src/store.ts','src/memory/recovery/ledger.ts'].map(p=>[p,sha(readFileSync(resolve(root,p)))])),generatorSHA256:sha(readFileSync(new URL(import.meta.url)))},policy:DEFAULT_HABIT_POLICY,cases}
const target=new URL('../internal/recoverydomains/testdata/habit-decay-node.json',import.meta.url)
const json=JSON.stringify(result,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),json)
else writeFileSync(target,json)
console.log(`${cases.length} actual source offline habit decay cases ${process.argv.includes('--check')?'checked':'written'}`)
