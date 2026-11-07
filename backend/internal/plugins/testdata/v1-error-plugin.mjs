import {
  ModelProtocolError,
  ModelProtocolRegistry,
  isBuiltinProtocol,
} from './sdk/model-protocols.ts'
import { ModelProtocolError as AliasError } from './alias/model-protocols.js'
import { ModelProtocolError as ForeignError } from './foreign/model-protocols.mjs'
import { makeFailure } from './v1-error-cases.mjs'
if (ModelProtocolError !== AliasError) throw Error('sdk_alias_identity_lost')
if (!isBuiltinProtocol('openai-chat') || isBuiltinProtocol('fixture:custom'))
  throw Error('sdk_export_missing')
const local = new ModelProtocolRegistry(),
  controller = new AbortController()
local.register('fixture:local', 'Local', { complete: async () => true }, controller.signal)
if (local.list().length !== 3 || !local.lookup('fixture:local')) throw Error('sdk_registry_missing')
controller.abort()
if (local.lookup('fixture:local')) throw Error('sdk_registry_lifecycle_lost')
const visible = () => ({
  text: 'synthetic',
  toolCalls: [],
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
})
export default {
  manifest: { id: 'identity', configSchema: { type: 'object' }, capabilities: ['model-protocol'] },
  setup(ctx) {
    const reads = []
    ctx.registerAdminAction('reads', () => reads.splice(0))
    ctx.registerModelProtocol('custom', 'Synthetic identity fixture', {
      complete(context, mode) {
        if (mode === 'foreign-sdk') throw new ForeignError('http', { status: 503 })
        if (mode === 'visible-getter')
          return {
            ...visible(),
            get text() {
              throw new ModelProtocolError('network')
            },
          }
        if (mode === 'continuation-getter')
          return {
            ...visible(),
            get continuation() {
              throw new ModelProtocolError('network')
            },
          }
        if (mode === 'thenable-getter')
          return {
            get then() {
              throw new ModelProtocolError('network')
            },
          }
        if (mode === 'wait-cancellation')
          return new Promise((_, reject) =>
            context.signal.addEventListener(
              'abort',
              () => reject(new ModelProtocolError('network')),
              { once: true },
            ),
          )
        throw makeFailure(ModelProtocolError, mode, reads)
      },
      async embeddings(context, mode) {
        await Promise.resolve()
        throw makeFailure(ModelProtocolError, mode, reads)
      },
      listModels(context) {
        throw makeFailure(ModelProtocolError, context.mode, reads)
      },
    })
  },
}
