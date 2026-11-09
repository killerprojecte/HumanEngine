/** Actual source Store/Engine/API and SQL schema, synthetic accounting only. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs'
import {resolve,join} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {tmpdir} from 'node:os'
import {createHash} from 'node:crypto'
const root=resolve(fileURLToPath(new URL('../../',import.meta.url)))
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const { Hono } = await import(pathToFileURL(join(root,'node_modules/hono/dist/hono.js')))
const queryProbe=new Hono();queryProbe.get('/probe',c=>c.json(c.req.queries()))
const {createApp}=await import(pathToFileURL(join(root,'src/api.ts')))
const sourceHashes=Object.fromEntries(['src/api.ts','src/store.ts','src/usage-time.ts','node_modules/hono/dist/utils/url.js'].map(p=>[p,createHash('sha256').update(readFileSync(join(root,p))).digest('hex')]))
const range='start=2026-11-01T00%3A00%3A00Z&end=2026-11-03T00%3A00%3A00Z'
const specs=[{name:'empty',empty:true},{name:'all'},{name:'range',query:range},{name:'hour',query:range+'&bucket=hour'},{name:'explicit-utc',query:range+'&timeZone=UTC'},{name:'alias-utc',query:range+'&timeZone=Etc%2FGMT'},{name:'dst-hour',query:range+'&bucket=hour&timeZone=America%2FNew_York'},{name:'quarter-hour',query:range+'&bucket=hour&timeZone=Asia%2FKathmandu'},{name:'offset-day',query:range+'&timeZone=%2B05%3A45'},{name:'partial-history',query:'start=2026-11-01T12%3A00%3A00Z&end=2026-11-03T00%3A00%3A00Z'},{name:'planner-complete',query:'start=2026-11-01T12%3A00%3A00Z&end=2026-11-02T00%3A00%3A00Z'},{name:'no-overlap',query:'start=2025-01-01T00%3A00%3A00Z&end=2025-01-02T00%3A00%3A00Z'},{name:'offset-instant',query:'start=2026-11-01T01%3A00%3A00%2B01%3A00&end=2026-11-03T01%3A00%3A00%2B01%3A00'},{name:'unknown',query:'unknown=x'},{name:'start-only',query:'start=2026-11-01T00%3A00%3A00Z'},{name:'bucket-only',query:'bucket=day'},{name:'zone-only',query:'timeZone=UTC'},{name:'empty-zone',query:range+'&timeZone='},{name:'bad-zone',query:range+'&timeZone=No%2FSuch'},{name:'bad-bucket',query:range+'&bucket=minute'},{name:'repeated',query:range+'&bucket=day&bucket=hour'},{name:'missing-seconds',query:'start=2026-11-01T00%3A00Z&end=2026-11-03T00%3A00%3A00Z'},{name:'bad-leap',query:'start=2025-02-29T00%3A00%3A00Z&end=2026-11-03T00%3A00%3A00Z'},{name:'long-fraction',query:'start=2026-11-01T00%3A00%3A00.000000001Z&end=2026-11-03T00%3A00%3A00Z'},{name:'year-zero',query:'start=0000-01-01T00%3A00%3A00Z&end=0001-01-01T00%3A00%3A00Z'},{name:'reversed',query:'start=2026-11-03T00%3A00%3A00Z&end=2026-11-01T00%3A00%3A00Z'},{name:'head',method:'HEAD'},{name:'auth',badAuth:true},{name:'method',method:'PUT'},{name:'trailing',path:'/api/usage/'}]
const rawQueries=[
 ['semicolon-bucket','bucket=hour;'],
 ['semicolon-unknown','unknown=x;'],
 ['semicolon-key','bucket;=hour'],
 ['semicolon-end',range+';'],
 ['bad-percent-value','bucket=%'],
 ['bad-percent-known','bucket=hour%GG'],
 ['bad-percent-unknown','unknown=%'],
 ['bad-percent-key','buck%et=hour'],
 ['bad-utf8-value','bucket=%ED%A0%80'],
 ['bad-utf8-key','%FFbucket=hour'],
 ['mixed-percent-groups','bucket=%68%6F%75%72%FF'],
 ['valid-name-bad-value','%62ucket=hour%'],
 ['encoded-semicolon',range+'&bucket=day%3B'],
 ['encoded-equals',range+'&bucket=day%3Dhour'],
 ['extra-equals',range+'&bucket=day=hour'],
 ['plus-space',range+'&timeZone=Etc%2FGMT+1'],
 ['plus-offset-escaped',range+'&timeZone=%2B05%3A45'],
 ['plus-offset-unescaped',range+'&timeZone=+05%3A45'],
 ['encoded-key',range+'&%62ucket=day'],
 ['duplicate-encoded-key',range+'&bucket=day&%62ucket=hour'],
 ['duplicate-malformed',range+'&bucket=day&bucket=%'],
 ['duplicate-unknown','unknown=a&unknown=b'],
 ['empty-keys','&&=ignored&'],
 ['empty-known','bucket'],
 ['encoded-ampersand',range+'&bucket=day%26bucket%3Dhour'],
 ['unknown-valid-percent','unknown=%E2%82%AC'],
 ['known-name-plus','bucket+=hour'],
 ['encoded-nul','bucket=%00'],
 ['bad-byte-after-valid','bucket=%68%6F%75%72%FF%20'],
 ['partial-bad-group','bucket=%68%6F%75%72%FF!%64%61%79'],
]
for(const [name,query] of rawQueries)specs.push({name:'raw-'+name,query})
let schema,seed
const cases=[]
for(const spec of specs){
 const dir=mkdtempSync(join(tmpdir(),'he-usage-read-')),cwd=process.cwd();let store,engine
 try{
  process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
  if(!schema){
   schema=store.db.prepare("SELECT sql FROM sqlite_master WHERE type='table' AND name IN ('model_usage','model_usage_events','model_usage_history') ORDER BY name").all().map(x=>x.sql+';').join('\n')
   const columns=store.db.prepare('PRAGMA table_info(model_usage)').all().map(x=>x.name)
   const row=(day,role,at)=>Object.fromEntries(columns.map((k,i)=>[k,k==='day'?day:k==='role'?role:k==='model_id'?'synthetic':k==='provider_id'?'p':k==='api_model'?'api':i]))
   const events=[{...row('2026-11-01','thinker'),at:'2026-11-01T05:15:00.000Z'},{...row('2026-11-01','replyer'),at:'2026-11-01T06:15:00.000Z'}],history=[row('2026-11-01','replyer'),row('2026-11-02','planner')]
   seed={events,history,columns}
  }
  if(!spec.empty){
   for(const [table,items] of [['model_usage_events',seed.events],['model_usage_history',seed.history]])for(const row of items){const keys=Object.keys(row);store.db.prepare(`INSERT INTO ${table} (${keys.join(',')}) VALUES (${keys.map(k=>'@'+k).join(',')})`).run(row)}
   const keys=seed.columns.slice(0,5),vals=seed.columns.slice(5)
   store.db.exec(`INSERT INTO model_usage SELECT ${keys.join(',')},${vals.map(k=>'SUM('+k+')').join(',')} FROM (SELECT ${seed.columns.join(',')} FROM model_usage_events UNION ALL SELECT ${seed.columns.join(',')} FROM model_usage_history) GROUP BY ${keys.join(',')}`)
  }
  const app=createApp(store,{engine,token:'synthetic-token'}),path=(spec.path??'/api/usage')+(spec.query?'?'+spec.query:''),method=spec.method??'GET'
  const response=await app.request(path,{method,headers:{Authorization:'Bearer '+(spec.badAuth?'bad':'synthetic-token')}})
  const queryValues=await (await queryProbe.request('/probe'+(spec.query?'?'+spec.query:''))).json()
  cases.push({...spec,path,method,queryValues,status:response.status,text:await response.text(),responseHeaders:Object.fromEntries(['content-type','cache-control','x-content-type-options','x-usage-incomplete','x-usage-planner-incomplete'].map(k=>[k,response.headers.get(k)]))})
 }finally{await engine?.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}
}
const out=JSON.stringify({node:process.version,sourceHashes,schema,seed,cases},null,2)+'\n',file=new URL('../internal/usageview/testdata/http-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),out);else writeFileSync(file,out)
console.log(`PASS ${cases.length} actual usage API cases`)
