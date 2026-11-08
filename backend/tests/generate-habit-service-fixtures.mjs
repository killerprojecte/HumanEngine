/** Actual TypeScript HabitService + Store oracle. Synthetic data only.
 * Run: HE_SOURCE_ROOT=/path/to/baseline node --import /path/to/baseline/node_modules/tsx/dist/loader.mjs this-file.mjs
 * IDs assigned by propose are renamed in SQLite immediately after each action;
 * no other raw bytes/order are changed. Subsequent identities hash those exact bytes.
 */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const root = process.env.HE_SOURCE_ROOT ?? fileURLToPath(new URL('../../', import.meta.url))
const { Store } = await import(pathToFileURL(resolve(root, 'src/store.ts')).href)
const { HabitService } = await import(pathToFileURL(resolve(root, 'src/habits.ts')).href)
assert.equal(process.version, 'v24.22.1')
const NOW = Date.parse('2026-11-02T00:00:00.000Z'), DAY = 86400000
const sha = x => createHash('sha256').update(x).digest('hex')
const scene = (id, extra = {}) => ({id,kind:'group',adapterId:'synthetic',accountId:'account',platformSceneId:id,name:'Fixture',participationEnabled:true,learningEnabled:true,proactiveEnabled:false,...extra})
const scenes = [scene('s'),scene('other'),scene('off',{learningEnabled:false})]
const source = (messageId='m1', senderId='p1', sceneId='s', extra={}) => ({sceneId,messageId,senderId,...extra})
const ctx = (sources=[source()], text='assistant always use concise replies', extra={}) => ({sceneId:'s',simulation:false,sourceEvidence:sources.map(s=>({...s,available:true,isBot:false,origin:'live',timestamp:NOW,text})),...extra})
const propose = (input={kind:'botExpression',preference:'concise',direction:1,sources:[source()]}, context=ctx(input.sources)) => ({method:'propose',ctx:context,input})
const feedback = (sources=[source('m2','p2')], direction=-1, context=ctx(sources)) => ({method:'feedback',id:'habit-1',ctx:context,input:{direction,sources}})
const list = (extra={}) => ({method:'list',ctx:{sceneId:'s',simulation:false,...extra}})
const identities = (ids=['habit-1'], extra={}) => ({method:'validationIdentities',ctx:{sceneId:'s',simulation:false,...extra},ids})
const admin = {method:'adminList'}
const record = (id='seed', extra={}) => ({id,kind:'sceneRule',sceneId:'s',content:'Synthetic rule',evidenceVersion:2,sources:[{...source(),direction:1,timestamp:NOW}],weight:0,confidence:0,status:'active',locked:false,learnedAt:new Date(NOW).toISOString(),...extra})
const row = (r, fingerprint=r.id, id=r.id) => ({id,fingerprint,value:JSON.stringify(r)})
const cases=[]
function trace(name, actions, options={}) {
 const store=new Store(':memory:'); let now=NOW, clockMutation, serial=0
 const invokeClock=()=>{const m=clockMutation; clockMutation=undefined;if(m) execute(m);return now}
 let habits
 const initial={scenes:options.scenes??scenes,rows:options.rows??[],deleted:options.deleted??[]}
 function execute(a) {
  switch(a.method) {
   case 'propose': return habits.propose(a.ctx,a.input)
   case 'feedback': return habits.feedback(a.ctx,a.id,a.input)
   case 'list': return habits.list(a.ctx)
   case 'adminList': return habits.adminList()
   case 'validationIdentity': return habits.validationIdentity(a.ctx,a.id)??null
   case 'validationIdentities': return [...habits.validationIdentities(a.ctx,a.ids)].map(([id,v])=>[id,v??null])
   case 'update': return habits.update(a.id,a.patch)
   case 'delete': habits.delete(a.id);return null
   case 'recall': return habits.recall(a.source)
   case 'storeRecall': store.recall(a.source.sceneId,a.source.messageId);return null
   case 'advance': now+=a.ms;return null
   case 'restart': habits=new HabitService(store,{now:invokeClock,policy:options.policy});return null
   case 'armClock': clockMutation=a.action;return null
   case 'rawPut': store.db.prepare('INSERT OR REPLACE INTO habits VALUES(?,?,?)').run(a.row.id,a.row.fingerprint,a.row.value);return null
   case 'rawDelete': store.db.prepare('DELETE FROM habits WHERE id=?').run(a.id);return null
   case 'saveScene': store.saveScene(a.scene);return null
   case 'sourceObserve': store.sourceEvents.observe(a.change);return null
   default: throw Error('unknown fixture action '+a.method)
  }
 }
 try {
  initial.scenes.forEach(s=>store.saveScene(s))
  try {habits=new HabitService(store,{now:invokeClock,policy:options.policy})} catch(e) {cases.push({name,now:NOW,policy:options.policy??{},initial,constructorError:e.message,steps:[]});return}
  for(const r of initial.rows) store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(r.id,r.fingerprint,r.value)
  for(const fp of initial.deleted) store.db.prepare('INSERT INTO habit_deleted VALUES(?)').run(fp)
  const steps=[]
  for(const action of actions) {
   let result=null,error
   try {result=execute(action)??null} catch(e){error=e.message}
   // Only proposal-generated random UUIDs are canonicalized. All fixed seed IDs stay exact.
   if(action.method==='propose' && result?.status==='saved' && /^[0-9a-f]{8}-/.test(result.habit.id)) {
    const old=result.habit.id,id=`habit-${++serial}`
    const saved=store.db.prepare('SELECT value FROM habits WHERE id=?').get(old)
    const value=JSON.parse(saved.value);value.id=id
    store.db.prepare('UPDATE habits SET id=?,value=? WHERE id=?').run(id,JSON.stringify(value),old)
    result.habit.id=id
   }
   const snapshot={rows:store.db.prepare('SELECT id,fingerprint,value FROM habits ORDER BY rowid').all(),deleted:store.db.prepare('SELECT fingerprint FROM habit_deleted ORDER BY rowid').all().map(r=>r.fingerprint)}
   steps.push({action,result,...(error?{error}:{}),snapshot})
  }
  cases.push({name,now:NOW,policy:options.policy??{},initial,steps})
 } finally {store.close()}
}
trace('shared-expression-feedback-privacy-and-decay',[propose(),admin,list(),list({sceneId:'other'}),list({simulation:true}),identities(['habit-1','missing','habit-1']),feedback(),admin,list(),{method:'advance',ms:DAY*30},admin,identities(),{method:'restart'},list()])
trace('scene-rule-normalization-dispute-lock-delete',[
 propose({kind:'sceneRule',content:'  Keep   Order  ',sources:[source()]},ctx()),
 propose({kind:'sceneRule',content:'keep order',sources:[source('m2','p2')]},ctx([source('m2','p2')])),
 identities(),list({sceneId:'other'}),list({simulation:true}),feedback([source('m3','p3')],-1),feedback([source('m4','p4')],-1),admin,
 {method:'update',id:'habit-1',patch:{locked:true}},identities(),propose({kind:'sceneRule',content:'keep order',sources:[source()]}),
 {method:'update',id:'habit-1',patch:{locked:false}}, {method:'delete',id:'habit-1'}, {method:'restart'},propose({kind:'sceneRule',content:'keep order',sources:[source()]}),{method:'delete',id:'missing'}])
