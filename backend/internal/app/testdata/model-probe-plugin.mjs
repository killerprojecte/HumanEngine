export default {
  manifest: { id:'fixture', name:'Synthetic Probe', version:'1', configSchema:{type:'object',properties:{},additionalProperties:false}, capabilities:['model-protocol'] },
  setup(ctx) {
    const calls = []
    ctx.registerAdminAction('probe-calls', () => calls.splice(0))
    const usage = { inputTokens:10, outputTokens:2, totalTokens:12 }
    ctx.registerModelProtocol('probe','Synthetic Probe',{
      async complete(context,messages,tools) {
        calls.push({ operation:'complete', model:context.model.id, capabilities:context.model.capabilities, messages, tools })
        return { text:'OK', toolCalls:[], usage }
      },
      async embeddings(context,inputs) {
        calls.push({ operation:'embeddings', model:context.model.id, capabilities:context.model.capabilities, inputs })
        return { vectors:[[1,2]], usage }
      },
    })
  },
}
