// Source-derived synthetic fixtures, never user conversation data.
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { Store } from '../../src/store.ts'
import { MemoryService } from '../../src/memory.ts'
import { personalPronounsSupported } from '../../src/learning-policy.ts'
assert.equal(process.version,'v24.22.1')
assert(process.argv.slice(2).every(x=>x==='--check'))
const check=process.argv.includes('--check')
const sourcePaths=['src/memory.ts','src/learning-policy.ts','src/memory/write/subject-attribution.ts','backend/tests/generate-learning-owner-fixtures.ts']
const sources=Object.fromEntries(sourcePaths.map(p=>[p,createHash('sha256').update(readFileSync(p)).digest('hex')]))
const pronouns=[
 ['none','Synthetic parser uses UTF-8.',[],[]],
 ['unicode-hypothetical','She likes tools.',['ſuppose. I am a woman.'],[]],
 ['unicode-no-boundary','She likes tools.',['Kif. I am a woman.'],[]],
 ['new-letter-quote','She likes tools.',["\u1c89'prefix. I am a woman.'"],[]],
 ['unsupported','She likes tools.',[],[]],
 ['female','She likes tools.',['I am a woman'],[]],
 ['male','He likes tools.',['I am a man'],[]],
 ['other-family','He likes tools.',['I am a woman'],[]],
 ['literal','If she likes tools, she may join.',[],['If she likes tools, she may join.']],
 ['not-substring','She likes tools.',[],['If she likes tools.']],
 ['quoted','She likes tools.',['"I am a woman"'],[]],
 ['hypothetical','She likes tools.',['Perhaps I am a woman'],[]],
 ['chinese','她喜歡工具。',['本人是一位女性'],[]],
 ['plural','他们喜歡工具。',[],[]],
 ['nonpersonal','他山之石可以攻玉。',[],[]],
 ['declaration','She likes tools.',['My pronouns are she/her'],[]],
 ['contraction',"She's a programmer.",['my pronouns are she/her'],[]],
 ['fenced','She likes tools.',['```I am a woman```'],[]],
 ['two-families','He and she like tools.',['I am a woman','I am a man'],[]],
 ['literal-space','She  likes\n tools.',[],['She likes tools.']],
 ['NEL-not-space','She\u0085likes tools.',[],['She likes tools.']],
].map(([name,content,authors,literals])=>({name,content,authors,literals,expected:personalPronounsSupported(content as string,authors as string[],literals as string[])}))
const cases=[]
for(const name of ['metadata','semantic-reservation','personal','foreign-person','history','recalled','disabled','simulation','candidate-cap','group-cap','target-cap']){
 const store=new Store(':memory:');store.saveScene({id:'s',kind:'group',adapterId:'adapter',accountId:'account',platformSceneId:'s',name:'synthetic',participationEnabled:false,learningEnabled:name!=='disabled',proactiveEnabled:false})
 const memory=new MemoryService(store)
 const rows:any[]=[]
 for(let i=0;i<12;i++)rows.push({id:`m${i}`,scope:{kind:'scene',sceneId:'s',...(name==='personal'||name==='foreign-person'?{personId:'p'}:{})},content:i===3?'Synthetic alpha beta token.':`Synthetic register ${i}.`,kind:i%2?'claim':'fact',status:'active',origin:'manual',confidence:0.7,importance:0.5,sources:[],learnedAt:'2026-01-01T00:00:00.000Z'})
 rows.push({id:'shared',scope:{kind:'sharedPublicKnowledge'},content:'Synthetic alpha shared.',kind:'fact',status:'active',origin:'manual',confidence:0.7,importance:0.5,sources:[],learnedAt:'2026-01-01T00:00:00.000Z'})
 if(name==='group-cap')for(let i=0;i<12;i++)rows[i].content=`Synthetic code${i}`
 if(name==='target-cap'){rows.length=0;for(let p=0;p<5;p++)for(let i=0;i<4;i++)rows.push({id:`person${p}-${i}`,scope:{kind:'scene',sceneId:'s',personId:`p${p}`},content:`Synthetic register ${i}.`,kind:'fact',status:'active',origin:'manual',confidence:0.7,importance:0.5,sources:[],learnedAt:'2026-01-01T00:00:00.000Z'})}
 for(const r of rows)store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)').run(r.id,r.scope.kind==='scene'?'scene:s':'shared:public',r.scope.personId??null,r.content,r.id,JSON.stringify(r))
 const input:any={sceneId:'s',simulation:name==='simulation',sourceEvidence:[{sceneId:'s',messageId:'source',senderId:'p',origin:name==='history'?'history':'live',available:true,isBot:false}]}
 const base={candidateIndex:0,kind:'claim',content:'Synthetic alpha beta token.',sourceMessageIds:['source'],...(name==='personal'?{personId:'p'}:name==='foreign-person'?{personId:'other'}:{})}
 let candidates:any[]=name==='candidate-cap'?Array.from({length:9},(_,i)=>({...base,candidateIndex:i})):name==='group-cap'?Array.from({length:4},(_,i)=>({...base,candidateIndex:i,content:`Synthetic code${4+i*2} code${5+i*2}`})):[base]
 if(name==='target-cap'){input.sourceEvidence=Array.from({length:5},(_,i)=>({sceneId:'s',messageId:`source${i}`,senderId:`p${i}`,origin:'live',available:true,isBot:false}));candidates=Array.from({length:5},(_,i)=>({...base,candidateIndex:i,personId:`p${i}`,sourceMessageIds:[`source${i}`]}))}
 const semantic=name==='semantic-reservation'?[rows[0],rows[1],rows[12]]:name==='group-cap'?rows.slice(0,8):[]
 if(name==='recalled')store.db.prepare('INSERT INTO recall_tombstones(scene_id,message_id,recalled_at) VALUES(?,?,?)').run('s','source','2026-01-01')
 const expected=memory.reconciliationTargets(input as never,candidates as never,semantic as never)
 cases.push({name,input,candidates,semantic,rows,recalled:name==='recalled',enabled:name!=='disabled',expected})
 memory.dispose();store.close()
}
const output='backend/internal/learningowner/testdata/source.json'
const generated=JSON.stringify({sources,pronouns,cases},null,2)+'\n'
if(check)assert.equal(readFileSync(output,'utf8'),generated)
else writeFileSync(output,generated)
