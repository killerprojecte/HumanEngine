import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import { timingSafeEqual } from 'node:crypto'
import { Store } from './store.ts'
import { ModelConfigSchema, SettingsSchema, SceneSchema, LogSchema, StatusSchema, ErrorSchema } from './schema.ts'
import { Router } from './models.ts'

const json = <T extends z.ZodType>(schema:T, description='Success') => ({description,content:{'application/json':{schema}}})
const body = <T extends z.ZodType>(schema:T) => ({required:true,content:{'application/json':{schema}}})
const errorResponses = {400:json(ErrorSchema,'Invalid request'),401:json(ErrorSchema,'Authentication required'),503:json(ErrorSchema,'Service unavailable')}
export function createApp(store:Store, options:{token?:string}={}) {
  const app = new OpenAPIHono({defaultHook:(result,c)=> {if(!result.success)return c.json({error:'Invalid request'},400)}})
  const startedAt = new Date().toISOString()
  app.use('/api/*',async(c,next)=>{
    const token=options.token ?? process.env.HUMANENGINE_ADMIN_TOKEN
    if(!token)return c.json({error:'Set HUMANENGINE_ADMIN_TOKEN on the server before using the API'},503)
    const provided=c.req.header('Authorization')?.replace(/^Bearer /,'') ?? ''
    const a=Buffer.from(provided),b=Buffer.from(token)
    if(a.length!==b.length || !timingSafeEqual(a,b))return c.json({error:'Authentication required'},401)
    await next()
    c.header('Cache-Control','no-store')
    c.header('X-Content-Type-Options','nosniff')
  })
  app.onError((_error,c)=>{store.log('error','api','request_failed');return c.json({error:'Request failed; inspect server diagnostics'},503)})
  app.openapi(createRoute({method:'get',path:'/api/status',responses:{200:json(StatusSchema),...errorResponses}}),c=>{
    const scenes=store.scenes(),models=store.models
    return c.json({name:store.settings.name,startedAt,uptimeSeconds:Math.floor((Date.now()-Date.parse(startedAt))/1000),sceneCount:scenes.length,enabledScenes:scenes.filter(s=>s.participationEnabled).length,providerCount:models.providers.length,modelCount:models.models.length,activeTasks:0,recentErrors:store.logs.filter(l=>l.level==='error').length,vectorVersion:store.vectorVersion,version:'0.1.0'},200)
  })
  app.openapi(createRoute({method:'get',path:'/api/models',responses:{200:json(ModelConfigSchema),...errorResponses}}),c=>c.json(store.models,200))
  app.openapi(createRoute({method:'put',path:'/api/models',request:{body:body(ModelConfigSchema)},responses:{200:json(ModelConfigSchema),...errorResponses}}),c=>{
    const config=c.req.valid('json');store.set('models',config);store.log('info','config','models_updated');return c.json(config,200)
  })
  app.openapi(createRoute({method:'get',path:'/api/providers/{id}/models',request:{params:z.object({id:z.string()})},responses:{200:json(z.object({models:z.array(z.string())})),...errorResponses}}),async c=>{
    const models=await new Router(store.models).listModels(c.req.valid('param').id)
    return c.json({models},200)
  })
  app.openapi(createRoute({method:'get',path:'/api/settings',responses:{200:json(SettingsSchema),...errorResponses}}),c=>c.json(store.settings,200))
  app.openapi(createRoute({method:'put',path:'/api/settings',request:{body:body(SettingsSchema)},responses:{200:json(SettingsSchema),...errorResponses}}),c=>{
    const settings=c.req.valid('json');store.set('settings',settings);store.log('info','config','settings_updated');return c.json(settings,200)
  })
  app.openapi(createRoute({method:'get',path:'/api/scenes',responses:{200:json(z.array(SceneSchema)),...errorResponses}}),c=>c.json(store.scenes(),200))
  app.openapi(createRoute({method:'put',path:'/api/scenes/{id}',request:{params:z.object({id:z.string()}),body:body(SceneSchema)},responses:{200:json(SceneSchema),...errorResponses}}),c=>{
    const scene=c.req.valid('json');if(scene.id!==c.req.valid('param').id)return c.json({error:'Scene ID mismatch'},400)
    store.saveScene(scene);return c.json(scene,200)
  })
  app.openapi(createRoute({method:'get',path:'/api/logs',responses:{200:json(z.array(LogSchema)),...errorResponses}}),c=>c.json(store.logs.toReversed(),200))
  app.doc('/api/openapi.json',{openapi:'3.1.0',info:{title:'HumanEngine API',version:'0.1.0'},security:[{adminToken:[]}]})
  app.openAPIRegistry.registerComponent('securitySchemes','adminToken',{type:'http',scheme:'bearer'})
  return app
}
