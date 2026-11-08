import assert from 'node:assert/strict';
// Explicit resource-window probe: not run by the small/default fixture generator.
// Creates three valid 8-bit grayscale PNGs around the source's 40M-pixel gate.
import {createRequire} from 'node:module';
import {createDeflate} from 'node:zlib';
import {readFileSync,writeFileSync} from 'node:fs';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT||'.');
const require=createRequire(join(root,'package.json'));const sharp=require('sharp');
assert.equal(process.version, 'v24.22.1'); assert.equal(sharp.versions.sharp, '0.35.5');
const {compressImage}=await import(pathToFileURL(join(root,'src/image-upload.ts')));
function crc32(bytes){let c=0xffffffff;for(const b of bytes){c^=b;for(let j=0;j<8;j++)c=(c>>>1)^((c&1)?0xedb88320:0);}return(c^0xffffffff)>>>0;}
function chunk(type,data){const name=Buffer.from(type),n=Buffer.alloc(4),crc=Buffer.alloc(4);n.writeUInt32BE(data.length);crc.writeUInt32BE(crc32(Buffer.concat([name,data])));return Buffer.concat([n,name,data,crc]);}
async function png(w,h){
 const ihdr=Buffer.alloc(13);ihdr.writeUInt32BE(w);ihdr.writeUInt32BE(h,4);ihdr[8]=8;
 const deflate=createDeflate({level:9}),parts=[];const done=new Promise((resolve,reject)=>{deflate.on('data',x=>parts.push(x));deflate.on('end',resolve);deflate.on('error',reject)});
 const scanline=Buffer.alloc(w+1);for(let y=0;y<h;y++){if(!deflate.write(scanline))await new Promise(r=>deflate.once('drain',r));}deflate.end();await done;
 return Buffer.concat([Buffer.from([137,80,78,71,13,10,26,10]),chunk('IHDR',ihdr),chunk('IDAT',Buffer.concat(parts)),chunk('IEND',Buffer.alloc(0))]);
}
const cases=[];for(const [name,w,h] of [['pixels-below',7999,5000],['pixels-at',8000,5000],['pixels-above',8000,5001]]){const input=await png(w,h);const row={name,input:input.toString('base64'),limits:{maxWidth:8000,maxHeight:5001},firstFrame:false};try{row.output=await compressImage(input,row.limits)}catch(e){row.error=e.message};cases.push(row);console.log(name,w*h,row.error||'accepted');}
const out=JSON.stringify({sharp:sharp.versions,cases},null,2)+'\n';const target=new URL('../internal/imagecodec/testdata/large-boundary.json',import.meta.url);if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==out)throw Error('boundary source drift')}else writeFileSync(target,out);
