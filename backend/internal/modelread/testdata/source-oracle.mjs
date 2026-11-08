import {readFileSync} from 'node:fs'
import {resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
const root=resolve(process.env.HE_SOURCE_ROOT??new URL('../../../..',import.meta.url).pathname)
const {ModelReadPolicy}=await import(pathToFileURL(resolve(root,'src/memory/read/model-read-policy.ts')))
let scene
const policy=new ModelReadPolicy({scene:()=>scene})
const attempt=fn=>{try{return fn()}catch(e){return {error:e.message}}}
const cases=[]
for(const value of [undefined,{id:'s'},{id:'s',memoryReadEnabled:false},{id:'s',memoryReadEnabled:null},{id:'s',memoryReadRevision:null},{id:'s',memoryReadRevision:0.5},{id:'s',memoryReadRevision:-1},{id:'s',personalizationEnabled:false},{id:'s',personalizationEnabled:'yes'}]){
 scene=value
 cases.push({scene:value??null,allowed:attempt(()=>policy.allowed('s')),personal:attempt(()=>policy.personalizationAllowed('s')),require:attempt(()=>{const t=policy.require('s');return {revision:t.revision,personal:t.personalizationEnabled,valid:policy.valid(t),copy:policy.valid({...t})}})})
}
const output=JSON.stringify({source:'src/memory/read/model-read-policy.ts',cases},null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(new URL('source-oracle.json',import.meta.url),'utf8')!==output)throw new Error('model_read_fixture_mismatch')}else process.stdout.write(output)
