// Executes only extracted timer registration/disposal with synthetic dependencies.
// Does not start Engine or exercise the runtime proactive business pipeline.
import fs from 'node:fs'
import vm from 'node:vm'
import assert from 'node:assert/strict'
const source = fs.readFileSync(new URL('../../src/engine.ts', import.meta.url), 'utf8')
const begin = source.indexOf('    this.proactiveTimer = setInterval(')
const end = source.indexOf('    this.proactiveTimer.unref()', begin) + '    this.proactiveTimer.unref()'.length
assert(begin >= 0 && end > begin)
const registration = source.slice(begin, end)
const disposeStart = source.indexOf('  async dispose() {')
const disposeEnd = source.indexOf('  private unsupportedSourcePronouns', disposeStart)
assert(disposeStart >= 0 && disposeEnd > disposeStart)
const dispose = source.slice(disposeStart, disposeEnd).replace('const errors: unknown[]', 'const errors')
let callback, interval, stopped = false, now = 0
const trace = [], pending = []
let scenes = [{id:'a',kind:'group',proactiveEnabled:true},{id:'direct',kind:'direct',proactiveEnabled:true},{id:'off',kind:'group',proactiveEnabled:false}]
const engine = {store:{scenes:()=>scenes},runtime:{proactive:(id,scheduled)=>{trace.push([id,scheduled]); return new Promise(resolve=>pending.push(resolve))}},mediaTasks:new Map()}
const sandbox = {setInterval:(f,ms)=>{callback=f;interval=ms;return {unref(){trace.push(['unref'])}}},clearInterval:()=>{stopped=true;trace.push(['stop'])},AggregateError}
vm.runInNewContext(`(function(){${registration}})`, sandbox).call(engine)
assert.equal(interval,60000)
assert.deepEqual(trace,[['unref']])
function advance(ms){const before=now;now+=ms;for(let n=Math.floor(before/interval)+1;n<=Math.floor(now/interval);n++)if(!stopped)callback()}
advance(59999);assert.equal(pending.length,0)
advance(1);assert.equal(pending.length,1)
scenes=[{id:'b',kind:'group',proactiveEnabled:true}]
advance(60000);assert.equal(pending.length,2) // no awaiting previous Promise
const disposeOrder = ['workspaceMaintenance','semanticLearning','memory','imageDescriptions','learning','contextualLearning','embeddingIndex','memes','plugins','runtime']
for(const name of disposeOrder){const fn=()=>trace.push([name]);if(name==='contextualLearning')engine[name]={queue:{dispose:fn}};else if(name==='imageDescriptions')engine[name]={clear:fn};else engine[name]={...engine[name],dispose:fn}}
await vm.runInNewContext(`({${dispose}}).dispose`, sandbox).call(engine)
advance(60000);assert.equal(pending.length,2)
assert.equal(trace.findIndex(x=>x[0]==='stop')+1,trace.findIndex(x=>x[0]==='workspaceMaintenance'))
assert.deepEqual(trace.slice(-10).map(x=>x[0]),disposeOrder)
assert.equal(engine.disposed,true)
pending.forEach(resolve=>resolve())
// The extracted callback has no catch: synchronous runtime failure propagates.
let failure=new Error('synthetic_proactive_failure')
engine.runtime.proactive=()=>{throw failure}
assert.throws(()=>callback(),e=>e===failure)
const result={intervalMS:interval,initialTick:false,calls:trace.filter(x=>typeof x[1]==='boolean'),overlap:true,disposeOrder:['stop',...disposeOrder],sourceCatchesErrors:false,sourceCancelsProactive:false}
console.log(JSON.stringify(result,null,2))
