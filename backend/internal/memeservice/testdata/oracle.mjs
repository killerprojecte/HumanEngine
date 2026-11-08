import fs from 'node:fs';
import {createHash} from 'node:crypto';
import {stripTypeScriptTypes} from 'node:module';
import vm from 'node:vm';
const source=fs.readFileSync(process.argv[2],'utf8');
const sourceSHA256=createHash('sha256').update(source).digest('hex');
if(sourceSHA256!=='cf477ac3f57827e77cc13b33aa9a177d5afa450abce3e84ba4c99fae25bf0756')throw Error('source_drift');
const code=source.slice(source.indexOf('export const TAG_CATEGORIES'),source.indexOf('export class MemeService')).replaceAll('export ','');
const box={Buffer,createHash,MAX_IMAGE_SOURCE_BYTES:64*1024*1024};vm.createContext(box);
vm.runInContext(stripTypeScriptTypes(code)+`;globalThis.oracle={emptyTags,validateTags,concepts,digest};`,box);
const inputs=['happy unhappy 不开心 开心','CONGRATULATIONS joyfully joy','ｈａｐｐｙ','thank you gratitude','surprised & surprised','not happy, sad','unknown'];
const cases=[];for(const [name,mutate] of [['empty',()=>{}],['duplicate',t=>t.intent=['x','x','y']],['max160',t=>t.style=['😀'.repeat(80)]],['tooLong',t=>t.style=['😀'.repeat(81)]],['maxOCR',t=>t.ocr='😀'.repeat(2000)],['longOCR',t=>t.ocr='😀'.repeat(2001)],['tooMany',t=>t.style=Array(21).fill('x')],['missing',t=>delete t.intent]]){const input=box.oracle.emptyTags();mutate(input);let result,error;try{result=box.oracle.validateTags(input)}catch(e){error=e.message}cases.push({name,input,result,error})}
console.log(JSON.stringify({sourceSHA256,node:process.version,tags:cases,concepts:inputs.map(input=>({input,result:box.oracle.concepts(input)}))},null,2));
