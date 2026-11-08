// Run with the pinned Node runtime from this directory; imports real source.
import { redactConfig, preserveSecrets, publicSchema } from '../../../src/plugins/config.ts'
import { writeFileSync } from 'node:fs'
const schema = {type:'object',properties:{token:{type:'string',secret:true,default:'hidden'},nested:{type:'object',properties:{token:{secret:true}}},array:{type:'array',items:{secret:true}},ordinary:{type:'string'}}}
const previous={token:'secret',nested:{token:'nested-secret'},array:['a','b'],ordinary:'old'}
const inputs=[{}, {token:null}, {token:''}, {nested:{}}, {nested:null}, {array:[null,'',null]}, {ordinary:'\ud800'}, {array:[]}]
const fixtures=inputs.map(input=>({schema,previous,input,preserved:preserveSecrets(input,previous,schema),redacted:redactConfig(input,schema),publicSchema:publicSchema(schema)}))
writeFileSync(new URL('./testdata/source.json',import.meta.url),JSON.stringify(fixtures,null,2)+'\n')
