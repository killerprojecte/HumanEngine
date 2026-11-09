// Executes the real source module, never a reimplementation.
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createHash } from 'node:crypto';
const { OutboundEncoder, outboundEncodingUsage } = await import(pathToFileURL(resolve(process.argv[2], 'src/plugins/builtin/snowluma/outbound.ts')).href);
const root = await mkdtemp(join(tmpdir(), 'he-native-outbound-oracle-'));
const encoder = new OutboundEncoder();
const output = [];
const payload = () => ({ note: '😀\ud800<&', message: [{type:'image',data:{file:''}}] });
try {
 for (const size of [1,2,3,49151,49152,49153,65536]) {
  const data=Buffer.alloc(size); for(let i=0;i<size;i++)data[i]=i%251;
  const path=join(root, `source-${size}`); await writeFile(path,data);
  const source={index:0,path,bytes:size,hash:createHash('sha256').update(data).digest('hex')};
  const value=payload(); const lease=await encoder.encode(value,[source]);
  output.push({size,hash:source.hash,wireHash:createHash('sha256').update(lease.bytes).digest('hex'),wireLength:lease.bytes.length});lease.release();
 }
 for (const [kind, params] of [['string', 'x'], ['boolean', true], ['number', 42], ['array', ['x']]]) {
  for (const textLength of [1, 12000]) {
   const value = { params, text: 'x'.repeat(textLength) };
   const lease = await encoder.encode(value, []);
   output.push({name:`empty-sources-${kind}-${textLength}`, params, textLength,
    wireHash:createHash('sha256').update(lease.bytes).digest('hex'),wireLength:lease.bytes.length});
   lease.release();
  }
 }
 const valid={index:0,path:join(root,'source-3'),bytes:3,hash:createHash('sha256').update(Buffer.from([0,1,2])).digest('hex')};
 const cases=[
  ['invalid-bytes',payload(),[{...valid,bytes:0}]],
  ['invalid-hash',payload(),[{...valid,hash:'A'.repeat(64)}]],
  ['inline-before-source',{text:'x'.repeat(180000)},[{...valid,bytes:0}]],
  ['many-sources',payload(),Array(65).fill(valid)],
  ['total-source',payload(),[{...valid,bytes:64*1024*1024},{...valid,index:1}]],
  ['duplicate-index',payload(),[valid,valid]],
  ['hash-mismatch',payload(),[{...valid,hash:'0'.repeat(64)}]],
  ['missing-path',payload(),[{...valid,path:join(root,'missing')}]],
 ];
 for(const [name,value,sources] of cases){let error;try{const lease=await encoder.encode(value,sources);lease.release();error='SUCCESS'}catch(e){error=e.code??e.message}output.push({name,error})}
 console.log(JSON.stringify({source:'src/plugins/builtin/snowluma/outbound.ts',cases:output,usage:outboundEncodingUsage()},null,2));
} finally {encoder.dispose();await rm(root,{recursive:true,force:true})}
