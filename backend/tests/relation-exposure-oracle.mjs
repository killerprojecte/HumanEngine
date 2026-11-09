import fs from 'node:fs'
import path from 'node:path'
import assert from 'node:assert/strict'
import {pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT
const {RelationExposure}=await import(pathToFileURL(path.join(root,'src/memory/read/relation-exposure.ts')))
const {ModelMemoryExposure}=await import(pathToFileURL(path.join(root,'src/memory/read/model-exposure.ts')))
const output=[]
for(const scenario of ['original','clone','partial','foreign','changed-scope','drift','late-conflict','flags','aborted','detached','expiry']){
 let enabled=true,now=1000;const signal=new AbortController();const context=()=>({scene:{id:'s'},mode:'real',signal:signal.signal,dependencies:new Set(),validations:[]});const c=context()
 const records=['a','b'].map(id=>({id,scope:{kind:'scene',sceneId:'s',personId:'p'},content:id,kind:'fact',status:'active',origin:'learned',learnedAt:'2026-01-01T00:00:00Z',sources:[{sceneId:'s',messageId:id,senderId:'p'}]}))
 const reference=id=>({id,version:1,deletionEpoch:0});const edge={id:'edge',version:1,predicate:'participates_in',modality:'asserted',temporalScope:'current',personId:'p',target:{id:'target',kind:'plan',title:'Plan',backing:reference('b')},backing:reference('a'),polarity:'positive',conditions:[],status:'accepted',admission:'structural',proofs:[{sources:records[0].sources}],validUntil:'1970-01-01T00:00:02.000Z'}
 let edges=[edge];const trace=[];const memory={list:ctx=>{trace.push('memory.list:'+ctx.sceneId);return ctx.sceneId==='s'?records:[]},captureParentReference:(_ctx,r)=>reference(r.id),revisionState:()=>({version:1,deletionEpoch:0,deleted:false}),parentBinding:()=>undefined,relations:{list:ctx=>{trace.push('relations.list:'+ctx.sceneId);return {items:ctx.sceneId==='s'?structuredClone(edges):[],coverage:'complete'}}}}
 const ordinary=new ModelMemoryExposure(memory);const relation=new RelationExposure(memory,ordinary,()=>enabled,()=>now)
 const prepared=relation.query(c,'p');assert.equal(prepared.length,2);assert.equal(relation.captureLearningGuard(c),undefined);let input=prepared,receiver=c
 if(scenario==='clone')input=structuredClone(prepared)
 if(scenario==='partial')input=prepared.slice(0,1)
 if(scenario==='foreign')receiver=context()
 if(scenario==='changed-scope')c.scene.id='other'
 if(scenario==='drift')edges[0].version=2
 if(scenario==='late-conflict')edges.push({...structuredClone(edge),id:'new-edge',polarity:'negative'})
 if(scenario==='flags')enabled=false
 if(scenario==='aborted')signal.abort()
 const delivered=relation.deliver(receiver,input);const guard=relation.captureLearningGuard(c)
 let guardBefore=guard?.()??null,guardAfter=guardBefore
 if(scenario==='detached'){signal.abort();guardAfter=guard()}
 if(scenario==='expiry'){now=3000;guardAfter=guard()}
 const accepted=['original','detached','expiry'].includes(scenario);assert.equal(delivered.length,accepted?2:0);if(accepted)assert.equal(delivered[0],prepared[0]);if(scenario==='detached')assert.equal(guardAfter,true);if(scenario==='expiry')assert.equal(guardAfter,false)
 output.push({scenario,delivered:delivered.length,guardBefore,guardAfter,dependencies:[...receiver.dependencies],entities:relation.readExposedEntities(c),trace})
}
console.log(JSON.stringify({source:'actual RelationExposure and ModelMemoryExposure runtime; synthetic owner DTOs, not persistence proof',output},null,2))
