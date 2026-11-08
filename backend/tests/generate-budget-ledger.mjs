import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'
const root=resolve(process.argv[2]??'.')
const require=createRequire(root+'/package.json')
const Database=require('better-sqlite3')
const {NativeBudgetGate,withBudgetContext}=await import(pathToFileURL(root+'/src/budget/native-attempt.ts'))
const {BudgetLedger}=await import(pathToFileURL(root+'/src/budget/ledger.ts'))
const {quoteEstimate,exactTokenSubtotalNanoUsd}=await import(pathToFileURL(root+'/src/budget/estimate.ts'))
const sourcePaths=['src/budget/ledger.ts','src/budget/estimate.ts','src/budget/storage.ts','src/budget/contracts.ts','src/model-cost.ts','src/budget/external-cost/service.ts','src/budget/native-attempt.ts','src/budget/errors.ts','src/scheduler.ts']
const sources=Object.fromEntries(sourcePaths.map(p=>[p,createHash('sha256').update(readFileSync(root+'/'+p)).digest('hex')]))
const prices={inputUsdPerMillion:0.0000001,outputUsdPerMillion:0.0000002,cacheReadUsdPerMillion:0.00000003,cacheWriteUsdPerMillion:0.0000002,cacheWrite5mUsdPerMillion:0.0000003,cacheWrite1hUsdPerMillion:0.0000004,cacheWrite30mUsdPerMillion:0.0000005,cacheWrite12hUsdPerMillion:0.0000006}
const categories=Object.keys(prices).filter(k=>k!=='outputUsdPerMillion')
const base={id:'one',requestId:'request-one',attempt:1,providerId:'synthetic',modelId:'model',operation:'complete',lane:'foreground',turnId:'turn',estimate:{source:'verified_contract',estimatorVersion:'v1',contractVersion:'c1',enforceable:true,inputUpperTokens:100,outputUpperTokens:10,inputCategories:categories},prices}
const policy={dailyNanoUsd:1000000,maxUnresolvedNanoUsd:1000000}
const full={inputTokens:10,outputTokens:2,cacheKnown:true,cacheReadInputTokens:2,cacheWriteInputTokens:4,cacheWrite5mInputTokens:1,cacheWrite1hInputTokens:1,cacheWrite30mInputTokens:1,cacheWrite12hInputTokens:1}
let day=Date.parse('2026-11-08T12:00:00.000Z')
const cases=[]
function run(name,fn){const db=new Database(':memory:');const ledger=new BudgetLedger(db,{bootId:'boot',now:()=>day});const steps=[];const step=(method,...args)=>{try{const result=ledger[method](...args);steps.push({method,args,result});return result}catch(e){steps.push({method,args,error:e.message})}};fn(step,db,ledger);const rows=db.prepare('SELECT * FROM budget_attempts ORDER BY id').all();const reconciliations=db.prepare('SELECT * FROM budget_reconciliations ORDER BY evidence_id').all();cases.push({name,steps,rows,reconciliations});db.close();day=Date.parse('2026-11-08T12:00:00.000Z')}
run('complete_and_conflicts',s=>{s('reserve',base,policy);s('reserve',{...base,estimate:{...base.estimate,inputCategories:[...categories].reverse()}},policy);s('start','one');s('start','one');s('settle','one',full);s('settle','one',full);s('settle','one',{...full,outputTokens:3});s('coverage',policy)})
run('partial_reconciliation',s=>{s('reserve',base,policy);s('start','one');s('settle','one',{inputTokens:10,outputTokens:2});s('coverage',policy);s('reconcile','one',full,'evidence');s('reconcile','one',full,'evidence');s('reconcile','one',{...full,outputTokens:3},'evidence');s('coverage',policy)})
run('unknown_invalid_usage',s=>{s('reserve',base,policy);s('start','one');s('settle','one',{...full,cacheWriteInputTokens:100});s('coverage',policy);s('reconcile','one',full,'fixed')})
run('overrun_and_quarantine',s=>{s('reserve',base,policy);s('start','one');s('settle','one',{inputTokens:101,outputTokens:2});s('coverage',policy);s('reserve',{...base,id:'two',requestId:'two'},policy);s('reserve',{...base,id:'two',requestId:'two'},{...policy,mode:'best_effort'});s('reconcile','one',{inputTokens:101,outputTokens:2,cacheKnown:true,cacheReadInputTokens:0,cacheWriteInputTokens:0},'proof');s('reserve',{...base,id:'two',requestId:'two'},policy)})
run('release_and_identity',s=>{s('reserve',base,policy);s('reserve',{...base,id:'duplicate'},policy);s('reserve',{...base,modelId:'other'},policy);s('release','one');s('release','one');s('start','one');s('coverage',policy)})
run('policy_caps',s=>{s('reserve',base,{...policy,perTurnCalls:0});s('reserve',base,{...policy,perTurnTokens:1});s('reserve',base,{...policy,perTurnNanoUsd:0});s('reserve',base,{...policy,laneDailyNanoUsd:{foreground:0}});s('reserve',base,{...policy,dailyNanoUsd:0});s('reserve',{...base,turnId:undefined},{...policy,perTurnCalls:1})})
run('dead_boot', (s,db)=>{s('reserve',base,policy);s('reserve',{...base,id:'two',requestId:'two'},policy);s('start','two');s('recoverDeadBoot','boot');db.prepare("UPDATE budget_attempts SET boot_id='dead'").run();s('recoverDeadBoot','dead');s('coverage',policy)})
run('old_day_holds',(s,db)=>{s('reserve',base,policy);s('start','one');s('unknown','one');db.prepare("UPDATE budget_attempts SET window_start='2026-11-07'").run();s('coverage',policy);s('reserve',{...base,id:'two',requestId:'two'},{...policy,maxUnresolvedNanoUsd:0})})
const quotes=[]
for(const rate of [0,1e-7,0.1,0.30000000000000004,1e-20,1e20]){const request={...base,prices:Object.fromEntries(Object.keys(prices).map(k=>[k,rate]))};try{quotes.push({request,amount:quoteEstimate(request.estimate,request.prices)})}catch(e){quotes.push({request,error:e.message})}}
const nativeDB=new Database(':memory:')
const nativeLedger=new BudgetLedger(nativeDB,{bootId:'boot',now:()=>day})
const descriptor={providerId:'synthetic',modelId:'model',protocol:'openai-chat',operation:'complete',role:'planner',prices}
const nativeBody={messages:[{role:'user',content:'synthetic'}]}
const nativeUsage={inputTokens:10,outputTokens:2,cacheKnown:true,cacheReadInputTokens:0,cacheWriteInputTokens:0}
const gate=new NativeBudgetGate({ledger:nativeLedger,policy:()=>policy,mode:'best_effort',maxOutputTokens:10})
let limitedBody
withBudgetContext({turnId:'native-turn',lane:'maintenance'},()=>gate.runRequest(()=>{
 const complete=gate.prepare(descriptor,nativeBody);limitedBody=complete.body;complete.start();complete.settle(nativeUsage);complete.fail()
 const unknown=gate.prepare(descriptor,nativeBody);unknown.start();unknown.fail();unknown.fail()
 const released=gate.prepare(descriptor,nativeBody);released.fail()
}))
const nativeRows=nativeDB.prepare("SELECT * FROM budget_attempts ORDER BY json_extract(payload,'$.attempt')").all().map(r=>{const p=JSON.parse(r.payload);return {attempt:p.attempt,lane:p.lane,turnId:p.turnId,estimate:p.estimate,state:r.state,reservedNanoUsd:r.reserved_nano,knownNanoUsd:r.known_nano,heldNanoUsd:r.held_nano,overrun:r.overrun,unboundedExposure:r.unbounded_exposure}})
const native={descriptor,body:nativeBody,usage:nativeUsage,limitedBody,rows:nativeRows,coverage:nativeLedger.coverage(policy)}
nativeDB.close()
console.log(JSON.stringify({sources,base,policy,full,cases,quotes,native,exactSubtotal:exactTokenSubtotalNanoUsd([[1,0.0000001],[1,0.0000001]])},null,2))
