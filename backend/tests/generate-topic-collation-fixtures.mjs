// Test-only Node oracle. The native runtime never calls this or starts Node.
import assert from 'node:assert/strict'
import {readFileSync, writeFileSync} from 'node:fs'
assert.equal(process.version, 'v24.22.1')
assert.equal(process.versions.icu, '78.3')
assert.equal(process.versions.cldr, '48.0')
assert.equal(process.versions.unicode, '17.0')
const pairs=[]
const add=(a,b)=>pairs.push([JSON.stringify(a),JSON.stringify(b)])
const values=['','a','A','ä','a\u0308','å','Å','æ','Æ','z','Z','ß','ss','œ','oe','é','e\u0301',
 'ch','cH','cz','ll','lL','i','I','ı','İ','10','2','02','10.0','-','_',' ','a-b','ab','a b',
 '\u0000','a\u0000b','a\u0000c','a\u200db','a\u200cb','\ufeff','\u034f','\u00ad','\u2060',
 '😀','\ue000','\ud800','\ud801','\udfff','\ufffd','中文','中','一','二','三','国','國',
 'ガ','カ\u3099','か','ｶ','각','각','ا','أ','ا\u0654','ก','ก-','เก','กเ',
 '\u{1e6c0}','\u{1e6e3}','\u{11b60}','\u{16ea0}','\u{1cc00}',
 'a'+'\u0315'.repeat(32)+'\u0300','a'+'\u0300'+'\u0315'.repeat(32)]
for (const a of values) for (const b of values) add(a,b)
// Every lone surrogate is kept distinct, with both surrogate boundaries and NUL.
for(let cp=0xd800;cp<=0xdfff;cp++) {
 const a=String.fromCharCode(cp), b=String.fromCharCode(cp===0xdfff?0xd800:cp+1)
 add(a,b);add('a\u0000'+a,a+'\u0000a')
}
let seed=0x31ca9701
function rand(n){seed^=seed<<13;seed^=seed>>>17;seed^=seed<<5;return(seed>>>0)%n}
for(let i=0;i<4096;i++) {
 const units=()=>String.fromCharCode(...Array.from({length:rand(24)},()=>rand(65536)))
 const scalar=()=>String.fromCodePoint(rand(0x110000))
 add(i%2?units():scalar()+scalar(),i%2?units():scalar()+scalar())
}
for(let i=0;i<128;i++) {
 const marks=['\u0301','\u0315','\u0316','\u0327','\u0344','\u034f','\u035c','\u05ae','\u05b0','\u1dce','\u{1e6e3}']
 const a='a'+Array.from({length:32+rand(256)},()=>marks[rand(marks.length)]).join('')
 add(a,a.normalize('NFC'));add(a+'\ud800',a+'\ud801')
}
const decoded=pairs.map(([a,b])=>[JSON.parse(a),JSON.parse(b)])
const locales=['en-US','de','sv','da','th','tr','zh-Hans-CN','zh-Hant-TW','ja','ko','ar','fr','es','es-u-co-trad','de-u-co-phonebk']
const oracle=locales.map(locale=>{
 const c=new Intl.Collator(locale), resolved=c.resolvedOptions()
 assert.equal(resolved.locale,locale)
 return {locale,resolved,results:decoded.map(([a,b])=>Math.sign(a.localeCompare(b,locale)))}
})
const result=JSON.stringify({node:process.version,icu:process.versions.icu,cldr:process.versions.cldr,unicode:process.versions.unicode,pairs,oracle})+'\n'
const path='backend/internal/topiccollation/testdata/node.json'
if(process.argv.includes('--check')) assert.equal(readFileSync(path,'utf8'),result)
else writeFileSync(path,result)
console.log(`${pairs.length} UTF-16 pairs across ${locales.length} locales (${pairs.length*locales.length} comparisons)`)
