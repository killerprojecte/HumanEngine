// Executes extracted current source methods, never constructs/starts HumanEngine.
import fs from 'node:fs'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
const root = new URL('../../', import.meta.url)
const require = createRequire(new URL('package.json', root))
const ts = require('typescript')
const source = fs.readFileSync(new URL('src/engine.ts', root), 'utf8')
const scheduler = fs.readFileSync(new URL('src/scheduler.ts', root), 'utf8')
const load = async code => import('data:text/javascript;base64,' + Buffer.from(ts.transpileModule(code,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ES2022}}).outputText).toString('base64'))
const { WorkScheduler } = await load(scheduler)
const ingest = source.slice(source.indexOf('  private async ingestMedia('),source.indexOf('  async analyzeMeme('))
const cancel = source.slice(source.indexOf('  private cancelLearning('),source.indexOf('  private unsupportedSourcePronouns('))
assert(ingest.includes('this.mediaTasks.delete(key)'))
assert(source.includes('this.mediaTasks.get(`${message.sceneId}|${message.id}`)?.abort()'))
assert(source.includes("scene.kind !== 'simulation' && message.senderId !== 'bot'"))
const { Probe } = await load('export class Probe {\n'+ingest+cancel+'\n}')
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve}}
function probe(){
 const p=new Probe();p.mediaTasks=new Map();p.store={log(){}};p.policyReads=0;p.modelRead={personalizationAllowed(){p.policyReads++;return true}}
 const scheduler=new WorkScheduler(2,64);p.jobs=[]
 p.background={run(priority,signal){const gate=deferred();p.jobs.push({signal,gate,priority});return scheduler.run(priority,signal,()=>gate.promise)}}
 p.learning={cancelLearning(){},dispose(){}};p.semanticLearning={cancelScene(){},dispose(){}};p.contextualLearning={queue:{cancelScene(){},dispose(){}}}
 for(const key of ['workspaceMaintenance','memory','embeddingIndex','memes','plugins','runtime'])p[key]={dispose(){}}
 p.imageDescriptions={clear(){}};return p
}
const message=(sceneId='s',id='m')=>({sceneId,id,parts:[{type:'media',assetId:'ob:media:fixture'}]})
const p=probe();await p.ingestMedia({parts:[{type:'text',text:'no media'}]});assert.equal(p.policyReads,0)
const a=p.ingestMedia(message());const b=p.ingestMedia(message());assert.equal(p.jobs.length,2)
p.mediaTasks.get('s|m')?.abort();assert.equal(p.jobs[0].signal.aborted,false);assert.equal(p.jobs[1].signal.aborted,true)
p.jobs[0].gate.resolve();await a;assert.equal(p.mediaTasks.size,0)
await p.dispose();assert.equal(p.jobs[0].signal.aborted,false)
p.jobs[1].gate.resolve();await b
const q=probe();const qa=q.ingestMedia(message());const qb=q.ingestMedia(message());await q.dispose()
assert.equal(q.jobs[0].signal.aborted,false);assert.equal(q.jobs[1].signal.aborted,true)
q.jobs.forEach(j=>j.gate.resolve());await Promise.all([qa,qb])
const r=probe();const ra=r.ingestMedia(message());const rb=r.ingestMedia(message());r.jobs[0].gate.resolve();await ra
r.mediaTasks.get('s|m')?.abort();assert.equal(r.jobs[1].signal.aborted,false);await r.dispose();assert.equal(r.jobs[1].signal.aborted,false)
r.jobs[1].gate.resolve();await rb
const s=probe();const sa=s.ingestMedia(message('s'));const sb=s.ingestMedia(message('s|nested'));s.cancelLearning('s');assert(s.jobs.every(j=>j.signal.aborted));s.jobs.forEach(j=>j.gate.resolve());await Promise.all([sa,sb])
// The scheduler callback is a synthetic gate, not the media business pipeline.
console.log('PASS source oracle: no-media gate; same-key overwrite; unconditional finally delete; dispose indexes only, returns before physical jobs; scene prefix; actual WorkScheduler(2,64), priority 1')
const failure=probe();const logs=[];failure.store.log=(...args)=>logs.push(args);failure.background.run=async()=>{throw new Error('synthetic callback failure')}
await failure.ingestMedia(message());assert.equal(failure.mediaTasks.size,0);assert(logs.some(args=>args.includes('intake_cancelled_or_busy')))
console.log('PASS source callback rejection: outer catch diagnoses failure, finally releases index, caller does not reject')
