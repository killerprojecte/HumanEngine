// Synthetic model IO only. No adapter, source authority or send implementation.
export default {
 manifest:{id:'snow-model',name:'Snow synthetic model',version:'1',capabilities:['model-protocol'],configSchema:{type:'object',properties:{media:{type:'boolean'}}}},
 setup(ctx){let serial=0
  ctx.registerModelProtocol('deterministic','Synthetic Snow replies',{async complete(context,messages,tools){
   context.signal.throwIfAborted();serial++;const names=tools.map(t=>t.name),data={}
   for(const m of messages){if(m.role==='assistant'||m.role==='tool')break;if(m.role==='user')Object.assign(data,JSON.parse(m.content))}
   const response=toolCalls=>({text:'',toolCalls,usage:{inputTokens:0,outputTokens:0,totalTokens:0}}),call=(name,args)=>response([{id:'snow-call-'+serial,name,arguments:args}])
   if(names.includes('reply.validate')){
    const current=Array.isArray(data.newMessages)?data.newMessages:[],targets=data.originalTarget,segments=data.proposed?.segments
    const valid=Array.isArray(targets)&&targets.length>0&&targets.every(id=>current.some(m=>m.id===id&&m.status==='available'))&&
     current.every(m=>m.status==='available'&&Array.isArray(m.parts)&&m.parts.every(p=>p.type==='text'&&p.text==='Please reply to this synthetic request.'))&&
     Array.isArray(segments)&&segments.length>0&&segments.every(s=>(s.replyToId===undefined||targets.includes(s.replyToId))&&Array.isArray(s.parts)&&s.parts.length>0&&s.parts.every(p=>
      p.type==='text'?p.text==='Synthetic Snow reply.':p.type==='media'&&p.mediaType==='image'&&typeof p.assetId==='string'&&p.assetId.length>0))
    return call('reply.validate',{valid})
   }
   if(names.includes('participation.decide'))return call('participation.decide',{action:'reply',reasonCode:'fixture',targetMessageIds:[data.messages[0].id]})
   const receipts=messages.filter(m=>m.role==='tool').flatMap(m=>{try{const v=JSON.parse(m.content);return Array.isArray(v)?v:[v]}catch{return[]}})
   const media=receipts.find(v=>v.toolName==='memes.search'&&Array.isArray(v.value?.items))
   if(names.includes('reply.plan')){
    if(ctx.config.media&&!media)return call('memes.search',{query:''})
    if(ctx.config.media&&!media.value.items.length)throw Error('synthetic_meme_not_visible')
    return call('reply.plan',{action:'reply',deliveryTool:'messages.send',targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the selected synthetic request.',constraints:[],facts:[],assets:ctx.config.media?[{assetId:media.value.items[0].id,evidenceId:media.id}]:[],emoji:[],stateEvidenceIds:[],memorySelections:[],expressionGoals:['Compose a concise fixture reply.'],actions:[]})
   }
   if(names.includes('messages.send')){
    if(messages.some(m=>m.role==='tool'))return response([])
    const parts=[{type:'text',text:'Synthetic Snow reply.'}]
    if(ctx.config.media){const asset=data.plan?.assets?.[0]?.assetId;if(!asset)throw Error('synthetic_plan_asset_missing');parts.push({type:'media',assetId:asset,mediaType:'image'})}
    return call('messages.send',{operationId:'snow-send',segments:[{replyToId:data.plan?.targetMessageIds?.[0]??data.planContract?.targetMessageIds?.[0],parts}]})
   }
   throw Error('unexpected_snow_stage:'+names.join(','))
  }})
 }
}
