import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {resolve,dirname} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../',import.meta.url)))
const hashes=JSON.parse(readFileSync(new URL('./router-lifetime-hashes.json',import.meta.url),'utf8'))
for(const [path,hash] of Object.entries(hashes))assert.equal(createHash('sha256').update(readFileSync(resolve(root,path))).digest('hex'),hash,path)
assert.equal(process.version,'v24.22.1')
const {Router}=await import(pathToFileURL(resolve(root,'src/models.ts')))
const {ModelProtocolRegistry}=await import(pathToFileURL(resolve(root,'src/model-protocols.ts')))
const registry=new ModelProtocolRegistry(),controller=new AbortController(),opaque={private:'synthetic'}
registry.register('fixture:router','Fixture',{async complete(context,messages,tools,previous){return {text:context.model.id+':'+(previous===opaque?'continued':'fresh'),toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:opaque}}},controller.signal)
const config={providers:[{id:'p',name:'Synthetic',protocol:'fixture:router',baseUrl:'https://synthetic.invalid',credentialEnv:'',retryCount:0,timeoutMs:1000}],models:[{id:'m',providerId:'p',displayName:'Synthetic',apiModelName:'synthetic',capabilities:['text']}],routes:[{role:'replyer',orderedModelIds:['m']}]}
const make=value=>new Router(value,undefined,undefined,registry)
const captured=make(config),initialConfig=structuredClone(config),first=await captured.complete('replyer',[{role:'user',content:'synthetic'}])
config.models[0].id='m2';config.routes[0].orderedModelIds=['m2']
const same=await captured.complete('replyer',[first.assistant])
const otherSameModel=await make(initialConfig).complete('replyer',[first.assistant])
const next=await make(config).complete('replyer',[first.assistant])
const cases=[first.text,same.text,otherSameModel.text,next.text]
assert.deepEqual(cases,['m:fresh','m:continued','m:fresh','m2:fresh'])
controller.abort()
const target=new URL('../internal/providertransport/testdata/router-lifetime-source.json',import.meta.url)
const out=JSON.stringify({node:process.version,sourceHashes:hashes,cases},null,2)+'\n'
mkdirSync(dirname(fileURLToPath(target)),{recursive:true})
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),out);else writeFileSync(target,out)
console.log('PASS actual source Router snapshot and opaque lifetime 4 cases')
