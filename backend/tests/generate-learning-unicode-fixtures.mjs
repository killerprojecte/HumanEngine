// Pinned source /[\p{L}\p{N}]/u classification used by quote boundaries.
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
assert.equal(process.version,'v24.22.1')
const ranges=[];let start=-1
for(let cp=0;cp<=0x110000;cp++){
 const yes=cp<0x110000&&/[\p{L}\p{N}]/u.test(String.fromCodePoint(cp))
 if(yes&&start<0)start=cp
 if(!yes&&start>=0){ranges.push([start,cp-1]);start=-1}
}
const file='backend/internal/memorylexical/unicode.go'
const output='// Code generated from Node v24.22.1 Unicode property escapes. DO NOT EDIT.\npackage memorylexical\n\nimport "sort"\n\n// LetterOrNumber matches the actual source Unicode regex, including characters\n// newer than the Go standard library Unicode tables. Surrogates are not letters.\nfunc LetterOrNumber(r rune) bool {\n\ti := sort.Search(len(letterNumberRanges), func(i int) bool { return letterNumberRanges[i][1] >= r })\n\treturn i < len(letterNumberRanges) && letterNumberRanges[i][0] <= r\n}\n\nvar letterNumberRanges = [][2]rune{\n'+ranges.map(([a,b])=>`\t{0x${a.toString(16)}, 0x${b.toString(16)}},`).join('\n')+'\n}\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),output)
else writeFileSync(file,output)
