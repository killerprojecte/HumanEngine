// One explicit disabled configuration operation. There is no setup or idle host.
import { pathToFileURL } from 'node:url'
import { createInterface } from 'node:readline'
console.log = (...args) => console.error(...args)
let plugin
const lines = createInterface({input:process.stdin,crlfDelay:Infinity})
for await (const line of lines) {
 let result
 try {
  const input=JSON.parse(line)
  if(!plugin){ const module=await import(pathToFileURL(input.path).href);plugin=module.default??module.plugin }
  result=inspectPluginConfiguration(plugin,input)
 } catch { result={failed:true,code:'plugin_setup_failed'} }
 await new Promise(resolve=>process.stdout.write(JSON.stringify(result)+'\n',resolve))
 if(result.failed || !pluginConfigurationOperations.size) break
}
pluginConfigurationOperations.clear()
process.exit(0)
