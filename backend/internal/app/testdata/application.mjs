import fs from 'node:fs'
// Synthetic protocol and transport only. The test uses the real native Engine,
// source authority, scheduler, planner, reply validation and dispatch owners.
export default {
  manifest: {id:'runtime-fixture',name:'Native runtime bridge fixture',version:'1',configSchema:{type:'object',properties:{memoryMode:{type:'boolean'},timestamp:{type:'string'},followupText:{type:'string'},receivedMarker:{type:'string'},plannerArrived:{type:'string'},plannerResume:{type:'string'},beforeArrived:{type:'string'},beforeResume:{type:'string'}}},capabilities:['adapter','model-protocol']},
  setup(ctx) {
    try {
    let api, scene, source, disposed = 0, modelCalls = 0, sourceSerial = 1
    const sent = [], modelIDs = [], sourceReceipts = [], habitReceipts = [], proposalReceipts = []
    let habitFeedback, habitCalls=0, habitTool, habitProposal, proposalCalls=0, proposalTool
    let memorySourceTarget, lastSourceAlias, memorySourceCalls = 0
    let episodesMode, episodeBarrier, episodePlannerInput
    const episodeReads=[],episodeBundles=[],episodeAds=[]
    const message = () => ({id:source,sceneId:scene.id,senderId:habitFeedback||habitProposal?'synthetic-feedback-'+sourceSerial:'synthetic-user',sentAt:ctx.config.timestamp,receivedAt:ctx.config.timestamp,origin:'live',status:'available',parts:[{type:'text',text:habitProposal?.text||sourceSerial>1&&ctx.config.followupText||'I prefer concise replies.'}]})
    ctx.registerAdminAction('inspect',()=>({scene,source,sent,disposed,modelCalls,modelIDs,memorySourceCalls,sourceReceipts,habitCalls,habitTool,habitReceipts,proposalCalls,proposalTool,proposalReceipts,episodeReads,episodeBundles,episodeAds,episodePlannerInput}))
    ctx.registerAdminAction('episode-mode',input=>{episodesMode=input.mode;return {ok:true}})
    ctx.registerAdminAction('episode-hold-send',input=>{episodeBarrier=input;return {ok:true}})
    ctx.registerAdminAction('habit-proposal', input => {habitProposal=input;return {ok:true}})
    ctx.registerAdminAction('habit-feedback', input => {habitFeedback=input;return {ok:true}})
    ctx.registerAdminAction('source-memory', input => {memorySourceTarget=input.id;return {ok:true}})
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
          const barrier=episodeBarrier
          if(barrier){episodeBarrier=undefined;fs.writeFileSync(barrier.arrived,'ready');while(!fs.existsSync(barrier.resume)){signal.throwIfAborted();await new Promise(resolve=>setTimeout(resolve,1))}}
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
        if(names.includes('participation.decide'))return call('participation.decide',{action:'reply',reasonCode:'fixture',targetMessageIds:[((habitFeedback||habitProposal) && data.messages.find(m=>m.senderId==='synthetic-feedback-'+sourceSerial)||data.messages[0]).id]})
        const receipts=request.messages.filter(m=>m.role==='tool').flatMap(m=>{try{const v=JSON.parse(m.content);return Array.isArray(v)?v:[v]}catch{return []}})
        if(names.includes('reply.plan')&&episodesMode){
          const plan={targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the synthetic experience request.',constraints:[],facts:[],assets:[],emoji:[],expressionGoals:[],actions:[]}
          const tool=episodesMode.startsWith('working-')?'memory.workingSet':'memory.episodes'
          const episode=receipts.filter(v=>v.toolName===tool)
          if(!episode.length){
            episodeAds.push(names.includes(tool))
            if(!names.includes(tool))return call('reply.plan',{...plan,action:'noReply'})
            const args=['person','temporal'].includes(episodesMode)?{personId:'synthetic-user'}:
              episodesMode==='working-cold-only'?{personId:'cold-user'}:
              episodesMode==='working-include-cold'?{personId:'cold-user',includeCold:true}:
              episodesMode==='working-second'?{personId:'second',includeCold:true}:{}
            if(episodesMode==='selected'||episodesMode==='working-selected')return response([1,2].map(i=>({id:'workspace-'+modelCalls+'-'+i,name:tool,arguments:args})))
            return call(tool,args)
          }
          if(episodesMode==='temporal')episodePlannerInput=data
          episodeReads.push(episode.map(v=>v.value))
          const selected=episode.filter(v=>v.value.length).map(v=>v.id)
          const id=data.planContract.targetMessageIds[0]
          if(episodesMode==='selected'||episodesMode==='working-selected')plan.actions=[{toolName:'learning.propose',arguments:{content:'Episode state cannot authorize derived learning.',kind:'fact',epistemicKind:'explicit',sourceMessageIds:[id]}}]
          return call('reply.plan',{...plan,action:'reply',deliveryTool:'messages.send',stateEvidenceIds:selected})
        }
        if(memorySourceTarget && names.includes('reply.plan')){
          const receipt=receipts.find(v=>v.toolName==='memory.source')
          const person=receipts.find(v=>v.toolName==='memory.person')
          if(!receipt){
            if(!names.includes('memory.source'))throw Error('missing_memory_source_tool')
            const anchor=receipts.find(v=>v.toolName==='fixture.memoryAnchor')
            if(memorySourceTarget==='anchor'&&!anchor)return call('fixture.memoryAnchor',{})
            if(memorySourceTarget==='person'&&!person)return call('memory.person',{personId:'synthetic-user'})
            const id=person?.value[0]?.id??anchor?.value.memoryId??(memorySourceTarget==='prior'?lastSourceAlias:memorySourceTarget)
            if(person)lastSourceAlias=id
            memorySourceCalls++
            return call('memory.source',{memoryId:id})
          }
          if(person&&JSON.stringify(receipt.value.sources)!==JSON.stringify(person.value[0]?.sources))
            throw Error('source_fixture_not_original_references')
          sourceReceipts.push(receipt.value)
          if(memorySourceTarget==='prior')throw Error('fixture_prior_reference_finished')
        }
        const memoryReceipt=receipts.find(v=>v.toolName==='memory.person'&&Array.isArray(v.value))
        if(ctx.config.memoryMode&&names.includes('reply.plan')&&!memoryReceipt)return call('memory.person',{personId:'synthetic-user'})
        if(ctx.config.memoryMode&&memoryReceipt?.value.length===0)throw Error('synthetic_memory_missing')
        const identityReceipt=receipts.find(v=>v.toolName==='memory.identities'&&Array.isArray(v.value))
        if(ctx.config.memoryMode&&memoryReceipt&&!identityReceipt)return call('memory.identities',{personId:'synthetic-user'})
        if(ctx.config.memoryMode&&identityReceipt?.value.length===0)throw Error('synthetic_identity_page_missing')
        if(habitFeedback && names.includes('reply.plan')){
          const name=habitFeedback.anchor?'fixture.habitAnchor':'rules.list'
          const found=receipts.find(v=>v.toolName===name)
          if(!found && names.includes(name))return call(name,{})
          habitFeedback.alias=found?.value.learned?.[0]?.id??found?.value.habitId
          habitFeedback.sourceAlias=found?.value.sourceMessageId
        }
        if(habitProposal&&names.includes('reply.plan')){
          proposalCalls++
          proposalTool=data.actionTools.find(t=>t.name==='habits.propose')
          const noReply=habitProposal.noReply
          return call('reply.plan',{action:noReply?'noReply':'reply',...(!noReply&&{deliveryTool:'messages.send'}),targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Apply the explicit scoped synthetic feedback.',constraints:[],facts:[],assets:[],emoji:[],expressionGoals:[],actions:[{toolName:'habits.propose',arguments:{...habitProposal.candidate,sourceMessageIds:[data.planContract.targetMessageIds[0]]}}]})
        }
        if(names.includes('reply.plan'))return call('reply.plan',{action:'reply',deliveryTool:'messages.send',targetMessageIds:data.planContract.targetMessageIds,contextMessageIds:data.planContract.contextMessageIds,strategy:'Answer the selected synthetic request.',constraints:[],facts:[],assets:[],emoji:[],stateEvidenceIds:identityReceipt?[identityReceipt.id]:[],memorySelections:memoryReceipt?[{evidenceId:memoryReceipt.id,memoryIds:memoryReceipt.value.map(m=>m.id)}]:[],expressionGoals:['Compose a concise fixture reply.'],actions:habitFeedback?(()=> {
          habitCalls++
          habitTool=data.actionTools.find(t=>t.name==='habits.feedback')
          return [{toolName:'habits.feedback',arguments:{id:habitFeedback.alias,direction:habitFeedback.direction,sourceMessageIds:[habitFeedback.sourceAlias??data.planContract.targetMessageIds[0]]}}]
        })():identityReceipt?[{toolName:'habits.propose',arguments:{kind:'sceneRule',content:'Must not derive a rule from identity states.',sourceMessageIds:[data.planContract.targetMessageIds[0]]}}]:[]})
        if(names.includes('messages.send')){
          if(episodesMode&&!request.messages.some(m=>m.role==='tool'))
            episodeBundles.push({evidence:data.evidence,actions:data.actionReceipts,coverage:data.memoryEvidenceCoverage})
          if(!request.messages.some(m=>m.role==='tool')&&habitProposal)
            proposalReceipts.push(...(data.actionReceipts??[]).filter(r=>r.toolName==='habits.propose'))
          if(!request.messages.some(m=>m.role==='tool')&&habitFeedback)
            habitReceipts.push(...(data.actionReceipts??[]).filter(r=>r.toolName==='habits.feedback'))
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
