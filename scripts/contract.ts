import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import openapiTS, { astToString } from 'openapi-typescript'
import { createApp } from '../src/api.ts'
import { Store } from '../src/store.ts'
const store = new Store(':memory:')
try {
  const app=createApp(store,{token:'contract-only'})
  const spec=app.getOpenAPI31Document({openapi:'3.1.0',info:{title:'HumanEngine API',version:'0.1.0'},security:[{adminToken:[]}]})
  const files={'openapi.json':JSON.stringify(spec,null,2)+'\n','app/types/api.d.ts':astToString(await openapiTS(spec as Parameters<typeof openapiTS>[0]))}
  mkdirSync('app/types',{recursive:true})
  for(const [path,content] of Object.entries(files)) {
    if(process.argv.includes('--check')) {if(readFileSync(path,'utf8')!==content)throw new Error(`Generated contract differs: ${path}`)}
    else writeFileSync(path,content)
  }
} finally {store.close()}
