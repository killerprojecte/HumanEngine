import fs from 'node:fs'
// Synthetic protocol and transport only. The test uses the real native Engine,
// source authority, scheduler, planner, reply validation and dispatch owners.
export default {
  manifest: {id:'runtime-fixture',name:'Native runtime bridge fixture',version:'1',configSchema:{type:'object',properties:{timestamp:{type:'string'},beforeArrived:{type:'string'},beforeResume:{type:'string'}}},capabilities:['adapter','model-protocol']},
  setup(ctx) {
    let api, scene, source, disposed = 0, modelCalls = 0
    const sent = []
    const message = () => ({id:source,sceneId:scene.id,senderId:'synthetic-user',sentAt:ctx.config.timestamp,receivedAt:ctx.config.timestamp,origin:'live',status:'available',parts:[{type:'text',text:'Please give a short synthetic reply.'}]})
    ctx.registerAdminAction('inspect',()=>({scene,source,sent,disposed,modelCalls}))
    ctx.registerAdminAction('edit',()=>api.edit({...message(),parts:[{type:'text',text:'Please reply to the edited synthetic request.'}]},scene,{editId:'synthetic-edit-2',order:{kind:'platform_sequence',sequence:2}}))
    ctx.registerAdminAction('recall',()=>{api.recall(scene,source);return api.isRecalled(scene,source)})
    ctx.registerAdminAction('media',id=>api.media(id,scene))
    ctx.registerAdapter('transport',context=>{
      api=context;scene=api.scene('synthetic-account','group','synthetic-room');source=api.messageId(scene,'source')
      return {
        start(){api.status('connected');api.receive(message(),scene,false)},
        async send(scope,segment,signal,beforeTransmit){
          if(ctx.config.beforeArrived){fs.writeFileSync(ctx.config.beforeArrived,'ready');while(!fs.existsSync(ctx.config.beforeResume))await new Promise(resolve=>setTimeout(resolve,1))}
          signal.throwIfAborted();beforeTransmit?.();sent.push({scope,segment})
          return {status:'sent',messageId:api.messageId(scope,'sent-'+sent.length)}
        },
        history(){return {coverage:'complete',items:[message()]}},
        getMessage(){return {coverage:'complete',items:[message()]}},
        readImage(){throw Error('legacy_read_image_must_not_be_exposed')},
        dispose(){disposed++}
      }
    })
    ctx.registerModelProtocol('deterministic','Synthetic runtime protocol',{
      complete(context,messages,tools){
        const request={messages,tools}
        context.signal.throwIfAborted();modelCalls++
        const names=request.tools.map(t=>t.name)
        const data={}
        for(const m of request.messages){if(m.role==='assistant'||m.role==='tool')break;if(m.role==='user')Object.assign(data,JSON.parse(m.content))}
        const response=toolCalls=>({text:'',toolCalls,usage:{inputTokens:0,outputTokens:0,totalTokens:0}})
        const call=(name,args)=>response([{id:'synthetic-call-'+modelCalls,name,arguments:args}])
        if(names.includes('participation.decide'))return call('participation.decide',{action:'reply',reasonCode:'fixture',targetMessageIds:[data.messages[0].id]})
        if(names.includes('reply.plan'))return call('reply.plan',{action:'reply',deliveryTool:'messages.send',targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the selected synthetic request.',constraints:[],facts:[],assets:[],emoji:[],memorySelections:[],expressionGoals:['Compose a concise fixture reply.'],actions:[]})
        if(names.includes('messages.send')){
          if(request.messages.some(m=>m.role==='tool'))return response([])
          return call('messages.send',{operationId:'synthetic-send',segments:[{replyToId:data.plan?.targetMessageIds?.[0]??data.planContract?.targetMessageIds?.[0]??source,parts:[{type:'text',text:'Synthetic native runtime reply.'}]}]})
        }
        throw Error('unexpected_runtime_stage:'+names.join(','))
      }
    })
  }
}
