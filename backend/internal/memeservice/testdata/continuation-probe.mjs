// Transparent fixture wire proxy around the real shipped Node host. No response
// is fabricated: status probes are answered by the actual worker's existing
// continuation_status operation. Application wire bytes are forwarded unchanged.
import {spawn} from 'node:child_process'
import {appendFileSync} from 'node:fs'
import {createInterface} from 'node:readline'
const [node,log,...args]=process.argv.slice(2)
const child=spawn(node,args,{stdio:['pipe','pipe','inherit']})
let sequence=0
const requests=new Map()
const probe=(plugin,phase)=>{
 const id='fixture-probe-'+(++sequence)
 requests.set(id,{phase,plugin})
 child.stdin.write(JSON.stringify({id,op:'continuation_status',plugin})+'\n')
}
createInterface({input:process.stdin,crlfDelay:Infinity}).on('line',line=>{
 const message=JSON.parse(line)
 if(message.op==='claim_continuation'||message.op==='release_continuation')requests.set(message.id,{phase:message.op,plugin:message.plugin,ordinary:true})
 child.stdin.write(line+'\n')
}).on('close',()=>child.stdin.end())
createInterface({input:child.stdout,crlfDelay:Infinity}).on('line',line=>{
 const message=JSON.parse(line),request=requests.get(message.id)
 if(request){requests.delete(message.id);if(request.ordinary){probe(request.plugin,request.phase)}else{appendFileSync(log,JSON.stringify({phase:request.phase,...message})+'\n');return}}
 process.stdout.write(line+'\n')
})
child.on('exit',(code,signal)=>{process.exitCode=code??1;process.stdin.destroy()})
