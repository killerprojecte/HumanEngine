/** Actual exponentiation operator, not a copied approximation. Synthetic only. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {spawnSync} from 'node:child_process'
assert.equal(process.version,'v24.22.1')
const header=process.report.getReport().header
const hostIndex=process.argv.indexOf('--host-output')
const hostOutput=hostIndex<0?undefined:process.argv[hostIndex+1]
if(hostIndex>=0)assert.ok(hostOutput,'--host-output needs a path')
if(!hostOutput) {
 assert.equal(header.glibcVersionRuntime,'2.41')
 assert.equal(process.platform,'linux');assert.equal(process.arch,'x64')
}
const flags=spawnSync(process.execPath,['--v8-options'],{encoding:'utf8'})
assert.equal(flags.status,0)
assert.match(flags.stdout,/--use-std-math-pow[^\n]*\n\s+type: bool\s+default: --use-std-math-pow/)
assert.ok(!process.execArgv.some(x=>x.includes('no-use-std-math-pow')))
const buf=Buffer.alloc(8)
const bits=n=>{buf.writeDoubleBE(n);return buf.toString('hex')}
const number=s=>{buf.writeBigUInt64BE(BigInt('0x'+s));return buf.readDoubleBE()}
const neighbor=(n,delta)=>{buf.writeDoubleBE(n);let b=buf.readBigUInt64BE();buf.writeBigUInt64BE(BigInt.asUintN(64,b+BigInt(delta)));return buf.readDoubleBE()}
const cases=[],seen=new Set()
function addBits(name,xb,yb) {
 const key=xb+yb
 if(seen.has(key)) return
 seen.add(key)
 // Scalars decoded directly from their original bits avoid double-array stores
 // quieting signaling NaNs before the source exponentiation sees them.
 const x=number(xb),y=number(yb)
 assert.equal(bits(x),xb,'base bits changed before exponentiation')
 assert.equal(bits(y),yb,'exponent bits changed before exponentiation')
 const result=x**y
 cases.push({name,x:xb,y:yb,result:Number.isNaN(result)?'NaN':bits(result)})
}
function add(name,x,y) {addBits(name,bits(x),bits(y))}
// Directed signaling base/exponent cases, not incidental random coverage.
// Keep these as strings: a numeric array can set the NaN quiet bit on insertion.
const signaling=['7ff0000000000001','fff0000000000042']
const directed=['0000000000000000','8000000000000000','3ff0000000000000','bff0000000000000','4000000000000000','3fe0000000000000','7ff0000000000000','fff0000000000000']
for(const snan of signaling) {
 for(const other of directed) {
  addBits('signaling-base',snan,other)
  addBits('signaling-exponent',other,snan)
 }
 for(const other of signaling)addBits('signaling-both',snan,other)
}
const specials=[0,-0,1,-1,2,-2,0.5,-0.5,Number.MIN_VALUE,-Number.MIN_VALUE,Number.MAX_VALUE,-Number.MAX_VALUE,Infinity,-Infinity,NaN,number('7ff0000000000001'),number('fff8000000000042')]
for(const x of specials)for(const y of specials)add('special',x,y)
const exponents=[-1075,-1074,-1022,-1021,-745,-512,-90,-30,-7.25,-3,-2,-1,-0.5,0,0.5,1,2,3,7.25,30,512,1023,1024,2**31,2**53,-(2**53),2**63,-(2**63),2**-65,-(2**-65)]
for(const x of [2,0.5,-2,1,neighbor(1,1),neighbor(1,-1),1.5,Number.MIN_VALUE,Number.MAX_VALUE])for(const y of exponents)for(const delta of [-1,0,1])add('boundary',x,neighbor(y,delta))
// Default/custom fractional half-lives and the exact ages exposed by strict
// persisted-HabitService comparison. These are fixture bounds, not runtime caps.
for(const half of [1,7.25,30,365])for(const age of [0,1,1234.567,123456789,124691356,234567891,345678912,86399999,86400001,30*86400000-1,30*86400000+1,90*86400000-1])for(const delta of [-1,0,1])add('habit-decay',2,-Math.max(0,age+delta)/(half*86400000))
for(let i=0;i<128;i++)for(const sign of [-1,1])for(const delta of [-1,0,1])add('table-cell-halfway',2,neighbor(sign*(i+0.5)/128,delta))
// A reproducible bitstream spans all binary64 exponent/sign classes. The
// second population focuses on finite outputs rather than mostly 0/Infinity.
let state=0x2a1752bd
function word(){state^=state<<13;state^=state>>>17;state^=state<<5;return state>>>0}
for(let i=0;i<2048;i++) {
 const raw=()=>number(word().toString(16).padStart(8,'0')+word().toString(16).padStart(8,'0'))
 add('binary64-sample',raw(),raw())
 add('finite-power-sample',0.01+(word()/2**32)*3.99,(word()/2**32-0.5)*180)
 add('finite-decay-sample',2,-word()/2**32*1076)
}
const sha=b=>createHash('sha256').update(b).digest('hex')
const out={runtime:{node:process.version,v8:process.versions.v8,platform:process.platform,arch:process.arch,glibc:header.glibcVersionRuntime,powFlag:'--use-std-math-pow',rounding:'FE_TONEAREST',cpu:header.cpus[0].model},source:{url:'https://github.com/nodejs/node/blob/v24.22.1/deps/v8/src/numbers/ieee754.cc',sha256:sha(readFileSync(new URL('../internal/jsnumber/testdata/v8-pow-wrapper.cc.txt',import.meta.url))),generatorSHA256:sha(readFileSync(new URL(import.meta.url)))},cases}
const target=hostOutput??new URL('../internal/jsnumber/testdata/pow-node.json',import.meta.url)
const json=JSON.stringify(out,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(target,'utf8'),json)
else writeFileSync(target,json)
console.log(`${cases.length} exact Node pow cases ${process.argv.includes('--check')?'checked':'written'}`)
