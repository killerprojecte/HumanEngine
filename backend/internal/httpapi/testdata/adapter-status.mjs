export default {
 manifest:{id:'status-fixture',name:'Synthetic status owner',version:'1',configSchema:{type:'object',additionalProperties:false},capabilities:['adapter']},
 setup(ctx){
  let api, factoryReads=0
  ctx.registerAdminAction('status',value=>{api.status(value);return factoryReads})
  ctx.registerAdapter('transport',context=>{
   api=context
   return {
    get status(){factoryReads++;throw Error('factory_status_not_runtime_status')},
    start(){api.status('connected')},
    send(){throw Error('status_must_not_send')},
    history(){throw Error('status_must_not_read_history')},
    getMessage(){throw Error('status_must_not_read_message')},
    dispose(){},
   }
  })
 }
}
