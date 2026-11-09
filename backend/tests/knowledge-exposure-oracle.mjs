import fs from 'node:fs'
import path from 'node:path'
import assert from 'node:assert/strict'
import {pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT
const {KnowledgeExposure}=await import(pathToFileURL(path.join(root,'src/memory/read/knowledge-exposure.ts')))
const {KnowledgeToolQuerySchema}=await import(pathToFileURL(path.join(root,'src/memory/knowledge/query.ts')))
const reference={id:'backing',version:1,deletionEpoch:0}
const page=()=>({interpretation:'attributed_claims_not_transitive_truth',coverage:'complete',edges:[{id:'edge',version:1,sceneId:'s',backing:reference,subject:{kind:'person',personId:'p'},object:{kind:'entity',entity:{id:'entity',sceneId:'s',backing:reference}},proofs:[{sources:[{messageId:'proof-source'}]}],spans:[{source:{messageId:'span-source'}}],dependencies:[],readDependencies:[]}],supportClosure:{edges:[],records:[{ref:reference,record:{id:'backing',scope:{kind:'scene',sceneId:'s'},sources:[{messageId:'record-source'}]}}]},paths:[['edge']]})
const results=[]
for(const scenario of ['original','copy','foreign','prepared-entity-anchor','delivered-entity-anchor','policy-drift','flag-revoke','page-drift','scope','partial','memory-anchor-revoke','out-of-order','detached','reentry']){
 let enabled=true,revision=0,changed=false,anchorValid=true,reenter=false;let original;const controller=new AbortController();const makeContext=()=>({scene:{id:'s'},mode:'real',signal:controller.signal,dependencies:new Set(),validations:[]});const c=makeContext();const trace=[]
 let exposure
 const load=()=>{trace.push('load');if(reenter){reenter=false;exposure.deliver(c,original)};const v=page();if(changed)v.coverage='limited';return v}
 const port={allowed:()=>{trace.push('allowed');return enabled},policyIdentity:()=>{trace.push('policy');return revision},query:load,discover:load}
 exposure=new KnowledgeExposure(port)
 const query={anchor:{kind:'person',personId:'p'},predicates:['prefers_topic'],depth:1};KnowledgeToolQuerySchema.parse(query)
 original=scenario==='memory-anchor-revoke'?exposure.queryFromBacking(c,reference,{predicates:query.predicates,depth:1},()=>anchorValid):exposure.query(c,query)
 assert.equal(original.length,1);assert.equal(exposure.captureGuard(c),undefined);let input=original,receiver=c
 if(scenario==='copy')input=structuredClone(original)
 if(scenario==='foreign')receiver=makeContext()
 if(scenario==='prepared-entity-anchor'){assert.equal(exposure.query(c,{...query,anchor:{kind:'entity',entityId:'entity',backingRef:reference}}).length,0)}
 if(scenario==='policy-drift')revision++
 if(scenario==='flag-revoke')enabled=false
 if(scenario==='page-drift')changed=true
 if(scenario==='scope')c.scene.id='other'
 if(scenario==='partial')input=[]
 if(scenario==='memory-anchor-revoke')anchorValid=false
 if(scenario==='reentry')reenter=true
 let sibling
 if(scenario==='out-of-order')sibling=exposure.query(c,query)
 let delivered=[],error
 try{delivered=exposure.deliver(receiver,input)}catch(e){error=e.message;if(scenario!=='scope')throw e}
 if(scenario==='scope')assert.equal(error,'knowledge_exposure_page_invalid')
 const accept=['original','prepared-entity-anchor','delivered-entity-anchor','out-of-order','detached'].includes(scenario)
 assert.equal(delivered.length,accept?1:0);if(accept)assert.equal(delivered[0],original[0])
 if(scenario==='delivered-entity-anchor'){const next=exposure.query(c,{...query,anchor:{kind:'entity',entityId:'entity',backingRef:reference}});assert.equal(exposure.deliver(c,next).length,1)}
 if(scenario==='out-of-order'){assert.equal(exposure.deliver(c,sibling).length,1);assert.equal(exposure.readDelivered(c).length,2);revision++;assert.equal(exposure.captureGuard(c)(),false)}
 const guard=exposure.captureGuard(c);let detached
 if(scenario==='detached'){controller.abort();detached=guard();assert.equal(detached,true)}
 results.push({scenario,error,delivered:delivered.length,dependencies:[...receiver.dependencies],entities:exposure.readEntities(c),edges:exposure.readEdges(c),guardPresent:!!guard,detached,trace})
}
console.log(JSON.stringify({source:'actual TS KnowledgeExposure/StateExposure with synthetic page owner; not native persisted graph proof',results},null,2))
