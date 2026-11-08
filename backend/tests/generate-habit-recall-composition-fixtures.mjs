/** Actual Engine.runtime.recall on a disk-enrolled Store and independent ledger.
 * No model, adapter, startup replay or network work is run. Synthetic data only. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve, join } from 'node:path'
import { pathToFileURL, fileURLToPath } from 'node:url'
const root = resolve(process.env.HE_SOURCE_ROOT ?? fileURLToPath(new URL('../../', import.meta.url)))
assert.equal(process.version, 'v24.22.1')
const { Store } = await import(pathToFileURL(join(root,'src/store.ts')))
const { Engine } = await import(pathToFileURL(join(root,'src/engine.ts')))
const { metadata } = await import(pathToFileURL(join(root,'src/memory/recovery/ledger.ts')))
const output=new URL('../internal/habitservice/testdata/recall-composition.json',import.meta.url)
const scenarios=[['success',null],['memory-append-failure','memory:append'],['meme-append-failure','meme:append'],['habit-append-failure','habit:append'],['memory-ack-failure','memory:ack'],['habit-ack-failure','habit:ack'],['habit-tombstone-failure','habit:tombstone']]
const cases=[]
const originalCwd=process.cwd()
for(const [name,fault] of scenarios){
 const temporary=mkdtempSync(join(tmpdir(),'he-habit-recall-'));let store,engine;const events=[];let phase='setup'
 try{
  process.chdir(temporary)
  store=new Store(join(temporary,'live','main.sqlite'))
  store.saveScene({id:'s',kind:'group',adapterId:'synthetic',accountId:'account',platformSceneId:'s',name:'Synthetic',participationEnabled:true,learningEnabled:true,proactiveEnabled:false})
  engine=new Engine(store)
  const authority=store.recoveryAuthority
  assert.ok(authority,'fixture must have actual independent durable authority')
  const before=authority.head().sequence
  const report=(type,detail={})=>{events.push({type,phase,...detail});if(fault===`${phase}:${type}`)throw Error(`synthetic_${phase}_${type}`)}
  const append=authority.append.bind(authority)
  authority.append=(event,expected,force)=>{report('append',{payload:event,expectedSequence:expected.sequence});const next=append(event,expected,force);events.push({type:'append-return',phase,sequence:next.sequence});return next}
  store.db.function('fixture_ack',sequence=>{report('ack',{sequence});return 0})
  store.db.function('fixture_tombstone',()=>{report('tombstone');return 0})
  store.db.exec(`CREATE TRIGGER fixture_gate_ack AFTER UPDATE ON recovery_gate BEGIN SELECT fixture_ack(json_extract(NEW.value,'$.head.sequence')); END;
    CREATE TRIGGER fixture_source_attempt BEFORE INSERT ON recall_tombstones BEGIN SELECT fixture_tombstone(); END;`)
  const wrapped=(owner,key,label)=>{const original=owner[key].bind(owner);owner[key]=(...args)=>{const previous=phase;phase=label;events.push({type:'enter',phase});try{return original(...args)}finally{events.push({type:'leave',phase});phase=previous}}}
  const originalRecall=store.recall.bind(store)
  store.recall=(...args)=>{events.push({type:'store-recall',phase});return originalRecall(...args)}
  wrapped(engine.experience,'revokeSource','experience')
  const originalPrepare=engine.memory.prepareRecall.bind(engine.memory)
  engine.memory.prepareRecall=(...args)=>{const apply=originalPrepare(...args);return ()=>{const previous=phase;phase='memory';events.push({type:'enter',phase});try{return apply()}finally{events.push({type:'leave',phase});phase=previous}}}
  wrapped(engine.memes,'recall','meme')
  wrapped(engine.habits,'recall','habit')
  wrapped(engine.learning,'recall','learning')
  wrapped(engine.contextualLearning.queue,'recall','contextual-learning')
  const now=Date.now();const habit={id:'h',kind:'sceneRule',sceneId:'s',content:'Synthetic',evidenceVersion:2,sources:[{sceneId:'s',messageId:'m1',senderId:'p',direction:1,timestamp:now}],weight:.05,confidence:1/3,status:'active',locked:false,learnedAt:new Date(now).toISOString()}
  store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run('h','h',JSON.stringify(habit))
  phase='root';let error=null
  try{engine.runtime.recall('s','m1')}catch(e){error=e.message}
  phase='inspect'
  const after=authority.head().sequence
  const rows=authority.db.prepare('SELECT sequence,payload FROM revocations WHERE sequence>? ORDER BY sequence').all(before)
  const retained=JSON.parse(store.db.prepare('SELECT value FROM habits WHERE id=?').get('h').value)
  cases.push({name,fault,events,error,authoritySequenceDelta:after-before,gateSequenceDelta:metadata(store.db).head.sequence-before,durablePayloads:rows.map(r=>JSON.parse(r.payload)),tombstones:store.db.prepare('SELECT scene_id,message_id FROM recall_tombstones').all(),habit:{status:retained.status,sourceCount:retained.sources.length},databaseEnrolled:true})
 }finally{
  if(engine)await engine.dispose()
  store?.close();process.chdir(originalCwd);rmSync(temporary,{recursive:true,force:true})
 }
}
const files=['src/runtime.ts','src/engine.ts','src/store.ts','src/habits.ts','src/memes.ts','src/memory/lifecycle/assertion-lifecycle.ts','src/memory/recovery/ledger.ts','package-lock.json']
const hashes=Object.fromEntries(files.map(p=>[p,createHash('sha256').update(readFileSync(join(root,p))).digest('hex')]))
hashes['backend/tests/generate-habit-recall-composition-fixtures.mjs']=createHash('sha256').update(readFileSync(fileURLToPath(import.meta.url))).digest('hex')
const value=JSON.stringify({node:process.version,entrypoint:'actual Engine.runtime.recall; engine.start is never called',hashes,cases},null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(output,'utf8'),value)
else{mkdirSync(new URL('.',output),{recursive:true});writeFileSync(output,value)}
console.log(`${cases.length} actual Engine enrolled recall traces verified`)
