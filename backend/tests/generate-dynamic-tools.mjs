import fs from 'node:fs'
import crypto from 'node:crypto'
import path from 'node:path'
import {pathToFileURL} from 'node:url'
import assert from 'node:assert/strict'
const root=process.env.HE_SOURCE_ROOT
if(!root)throw Error('HE_SOURCE_ROOT required')
const {Store}=await import(pathToFileURL(path.join(root,'src/store.ts')))
const {Runtime}=await import(pathToFileURL(path.join(root,'src/runtime.ts')))
const {fixtureReplyPlan}=await import(pathToFileURL(path.join(root,'tests/helpers/reply-plan.ts')))
const result=(name,args)=>{const toolCalls=name?[{id:name,name,arguments:args}]:[];return {text:'',toolCalls,modelId:'fixture',usage:{inputTokens:0,outputTokens:0,totalTokens:0},assistant:{role:'assistant',content:'',toolCalls}}}
const outcomes=[]
for(const scenario of ['stable','revoke-before-execute','enable-unadvertised','enable-after-decision','cancel-before-execute','recall-before-execute','stable-window','tool-rejected','tool-queued','remove-before-deliver','remove-window','duplicate-name-no-hook','send-effect-read','send-effect-action','enable-learning-after-decision','disable-learning-after-decision','remove-history-images']){
 const trace=[];let enabled=!['enable-unadvertised','enable-after-decision'].includes(scenario),executes=0,sends=0,planner=0,deliveries=0,customSends=0,sendEffect="send",imageCalls=0
 const store=new Store(':memory:')
 const scene={id:'dynamic-'+scenario,kind:'group',adapterId:'fixture',accountId:'bot',platformSceneId:'group'}
 store.saveScene({...scene,name:'Fixture',participationEnabled:true,proactiveEnabled:true,learningEnabled:scenario==='disable-learning-after-decision'})
 store.set('settings',{...store.settings,promptContextWindowEnabled:['stable-window','remove-window'].includes(scenario)})
 const runtime=new Runtime(store,()=>({complete:async(role,messages,tools)=>{
  const names=tools.map(t=>t.name)
  trace.push('model:'+role+':'+names.includes('fixture.read'))
  if(names.includes('participation.decide')){if(scenario==='enable-after-decision')enabled=true;if(scenario.includes('learning-after-decision'))store.saveScene({...store.scene(scene.id),learningEnabled:scenario==='enable-learning-after-decision'});return result('participation.decide',{action:'reply',reasonCode:'fixture',targetMessageIds:['source']})}
  if(names.includes('reply.plan')){
   if(scenario.includes('learning-after-decision')){const data={};for(const m of messages){if(m.role==='assistant'||m.role==='tool')break;if(m.role==='user')Object.assign(data,JSON.parse(m.content))};trace.push('learning-actions:'+data.actionTools.some(t=>t.name==='learning.propose'))}
   if(++planner===1){if(scenario==='revoke-before-execute')enabled=false;if(scenario==='enable-unadvertised')enabled=true;if(scenario==='cancel-before-execute')runtime.cancel(scene.id);if(scenario==='recall-before-execute')runtime.recall(scene.id,'source');return scenario==='remove-history-images'?result('messages.around',{messageId:fixtureReplyPlan(messages).targetMessageIds[0],limit:1}):result('fixture.read',{})}
   return result('reply.plan',fixtureReplyPlan(messages,scenario.startsWith('send-effect-')?{deliveryTool:'fixture.send'}:{}))
  }
  if(role==='replyer'){
   if(messages.some(m=>m.role==='tool'))return result()
   if(scenario.startsWith('send-effect-')){sendEffect=scenario==='send-effect-read'?'read':'learningCandidate';return result('fixture.send',{})}
   return result('messages.send',{operationId:'send',segments:[{replyToId:'source',parts:[{type:'text',text:'Synthetic'}]}]})
  }
  throw Error('unexpected stage')
 }}))
 runtime.addAdapter('fixture',{send:async()=>{sends++;return {status:'sent',messageId:'sent'}},history:async()=>{if(scenario==='remove-history-images'){trace.push('history-entered');await Promise.resolve();removeImages();trace.push('history-returned')};return {items:[],coverage:'partial'}},getMessage:async()=>{if(scenario==='remove-history-images'){trace.push('history-entered');await Promise.resolve();removeImages();trace.push('history-returned')};return {items:[],coverage:'partial'}},dispose(){}})
 const remove=runtime.addExtension({get tools(){
  const stack=String(new Error().stack)
  const lines=[...stack.matchAll(/src\/runtime\.ts:(\d+):/g)].map(m=>Number(m[1]))
  trace.push('getter:'+lines.join('/')+':'+enabled)
  return enabled?[{name:'fixture.read',description:'Fixture read',effect:'read',inputSchema:{type:'object',properties:{}}},...(scenario.startsWith('send-effect-')?[{name:'fixture.send',description:'Fixture send',effect:sendEffect,inputSchema:{type:'object',properties:{}}}]:[]),...(scenario.includes('learning-after-decision')?[{name:'learning.propose',description:'Fixture learning',effect:'learningCandidate',inputSchema:{type:'object',properties:{}}}]:[])]:[]
 },delivered:scenario==='duplicate-name-no-hook'?undefined:(_name,value)=>{deliveries++;return value},execute:async(name)=>{if(name==='fixture.send'){customSends++;return []};executes++;trace.push('execute');if(['remove-before-deliver','remove-window'].includes(scenario))remove();return scenario==='tool-rejected'?{status:'rejected'}:scenario==='tool-queued'?{status:'queued'}:{value:'synthetic'}}})
 if(scenario==='duplicate-name-no-hook')runtime.addExtension({tools:[{name:'fixture.read',description:'Duplicate',effect:'read',inputSchema:{type:'object',properties:{}}}],execute:async()=>{throw Error('duplicate_execute')},delivered:(_name,value)=>{deliveries++;trace.push('duplicate-delivered');return value}})
 const removeImages=runtime.addExtension({tools:[],describeImages:()=>{if(scenario==='remove-history-images'){imageCalls++;trace.push('describe-images')};return undefined},execute:async()=>({})})
 const now=new Date().toISOString()
 runtime.ingest({id:'source',sceneId:scene.id,senderId:'user',sentAt:now,receivedAt:now,origin:'live',status:'available',parts:[{type:'text',text:'Please reply.'}]},scene,false)
 try{await runtime.proactive(scene.id);outcomes.push({scenario,trace,executes,sends,deliveries,customSends,imageCalls,toolEvents:store.logs.filter(log=>log.stage==='tool'&&log.toolName==='fixture.read'&&log.status!=='started').map(log=>({status:log.status,reason:log.reason,...(log.counts?{counts:log.counts}:{}),...(log.receiptState?{receiptState:log.receiptState}:{})}))});if(['stable','enable-after-decision','stable-window','tool-rejected','tool-queued','remove-before-deliver','duplicate-name-no-hook','enable-learning-after-decision','disable-learning-after-decision','remove-history-images'].includes(scenario)){assert.equal(executes,scenario==='remove-history-images'?0:1);assert.equal(sends,1)}else{assert.equal(executes,scenario==='remove-window'||scenario.startsWith('send-effect-')?1:0);assert.equal(sends,0)}}finally{runtime.dispose();store.close()}
}
const {toolOutcome}=await import(pathToFileURL(path.join(root,'src/diagnostics/runtime-results.ts')))
const typedInputs=[null,[],[1,2],{items:[1,2]},{results:[1]},{status:'cancelled'},{status:'rejected'},{status:'unavailable'},{status:'failed'},{error:null},{ok:false},{status:'queued'},{status:'duplicate'},{status:'no_results'},[{status:'sent'}],[{status:'sent'},{status:'unknown'}],[{status:'failed'}],[{status:'cancelled'}],[{status:'sent'},{status:'cancelled'}]]
const typedOutcomes=typedInputs.map(input=>({input,result:toolOutcome(input)}))
const sourceHashes=Object.fromEntries(['src/runtime.ts','src/runtime/reply-plan.ts','src/diagnostics/runtime-results.ts'].map(name=>[name,crypto.createHash('sha256').update(fs.readFileSync(path.join(root,name))).digest('hex')]))
const encoded=JSON.stringify({node:process.version,sourceHashes,outcomes,typedOutcomes},null,2)+'\n' 
if(process.argv.includes('--check'))assert.equal(fs.readFileSync(process.argv[2],'utf8'),encoded);else fs.writeFileSync(process.argv[2],encoded)
console.log('PASS actual Runtime dynamic getter 17 scenarios')
