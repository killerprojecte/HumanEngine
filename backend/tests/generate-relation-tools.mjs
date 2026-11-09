import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import {pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT;if(!root)throw Error('HE_SOURCE_ROOT required')
const {z}=await import(pathToFileURL(path.join(root,'node_modules/zod/index.js')))
const {definition}=await import(pathToFileURL(path.join(root,'src/memory/learning/contracts.ts')))
const engine=fs.readFileSync(path.join(root,'src/engine.ts'),'utf8')
const at=engine.indexOf("'memory.relations',"),start=engine.lastIndexOf('definition(',at),end=engine.indexOf('\n              ]',at)
const expression=engine.slice(start,end).trim().replace(/,$/,'')
const value=new Function('z','definition',`return ${expression}`)(z,definition)
const out={engineSHA256:crypto.createHash('sha256').update(engine).digest('hex'),definition:value}
const encoded=JSON.stringify(out,null,2)+'\n';const target=process.argv[2]
if(process.argv.includes('--check')){if(fs.readFileSync(target,'utf8')!==encoded)throw Error('relation tool source drift')}else fs.writeFileSync(target,encoded)
