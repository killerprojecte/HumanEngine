import fs from 'node:fs'
// Synthetic protocol and transport only. The test uses the real native Engine,
// source authority, scheduler, planner, reply validation and dispatch owners.
export default {
  manifest: {id:'web-model',name:'Native runtime bridge fixture',version:'1',configSchema:{type:'object',properties:{memoryMode:{type:'boolean'},timestamp:{type:'string'},receivedMarker:{type:'string'},plannerArrived:{type:'string'},plannerResume:{type:'string'},beforeArrived:{type:'string'},beforeResume:{type:'string'}}},capabilities:['adapter','model-protocol']},
  setup(ctx) {
    try {
    let api, scene, source, disposed = 0, modelCalls = 0, sourceSerial = 1
    const sent = [], modelIDs = [], webReceipts = [], advertised = []
    let expectWeb = true
    const message = () => ({id:source,sceneId:scene.id,senderId:'synthetic-user',sentAt:ctx.config.timestamp,receivedAt:ctx.config.timestamp,origin:'live',status:'available',parts:[{type:'text',text:'I prefer concise replies.'}]})
    ctx.registerAdminAction('inspect',()=>({scene,source,sent,disposed,modelCalls,modelIDs,webReceipts,advertised}))
    ctx.registerAdminAction('expect-web',value=>{expectWeb=value.enabled;return {ok:true}})
    ctx.registerAdminAction('new-source',()=>{source=api.messageId(scene,'source-'+(++sourceSerial));api.receive(message(),scene,false);return {ok:true}})
    ctx.registerAdminAction('edit',()=>api.edit({...message(),parts:[{type:'text',text:'Please reply to the edited synthetic request.'}]},scene,{editId:'synthetic-edit-2',order:{kind:'platform_sequence',sequence:2}}))
    ctx.registerAdminAction('recall',()=>{api.recall(scene,source);return api.isRecalled(scene,source)})
    ctx.registerAdminAction('media',id=>api.media(id,scene))
    ctx.registerAdapter('transport',context=>{
      api=context;scene=api.scene('synthetic-account','group','synthetic-room');source=api.messageId(scene,'source')
      return {
        start(){api.status('connected');if(!ctx.config.receivedMarker||!fs.existsSync(ctx.config.receivedMarker)){if(ctx.config.receivedMarker)fs.writeFileSync(ctx.config.receivedMarker,'received');api.receive(message(),scene,false)}},
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
      async complete(context,messages,tools){
        const request={messages,tools}
        context.signal.throwIfAborted();modelCalls++;modelIDs.push(context.model.id)
        if(modelCalls===1&&ctx.config.plannerArrived){fs.writeFileSync(ctx.config.plannerArrived,'ready');while(!fs.existsSync(ctx.config.plannerResume)){context.signal.throwIfAborted();await new Promise(resolve=>setTimeout(resolve,1))}}
        const names=request.tools.map(t=>t.name)
        const data={}
        for(const m of request.messages){if(m.role==='assistant'||m.role==='tool')break;if(m.role==='user')Object.assign(data,JSON.parse(m.content))}
        const response=toolCalls=>({text:'',toolCalls,usage:{inputTokens:0,outputTokens:0,totalTokens:0}})
        const call=(name,args)=>response([{id:'synthetic-call-'+modelCalls,name,arguments:args}])
        if(names.includes('participation.decide'))return call('participation.decide',{action:'reply',reasonCode:'fixture',targetMessageIds:[data.messages[0].id]})
        const receipts=request.messages.filter(m=>m.role==='tool').flatMap(m=>{try{const v=JSON.parse(m.content);return Array.isArray(v)?v:[v]}catch{return []}})
        if(names.includes('reply.plan')) {
          const web = receipts.find(v=>v.toolName==='web-read.read')
          advertised.push(names.filter(name=>name.startsWith('web-read.')))
          if(expectWeb !== names.includes('web-read.read'))throw Error('web_read_advertisement_mismatch')
          if(expectWeb && !web)return call('web-read.read',{url:'http://127.0.0.1/'})
          if(web){if(web.value.status!=='address_not_public')throw Error('web_read_private_destination_not_denied');webReceipts.push(web)}
        }
        const memoryReceipt=receipts.find(v=>v.toolName==='memory.person'&&Array.isArray(v.value))
        if(ctx.config.memoryMode&&names.includes('reply.plan')&&!memoryReceipt)return call('memory.person',{personId:'synthetic-user'})
        if(ctx.config.memoryMode&&memoryReceipt?.value.length===0)throw Error('synthetic_memory_missing')
        const identityReceipt=receipts.find(v=>v.toolName==='memory.identities'&&Array.isArray(v.value))
        if(ctx.config.memoryMode&&memoryReceipt&&!identityReceipt)return call('memory.identities',{personId:'synthetic-user'})
        if(ctx.config.memoryMode&&identityReceipt?.value.length===0)throw Error('synthetic_identity_page_missing')
        if(names.includes('reply.plan'))return call('reply.plan',{action:'reply',deliveryTool:'messages.send',targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the selected synthetic request.',constraints:[],facts:[],assets:[],emoji:[],stateEvidenceIds:identityReceipt?[identityReceipt.id]:[],memorySelections:memoryReceipt?[{evidenceId:memoryReceipt.id,memoryIds:memoryReceipt.value.map(m=>m.id)}]:[],expressionGoals:['Compose a concise fixture reply.'],actions:[]})
        if(names.includes('messages.send')){
          if(request.messages.some(m=>m.role==='tool'))return response([])
          return call('messages.send',{operationId:'synthetic-send',segments:[{replyToId:data.plan?.targetMessageIds?.[0]??data.planContract?.targetMessageIds?.[0]??source,parts:[{type:'text',text:'Synthetic native runtime reply.'}]}]})
        }
        throw Error('unexpected_runtime_stage:'+names.join(','))
      }
    })
    } catch (error) {
      // Synthetic fixture only: preserve the exact setup cause for this test.
      console.error('synthetic_application_setup_failed', error?.stack ?? String(error))
      throw error
    }
  }
}
