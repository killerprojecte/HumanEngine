import fs from 'node:fs'
// Synthetic protocol and transport only. The test uses the real native Engine,
// source authority, scheduler, planner, reply validation and dispatch owners.
export default {
  manifest: {id:'outgoing-fixture',name:'Native runtime bridge fixture',version:'1',configSchema:{type:'object',properties:{timestamp:{type:'string'},beforeArrived:{type:'string'},beforeResume:{type:'string'},status:{type:'string'},noID:{type:'boolean'}}},capabilities:['adapter','model-protocol']},
  setup(ctx) {
    let api, scene, source, disposed = 0, modelCalls = 0
    const sent = [];const resolved=[];let assetId,assetReference;ctx.registerAdminAction('asset',id=>{assetId=id;return true})
    const message = () => ({id:source,sceneId:scene.id,senderId:'synthetic-user',sentAt:ctx.config.timestamp,receivedAt:ctx.config.timestamp,origin:'live',status:'available',parts:[{type:'text',text:'Please give a short synthetic reply.'}]})
    ctx.registerAdminAction('sentID',()=>api.messageId(scene,'sent-1'))
    ctx.registerAdminAction('inspect',()=>({scene,source,sent,disposed,modelCalls,resolved}))
    ctx.registerAdminAction('edit',()=>api.edit({...message(),parts:[{type:'text',text:'Please reply to the edited synthetic request.'}]},scene,{editId:'synthetic-edit-2',order:{kind:'platform_sequence',sequence:2}}))
    ctx.registerAdminAction('recall',()=>{api.recall(scene,source);return api.isRecalled(scene,source)})
    ctx.registerAdminAction('media',id=>api.media(id,scene))
    ctx.registerAdapter('transport',context=>{
      api=context;scene=api.scene('synthetic-account','group','synthetic-room');source=api.messageId(scene,'source')
      return {
        start(){api.status('connected');api.receive(message(),scene,false)},
        async send(scope,segment,signal,beforeTransmit){
          signal.throwIfAborted();
          for(const part of segment.parts)if(part.type==='media'){const media=api.media(part.assetId,scope);if(!media?.file?.startsWith('base64://'))throw Error('actual_managed_bytes_missing');resolved.push({assetId:part.assetId,type:media.type,bytes:Buffer.from(media.file.slice(9),'base64').length})}
          beforeTransmit?.();
          if(ctx.config.beforeArrived){fs.writeFileSync(ctx.config.beforeArrived,'ready');while(!fs.existsSync(ctx.config.beforeResume))await new Promise(resolve=>setTimeout(resolve,1))}
          signal.throwIfAborted();sent.push({scope,segment})
          return {status:ctx.config.status??'sent',...(ctx.config.noID?{}:{messageId:api.messageId(scope,'sent-'+sent.length)})}
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
        if(names.includes('reply.plan')){
 const receipt=messages.filter(m=>m.role==='tool').map(m=>JSON.parse(m.content)).find(v=>v.toolName==='memes.search');
 if(!receipt)return call('memes.search',{query:''});
 if(receipt.value.items.length!==1)throw Error('actual_memes_search_missing_asset');
 assetReference=receipt.value.items[0].id;
 return call('reply.plan',{action:'reply',deliveryTool:'messages.send',targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the selected synthetic request.',constraints:[],facts:[],assets:[{assetId:assetReference,evidenceId:receipt.id}],emoji:[],memorySelections:[],expressionGoals:['Compose a concise fixture reply.'],actions:[]})}
        if(names.includes('messages.send')){
          if(request.messages.some(m=>m.role==='tool'))return response([])
          return call('messages.send',{operationId:'synthetic-send',segments:[{replyToId:data.plan?.targetMessageIds?.[0]??data.planContract?.targetMessageIds?.[0]??source,parts:[{type:'media',mediaType:'image',assetId:data.plan?.assets?.[0]?.assetId??assetReference}]}]})
        }
        throw Error('unexpected_runtime_stage:'+names.join(','))
      }
    })
  }
}
