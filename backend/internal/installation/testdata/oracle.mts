import { mkdtempSync, mkdirSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT || '../../../..')
const { default: Database } = await import(pathToFileURL(join(root,'node_modules/better-sqlite3/lib/index.js')).href)
const { Store }=await import(pathToFileURL(join(root,'src/store.ts')).href)
const { enroll }=await import(pathToFileURL(join(root,'src/memory/recovery/ledger.ts')).href)
const { ensureAssertionEligibilityBaseline }=await import(pathToFileURL(join(root,'src/memory/recovery/eligibility.ts')).href)
const dir=mkdtempSync(join(tmpdir(),'he-installation-source-'))
const output:any={}
try {
 for(const scenario of ['fresh','legacy','reporting','negative','transition']) {
  const data=join(dir,scenario,'data');mkdirSync(data,{recursive:true})
  const database=join(data,'main.sqlite'),authority=join(dir,scenario,'authority')
  process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=authority
  if(scenario==='legacy'||scenario==='reporting') {
   const db=new Database(database);const a=enroll(db,database,authority);a.close()
   db.exec(`CREATE TABLE model_usage (day TEXT NOT NULL, model_id TEXT NOT NULL, provider_id TEXT NOT NULL, api_model TEXT NOT NULL, role TEXT NOT NULL, calls INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, duration_ms REAL NOT NULL, priced_calls INTEGER NOT NULL, estimated_usd REAL NOT NULL, PRIMARY KEY(day,model_id,provider_id,api_model,role));
   INSERT INTO model_usage VALUES('2026-11-01','m','p','api','replyer',2,100,20,50,2,0.02);`)
   if(scenario==='reporting') {
    const columns=['ordinary_input_tokens','cache_read_input_tokens','cache_write_input_tokens','cache_write_5m_input_tokens','cache_write_1h_input_tokens','cache_write_30m_input_tokens','cache_write_12h_input_tokens','cache_known_calls','cache_read_known_calls','cache_write_known_calls','partial_priced_calls','legacy_calls','unknown_usage_calls','ordinary_input_usd','output_usd','cache_read_usd','cache_write_usd','legacy_estimated_usd']
    for(const col of columns)db.exec(`ALTER TABLE model_usage ADD COLUMN ${col} ${col.endsWith('_usd')?'REAL':'INTEGER'} NOT NULL DEFAULT 0`)
    db.exec(`UPDATE model_usage SET cache_read_known_calls=2,cache_read_input_tokens=25;
    CREATE TABLE model_usage_history AS SELECT * FROM model_usage;CREATE UNIQUE INDEX model_usage_history_key ON model_usage_history(day,model_id,provider_id,api_model,role);
    CREATE TABLE model_usage_events AS SELECT *, '2026-11-01T12:00:00.000Z' AS at FROM model_usage;CREATE INDEX model_usage_events_at ON model_usage_events(at);
    UPDATE model_usage SET calls=4,input_tokens=200,output_tokens=40,cache_read_known_calls=4,cache_read_input_tokens=50,priced_calls=4;`)
   }
   db.close()
  }
  const s=new Store(database)
  if(scenario==='negative'||scenario==='transition') {
   s.db.exec('CREATE TABLE memories (id TEXT PRIMARY KEY,value TEXT NOT NULL)')
   for(const [id,status,lineageStatus] of [['z','active',null],['b','active','quarantined'],['a','invalidated','verified']])s.db.prepare('INSERT INTO memories VALUES(?,?)').run(id,JSON.stringify({id,status,lineageStatus,learnedAt:'2026-11-01T00:00:00.000Z',content:'synthetic'}))
  }
  if(scenario==='transition') {
   const {revoke,requireEligibilityReader}=await import(pathToFileURL(join(root,'src/memory/recovery/ledger.ts')).href)
   const {eligibilityIncarnation}=await import(pathToFileURL(join(root,'src/memory/recovery/eligibility-contract.ts')).href)
   s.db.transaction(()=>{requireEligibilityReader(s.db);revoke(s.db,{kind:'assertion_eligibility',schemaVersion:1,operationId:'12345678-1234-4123-8123-123456789012',mode:'transition',changes:[{id:'a',incarnationHash:eligibilityIncarnation({id:'a',learnedAt:'2026-11-01T00:00:00.000Z'}),state:{status:'invalidated',lineageStatus:'verified'},materialHash:null}]})}).immediate()
  }
  ensureAssertionEligibilityBaseline(s.db);ensureAssertionEligibilityBaseline(s.db)
  const auth=new Database(join(authority,'authority.sqlite'),{readonly:true})
  const events=auth.prepare('SELECT payload FROM revocations ORDER BY sequence').all().map((r:any)=>{const e=JSON.parse(r.payload);e.operationId='<uuid>';return JSON.stringify(e)})
  const gate=JSON.parse(s.db.prepare('SELECT value FROM recovery_gate').get().value)
  const tables=['model_usage','model_usage_history','model_usage_events','config','scenes','recall_tombstones','secrets','model_calls']
  output[scenario]={events,reader:gate.minimumReader,vector:s.vectorVersion,mainJournal:s.db.pragma('journal_mode',{simple:true}),authorityJournal:auth.pragma('journal_mode',{simple:true}),schema:Object.fromEntries(tables.map(t=>[t,s.db.pragma(`table_info(${t})`)])),rows:Object.fromEntries(['model_usage','model_usage_history','model_usage_events'].map(t=>[t,s.db.prepare(`SELECT * FROM ${t}`).all()]))}
  auth.close();s.recoveryAuthority?.close();s.db.close()
 }
 if(process.argv.includes('--check')) {
  const expected=JSON.parse(readFileSync(new URL('./oracle.json',import.meta.url),'utf8'))
  if(JSON.stringify(expected)!==JSON.stringify(output))throw new Error('installation source oracle drift')
  console.log('Actual-source installation oracle: 5 scenarios matched')
 }else console.log(JSON.stringify(output,null,2))
}finally{rmSync(dir,{recursive:true,force:true})}
