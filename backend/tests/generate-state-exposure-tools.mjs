import fs from 'node:fs'
import {createRequire} from 'node:module'
import path from 'node:path'
import {pathToFileURL} from 'node:url'
import crypto from 'node:crypto'
const root=process.env.HE_SOURCE_ROOT
if(!root)throw Error('HE_SOURCE_ROOT required')
const require=createRequire(path.join(root,'package.json'))
const {z}=require('@hono/zod-openapi')
const {definition}=await import(pathToFileURL(path.join(root,'src/memory/learning/contracts.ts')))
const engine=fs.readFileSync(path.join(root,'src/engine.ts'),'utf8')
const from=engine.indexOf('...(settings.memoryCommitmentsEnabled && settings.memoryCommitmentsAnswerEnabled')
const until=engine.indexOf('...(settings.memoryGraphEnabled && settings.memoryGraphAnswerEnabled',from)
if(from<0||until<0)throw Error('source tool block unavailable')
const block=engine.slice(from,until)
const build=new Function('z','definition','settings',`return [${block}]`)
const combinations=[]
for(let flags=0;flags<8;flags++){
 const settings={memoryCommitmentsEnabled:!!(flags&1),memoryCommitmentsAnswerEnabled:!!(flags&2),memoryHistoricalReadEnabled:!!(flags&4)}
 combinations.push({settings,tools:build(z,definition,settings)})
}
const iso=['2024-02-29T12:00Z','0000-02-29T12:00:00Z','2024-02-29T12:00:00.123456789012Z','2023-02-29T12:00:00Z','2024-02-29T12:00:00+00:00','2024-02-29T12:00:00z','2024-02-29T24:00:00Z','2024-02-29T12:00:60Z'].map(value=>({value,valid:z.iso.datetime().safeParse(value).success}))
// Capture the actual schemas supplied to definition, not hand-built lookalikes.
const schemas=new Map()
build(z,(name,description,schema)=>{schemas.set(name,schema);return definition(name,description,schema)},combinations[7].settings)
const inputs=[['memory.commitments',{personId:17,unknown:true}],['memory.commitments',{lifecycle:'terminal'}],['memory.history',{memoryId:'a',asOfRecorded:'not-a-date'}],['memory.history',{memoryId:'a',anchor:{}}],['memory.commitmentHistory',{id:'p',version:9007199254740992,deletionEpoch:0}],['memory.commitmentTimeline',{id:'p',version:1,deletionEpoch:0,asOfRecorded:'2024-02-29T12:00Z'}]].map(([name,input])=>({name,input,schemaValid:schemas.get(name).safeParse(input).success}))
const result={sourceSHA256:crypto.createHash('sha256').update(engine).digest('hex'),combinations,iso,inputs}
const target=process.argv[2]
if(!target)throw Error('output path required')
const encoded=JSON.stringify(result,null,2)+'\n'
if(process.argv.includes('--check')){if(fs.readFileSync(target,'utf8')!==encoded)throw Error('state tool source oracle drift')}else{fs.writeFileSync(target,encoded)}