trace('detail-alias-and-technical-scope',[
 propose({kind:'sceneRule',content:'Technical detailed replies',sources:[source()]},ctx([source()],'technical detailed replies')),
 propose({kind:'botExpression',preference:'detail',direction:1,sources:[source('m2','p2')]},ctx([source('m2','p2')],'technical detailed replies')),
 admin,list(),feedback([source('m3','p3')],1),feedback([source('m3','p3')],1,ctx([source('m3','p3')],'technical concise replies')),
 list({sceneId:'other'}),list({simulation:true}),identities()])
trace('same-author-newest-only-and-replay',[propose(),feedback([source()],-1),feedback([source('new','p1')],-1,ctx([source('new','p1')],'assistant always use concise replies',{sourceEvidence:[{...ctx([source('new','p1')]).sourceEvidence[0],timestamp:NOW+100}]})),feedback([source('old','p1')],1),admin,identities()])
trace('recall-persist-invalidation-watermarks',[propose(),{method:'recall',source:source()},admin,list(),identities(),{method:'restart'},feedback([source('new','p1')],1),propose(),{method:'recall',source:source()}])
trace('store-recall-effective-not-raw',[propose(),{method:'storeRecall',source:source()},admin,list(),identities(),{method:'restart'},list()])
trace('expiry-read-not-write',[propose(),identities(),{method:'advance',ms:DAY},identities(),{method:'advance',ms:DAY*89},admin,list(),identities(),{method:'restart'},admin])
trace('manual-update-validation',[{method:'update',id:'missing',patch:{content:'x'}},propose(),{method:'update',id:'habit-1',patch:{content:'x'}},{method:'update',id:'habit-1',patch:{locked:1}},identities(),{method:'update',id:'habit-1',patch:{}},identities(),{method:'update',id:'habit-1',patch:{locked:true}},identities(),feedback()])
trace('validation-order',[
 propose({kind:'invalid',sources:null}, {sceneId:'off',simulation:true}),propose({kind:'invalid',sources:null},{sceneId:'off',simulation:false}),propose({kind:'invalid',sources:[]},ctx([])),
 propose({kind:'invalid',sources:[source()]},ctx()),propose({kind:'sceneRule',content:'',sources:[source()]},ctx()),propose({kind:'sceneRule',content:'x'.repeat(1001),sources:[source()]},ctx()),
 feedback([],7,{sceneId:'s',simulation:true}),{method:'feedback',id:'missing',ctx:ctx(),input:{direction:7,sources:[source()]}},propose(),feedback([source('a')],7)])
