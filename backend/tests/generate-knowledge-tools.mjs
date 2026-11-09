import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import {pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT;if(!root)throw Error('HE_SOURCE_ROOT required')
const {definition}=await import(pathToFileURL(path.join(root,'src/memory/learning/contracts.ts')))
const {KnowledgeToolQuerySchema}=await import(pathToFileURL(path.join(root,'src/memory/knowledge/query.ts')))
const engine=fs.readFileSync(path.join(root,'src/engine.ts'),'utf8')
const at=engine.indexOf("'memory.knowledge',"),start=engine.lastIndexOf('definition(',at),end=engine.indexOf('\n          definition(',at)
const expression=engine.slice(start,end).trim().replace(/,$/,'')
const value=new Function('definition','KnowledgeToolQuerySchema',`return ${expression}`)(definition,KnowledgeToolQuerySchema)
const inputs=[{anchor:{kind:'person',personId:'p'},depth:1},{anchor:{kind:'entity',entityId:'e',backingRef:{id:'r',version:1,deletionEpoch:0}},depth:2},{anchor:{kind:'memory',memoryId:'r'},depth:1}]
const valid=inputs.map(input=>({...input,predicates:['uses_tool']}))
const cases=[...inputs,...valid,{...valid[0],depth:3},{...valid[0],predicates:[]},{...valid[0],unexpected:true}].map(input=>({input,result:KnowledgeToolQuerySchema.safeParse(input)}))
const out={engineSHA256:crypto.createHash('sha256').update(engine).digest('hex'),definition:value,queryCases:cases.map(({input,result})=>({input,success:result.success,...(result.success?{data:result.data}:{issues:result.error.issues})}))}
const encoded=JSON.stringify(out,null,2)+'\n';const target=process.argv[2]
if(process.argv.includes('--check')){if(fs.readFileSync(target,'utf8')!==encoded)throw Error('knowledge tool source drift')}else fs.writeFileSync(target,encoded)
