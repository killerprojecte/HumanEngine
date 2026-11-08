import Database from 'better-sqlite3'
import { readFileSync,writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { SemanticNeighborhood } from '../../src/memory/semantic/neighborhood.ts'
import { SemanticService } from '../../src/memory/semantic/candidates.ts'
import { SemanticNotifications } from '../../src/memory/semantic/notifications.ts'
import { semanticHash } from '../../src/memory/semantic/contracts.ts'
const h=(v:any)=>semanticHash(v)
const ref=(id:string)=>({id,version:1,deletionEpoch:0})
const view=(id:string,personId='person')=>({record:{id,scope:{kind:'scene',sceneId:'scene',personId},kind:'fact',epistemicKind:'explicit',status:'active',content:'Synthetic '+id},proofs:[{proofId:'proof-'+id,sources:[{sceneId:'scene',messageId:'msg-'+id,senderId:personId}],parents:[]}],dependencyKey:h(id),roots:[h('root-'+id)],rootCoverage:'complete',derived:false})
const base={a:view('a'),b:view('b')};const selection=(id:string)=>({ref:ref(id),dependencyKey:h(id)})
const proposal=(extra:any={})=>({kind:'summary_candidate',selectedParentIds:['a','b'],text:'Synthetic attributed summary',hypothesis:false,...extra})
const cases:any[]=[]
for(const scenario of ['auto','manual','hypothesis','disabled','cross-person','duplicate-parent','duplicate-proposal','seen','missing-parent','attempt','abandoned','attempt-missing']){
 const db=new Database(':memory:');const views:any=structuredClone(base);if(scenario==='cross-person')views.b.record.scope.personId='other';if(scenario==='missing-parent')delete views.b
 const enabled=scenario!=='disabled',auto=scenario!=='manual';let ids=0;const service=new SemanticService(db,{assertReady(){},enabled:()=>enabled,learningEnabled:()=>true,autoSummary:()=>auto,current:(_ctx,r)=>views[r.id],anchorCurrent:()=>undefined,episodeAnchor:()=>false,partitionKey:()=>h('partition')},{now:()=>Date.parse('2026-01-02T03:04:05.006Z'),id:()=>`candidate-${++ids}`});let parents=[selection('a'),selection(scenario==='duplicate-parent'?'a':'b')];const inputs=[proposal({hypothesis:scenario==='hypothesis'})];if(scenario==='duplicate-proposal')inputs.push(proposal({text:'Other prose, same semantic input'}));let result:any,error:string|undefined
 try{result=db.transaction(()=>{if(scenario==='attempt-missing'){service.finishAttempt({sceneId:'scene',simulation:false},h('input'),'finished');return null};if(scenario==='attempt'||scenario==='abandoned'){const first=service.beginAttempt({sceneId:'scene',simulation:false},{inputKey:h('input'),parents});const duplicate=service.beginAttempt({sceneId:'scene',simulation:false},{inputKey:h('input'),parents});service.finishAttempt({sceneId:'scene',simulation:false},h('input'),scenario==='abandoned'?'abandoned':'finished');service.finishAttempt({sceneId:'scene',simulation:false},h('input'),'abandoned');return {first,duplicate}};const first=service.admit({sceneId:'scene',simulation:false},parents,inputs);return scenario==='seen'?{first,second:service.admit({sceneId:'scene',simulation:false},parents,inputs)}:first})()}catch(e){error=(e as Error).message}
 cases.push({scenario,views,enabled,auto,parents,inputs,result:result??null,error:error??null,rows:service.storage.rows(),attempts:service.storage.attempts()});db.close()
}
const notificationCases:any[]=[]
for(const scenario of ['committed','rolled-back','policy-changed','listener-failure','replaced-reason']){
 let current:any=undefined;let revision=0;const trace:any[]=[]
 const queue=new SemanticNotifications({enabled:()=>true,policyIdentity:()=>String(revision),current:()=>current,episodes:()=>[],failed:()=>trace.push(['failed'])})
 queue.subscribe(d=>{trace.push(['notify',d.reason,d.trigger.ref]);if(scenario==='listener-failure')throw new Error('synthetic listener failure')})
 queue.notify('scene',ref('a'),'assertion_committed');trace.push(['checkpoint',trace.length]);if(scenario==='replaced-reason')queue.notify('scene',ref('a'),'source_qualification_changed');if(scenario!=='rolled-back')current=view('a');if(scenario==='policy-changed')revision++
 await Promise.resolve();queue.dispose();notificationCases.push({scenario,trace})
}
const neighborhoodCases:any[]=[]
for(const scenario of ['complete','stale-trigger','stale-then-current','mixed-scene','missing-ref','unavailable-member','overflow','filtered','episode','stale-episode']){
 const views:any={};const records:any[]=[]
 for(let i=0;i<(scenario==='overflow'?13:2);i++){const id=String.fromCharCode(97+i);const a:any=view(id);a.record.origin='learned';a.record.scope.sceneId='s';views[id]=a;records.push(a.record)}
 if(scenario==='filtered'){const a:any=view('hidden');a.record.origin='learned';a.record.scope.sceneId='s';a.record.internalUse=true;views.hidden=a;records.push(a.record)}
 const anchor={id:'episode',version:1};const group={anchor,anchorMembershipKey:h('membership'),anchorDependencies:[],anchorQualifications:[],parents:[selection('a'),selection('b')]}
 const neighborhood=new SemanticNeighborhood({records:()=>records,references:(_c,rs)=>rs.flatMap(r=>scenario==='missing-ref'&&r.id==='b'?[]:[ref(r.id)]),current:(_c,r)=>scenario==='unavailable-member'&&r.id==='b'?undefined:views[r.id],episodes:()=>[group]})
 let dirty:any[]=[{sceneId:'s',trigger:selection('a'),reason:'assertion_committed'}]
 if(scenario==='stale-trigger'||scenario==='stale-then-current'){dirty[0].trigger={ref:ref('a'),dependencyKey:h('obsolete')};if(scenario==='stale-then-current')dirty.push({sceneId:'s',trigger:selection('b'),reason:'assertion_committed'})}
 if(scenario==='mixed-scene')dirty.push({...dirty[0],sceneId:'other'})
 if(scenario.includes('episode'))dirty[0].episodeAnchor={...anchor,version:scenario==='stale-episode'?2:1}
 const result=neighborhood.resolve(dirty,{sceneId:'s',personalizationEnabled:true} as any)
 const normalized=result?{coverage:result.coverage,ids:result.entries.map(e=>e.selection.ref.id),partitionKey:result.partitionKey??null,episodeAnchor:result.episodeAnchor??null,episodeMembershipKey:result.episodeMembershipKey??null}:null
 neighborhoodCases.push({scenario,views,records,group,dirty,expected:normalized})
}
const paths=['src/memory.ts','src/memory/semantic/candidates.ts','src/memory/semantic/neighborhood.ts','src/memory/semantic/notifications.ts','src/memory/semantic/authority.ts','src/memory/semantic/episode-anchors.ts','src/memory/semantic/contracts.ts','src/memory/semantic/storage.ts'];const sources=Object.fromEntries(paths.map(p=>[p,createHash('sha256').update(readFileSync(p)).digest('hex')]));const output=JSON.stringify({sources,cases,notificationCases,neighborhoodCases},null,2)+'\n';const path='backend/internal/semantic/testdata/owner-source.json';if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==output)throw new Error('semantic_owner_fixture_stale')}else writeFileSync(path,output)
