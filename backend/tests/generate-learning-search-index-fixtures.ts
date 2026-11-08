// Actual source owners and wrapper methods; all records are synthetic.
import assert from 'node:assert/strict'
import { readFileSync,writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { Store } from '../../src/store.ts'
import { MemoryService } from '../../src/memory.ts'
import { EmbeddingIndex } from '../../src/embedding-index.ts'
import { Engine } from '../../src/engine.ts'
import { memorySearchTerms } from '../../src/memory/read/lexical.ts'
import { publicTechnicalCandidate } from '../../src/learning-policy.ts'
assert.equal(process.version,'v24.22.1')
const paths=['src/memory.ts','src/memory/read/search.ts','src/memory/read/lexical.ts','src/embedding-index.ts','src/engine.ts','src/learning-policy.ts','backend/tests/generate-learning-search-index-fixtures.ts']
const sources=Object.fromEntries(paths.map(p=>[p,createHash('sha256').update(readFileSync(p)).digest('hex')]))
const queries=['','synthetic parser','如何解析 JSON 数据？','SQLite:memory UTF-8 + HTTP/2','İ ΣΟΣ ΣΟ\u0301Σ','a'.repeat(1999)+'𐐀x','汉字 空间 中文','𰀀𰀁 synthetic',Array.from({length:70},(_,i)=>`term${i}`).join(' ')]
const lexical=queries.map(query=>({query,terms:memorySearchTerms(query)}))
const search=[]
for(const query of ['','synthetic','parser json','如何解析 JSON 数据？']){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',learningEnabled:true,participationEnabled:false,proactiveEnabled:false});const memory=new MemoryService(store)
 const records=[['a','Synthetic JSON parser supports Unicode.','medium',0.7],['b','Synthetic parser reads JSON.','low',0.9],['c','SQL selects rows.','high',0.8],['d','JSON 数据解析。','medium',0.4]].map(([id,content,importance,confidence])=>({id,scope:{kind:'scene',sceneId:'s'},content,kind:'fact',status:'active',origin:'manual',confidence,importance,sources:[],learnedAt:'2026-01-01T00:00:00.000Z'}))
 for(const r of records)store.db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)').run(r.id,'scene:s',null,r.content,r.id,JSON.stringify(r))
 search.push({query,records,expected:memory.search({sceneId:'s',simulation:false},query)})
 memory.dispose();store.close()
}
const publicCandidates=['HTTP defines request methods.','Our HTTP project uses GET.','JSON supports arrays.','JSON customer data uses arrays.','Git stores objects.','SQL 127.0.0.1 server.','HTTP API KEY example.','HTTP ſecret example.','HTTP SECRET example.'].map(content=>({content,domain:content.toLowerCase().split(' ')[0],expected:publicTechnicalCandidate(content,content.toLowerCase().split(' ')[0] as never)}))
// Execute the actual private Engine method against actual memory/policy source
// owners. Capture the model tool schema and prompt, not an invented duplicate.
const publicReview=[]
for(const shareable of [true,false]){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',learningEnabled:true,participationEnabled:false,proactiveEnabled:false});const memory=new MemoryService(store)
 const message={id:'m',sceneId:'s',senderId:'p',origin:'live',status:'available',parts:[{type:'text',text:'HTTP defines request methods.'}],sentAt:'2026-01-01T00:00:00.000Z',receivedAt:'2026-01-01T00:00:00.000Z'}
 const evidence=[{sceneId:'s',messageId:'m',senderId:'p',available:true,isBot:false,origin:'live'}]
 const saved=memory.propose({sceneId:'s',simulation:false,provenance:'source-only',sourceEvidence:evidence} as never,{content:'HTTP defines request methods.',kind:'fact',sources:[{sceneId:'s',messageId:'m',senderId:'p'}]} as never)
 assert.equal(saved.status,'saved');let request:any
 const fake={store,memory,runtime:{preview:()=>[]},modelRead:{allowed:()=>true,require:()=>({}),assert:()=>{}},router:()=>({complete:async(role:any,messages:any,tools:any)=>{request={role,messages,tools};return {toolCalls:[{name:'knowledge.review',arguments:{shareable,publicnessConfidence:.99,sourceSpecific:false,containsPersonalData:false}}]}}})}
 const result=await (Engine.prototype as any).considerPublic.call(fake,'s',(saved as any).memory.id,'http',[message]);request.messages[1].content=JSON.parse(request.messages[1].content);delete request.messages[1].content.now
 publicReview.push({shareable,request,promoted:result?.scope.kind??null});memory.dispose();store.close()
}
// Actual source single-item bootstrap with a synthetic embedding router.
const index=[]
for(const scenario of ['bootstrap','legacy','cancelled','unconfigured','changed-content']){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',learningEnabled:true,participationEnabled:false,proactiveEnabled:false});const memory=new MemoryService(store)
 if(scenario!=='unconfigured')store.set('models',{providers:[{id:'p',name:'Synthetic',protocol:'openai-chat',baseUrl:'https://synthetic.invalid/'}],models:[{id:'m',displayName:'Synthetic',providerId:'p',apiModelName:'synthetic',capabilities:['embedding']}],routes:[{role:'embedding',orderedModelIds:['m'],timeoutMs:1000}]} as never)
 const put=()=>memory.create({scope:{kind:'scene',sceneId:'s'},content:'Synthetic parser.',kind:'fact'})
 let record:any;if(scenario==='legacy')record=put();let calls=0
 const embedding=new EmbeddingIndex(store,memory,()=>({embeddings:async()=>{calls++;if(scenario==='changed-content')memory.update(record.id,{content:'Changed synthetic parser.'});return {vectors:[[1,0]]}}}) as never)
 record??=put();const c=new AbortController();if(scenario==='cancelled')c.abort();await embedding.index(record.id,record.content,c.signal)
 index.push({scenario,calls,active:!!store.get('embedding.active',undefined),vectors:(store.db.prepare('SELECT count(*) n FROM memory_vectors').get() as any).n});await embedding.dispose();memory.dispose();store.close()
}
const vectors=[]
for(const normalized of [false,true]){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',learningEnabled:true,participationEnabled:false,proactiveEnabled:false});const memory=new MemoryService(store)
 const spec={providerId:'p',modelId:'m',apiModelName:'synthetic',deployment:'https://synthetic.invalid/',dimensions:3,preprocessing:'text-v1',normalized};const space=memory.registerSpace(spec)
 const records=[{id:'va',content:'Unmatched synthetic first',vector:[.2,.3,.4]},{id:'vb',content:'Unmatched synthetic second',vector:[9,8,7]}].map((v:any)=>({...v,scope:{kind:'scene',sceneId:'s'},kind:'fact',status:'active',origin:'manual',confidence:.7,importance:'medium',sources:[],learnedAt:'2026-01-01T00:00:00.000Z'}))
 for(const r of records){const {vector,...record}=r;store.db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)').run(r.id,'scene:s',null,r.content,r.id,JSON.stringify(record));memory.index(r.id,space,vector)}
 const queryVector=[.3,.5,.7];const expected=memory.search({sceneId:'s',simulation:false},'nonlexical query',undefined,{spaceId:space,vector:queryVector})
 const bytes=store.db.prepare('SELECT memory_id,hex(embedding) bytes FROM memory_vectors ORDER BY memory_id').all()
 vectors.push({spec,records,queryVector,expected,bytes});memory.dispose();store.close()
}
const encoders=[]
for(const endpoint of ['https://EXAMPLE.test:443/a/../v1','https://例え.テスト/路径','https://example.test/%2e%2e/v1','https://example.test/v1?','https://example.test/v1#']) {
 const store=new Store(':memory:');store.set('models',{providers:[{id:'p',name:'Synthetic',protocol:'openai-chat',baseUrl:endpoint}],models:[{id:'m',displayName:'Synthetic',providerId:'p',apiModelName:'synthetic',capabilities:['embedding']}],routes:[{role:'embedding',orderedModelIds:['m'],timeoutMs:1000}]} as never)
 const memory=new MemoryService(store),embedding=new EmbeddingIndex(store,memory,()=>({}) as never)
 const selected=embedding.status().selected
 encoders.push({endpoint,revision:selected?.encoderRevision??null});await embedding.dispose();memory.dispose();store.close()
}
const output=JSON.stringify({sources,lexical,search,publicCandidates,publicReview,index,vectors,encoders},null,2)+'\n'
const destination='backend/internal/learningowner/testdata/search-index.json'
if(process.argv.includes('--check'))assert.equal(readFileSync(destination,'utf8'),output)
else writeFileSync(destination,output)
