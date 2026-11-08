import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT || '.');
const require=createRequire(join(root,'package.json')); const sharp=require('sharp');
assert.equal(process.version, 'v24.22.1'); assert.equal(sharp.versions.sharp, '0.35.5');
const {compressImage}=await import(pathToFileURL(join(root,'src/image-upload.ts')));
const raw=Buffer.alloc(32*24*3); for(let i=0;i<raw.length;i++) raw[i]=(i*79+(i>>3)*11)%256;
const image=()=>sharp(raw,{raw:{width:32,height:24,channels:3}});
const png=await image().png().toBuffer(); const jpeg=await image().jpeg().toBuffer();
const alpha=await image().ensureAlpha(.4).png().toBuffer();
const webp=await image().webp().toBuffer(); const gif=await image().gif().toBuffer();
const rotated=await image().withMetadata({orientation:6}).jpeg().toBuffer();
const animation=await sharp(Buffer.concat([raw,Buffer.from(raw).reverse()]),{raw:{width:32,height:48,channels:3,pageHeight:24}}).gif({delay:[100,200],loop:0}).toBuffer();
const inputs=[['png-original',png,{}],['jpeg-original',jpeg,{}],['webp-original',webp,{}],['gif-original',gif,{}],['png-resize',png,{maxWidth:12,maxHeight:10}],['alpha-resize',alpha,{maxWidth:12,maxHeight:10}],['jpeg-rotation',rotated,{maxWidth:10,maxHeight:14}],['byte-shrink',png,{maxImageBytes:180}],['impossible-alpha',alpha,{maxImageBytes:1}],['empty',Buffer.alloc(0),{}],['corrupt',png.subarray(0,40),{}],['invalid-limit',png,{maxWidth:0}]];
inputs.push(['animated-original',animation,{}],['animated-reject-resize',animation,{maxWidth:8}],['animated-first-frame',animation,{maxWidth:8},true],['jpeg-warning-truncated',jpeg.subarray(0,jpeg.length-12),{}],['webp-resize',webp,{maxWidth:7}],['gif-resize',gif,{maxWidth:7}],['limit-safe-integer',png,{maxWidth:9007199254740992}],['jpeg-search',png,{maxImageBytes:600}],['cancelled',png,{},false,true]);
for(let orientation=2;orientation<=8;orientation++){inputs.push([`orientation-${orientation}`,await image().withMetadata({orientation}).jpeg().toBuffer(),{maxWidth:7,maxHeight:11}]);}
const cmyk=await image().toColourspace('cmyk').jpeg().toBuffer();
const icc=await image().withIccProfile('srgb').png().toBuffer();
const p3=await image().withIccProfile('p3').png().toBuffer();
const rgb16p3=await image().toColourspace('rgb16').withIccProfile('p3').png().toBuffer();
const gray16=await image().toColourspace('grey16').png().toBuffer();
const gray=await image().greyscale().png().toBuffer();
for (const [name,bytes] of [['cmyk',cmyk],['icc-srgb',icc],['icc-p3',p3],['rgb16-p3',rgb16p3],['gray16',gray16],['gray',gray]]) {
 inputs.push([name+'-original',bytes,{}],[name+'-resize',bytes,{maxWidth:9,maxHeight:10}]);
}
// A valid CRC on an oversized IHDR ensures metadata reaches the source pixel
// admission check. Full at-boundary pixel evaluation is a separate heavy probe.
function crc32(bytes){let c=0xffffffff;for(const b of bytes){c^=b;for(let j=0;j<8;j++)c=(c>>>1)^((c&1)?0xedb88320:0);}return (c^0xffffffff)>>>0;}
const oversized=Buffer.from(png);oversized.writeUInt32BE(8000,16);oversized.writeUInt32BE(5001,20);oversized.writeUInt32BE(crc32(oversized.subarray(12,29)),29);
inputs.push(['pixel-limit-header',oversized,{}]);
for(const [w,h] of [[100,1],[1,100]])for(const fmt of ['jpeg','webp']){
 const pixels=Buffer.alloc(w*h*3);for(let i=0;i<pixels.length;i++)pixels[i]=(i*79)%256;
 const bytes=await sharp(pixels,{raw:{width:w,height:h,channels:3}})[fmt]().toBuffer();
 inputs.push([`thin-${w}x${h}-${fmt}`,bytes,{maxWidth:10,maxHeight:10}]);
}
inputs.push(['cancel-during-jpeg-search',png,{maxImageBytes:600},false,false,3],['cancel-during-alpha-scale',alpha,{maxImageBytes:1},false,false,3]);
const uhdr=await sharp(Buffer.concat([raw,raw,raw,raw]),{raw:{width:64,height:48,channels:3}}).withGainMap().jpeg().toBuffer();
const uhdrMetadata=await sharp(uhdr).metadata();if(!uhdrMetadata.gainMap?.image?.length)throw Error('UltraHDR fixture must contain an actual gain map');
inputs.push(['uhdr-original',uhdr,{}],['uhdr-first-frame',uhdr,{},true],['uhdr-resize-no-preload',uhdr,{maxWidth:48,maxHeight:36}],['uhdr-resize-jpeg-preload',uhdr,{maxWidth:17,maxHeight:13}],['uhdr-byte-search',uhdr,{maxImageBytes:600}]);
const cases=[];
for(const [name,input,limits,firstFrame=false,cancelled=false,cancelAfterChecks=0] of inputs){const row={name,input:input.toString('base64'),limits,firstFrame,cancelled,cancelAfterChecks};let checks=0;const signal=cancelled?AbortSignal.abort(new Error('context canceled')):cancelAfterChecks?{throwIfAborted(){if(++checks>=cancelAfterChecks)throw new Error('context canceled');}}:undefined;try{row.output=await compressImage(input,limits,firstFrame,signal)}catch(e){row.error=e.message}cases.push(row);}
const out={sourceSHA256:createHash('sha256').update(readFileSync(join(root,'src/image-upload.ts'))).digest('hex'),sharp:sharp.versions,fixtureEvidence:{ultraHDR:{format:uhdrMetadata.format,width:uhdrMetadata.width,height:uhdrMetadata.height,space:uhdrMetadata.space,depth:uhdrMetadata.depth,gainMapBytes:uhdrMetadata.gainMap.image.length,inputSHA256:createHash('sha256').update(uhdr).digest('hex'),generation:'sharp(raw).withGainMap().jpeg()',loaderClass:'not exposed by Sharp metadata'}},cases};
const target=new URL('../internal/imagecodec/testdata/source.json',import.meta.url);const text=JSON.stringify(out,null,2)+'\n';
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('image source fixture drift');}else writeFileSync(target,text);
console.log(`Actual-source compressImage: ${cases.length} cases ${process.argv.includes('--check')?'matched':'generated'}`);
