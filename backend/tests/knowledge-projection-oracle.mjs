import assert from 'node:assert/strict'
import {ContextProjection} from '../../src/runtime/context-projection.ts'
import {buildReplyBrief} from '../../src/runtime/reply-brief.ts'
const scene='state:transport:synthetic-scene'
const originals=['state:transport:synthetic-scene:original-one','state:transport:synthetic-scene:original-two']
const projection=new ContextProjection(scene)
const messages=projection.project(originals.map(id=>({id,sceneId:scene,senderId:'p',parts:[{type:'text',text:'I prefer concise replies.'}]})))
assert.deepEqual(messages.map(m=>m.id),['m1','m2'])
const modelPlan={action:'reply',deliveryTool:'messages.send',targetMessageIds:['m1'],contextMessageIds:['m1','m2'],strategy:'Reply',constraints:[],facts:[],assets:[],emoji:[],stateEvidenceIds:[],memorySelections:[],expressionGoals:[],actions:[]}
const restored=projection.restoreArguments(modelPlan)
const brief=buildReplyBrief(restored,[],[],[],[])
assert.equal(brief.plan.targetMessageIds[0],originals[0])
assert.notEqual(brief.plan.targetMessageIds[0],originals[1])
assert.notEqual(brief.plan.targetMessageIds[0],modelPlan.targetMessageIds[0])
const action=projection.restoreArguments({sourceMessageIds:['m1'],subject:{kind:'self',sourceMessageId:'m1'},knowledge:[{sourceSpans:[{messageId:'m1',partIndex:0,start:9,end:24,stance:'self'}]}]})
assert.equal(action.sourceMessageIds[0],brief.plan.targetMessageIds[0])
assert.equal(action.knowledge[0].sourceSpans[0].messageId,originals[0])
console.log(JSON.stringify({source:'actual TS ContextProjection.restoreArguments and buildReplyBrief',modelPlan,restoredBrief:brief,restoredAction:action,latestSource:originals[1]},null,2))