for (const [name,patch] of Object.entries({history:{origin:'history'},bot:{isBot:true},unavailable:{available:false},future:{timestamp:NOW+300001},expired:{timestamp:NOW-DAY*90},noTimestamp:{timestamp:null},wrongSender:{senderId:'other'}})) {
 const c=ctx();Object.assign(c.sourceEvidence[0],patch);trace('evidence-'+name,[propose(undefined,c)])
}
trace('evidence-boundaries',[propose(undefined,ctx([source()],'assistant always concise',{sourceEvidence:[{...ctx().sourceEvidence[0],timestamp:NOW+300000}]})),propose({kind:'botExpression',preference:'emoji',direction:1,sources:Array(33).fill(source())},ctx()),propose({kind:'botExpression',preference:'emoji',direction:1,sources:[source('m','p','other')]},ctx([source('m','p','other')]))])
for(const text of ['', 'for me concise', 'quote bot concise', 'concise', 'assistant always use emoji', 'assistant concise', 'in this group concise']) trace('scope-'+(text||'empty'),[propose(undefined,ctx([source()],text)),admin,list({sceneId:'other'})])
const legacy=record('legacy',{kind:'botExpression',sceneId:undefined,preference:'detail',evidenceVersion:undefined,locked:true})
trace('legacy-locked-alias',[admin,list(),identities(['legacy']),propose(),{method:'update',id:'legacy',patch:{locked:false}},propose(),admin],{rows:[row(legacy,sha(JSON.stringify(['bot','detail'])))]})
trace('legacy-delete-alias',[{method:'delete',id:'legacy'},propose()],{rows:[row({...legacy,locked:false},sha(JSON.stringify(['bot','detail'])))]})
trace('legacy-no-upgrade-on-read',[admin,list(),identities(['legacy']),{method:'restart'},admin],{rows:[row({...legacy,locked:false})]})
const many=Array.from({length:12},(_,i)=>source(`m${i}`,`p${i}`)); const manyCtx=ctx(many);manyCtx.sourceEvidence.forEach((e,i)=>e.timestamp=NOW-i)
trace('existing-max-sources-compaction',[propose({kind:'botExpression',preference:'concise',direction:1,sources:many},manyCtx),admin,identities(),feedback([many[11]],-1,ctx([many[11]])),admin],{policy:{maxSources:8}})
for(const policy of [{halfLifeDays:0},{halfLifeDays:366},{retentionDays:29},{retentionDays:731},{maxSources:7},{maxSources:513},{maxSources:8.5}]) trace('invalid-policy-'+JSON.stringify(policy),[],{policy})
const eventIdentity={sceneId:'s',adapterId:'synthetic',accountId:'account',messageId:'m1',senderId:'p1'}
const ingress={method:'sourceObserve',change:{kind:'ingress',identity:eventIdentity,bodyHash:'a'.repeat(64),occurredAt:null}}
const edit={method:'sourceObserve',change:{kind:'edit',identity:eventIdentity,bodyHash:'b'.repeat(64),occurredAt:null,editId:'edit-1',order:{kind:'platform_sequence',sequence:1}}}
trace('source-event-edit-revokes-legacy-source',[ingress,propose(),identities(),edit,admin,list(),identities(),propose()])
trace('source-event-identity-mismatch',[ingress,{method:'saveScene',scene:scene('s',{accountId:'changed'})},propose()])
trace('forged-event-reference',[propose({kind:'botExpression',preference:'concise',direction:1,sources:[source('m1','p1','s',{eventRef:{eventId:'missing',revision:1,deletionEpoch:0}})]},ctx())])
trace('clock-recall-recheck',[identities(['seed']),{method:'armClock',action:{method:'storeRecall',source:source()}},identities(['seed']),admin,list()],{rows:[row(record())]})
for(const mutate of [{method:'rawDelete',id:'second'},{method:'rawPut',row:row(record('duplicate-json-id',{content:'Changed'}),'second','second')}]) trace('clock-raw-'+mutate.method,[identities(['first','second','first','missing']),{method:'armClock',action:mutate},identities(['first','second']),admin],{rows:[row(record('duplicate-json-id'),'first','first'),row(record('duplicate-json-id'),'second','second')]})
trace('clock-source-edit-recheck',[ingress,propose(),{method:'armClock',action:edit},identities(),list()])
trace('clock-list-final-source-recheck',[{method:'armClock',action:{method:'storeRecall',source:source()}},list(),admin],{rows:[row(record())]})
trace('compaction-cutoff-rejects-old-author-replay',[propose({kind:'botExpression',preference:'concise',direction:1,sources:many},manyCtx),feedback([many[11]],-1,ctx([many[11]],'assistant always concise',{sourceEvidence:[manyCtx.sourceEvidence[11]]})),admin,identities()],{policy:{maxSources:8}})
for(const [preference,text] of [['emoji','assistant always use emoji'],['meme_frequency','assistant always use memes'],['questions','assistant always ask questions']]) trace('supported-axis-'+preference,[propose({kind:'botExpression',preference,direction:1,sources:[source()]},ctx([source()],text)),list(),admin,identities()])
trace('cross-scene-authenticated-author-dedup',[propose(),propose({kind:'botExpression',preference:'concise',direction:-1,sources:[source('m2','p1','other')]},ctx([source('m2','p1','other')],'assistant always concise',{sceneId:'other',sourceEvidence:[{...ctx([source('m2','p1','other')]).sourceEvidence[0],timestamp:NOW+1}]})),admin,list(),list({sceneId:'other'}),identities()])
trace('foreign-feedback-unavailable',[propose({kind:'sceneRule',content:'Keep order',sources:[source()]}),{method:'feedback',id:'habit-1',ctx:ctx([source('m2','p2','other')],'concise',{sceneId:'other'}),input:{direction:1,sources:[source('m2','p2','other')]}},admin])
// Persist non-integral decay, then hash the exact raw magnitudes. These cases
// intentionally exercise source 2 ** x rather than approximate read-only scores.
for (const halfLifeDays of [30, 7.25]) {
  for (const method of ['feedback', 'propose']) {
    const elapsed = 123456789
    const current = source('fraction-current', 'fraction-author-current')
    const delayed = source('fraction-delayed', 'fraction-author-delayed')
    const timedContext = (s, timestamp) => ctx([s], 'assistant always concise', {
      sourceEvidence: [{ ...ctx([s]).sourceEvidence[0], timestamp }],
    })
    const save = (s, direction, timestamp) => method === 'feedback'
      ? feedback([s], direction, timedContext(s, timestamp))
      : propose({ kind: 'botExpression', preference: 'concise', direction, sources: [s] }, timedContext(s, timestamp))
    trace(`persisted-fractional-decay-${halfLifeDays}-${method}`, [
      propose(),
      { method: 'advance', ms: elapsed },
      save(current, -1, NOW + elapsed),
      { method: 'validationIdentity', ctx: { sceneId: 's', simulation: false }, id: 'habit-1' },
      admin,
      { method: 'advance', ms: 987654321 },
      save(delayed, 1, NOW + elapsed + 1234567),
      { method: 'validationIdentity', ctx: { sceneId: 's', simulation: false }, id: 'habit-1' },
      identities(),
      { method: 'restart' },
      identities(),
      list(),
    ], { policy: { halfLifeDays } })
  }
}
const paths=['src/habits.ts','src/store.ts','src/memory/read/domain-source-events.ts','src/memory/recovery/ledger.ts','src/memory/storage/event-revisions.ts','package-lock.json']
const hashes=Object.fromEntries(paths.map(p=>[p,sha(readFileSync(resolve(root,p)))]))
hashes['backend/tests/generate-habit-service-fixtures.mjs']=sha(readFileSync(fileURLToPath(import.meta.url)))
const output=JSON.stringify({formatVersion:1,baseline:'ce59b822',node:process.version,normalization:'Only newly proposed random habit UUIDs are renamed to habit-N in result and persisted SQLite immediately after the action. SQL values preserve JSON key order. Later identities are exact source hashes over these persisted bytes.',hashes,cases},null,2)+'\n'
const dir=new URL('../internal/habitservice/testdata/',import.meta.url), target=new URL('source.json',dir)
if(process.argv.includes('--check')) assert.equal(readFileSync(target,'utf8'),output,'habit service source oracle differs')
else {mkdirSync(dir,{recursive:true});writeFileSync(target,output)}
console.log(`${cases.length} HabitService traces, ${cases.reduce((n,c)=>n+c.steps.length,0)} steps`)
