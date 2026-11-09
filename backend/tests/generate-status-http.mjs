import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs'
import {join} from 'node:path'
import {tmpdir} from 'node:os'
import {Store} from '../../src/store.ts'
import {Engine} from '../../src/engine.ts'
import {createApp} from '../../src/api.ts'
const cases=[]
let seed
for(const spec of [{name:'empty'},{name:'error',error:true},{name:'head',method:'HEAD'},{name:'auth',badAuth:true},{name:'method',method:'PUT'},{name:'trailing',path:'/api/status/'}]){
 const dir=mkdtempSync(join(tmpdir(),'he-status-source-')),cwd=process.cwd()
 let store,engine
 try{
  process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
  seed??={settings:store.settings,models:store.models,vectorVersion:store.vectorVersion}
  if(spec.error)store.log('error','synthetic','status_error')
  const app=createApp(store,{engine,token:'synthetic-token'})
  const response=await app.request('http://local'+(spec.path??'/api/status'),{method:spec.method??'GET',headers:{authorization:'Bearer '+(spec.badAuth?'wrong':'synthetic-token')}})
  let text=await response.text()
  if(response.status===200&&spec.method!=='HEAD'){
   const body=JSON.parse(text)
   assert.match(body.startedAt,/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/)
   assert.ok(body.uptimeSeconds>=0)
   body.startedAt='<application-start>';body.uptimeSeconds=0;text=JSON.stringify(body)
  }
  cases.push({...spec,status:response.status,text,headers:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k=>[k,response.headers.get(k)]))})
 }finally{await engine?.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}
}
const output=JSON.stringify({seed,cases},null,2)+'\n',file=new URL('../internal/httpapi/testdata/status-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),output);else writeFileSync(file,output)
console.log('PASS 6 actual status HTTP source cases')
