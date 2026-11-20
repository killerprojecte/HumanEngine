import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const usage = { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 }
const call = args => ({ id:'call-probe', type:'function', function:{ name:'probe__echo', arguments:JSON.stringify(args) } })
const chat = (text, tools) => ({ choices:[{ finish_reason:tools?'tool_calls':'stop', message:{ role:'assistant',content:text,...(tools ? { tool_calls:tools } : {}) } }], usage })
const embed = vector => ({ data:[{ index:0, embedding:vector }], usage:{ prompt_tokens:10,total_tokens:10 } })
const nativeCases = [
  { name:'chat', scope:'text', provider:'chat', reply:chat('OK') },
  { name:'chat-empty', scope:'text', provider:'chat', reply:chat(' ') },
  { name:'chat-no-fallback', scope:'text', provider:'chat', reply:{error:'synthetic upstream payload'}, error:503 },
  { name:'tools', scope:'tools', provider:'chat', reply:chat('',[call({ value:'humanengine-probe' })]) },
  { name:'tools-wrong-value', scope:'tools', provider:'chat', reply:chat('',[call({ value:'wrong' })]) },
  { name:'tools-extra-argument', scope:'tools', provider:'chat', reply:chat('',[call({ value:'humanengine-probe',extra:true })]) },
  { name:'vision', scope:'vision', provider:'chat', reply:chat('Accepted') },
  { name:'embedding', scope:'embedding', provider:'chat', reply:embed([1,2]) },
  { name:'embedding-zero', scope:'embedding', provider:'chat', reply:embed([0,0]) },
  { name:'embedding-wrong-dimensions', scope:'embedding', provider:'chat', reply:embed([1,2,3]) },
  { name:'responses-text', scope:'text', provider:'responses', reply:{ status:'completed',output:[{type:'message',role:'assistant',content:[{type:'output_text',text:'OK'}]}],usage:{input_tokens:10,output_tokens:2,total_tokens:12} } },
  { name:'responses-tools', scope:'tools', provider:'responses', reply:{ status:'completed',output:[{type:'function_call',call_id:'call-probe',name:'probe__echo',arguments:'{"value":"humanengine-probe"}'}],usage:{input_tokens:10,output_tokens:2,total_tokens:12} } },
  { name:'responses-vision', scope:'vision', provider:'responses', reply:{ status:'completed',output:[{type:'message',role:'assistant',content:[{type:'output_text',text:'Accepted'}]}],usage:{input_tokens:10,output_tokens:2,total_tokens:12} } },
]
const cases = []
const store = new Store(':memory:')
const engine = new Engine(store)
let current, wire = [], guest = []
const peer = createServer(async (req,res) => {
  let raw = ''
  for await (const bytes of req) raw += bytes
  wire.push({ path:req.url, body:JSON.parse(raw) })
  res.writeHead(current.error ?? 200, { 'Content-Type':'application/json' })
  res.end(JSON.stringify(current.reply))
})
peer.listen(0, '127.0.0.1')
await once(peer,'listening')
try {
  const base = 'http://127.0.0.1:' + peer.address().port
  const protocol = new AbortController()
  const guestUsage = { inputTokens:10, outputTokens:2, totalTokens:12 }
  engine.protocols.register('fixture:probe','Synthetic Probe',{
    complete: async (ctx,messages,tools) => {
      guest.push({ operation:'complete', model:ctx.model.id, capabilities:ctx.model.capabilities, messages, tools })
      return { text:'OK', toolCalls:[], usage:guestUsage }
    },
    embeddings: async (ctx,inputs) => {
      guest.push({ operation:'embeddings', model:ctx.model.id, capabilities:ctx.model.capabilities, inputs })
      return { vectors:[[1,2]], usage:guestUsage }
    }
  },protocol.signal)
  const model = (id, providerId) => ({ id, providerId, displayName:id, apiModelName:'wire-'+id,
    capabilities:['text'], embeddingDimensions:2, embeddingBatchSize:4, streaming:false, inputUsdPerMillion:2, outputUsdPerMillion:3 })
  const models = {
    providers:[
      ['chat','openai-chat'], ['responses','openai-responses'], ['fallback','openai-chat'], ['custom','fixture:probe'],
      ['unregistered','fixture:unregistered']
    ].map(([id,protocol]) => ({ id,name:id,protocol,baseUrl:base+'/v1',credentialEnv:'',retryCount:0,timeoutMs:5000 })),
    models:['chat','responses','fallback','custom','unregistered'].map(id => model(id,id)),
    routes:[{role:'replyer',orderedModelIds:['fallback'],timeoutMs:5000},{role:'vision',orderedModelIds:['fallback'],timeoutMs:5000}],
  }
  store.saveModels(models,{})
  const app = createApp(store,{ engine, token:'synthetic-token' })
  const capture = async spec => {
    current = spec
    wire = []; guest = []
    const payload = spec.payload ?? JSON.stringify({ capability:spec.scope })
    const headers = {'Content-Type':spec.contentType ?? 'application/json','X-Request-ID':'12345678-1234-4123-8123-123456789012'}
    if (!spec.unauthorized) headers.Authorization = 'Bearer synthetic-token'
    const res = await app.request('/api/models/'+(spec.id ?? spec.provider ?? 'chat')+'/probe',{ method:'POST',headers,body:payload })
    const body = await res.json()
    if (body.requestId) body.requestId = '<request-id>'
    if ('durationMs' in body) {
      assert.ok(Number.isInteger(body.durationMs) && body.durationMs >= 0)
      delete body.durationMs
    }
    assert.ok(wire.every(req => req.body.model !== 'wire-fallback'))
    cases.push({ ...spec,payload, status:res.status,body,wire,guest,
      headers:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k => [k,res.headers.get(k) ?? ''])) })
  }
  await capture({name:'auth-first',scope:'text',payload:'{broken',unauthorized:true})
  await capture({name:'malformed',scope:'text',payload:'{broken'})
  await capture({name:'strict',scope:'text',payload:'{"capability":"text","extra":1}'})
  await capture({name:'invalid-capability',scope:'text',payload:'{"capability":"anything"}'})
  await capture({name:'missing',scope:'text',id:'missing'})
  for (const spec of nativeCases) await capture(spec)
  await capture({name:'encoded-id',scope:'text',id:'%63hat',reply:chat('OK')})
  await capture({name:'v1-completion',scope:'text',provider:'custom'})
  await capture({name:'v1-embedding',scope:'embedding',provider:'custom'})
  await capture({name:'v1-unregistered',scope:'text',provider:'unregistered'})
  store.db.exec("CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY,restore_id TEXT,status TEXT); INSERT INTO recovery_model_hold VALUES(1,'12345678-1234-4123-8123-123456789012','held')")
  await capture({name:'held-native',scope:'text',provider:'chat'})
  await capture({name:'held-custom',scope:'text',provider:'custom'})
  await capture({name:'held-unregistered',scope:'text',provider:'unregistered'})
  await capture({name:'held-unregistered-embedding',scope:'embedding',provider:'unregistered'})
  assert.equal(cases.find(c=>c.name==='held-native').body.detail,'budget_restore_accounting_hold')
  assert.equal(cases.find(c=>c.name==='held-custom').guest.length,0)
  for (const provider of models.providers) provider.baseUrl = 'http://127.0.0.1:0/v1'
  const output = JSON.stringify({ node:process.version, models, cases },null,2)+'\n'
  const file = new URL('../internal/app/testdata/model-probe-source.json',import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(file,'utf8'),output)
  else writeFileSync(file,output)
  console.log('PASS '+cases.length+' actual model probe source cases')
} finally {
  peer.closeAllConnections()
  await new Promise(resolve=>peer.close(resolve))
  await engine.dispose()
  store.close()
}
